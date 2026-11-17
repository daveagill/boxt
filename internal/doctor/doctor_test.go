package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/lima"
	"github.com/daveagill/boxt/internal/project"
)

type fakeRunner struct {
	outputs map[string]string
	errs    map[string]error
}

func (f *fakeRunner) Run(ctx context.Context, c command.Command) error {
	key := c.Name
	if len(c.Args) > 0 {
		key += " " + strings.Join(c.Args, " ")
	}
	if err := f.errs[key]; err != nil {
		return err
	}
	if f.errs[c.Name] != nil {
		return f.errs[c.Name]
	}
	out, ok := f.outputs[key]
	if !ok {
		out = f.outputs[c.Name]
	}
	if c.Stdout != nil {
		fmt.Fprint(c.Stdout, out)
	}
	return nil
}

func lookPathFor(names ...string) func(string) (string, error) {
	available := map[string]bool{}
	for _, n := range names {
		available[n] = true
	}
	return func(name string) (string, error) {
		if available[name] {
			return "/usr/bin/" + name, nil
		}
		return "", os.ErrNotExist
	}
}

func baseDeps(t *testing.T) Deps {
	t.Helper()
	data := t.TempDir()
	return Deps{
		OS:       "linux",
		Arch:     "arm64",
		LookPath: lookPathFor("git", "limactl", "qemu-system-aarch64", "qemu-img"),
		Runner: &fakeRunner{outputs: map[string]string{
			"git --version":     "git version 2.55.0\n",
			"limactl --version": "limactl version 2.3.0\n",
		}},
		DataDir:       func() (string, error) { return data, nil },
		HomeDir:       func() (string, error) { return "/home/me", nil },
		LookupProject: func() (*project.Project, error) { return nil, &project.ErrNotInitialized{Start: "/tmp"} },
		KVMUsable:     func() bool { return true },
	}
}

func find(report Report, label string) Check {
	for _, c := range report.Checks {
		if c.Label == label {
			return c
		}
	}
	return Check{}
}

func TestRunHealthyLinux(t *testing.T) {
	deps := baseDeps(t)
	report := Run(context.Background(), deps)
	if report.Failed() {
		t.Fatalf("unexpected failure:\n%s", Format(report, false))
	}
	for _, want := range []string{"host", "git", "limactl", "virtualization", "qemu", "data"} {
		if c := find(report, want); c.Status != OK {
			t.Fatalf("%s = %#v\n%s", want, c, Format(report, false))
		}
	}
}

func TestRunMissingDependencies(t *testing.T) {
	deps := baseDeps(t)
	deps.LookPath = lookPathFor()
	report := Run(context.Background(), deps)
	if !report.Failed() {
		t.Fatal("expected failures")
	}
	git := find(report, "git")
	if git.Status != FAIL || !strings.Contains(git.Hint, "Git") {
		t.Fatalf("git = %#v", git)
	}
	limaCheck := find(report, "limactl")
	if limaCheck.Status != FAIL || !strings.Contains(limaCheck.Hint, "lima-vm.io") {
		t.Fatalf("limactl = %#v", limaCheck)
	}
	qemu := find(report, "qemu")
	if qemu.Status != FAIL || !strings.Contains(qemu.Hint, "apt-get") {
		t.Fatalf("qemu = %#v", qemu)
	}
}

func TestRunOldVersions(t *testing.T) {
	deps := baseDeps(t)
	deps.Runner = &fakeRunner{outputs: map[string]string{
		"git --version":     "git version 2.41.0\n",
		"limactl --version": "limactl version 1.9.0\n",
	}}
	report := Run(context.Background(), deps)
	if find(report, "git").Status != FAIL {
		t.Fatalf("old Git not rejected:\n%s", Format(report, false))
	}
	if find(report, "limactl").Status != FAIL {
		t.Fatalf("old Lima not rejected:\n%s", Format(report, false))
	}
}

func TestRunKVMInaccessibleWarns(t *testing.T) {
	deps := baseDeps(t)
	deps.KVMUsable = func() bool { return false }
	report := Run(context.Background(), deps)
	c := find(report, "virtualization")
	if c.Status != WARN || !strings.Contains(c.Message, "/dev/kvm") {
		t.Fatalf("virtualization = %#v", c)
	}
	if report.Failed() {
		t.Fatal("KVM warning should not fail doctor")
	}
}

func TestRunWSLKVMHint(t *testing.T) {
	deps := baseDeps(t)
	deps.WSL = true
	deps.KVMUsable = func() bool { return false }
	report := Run(context.Background(), deps)
	c := find(report, "virtualization")
	if !strings.Contains(c.Hint, "nestedVirtualization=true") {
		t.Fatalf("WSL hint missing: %#v", c)
	}
	if find(report, "wsl").Status != OK {
		t.Fatalf("wsl check missing:\n%s", Format(report, false))
	}
}

func TestRunDarwinDoesNotRequireQEMU(t *testing.T) {
	deps := baseDeps(t)
	deps.OS = "darwin"
	deps.Arch = "arm64"
	deps.LookPath = lookPathFor("git", "limactl", "sysctl")
	deps.Runner = &fakeRunner{outputs: map[string]string{
		"git --version":             "git version 2.55.0\n",
		"limactl --version":         "limactl version 2.3.0\n",
		"sysctl -n kern.hv_support": "1\n",
	}}
	report := Run(context.Background(), deps)
	if report.Failed() {
		t.Fatalf("darwin failed without QEMU:\n%s", Format(report, false))
	}
	if c := find(report, "virtualization"); c.Status != OK {
		t.Fatalf("virtualization = %#v", c)
	}
}

func TestRunUnsupportedHostFails(t *testing.T) {
	deps := baseDeps(t)
	deps.OS = "windows"
	deps.Arch = "amd64"
	report := Run(context.Background(), deps)
	if find(report, "host").Status != FAIL {
		t.Fatalf("unsupported host accepted:\n%s", Format(report, false))
	}
}

func TestRunInvalidProjectConfig(t *testing.T) {
	deps := baseDeps(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".boxt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".boxt", "config.yaml"), []byte("version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps.LookupProject = func() (*project.Project, error) { return project.Lookup(dir) }
	report := Run(context.Background(), deps)
	c := find(report, "project")
	if c.Status != FAIL {
		t.Fatalf("invalid project not reported:\n%s", Format(report, false))
	}
}

func TestRunPortConflict(t *testing.T) {
	deps := baseDeps(t)
	dir := t.TempDir()
	p, err := project.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := `version: 1
vm:
  ports:
    - guest: 3000
      host: 8080
`
	if err := os.WriteFile(p.ConfigPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	deps.LookupProject = func() (*project.Project, error) { return project.Open(dir) }
	deps.CheckPorts = func(context.Context, *project.Project) error {
		return &lima.PortConflictError{HostIP: "127.0.0.1", Port: 8080, Cause: errors.New("address already in use")}
	}
	report := Run(context.Background(), deps)
	c := find(report, "ports")
	if c.Status != FAIL || !strings.Contains(c.Message, "8080") || !strings.Contains(c.Hint, "already in use") {
		t.Fatalf("ports = %#v\n%s", c, Format(report, true))
	}
}

func TestRunChecksAdditionalMounts(t *testing.T) {
	deps := baseDeps(t)
	dir := t.TempDir()
	p, err := project.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "version: 1\nvm:\n  mounts:\n    - host: " + host + "\n      guest: /mnt/data\n      writable: true\n"
	if err := os.WriteFile(p.ConfigPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	deps.LookupProject = func() (*project.Project, error) { return project.Open(dir) }
	report := Run(context.Background(), deps)
	if c := find(report, "mounts"); c.Status != OK {
		t.Fatalf("mounts = %#v\n%s", c, Format(report, false))
	}

	missingCfg := "version: 1\nvm:\n  mounts:\n    - host: " + filepath.Join(dir, "missing") + "\n      guest: /mnt/missing\n      writable: false\n"
	if err := os.WriteFile(p.ConfigPath, []byte(missingCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	report = Run(context.Background(), deps)
	c := find(report, "mounts")
	if c.Status != FAIL || !strings.Contains(c.Message, "cannot access host path") {
		t.Fatalf("mounts = %#v\n%s", c, Format(report, false))
	}
}

func TestFormatVerboseShowsDetails(t *testing.T) {
	report := Report{Checks: []Check{{Status: OK, Label: "git", Message: "2.55.0", Detail: "git version 2.55.0", Hint: "hidden"}}}
	if strings.Contains(Format(report, false), "Detail") || strings.Contains(Format(report, false), "git version") {
		t.Fatal("verbose detail leaked into normal output")
	}
	got := Format(report, true)
	if !strings.Contains(got, "git version 2.55.0") || !strings.Contains(got, "hidden") {
		t.Fatalf("verbose output missing detail:\n%s", got)
	}
}
