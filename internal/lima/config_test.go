package lima

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/config"
)

func testOptions(t *testing.T) VMOptions {
	t.Helper()
	base := t.TempDir()
	return VMOptions{
		ProjectID:     "0123456789abcdef",
		ProjectRoot:   filepath.Join(base, "project"),
		WorkspacesDir: filepath.Join(base, "workspaces"),
		CPUs:          4,
		Memory:        "8GiB",
	}
}

func TestGenerateConfig(t *testing.T) {
	opts := testOptions(t)
	opts.Ports = []config.PortForward{
		{Guest: 3000, Host: 3000},
		{Guest: 5432, Host: 15432, HostIP: "0.0.0.0"},
	}
	data, err := GenerateConfig(opts)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if err := ValidateGenerated(data); err != nil {
		t.Fatalf("ValidateGenerated: %v\n%s", err, data)
	}
	text := string(data)
	for _, want := range []string{
		"minimumLimaVersion: 2.0.0",
		"cpus: 4",
		"memory: 8GiB",
		"name: boxt",
		"home: /home/boxt",
		"passwordlessSudo: true",
		"mountPoint: /workspace",
		"mountPoint: /workspaces/0123456789abcdef",
		"arch: x86_64",
		"arch: aarch64",
		"system: false",
		"user: false",
		"propagateProxyEnv: false",
		"guestPort: 3000",
		"hostPort: 15432",
		"hostIP: 0.0.0.0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated config missing %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "location:") != 4 {
		t.Fatalf("expected exactly 2 images + 2 mounts, got:\n%s", text)
	}

	locations, err := MountLocations(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(locations) != 2 {
		t.Fatalf("mounts = %#v", locations)
	}
	if locations[0] != filepath.Clean(opts.ProjectRoot) || locations[1] != filepath.Clean(opts.WorkspacesDir) {
		t.Fatalf("unexpected mounts: %#v", locations)
	}
}

func TestGenerateConfigDefaultsHostIP(t *testing.T) {
	opts := testOptions(t)
	opts.Ports = []config.PortForward{{Guest: 8080, Host: 8080}}
	data, err := GenerateConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hostIP: 127.0.0.1") {
		t.Fatalf("default host IP missing:\n%s", data)
	}
}

func TestGenerateConfigDoesNotMountHome(t *testing.T) {
	opts := testOptions(t)
	data, err := GenerateConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	locations, err := MountLocations(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range locations {
		if loc == "~" || strings.Contains(loc, "/.docker") || strings.HasPrefix(loc, "/Users") || strings.HasPrefix(loc, "/home/") {
			t.Fatalf("generated config mounts unsafe host path %q", loc)
		}
	}
	if strings.Contains(string(data), "docker.sock") {
		t.Fatal("generated config mounts Docker socket")
	}
}

func TestGenerateConfigInvalid(t *testing.T) {
	base := testOptions(t)
	tests := []struct {
		name   string
		mutate func(*VMOptions)
	}{
		{"empty project ID", func(o *VMOptions) { o.ProjectID = "" }},
		{"path traversal project ID", func(o *VMOptions) { o.ProjectID = "../x" }},
		{"relative project root", func(o *VMOptions) { o.ProjectRoot = "rel" }},
		{"relative workspaces dir", func(o *VMOptions) { o.WorkspacesDir = "rel" }},
		{"zero cpus", func(o *VMOptions) { o.CPUs = 0 }},
		{"empty memory", func(o *VMOptions) { o.Memory = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := base
			tt.mutate(&opts)
			if _, err := GenerateConfig(opts); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestGenerateConfigRejectsOverlappingMounts(t *testing.T) {
	base := t.TempDir()
	opts := VMOptions{
		ProjectID:     "0123456789abcdef",
		ProjectRoot:   filepath.Join(base, "project"),
		WorkspacesDir: filepath.Join(base, "project", "data", "workspaces"),
		CPUs:          2,
		Memory:        "2GiB",
	}
	if _, err := GenerateConfig(opts); err == nil || !strings.Contains(err.Error(), "outside the project root") {
		t.Fatalf("expected overlapping-mount error, got %v", err)
	}
}

func TestWriteConfig(t *testing.T) {
	dir := t.TempDir()
	data := []byte("images: []\n")
	path, err := WriteConfig(dir, data)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, ConfigFileName) {
		t.Fatalf("path = %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("content = %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestValidateGeneratedRejectsBadConfig(t *testing.T) {
	if err := ValidateGenerated([]byte("images: []\n")); err == nil {
		t.Fatal("expected error")
	}
	if err := ValidateGenerated([]byte("not yaml: [")); err == nil {
		t.Fatal("expected YAML error")
	}
}

func TestWorkspacesGuestDir(t *testing.T) {
	if got := WorkspacesGuestDir("abc"); got != "/workspaces/abc" {
		t.Fatalf("WorkspacesGuestDir = %q", got)
	}
}

func TestGeneratedConfigPassesLimactlValidate(t *testing.T) {
	limactl := os.Getenv("BOXT_LIMACTL")
	if limactl == "" {
		t.Skip("set BOXT_LIMACTL to run real Lima config validation")
	}
	if _, err := os.Stat(limactl); err != nil {
		t.Skipf("BOXT_LIMACTL not usable: %v", err)
	}
	for _, mode := range []string{config.PortForwardingAuto, config.PortForwardingExplicit} {
		t.Run(mode, func(t *testing.T) {
			opts := testOptions(t)
			opts.PortForwarding = mode
			opts.Ports = []config.PortForward{{Guest: 3000, Host: 3000}}
			if err := os.MkdirAll(opts.ProjectRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(opts.WorkspacesDir, 0o755); err != nil {
				t.Fatal(err)
			}
			data, err := GenerateConfig(opts)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "lima.yaml")
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := command.Output(t.Context(), command.ExecRunner{}, command.Command{
				Name: limactl,
				Args: []string{"validate", path},
			})
			if err != nil {
				t.Fatalf("limactl validate failed: %v\n%s\n%s", err, out, data)
			}
		})
	}
}

func TestValidateGeneratedMissingWorkspaceMount(t *testing.T) {
	data := []byte(`images:
- location: x
  arch: aarch64
cpus: 1
memory: 1GiB
user:
  name: boxt
mounts:
- location: /tmp/x
  mountPoint: /workspace
  writable: true
`)
	if err := ValidateGenerated(data); err == nil || !strings.Contains(err.Error(), "workspace mount") {
		t.Fatalf("expected missing workspace mount error, got %v", err)
	}
}

func TestGeneratedConfigHasNoHomeMountFromLimaTemplate(t *testing.T) {
	opts := testOptions(t)
	data, err := GenerateConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "base:") || strings.Contains(string(data), "template:") {
		t.Fatal("generated config unexpectedly inherits a Lima template")
	}
}

func TestGeneratedConfigDisablesDynamicPortForwarding(t *testing.T) {
	opts := testOptions(t)
	opts.PortForwarding = config.PortForwardingExplicit
	data, err := GenerateConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"guestIP: 0.0.0.0", "guestIPMustBeZero: false", "proto: any", "ignore: true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("ignore-all port rule missing %q:\n%s", want, text)
		}
	}
	opts.Ports = []config.PortForward{{Guest: 3000, Host: 3000}}
	data, err = GenerateConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "ignore: true") != 1 {
		t.Fatalf("expected exactly one ignore-all rule:\n%s", data)
	}
	if idxExplicit, idxIgnore := strings.Index(string(data), "guestPort: 3000"), strings.Index(string(data), "ignore: true"); idxExplicit < 0 || idxIgnore < 0 || idxExplicit > idxIgnore {
		t.Fatalf("explicit rule must precede ignore-all:\n%s", data)
	}
}

func TestGeneratedConfigAutoPortForwarding(t *testing.T) {
	opts := testOptions(t)
	opts.PortForwarding = config.PortForwardingAuto
	opts.Ports = []config.PortForward{{Guest: 3000, Host: 3000}}
	data, err := GenerateConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "ignore: true") {
		t.Fatalf("auto mode unexpectedly ignores dynamic forwarding:\n%s", data)
	}
	if !strings.Contains(string(data), "guestPort: 3000") {
		t.Fatalf("explicit mapping missing in auto mode:\n%s", data)
	}
}

func TestPortConflictError(t *testing.T) {
	err := &PortConflictError{HostIP: "127.0.0.1", Port: 3000, Cause: errors.New("bind failed")}
	if !strings.Contains(err.Error(), "3000") || !strings.Contains(err.Hint(), "already in use") {
		t.Fatalf("unexpected error %q / hint %q", err.Error(), err.Hint())
	}
	var target *PortConflictError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed")
	}
}
