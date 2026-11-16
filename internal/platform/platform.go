// Package platform resolves host-specific paths and prerequisites.
package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Info describes the host platform.
type Info struct {
	OS   string
	Arch string
}

// Current returns the running host platform.
func Current() Info { return Info{OS: runtime.GOOS, Arch: runtime.GOARCH} }

// IsWSL reports whether boxt is running inside Windows Subsystem for Linux.
func IsWSL() bool {
	if os.Getenv("WSL_INTEROP") != "" || os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	version := strings.ToLower(string(data))
	return strings.Contains(version, "microsoft") || strings.Contains(version, "wsl")
}

// Supported reports whether boxt supports this host OS/architecture.
func Supported(goos, goarch string) bool {
	switch goos {
	case "darwin":
		return goarch == "arm64"
	case "linux":
		return goarch == "amd64" || goarch == "arm64"
	default:
		return false
	}
}

// UnsupportedError describes an unsupported host.
type UnsupportedError struct {
	OS   string
	Arch string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("unsupported host platform %s/%s; boxt supports macOS arm64 and Linux amd64/arm64", e.OS, e.Arch)
}

// CheckSupported verifies the running host.
func CheckSupported() error {
	info := Current()
	if !Supported(info.OS, info.Arch) {
		return &UnsupportedError{OS: info.OS, Arch: info.Arch}
	}
	return nil
}

// DataDirFor resolves the platform data directory. xdg is the raw
// XDG_DATA_HOME value. Relative XDG values are ignored per the XDG spec.
func DataDirFor(goos, home, xdg string) (string, error) {
	if home == "" {
		return "", errors.New("cannot determine home directory")
	}
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "boxt"), nil
	case "linux":
		if xdg != "" && filepath.IsAbs(xdg) {
			return filepath.Join(xdg, "boxt"), nil
		}
		return filepath.Join(home, ".local", "share", "boxt"), nil
	default:
		return "", &UnsupportedError{OS: goos}
	}
}

// DataDir resolves the current host's data directory.
func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return DataDirFor(runtime.GOOS, home, os.Getenv("XDG_DATA_HOME"))
}

// WorkspacesDir returns the host root for one project's named workspaces.
func WorkspacesDir(dataDir, projectID string) string {
	return filepath.Join(dataDir, "workspaces", projectID)
}

// VMsDir returns the host directory for generated Lima configuration.
func VMsDir(dataDir, projectID string) string {
	return filepath.Join(dataDir, "vms", projectID)
}

// LimaNotFoundError provides platform-specific installation guidance without
// installing anything automatically.
type LimaNotFoundError struct {
	OS  string
	WSL bool
}

func (e *LimaNotFoundError) Error() string {
	return "Lima is required but limactl was not found on PATH"
}

// Hint returns actionable installation guidance.
func (e *LimaNotFoundError) Hint() string {
	var b strings.Builder
	b.WriteString("Install Lima (boxt does not install host dependencies automatically):\n")
	b.WriteString("  https://lima-vm.io/docs/installation/\n")
	switch e.OS {
	case "darwin":
		b.WriteString("  macOS: brew install lima")
	case "linux":
		b.WriteString("  Linux: install the latest Linux Lima release and your distribution's QEMU/KVM packages.")
		b.WriteString("\n  Ensure /dev/kvm is available for hardware acceleration.")
		if e.WSL {
			b.WriteString("\n\n  WSL detected: install the Linux limactl binary and QEMU packages inside WSL.")
			b.WriteString("\n  Prefer projects on the WSL ext4 filesystem (~/projects), not /mnt/c.")
		}
	default:
		b.WriteString("  Unsupported host platform.")
	}
	return b.String()
}

// LookPath is injectable for tests.
type LookPath func(string) (string, error)

// CheckLima verifies that limactl is available.
func CheckLima(goos string, lookPath LookPath) error {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath("limactl"); err != nil {
		return &LimaNotFoundError{OS: goos, WSL: IsWSL()}
	}
	return nil
}

// CheckGit verifies that git is available.
func CheckGit(goos string, lookPath LookPath) error {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath("git"); err != nil {
		return fmt.Errorf("Git is required but was not found on PATH; install Git and try again")
	}
	return nil
}

// EnsureDir creates a directory with the supplied mode.
func EnsureDir(path string, perm os.FileMode) error {
	if err := os.MkdirAll(path, perm); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return nil
}

// VirtualizationWarning returns a non-fatal diagnostic when Linux hardware
// acceleration is unavailable. Lima can still run with software emulation.
func VirtualizationWarning(goos string) string {
	return virtualizationWarning(goos, IsWSL(), kvmAvailable())
}

func virtualizationWarning(goos string, wsl, kvm bool) string {
	if goos != "linux" || kvm {
		return ""
	}
	if wsl {
		return "WSL detected: /dev/kvm is unavailable. Enable nested virtualization " +
			"(`[wsl2] nestedVirtualization=true` in .wslconfig), restart WSL, and verify /dev/kvm. " +
			"boxt will fall back to very slow software emulation."
	}
	return "/dev/kvm is not available; the VM may run slowly without hardware acceleration."
}

func kvmAvailable() bool {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// WSLPathWarning warns about projects on Windows-mounted filesystems when
// running inside WSL.
func WSLPathWarning(path string) string {
	return wslPathWarning(path, IsWSL())
}

// WSLPathWarningFor is the injectable form of WSLPathWarning.
func WSLPathWarningFor(path string, wsl bool) string {
	return wslPathWarning(path, wsl)
}

func wslPathWarning(path string, wsl bool) string {
	if !wsl {
		return ""
	}
	clean := filepath.Clean(path)
	rel, err := filepath.Rel("/mnt", clean)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return "project is on a Windows-mounted filesystem (" + clean + "). " +
		"Use the WSL ext4 filesystem (for example ~/projects/...) for reliable permissions, " +
		"symlinks, inotify and Git worktree performance."
}
