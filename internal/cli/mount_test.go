package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/config"
)

func testMount(host, guest string, writable bool) config.Mount {
	return config.Mount{Host: host, Guest: guest, Writable: &writable}
}

func TestMountAddListRemove(t *testing.T) {
	f := newLifecycleFixture(t)
	host := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}

	if code := f.run(t, "mount", "add", host); code != 0 {
		t.Fatalf("mount add exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "Added mount /mnt/shared") {
		t.Fatalf("unexpected output: %s", f.out.String())
	}
	cfg := loadConfig(t, f)
	if len(cfg.VM.Mounts) != 1 {
		t.Fatalf("mounts = %#v", cfg.VM.Mounts)
	}
	if cfg.VM.Mounts[0].Host != host || cfg.VM.Mounts[0].GuestPath() != "/mnt/shared" || !cfg.VM.Mounts[0].WritableEnabled() {
		t.Fatalf("mount = %#v", cfg.VM.Mounts[0])
	}

	if code := f.run(t, "mount", "list"); code != 0 {
		t.Fatalf("mount list exit %d: %s", code, f.errOut.String())
	}
	for _, want := range []string{"GUEST", "/mnt/shared", host, "rw"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("mount list missing %q:\n%s", want, f.out.String())
		}
	}

	readOnlyHost := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(readOnlyHost, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := f.run(t, "mount", "add", readOnlyHost, "--guest", "/mnt/ro", "--read-only"); code != 0 {
		t.Fatalf("read-only add exit %d: %s", code, f.errOut.String())
	}
	cfg = loadConfig(t, f)
	if len(cfg.VM.Mounts) != 2 || cfg.VM.Mounts[1].WritableEnabled() {
		t.Fatalf("mounts = %#v", cfg.VM.Mounts)
	}

	if code := f.run(t, "mount", "remove", host); code != 0 {
		t.Fatalf("remove by host exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "Removed mount /mnt/shared") {
		t.Fatalf("unexpected output: %s", f.out.String())
	}
	if code := f.run(t, "mount", "remove", "/mnt/ro"); code != 0 {
		t.Fatalf("remove by guest exit %d: %s", code, f.errOut.String())
	}
	if cfg := loadConfig(t, f); len(cfg.VM.Mounts) != 0 {
		t.Fatalf("mounts remain: %#v", cfg.VM.Mounts)
	}
}

func TestMountAddResolvesRelativeAndHomePaths(t *testing.T) {
	f := newLifecycleFixture(t)
	data := filepath.Join(f.project.Root, "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := f.run(t, "mount", "add", "data", "--guest", "/mnt/relative"); code != 0 {
		t.Fatalf("relative add exit %d: %s", code, f.errOut.String())
	}
	cfg := loadConfig(t, f)
	want, err := filepath.EvalSymlinks(data)
	if err != nil {
		want = data
	}
	if got := cfg.VM.Mounts[0].Host; got != want && got != data {
		t.Fatalf("host = %q, want %q", got, want)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	homeData := filepath.Join(home, "home-data")
	if err := os.MkdirAll(homeData, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := f.run(t, "mount", "add", "~/home-data", "--guest", "/mnt/home"); code != 0 {
		t.Fatalf("home add exit %d: %s", code, f.errOut.String())
	}
	cfg = loadConfig(t, f)
	if got := cfg.VM.Mounts[1].Host; got != homeData {
		t.Fatalf("home host = %q, want %q", got, homeData)
	}
}

func TestMountAddRejectsReservedGuestAndMissingSource(t *testing.T) {
	f := newLifecycleFixture(t)
	host := t.TempDir()
	before, err := os.ReadFile(f.project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if code := f.run(t, "mount", "add", host, "--guest", "/workspace/data"); code != 1 {
		t.Fatalf("reserved guest accepted, exit %d", code)
	}
	after, err := os.ReadFile(f.project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed mount add modified the configuration")
	}
	if code := f.run(t, "mount", "add", filepath.Join(host, "missing")); code != 1 {
		t.Fatalf("missing source accepted, exit %d", code)
	}
}

func TestMountChangeWarnsWhenVMExists(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	host := t.TempDir()
	if code := f.run(t, "mount", "add", host); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "boxt recreate") {
		t.Fatalf("missing recreate hint: %s", f.out.String())
	}
}

func TestUpDetectsMountDrift(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("first up exit %d: %s", code, f.errOut.String())
	}
	host := t.TempDir()
	if err := config.AddMount(f.project.ConfigPath, testMount(host, "/mnt/data", true)); err != nil {
		t.Fatal(err)
	}
	f.errOut.Reset()
	if code := f.run(t, "up"); code != 1 {
		t.Fatalf("up exit %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "different configuration") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestUpRejectsMissingMountSource(t *testing.T) {
	f := newLifecycleFixture(t)
	missing := filepath.Join(t.TempDir(), "missing")
	if err := config.AddMount(f.project.ConfigPath, testMount(missing, "/mnt/missing", true)); err != nil {
		t.Fatal(err)
	}
	if code := f.run(t, "up"); code != 1 {
		t.Fatalf("up exit %d, want 1: %s", code, f.out.String())
	}
	if !strings.Contains(f.errOut.String(), "cannot access host path") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestStatusShowsExtraMounts(t *testing.T) {
	f := newLifecycleFixture(t)
	host := t.TempDir()
	if err := config.AddMount(f.project.ConfigPath, testMount(host, "/mnt/data", true)); err != nil {
		t.Fatal(err)
	}
	if code := f.run(t, "status"); code != 0 {
		t.Fatalf("status exit %d: %s", code, f.errOut.String())
	}
	want := "Extra mount:       /mnt/data (host " + host + ", rw)"
	if !strings.Contains(f.out.String(), want) {
		t.Fatalf("status missing %q:\n%s", want, f.out.String())
	}
}
