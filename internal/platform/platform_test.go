package platform

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDataDirFor(t *testing.T) {
	tests := []struct {
		name, goos, home, xdg, want, wantErr string
	}{
		{"macOS", "darwin", "/Users/me", "", "/Users/me/Library/Application Support/boxt", ""},
		{"linux default", "linux", "/home/me", "", "/home/me/.local/share/boxt", ""},
		{"linux XDG absolute", "linux", "/home/me", "/data/xdg", "/data/xdg/boxt", ""},
		{"linux XDG relative ignored", "linux", "/home/me", "relative/path", "/home/me/.local/share/boxt", ""},
		{"unsupported", "windows", `C:\Users\me`, "", "", "unsupported host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DataDirFor(tt.goos, tt.home, tt.xdg)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDataDirForNoHome(t *testing.T) {
	if _, err := DataDirFor("linux", "", ""); err == nil {
		t.Fatal("expected error for missing home")
	}
}

func TestSupported(t *testing.T) {
	tests := []struct {
		goos, goarch string
		want         bool
	}{
		{"darwin", "arm64", true},
		{"darwin", "amd64", false},
		{"linux", "amd64", true},
		{"linux", "arm64", true},
		{"linux", "386", false},
		{"windows", "amd64", false},
	}
	for _, tt := range tests {
		if got := Supported(tt.goos, tt.goarch); got != tt.want {
			t.Errorf("Supported(%s,%s)=%v want %v", tt.goos, tt.goarch, got, tt.want)
		}
	}
}

func TestCheckLima(t *testing.T) {
	ok := func(string) (string, error) { return "/usr/bin/limactl", nil }
	if err := CheckLima("linux", ok); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	err := CheckLima("darwin", missing)
	if err == nil {
		t.Fatal("expected error")
	}
	var nf *LimaNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected LimaNotFoundError, got %T", err)
	}
	if !strings.Contains(nf.Hint(), "brew install lima") {
		t.Fatalf("macOS hint missing brew command: %q", nf.Hint())
	}
	linuxErr := CheckLima("linux", missing)
	var lnf *LimaNotFoundError
	if !errors.As(linuxErr, &lnf) || !strings.Contains(lnf.Hint(), "QEMU") {
		t.Fatalf("unexpected Linux hint: %v", linuxErr)
	}
}

func TestPaths(t *testing.T) {
	data := filepath.Join("/data", "boxt")
	if got := WorkspacesDir(data, "abc"); got != filepath.Join("/data", "boxt", "workspaces", "abc") {
		t.Fatalf("WorkspacesDir = %q", got)
	}
	if got := VMsDir(data, "abc"); got != filepath.Join("/data", "boxt", "vms", "abc") {
		t.Fatalf("VMsDir = %q", got)
	}
}

func TestUnsupportedError(t *testing.T) {
	err := &UnsupportedError{OS: "plan9", Arch: "mips"}
	if !strings.Contains(err.Error(), "plan9/mips") {
		t.Fatalf("unexpected message %q", err.Error())
	}
}

func TestVirtualizationWarning(t *testing.T) {
	if got := VirtualizationWarning("darwin"); got != "" {
		t.Fatalf("macOS warning = %q", got)
	}
	got := VirtualizationWarning("linux")
	if kvmAvailable() {
		if got != "" {
			t.Fatalf("unexpected warning with usable /dev/kvm: %q", got)
		}
	} else if got == "" {
		t.Fatal("expected warning without usable /dev/kvm")
	}
}

func TestVirtualizationWarningVariants(t *testing.T) {
	if got := virtualizationWarning("linux", true, false); !strings.Contains(got, "WSL") || !strings.Contains(got, "nestedVirtualization") {
		t.Fatalf("WSL warning = %q", got)
	}
	if got := virtualizationWarning("linux", true, true); got != "" {
		t.Fatalf("warning with KVM = %q", got)
	}
	if got := virtualizationWarning("darwin", true, false); got != "" {
		t.Fatalf("macOS WSL warning = %q", got)
	}
}

func TestWSLPathWarning(t *testing.T) {
	for _, path := range []string{"/mnt/c/project", "/mnt/d/work/app", "/mnt/wsl/foo"} {
		if got := wslPathWarning(path, true); !strings.Contains(got, "Windows-mounted") {
			t.Errorf("wslPathWarning(%q) = %q", path, got)
		}
	}
	for _, path := range []string{"/home/me/project", "/mnt", "/"} {
		if got := wslPathWarning(path, true); got != "" {
			t.Errorf("wslPathWarning(%q) = %q", path, got)
		}
	}
	if got := wslPathWarning("/mnt/c/project", false); got != "" {
		t.Fatalf("non-WSL warning = %q", got)
	}
}

func TestIsWSLDetection(t *testing.T) {
	t.Setenv("WSL_INTEROP", "/run/WSL/1_interop")
	if !IsWSL() {
		t.Fatal("WSL_INTEROP not detected")
	}
	t.Setenv("WSL_INTEROP", "")
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu")
	if !IsWSL() {
		t.Fatal("WSL_DISTRO_NAME not detected")
	}
}

func TestLimaHintWSL(t *testing.T) {
	hint := (&LimaNotFoundError{OS: "linux", WSL: true}).Hint()
	for _, want := range []string{"WSL detected", "ext4", "QEMU"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("WSL hint missing %q:\n%s", want, hint)
		}
	}
}
