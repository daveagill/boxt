package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMountsRequiresWritable(t *testing.T) {
	cfg, err := Parse([]byte(`version: 1
vm:
  mounts:
    - host: /tmp/data
      guest: /mnt/data
      writable: true
    - host: /tmp/cache
      guest: /mnt/cache
      writable: false
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.VM.Mounts) != 2 {
		t.Fatalf("mounts = %#v", cfg.VM.Mounts)
	}
	if !cfg.VM.Mounts[0].WritableEnabled() {
		t.Fatal("writable: true was ignored")
	}
	if cfg.VM.Mounts[1].WritableEnabled() {
		t.Fatal("writable: false was ignored")
	}

	_, err = Parse([]byte(`version: 1
vm:
  mounts:
    - host: /tmp/data
      guest: /mnt/data
`))
	if err == nil || !strings.Contains(err.Error(), "vm.mounts[0].writable") || !strings.Contains(err.Error(), "required") {
		t.Fatalf("omitted writable should be rejected, got %v", err)
	}
}

func TestParseMountValidationErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Dir(home)
	tests := []struct {
		name   string
		yaml   string
		path   string
		errSub string
	}{
		{"relative host", "version: 1\nvm:\n  mounts:\n    - host: data\n      guest: /mnt/data\n      writable: true\n", "vm.mounts[0].host", "absolute host path"},
		{"empty host", "version: 1\nvm:\n  mounts:\n    - host: \"\"\n      guest: /mnt/data\n      writable: true\n", "vm.mounts[0].host", "must not be empty"},
		{"host root", "version: 1\nvm:\n  mounts:\n    - host: /\n      guest: /mnt/data\n      writable: true\n", "vm.mounts[0].host", "host root"},
		{"home directory", "version: 1\nvm:\n  mounts:\n    - host: " + home + "\n      guest: /mnt/data\n      writable: true\n", "vm.mounts[0].host", "home directory"},
		{"home ancestor", "version: 1\nvm:\n  mounts:\n    - host: " + parent + "\n      guest: /mnt/data\n      writable: true\n", "vm.mounts[0].host", "ancestor"},
		{"docker socket", "version: 1\nvm:\n  mounts:\n    - host: /var/run/docker.sock\n      guest: /mnt/docker.sock\n      writable: true\n", "vm.mounts[0].host", "Docker socket"},
		{"relative guest", "version: 1\nvm:\n  mounts:\n    - host: /tmp/data\n      guest: mnt/data\n      writable: true\n", "vm.mounts[0].guest", "absolute guest path"},
		{"guest root", "version: 1\nvm:\n  mounts:\n    - host: /tmp/data\n      guest: /\n      writable: true\n", "vm.mounts[0].guest", "guest root"},
		{"project guest", "version: 1\nvm:\n  mounts:\n    - host: /tmp/data\n      guest: /workspace/data\n      writable: true\n", "vm.mounts[0].guest", "reserved guest path"},
		{"workspaces guest", "version: 1\nvm:\n  mounts:\n    - host: /tmp/data\n      guest: /workspaces/abc\n      writable: true\n", "vm.mounts[0].guest", "reserved guest path"},
		{"home guest", "version: 1\nvm:\n  mounts:\n    - host: /tmp/data\n      guest: /home/boxt/data\n      writable: true\n", "vm.mounts[0].guest", "reserved guest path"},
		{"system guest", "version: 1\nvm:\n  mounts:\n    - host: /tmp/data\n      guest: /etc/boxt\n      writable: true\n", "vm.mounts[0].guest", "reserved guest path"},
		{"duplicate host", "version: 1\nvm:\n  mounts:\n    - host: /tmp/data\n      guest: /mnt/a\n      writable: true\n    - host: /tmp/data\n      guest: /mnt/b\n      writable: true\n", "vm.mounts[1].host", "duplicates"},
		{"duplicate guest", "version: 1\nvm:\n  mounts:\n    - host: /tmp/a\n      guest: /mnt/data\n      writable: true\n    - host: /tmp/b\n      guest: /mnt/data\n      writable: true\n", "vm.mounts[1].guest", "duplicates"},
		{"nested guest", "version: 1\nvm:\n  mounts:\n    - host: /tmp/a\n      guest: /mnt/data\n      writable: true\n    - host: /tmp/b\n      guest: /mnt/data/cache\n      writable: true\n", "vm.mounts[1].guest", "overlaps"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.path) || !strings.Contains(err.Error(), tt.errSub) {
				t.Fatalf("error %q does not contain %q / %q", err, tt.path, tt.errSub)
			}
		})
	}
}

func TestValidateMountSources(t *testing.T) {
	dir := t.TempDir()
	if err := ValidateMountSources([]Mount{{Host: dir, Guest: "/mnt/data"}}); err != nil {
		t.Fatalf("directory rejected: %v", err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMountSources([]Mount{{Host: file, Guest: "/mnt/file"}}); err != nil {
		t.Fatalf("file rejected: %v", err)
	}
	err := ValidateMountSources([]Mount{{Host: filepath.Join(dir, "missing"), Guest: "/mnt/missing"}})
	if err == nil || !strings.Contains(err.Error(), "cannot access host path") {
		t.Fatalf("expected missing-path error, got %v", err)
	}
}

func TestExpandMountHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, err := ExpandMountHost("~/data"); err != nil || got != filepath.Join(home, "data") {
		t.Fatalf("~/data = %q, %v", got, err)
	}
	if got, err := ExpandMountHost("~"); err != nil || got != home {
		t.Fatalf("~ = %q, %v", got, err)
	}
	if _, err := ExpandMountHost("~other/data"); err == nil {
		t.Fatal("~user expansion should be rejected")
	}
	if got, err := ExpandMountHost("/tmp/data"); err != nil || got != "/tmp/data" {
		t.Fatalf("/tmp/data = %q, %v", got, err)
	}
}

func TestAddAndRemoveMountPreservesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Create(path); err != nil {
		t.Fatal(err)
	}
	writable := true
	readOnly := false
	if err := AddMount(path, Mount{Host: "/tmp/data", Guest: "/mnt/data", Writable: &writable}); err != nil {
		t.Fatalf("AddMount: %v", err)
	}
	if err := AddMount(path, Mount{Host: "/tmp/cache", Guest: "/mnt/cache", Writable: &readOnly}); err != nil {
		t.Fatalf("AddMount read-only: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# boxt project configuration.", "# Tools are installed", "host: /tmp/data", "guest: /mnt/cache", "writable: true", "writable: false"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("updated config lost %q:\n%s", want, data)
		}
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.VM.Mounts) != 2 || cfg.VM.Mounts[1].WritableEnabled() {
		t.Fatalf("mounts = %#v", cfg.VM.Mounts)
	}

	if err := AddMount(path, Mount{Host: "/tmp/data", Guest: "/mnt/other", Writable: &writable}); err == nil || !strings.Contains(err.Error(), "already mounted") {
		t.Fatalf("expected duplicate host error, got %v", err)
	}
	if err := AddMount(path, Mount{Host: "/tmp/other", Guest: "/mnt/data", Writable: &writable}); err == nil || !strings.Contains(err.Error(), "already configured") {
		t.Fatalf("expected duplicate guest error, got %v", err)
	}
	if err := AddMount(path, Mount{Host: "/tmp/unset", Guest: "/mnt/unset"}); err == nil || !strings.Contains(err.Error(), "writable is required") {
		t.Fatalf("expected missing writable error, got %v", err)
	}

	removed, err := RemoveMount(path, "/tmp/data")
	if err != nil {
		t.Fatalf("RemoveMount by host: %v", err)
	}
	if removed.GuestPath() != "/mnt/data" {
		t.Fatalf("removed = %#v", removed)
	}
	removed, err = RemoveMount(path, "/mnt/cache")
	if err != nil {
		t.Fatalf("RemoveMount by guest: %v", err)
	}
	if removed.Host != "/tmp/cache" {
		t.Fatalf("removed = %#v", removed)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\n  mounts:") {
		t.Fatalf("empty mounts list left behind:\n%s", data)
	}
	if _, err := RemoveMount(path, "/mnt/nope"); !errors.Is(err, ErrMountNotFound) {
		t.Fatalf("expected ErrMountNotFound, got %v", err)
	}
}

func TestRemoveMountAmbiguousTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	initial := `version: 1
vm:
  mounts:
    - host: /tmp/a
      guest: /mnt/a
      writable: true
    - host: /mnt/a
      guest: /mnt/b
      writable: false
`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveMount(path, "/mnt/a"); err == nil {
		t.Fatal("expected ambiguous removal error")
	} else if !strings.Contains(err.Error(), "both a guest path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAddMountThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real-config.yaml")
	if err := Create(target); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writable := true
	if err := AddMount(link, Mount{Host: "/tmp/data", Guest: "/mnt/data", Writable: &writable}); err != nil {
		t.Fatalf("AddMount: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("configuration symlink was replaced")
	}
	cfg, err := Load(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.VM.Mounts) != 1 {
		t.Fatalf("mounts = %#v", cfg.VM.Mounts)
	}
}
