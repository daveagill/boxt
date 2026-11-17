// Package doctor inspects host prerequisites for boxt. It never installs
// software, starts a VM, or modifies project configuration.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/lima"
	"github.com/daveagill/boxt/internal/platform"
	"github.com/daveagill/boxt/internal/project"
)

// Status is the severity of one diagnostic check.
type Status string

const (
	OK   Status = "OK"
	WARN Status = "WARN"
	FAIL Status = "FAIL"
)

// Check is one diagnostic result.
type Check struct {
	Status  Status
	Label   string
	Message string
	Hint    string
	Detail  string
}

// Line renders the check without its hint.
func (c Check) Line() string {
	if c.Message == "" {
		return c.Label
	}
	if c.Label == "" {
		return c.Message
	}
	return c.Label + ": " + c.Message
}

// Report is the result of a doctor run.
type Report struct {
	Checks []Check
}

// Failed reports whether any check failed.
func (r Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == FAIL {
			return true
		}
	}
	return false
}

// Deps holds injectable host dependencies for tests.
type Deps struct {
	OS       string
	Arch     string
	WSL      bool
	LookPath platform.LookPath
	Runner   command.Runner
	DataDir  func() (string, error)
	HomeDir  func() (string, error)
	// LookupProject discovers the project containing the current directory.
	LookupProject func() (*project.Project, error)
	// KVMUsable reports whether /dev/kvm can be opened read-write.
	KVMUsable func() bool
	// CheckPorts reports a conflict for explicit host ports. The CLI supplies
	// an implementation that skips a running project VM.
	CheckPorts func(context.Context, *project.Project) error
}

// DefaultDeps returns production dependencies.
func DefaultDeps() Deps {
	return Deps{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		WSL:      platform.IsWSL(),
		LookPath: exec.LookPath,
		Runner:   command.ExecRunner{},
		DataDir:  platform.DataDir,
		HomeDir:  os.UserHomeDir,
		LookupProject: func() (*project.Project, error) {
			return project.Lookup("")
		},
		KVMUsable: func() bool {
			f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
			if err != nil {
				return false
			}
			_ = f.Close()
			return true
		},
		CheckPorts: func(ctx context.Context, p *project.Project) error {
			return lima.CheckPorts(p.Config.VM.Ports)
		},
	}
}

// Run performs all checks. Project checks are skipped outside an initialized
// project.
func Run(ctx context.Context, deps Deps) Report {
	deps = withDefaults(deps)
	r := Report{}

	r.Checks = append(r.Checks, hostCheck(deps))
	if deps.WSL {
		r.Checks = append(r.Checks, Check{
			Status:  OK,
			Label:   "wsl",
			Message: "WSL2 detected",
			Hint:    "Keep projects and boxt data on the WSL ext4 filesystem, not /mnt/c.",
		})
	}
	r.Checks = append(r.Checks, gitCheck(ctx, deps))
	r.Checks = append(r.Checks, limaCheck(ctx, deps))
	r.Checks = append(r.Checks, virtualizationCheck(ctx, deps))
	if deps.OS == "linux" {
		r.Checks = append(r.Checks, qemuCheck(deps))
	}

	p, projectCheck := lookupProject(deps)
	dataCheck, dataDir := dataDirCheck(deps, p)
	r.Checks = append(r.Checks, dataCheck)
	if projectCheck != nil {
		r.Checks = append(r.Checks, *projectCheck)
	}
	if p != nil {
		r.Checks = append(r.Checks, projectChecks(ctx, deps, p, dataDir)...)
	}
	if deps.WSL && p != nil {
		if warning := platform.WSLPathWarningFor(p.Root, true); warning != "" {
			r.Checks = append(r.Checks, Check{
				Status:  WARN,
				Label:   "wsl path",
				Message: warning,
				Hint:    "Move the project to the WSL ext4 filesystem and run `boxt init` there.",
			})
		}
	}
	return r
}

func withDefaults(deps Deps) Deps {
	if deps.OS == "" {
		deps.OS = runtime.GOOS
	}
	if deps.Arch == "" {
		deps.Arch = runtime.GOARCH
	}
	if deps.LookPath == nil {
		deps.LookPath = exec.LookPath
	}
	if deps.Runner == nil {
		deps.Runner = command.ExecRunner{}
	}
	if deps.DataDir == nil {
		deps.DataDir = platform.DataDir
	}
	if deps.HomeDir == nil {
		deps.HomeDir = os.UserHomeDir
	}
	if deps.LookupProject == nil {
		deps.LookupProject = func() (*project.Project, error) { return project.Lookup("") }
	}
	if deps.KVMUsable == nil {
		deps.KVMUsable = func() bool { return false }
	}
	return deps
}

func hostCheck(deps Deps) Check {
	label := deps.OS + "/" + deps.Arch
	if platform.Supported(deps.OS, deps.Arch) {
		return Check{Status: OK, Label: "host", Message: label}
	}
	return Check{
		Status:  FAIL,
		Label:   "host",
		Message: label + " is not supported",
		Hint:    "boxt supports macOS, Linux and Windows 11 via WSL2.",
	}
}

type version struct {
	Major, Minor, Patch int
}

func parseVersion(text string) (version, bool) {
	re := regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		return version{}, false
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return version{}, false
	}
	minor, err := strconv.Atoi(m[2])
	if err != nil {
		return version{}, false
	}
	patch := 0
	if m[3] != "" {
		patch, _ = strconv.Atoi(m[3])
	}
	return version{Major: major, Minor: minor, Patch: patch}, true
}

func (v version) atLeast(major, minor int) bool {
	return v.Major > major || (v.Major == major && v.Minor >= minor)
}

func (v version) String() string {
	if v.Patch > 0 {
		return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	}
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}

func gitCheck(ctx context.Context, deps Deps) Check {
	if _, err := deps.LookPath("git"); err != nil {
		return Check{
			Status:  FAIL,
			Label:   "git",
			Message: "not found on PATH",
			Hint:    gitInstallHint(deps.OS),
		}
	}
	out, err := command.Output(ctx, deps.Runner, command.Command{Name: "git", Args: []string{"--version"}})
	if err != nil {
		return Check{
			Status:  FAIL,
			Label:   "git",
			Message: "could not run git --version",
			Hint:    err.Error(),
		}
	}
	v, ok := parseVersion(out)
	if !ok {
		return Check{Status: WARN, Label: "git", Message: "could not parse version from " + strings.TrimSpace(out)}
	}
	if !v.atLeast(2, 42) {
		return Check{
			Status:  FAIL,
			Label:   "git",
			Message: v.String() + " is too old",
			Hint:    "boxt requires Git 2.42 or newer for orphan worktrees.\n" + gitInstallHint(deps.OS),
		}
	}
	return Check{Status: OK, Label: "git", Message: v.String(), Detail: strings.TrimSpace(out)}
}

func gitInstallHint(goos string) string {
	switch goos {
	case "darwin":
		return "Install Git with `brew install git`, then verify `git --version`."
	case "linux":
		return "Install Git with your distribution's package manager, for example `sudo apt-get install git` or `sudo dnf install git`."
	default:
		return "Install Git 2.42 or newer."
	}
}

func limaCheck(ctx context.Context, deps Deps) Check {
	if _, err := deps.LookPath("limactl"); err != nil {
		return Check{
			Status:  FAIL,
			Label:   "limactl",
			Message: "not found on PATH",
			Hint:    (&platform.LimaNotFoundError{OS: deps.OS, WSL: deps.WSL}).Hint(),
		}
	}
	out, err := command.Output(ctx, deps.Runner, command.Command{Name: "limactl", Args: []string{"--version"}})
	if err != nil {
		return Check{
			Status:  FAIL,
			Label:   "limactl",
			Message: "could not run limactl --version",
			Hint:    err.Error(),
		}
	}
	v, ok := parseVersion(out)
	if !ok {
		return Check{Status: WARN, Label: "limactl", Message: "could not parse version from " + strings.TrimSpace(out)}
	}
	if !v.atLeast(2, 0) {
		return Check{
			Status:  FAIL,
			Label:   "limactl",
			Message: v.String() + " is too old",
			Hint:    "boxt requires Lima 2.0 or newer.\n" + limaInstallHint(deps.OS, deps.WSL),
		}
	}
	return Check{Status: OK, Label: "limactl", Message: v.String(), Detail: strings.TrimSpace(out)}
}

func limaInstallHint(goos string, wsl bool) string {
	switch goos {
	case "darwin":
		return "Install Lima with `brew install lima`, then verify `limactl --version`."
	case "linux":
		hint := "Install the latest Linux Lima release from https://lima-vm.io/docs/installation/."
		if wsl {
			hint += "\nInstall the Linux limactl binary and QEMU packages inside WSL."
		}
		return hint
	default:
		return "Install Lima from https://lima-vm.io/docs/installation/."
	}
}

func virtualizationCheck(ctx context.Context, deps Deps) Check {
	if deps.OS == "darwin" {
		return darwinVirtualizationCheck(ctx, deps)
	}
	if deps.OS != "linux" {
		return Check{Status: WARN, Label: "virtualization", Message: "no automatic check for " + deps.OS}
	}
	if deps.KVMUsable() {
		return Check{Status: OK, Label: "virtualization", Message: "/dev/kvm is accessible"}
	}
	hint := "Lima will fall back to slow QEMU software emulation (TCG)."
	if deps.WSL {
		hint = "WSL detected: enable nested virtualization with `[wsl2] nestedVirtualization=true` in .wslconfig, restart WSL, then verify /dev/kvm.\nLima will otherwise fall back to very slow QEMU software emulation (TCG)."
	} else {
		hint += "\nInstall/configure your distribution's KVM support and add your user to the `kvm` group."
	}
	return Check{Status: WARN, Label: "virtualization", Message: "/dev/kvm is not accessible", Hint: hint}
}

func darwinVirtualizationCheck(ctx context.Context, deps Deps) Check {
	if _, err := deps.LookPath("sysctl"); err == nil {
		out, runErr := command.Output(ctx, deps.Runner, command.Command{Name: "sysctl", Args: []string{"-n", "kern.hv_support"}})
		if runErr == nil {
			switch strings.TrimSpace(out) {
			case "1":
				return Check{Status: OK, Label: "virtualization", Message: "Apple Virtualization (vz) is available"}
			case "0":
				return Check{
					Status:  WARN,
					Label:   "virtualization",
					Message: "kern.hv_support is 0",
					Hint:    "Apple Virtualization may be unavailable on this Mac. Lima can still fall back to QEMU, but it is slower.",
				}
			}
		}
	}
	return Check{Status: OK, Label: "virtualization", Message: "Apple Virtualization (vz) is assumed"}
}

func qemuCheck(deps Deps) Check {
	var system, systemPackage string
	switch deps.Arch {
	case "amd64":
		system = "qemu-system-x86_64"
		systemPackage = "qemu-system-x86"
	case "arm64":
		system = "qemu-system-aarch64"
		systemPackage = "qemu-system-arm"
	default:
		return Check{Status: WARN, Label: "qemu", Message: "no expected QEMU binary for " + deps.Arch}
	}
	want := []string{system, "qemu-img"}
	var missing []string
	for _, name := range want {
		if _, err := deps.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return Check{Status: OK, Label: "qemu", Message: strings.Join(want, ", ")}
	}
	return Check{
		Status:  FAIL,
		Label:   "qemu",
		Message: strings.Join(missing, ", ") + " missing",
		Hint: "Install QEMU for your distribution:\n" +
			"  Ubuntu/Debian: sudo apt-get install " + systemPackage + " qemu-utils\n" +
			"  Fedora: sudo dnf install " + systemPackage + " qemu-img",
	}
}

func lookupProject(deps Deps) (*project.Project, *Check) {
	p, err := deps.LookupProject()
	if err == nil {
		return p, nil
	}
	if project.IsNotInitialized(err) {
		return nil, nil
	}
	c := Check{
		Status:  FAIL,
		Label:   "project",
		Message: err.Error(),
		Hint:    "Fix .boxt/config.yaml, then run `boxt doctor` again.",
	}
	var hinter interface{ Hint() string }
	if errors.As(err, &hinter) && hinter.Hint() != "" {
		c.Hint = hinter.Hint()
	}
	return nil, &c
}

func dataDirCheck(deps Deps, p *project.Project) (Check, string) {
	dataDir, err := deps.DataDir()
	if err != nil {
		return Check{Status: FAIL, Label: "data", Message: err.Error(), Hint: "Set a writable home directory or XDG_DATA_HOME."}, ""
	}
	path := dataDir
	create := false
	label := "data"
	if p != nil {
		path = platform.WorkspacesDir(dataDir, p.ID)
		create = true
		label = "workspace data"
	}
	if err := checkWritable(path, create); err != nil {
		return Check{
			Status:  FAIL,
			Label:   label,
			Message: err.Error(),
			Hint:    "Ensure the directory is writable by your user. boxt stores VM configuration and worktrees there.",
		}, dataDir
	}
	return Check{Status: OK, Label: label, Message: path}, dataDir
}

func checkWritable(path string, create bool) error {
	if create {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
	}
	dir := path
	for {
		info, err := os.Stat(dir)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", dir)
			}
			f, err := os.CreateTemp(dir, ".boxt-doctor-*")
			if err != nil {
				return fmt.Errorf("%s is not writable: %w", dir, err)
			}
			name := f.Name()
			_ = f.Close()
			_ = os.Remove(name)
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect %s: %w", dir, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fmt.Errorf("no writable parent directory for %s", path)
		}
		dir = parent
	}
}

func projectChecks(ctx context.Context, deps Deps, p *project.Project, dataDir string) []Check {
	checks := []Check{{
		Status:  OK,
		Label:   "project",
		Message: p.Root,
	}}
	home, _ := deps.HomeDir()
	if p.Root == "/" || (home != "" && filepath.Clean(home) == filepath.Clean(p.Root)) {
		checks = append(checks, Check{
			Status:  FAIL,
			Label:   "project root",
			Message: "the project root is the home directory or filesystem root",
			Hint:    "Create the project in a subdirectory instead.",
		})
	}
	if len(p.Config.VM.Ports) > 0 {
		if deps.CheckPorts != nil {
			if err := deps.CheckPorts(ctx, p); err != nil {
				checks = append(checks, Check{
					Status:  FAIL,
					Label:   "ports",
					Message: err.Error(),
					Hint:    portHint(err),
				})
			} else {
				checks = append(checks, Check{Status: OK, Label: "ports", Message: fmt.Sprintf("%d explicit mapping(s) available", len(p.Config.VM.Ports))})
			}
		}
	}
	return checks
}

func portHint(err error) string {
	var conflict *lima.PortConflictError
	if errors.As(err, &conflict) {
		return conflict.Hint()
	}
	return "Stop the process using the port, or change vm.ports."
}

// Format renders a report as aligned lines with indented hints.
func Format(r Report, verbose bool) string {
	var b strings.Builder
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "%-4s  %s\n", c.Status, c.Line())
		if verbose && c.Detail != "" {
			fmt.Fprintf(&b, "      %s\n", c.Detail)
		}
		if c.Hint != "" && (c.Status != OK || verbose) {
			for _, line := range strings.Split(c.Hint, "\n") {
				fmt.Fprintf(&b, "      %s\n", line)
			}
		}
	}
	return b.String()
}
