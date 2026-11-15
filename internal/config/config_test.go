package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestDefaultYAMLLoadsAsDefault(t *testing.T) {
	cfg, err := Parse([]byte(DefaultYAML))
	if err != nil {
		t.Fatalf("Parse(DefaultYAML): %v", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("DefaultYAML does not match Default():\n got: %#v\nwant: %#v", cfg, Default())
	}
}

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.VM.Image != DefaultImage || cfg.VM.CPUs != DefaultCPUs || cfg.VM.Memory != DefaultMemory {
		t.Fatalf("defaults not applied: %#v", cfg.VM)
	}
	if cfg.VM.PortForwarding != PortForwardingAuto {
		t.Fatalf("port forwarding default = %q", cfg.VM.PortForwarding)
	}
	if !cfg.VM.AutoStartEnabled() {
		t.Fatal("auto_start should default to true")
	}
	if cfg.Tools == nil || cfg.Run.Targets == nil {
		t.Fatalf("maps should be initialized: %#v", cfg)
	}
}

func TestParseAutoStartFalse(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nvm:\n  auto_start: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VM.AutoStartEnabled() {
		t.Fatal("auto_start false was ignored")
	}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "auto_start: false") {
		t.Fatalf("auto_start false not preserved:\n%s", data)
	}
}

func TestParseValidationErrors(t *testing.T) {
	tests := []struct {
		name   string
		yaml   string
		path   string
		errSub string
	}{
		{"missing version", "vm: {}\n", "version", "required"},
		{"unsupported version", "version: 2\n", "version", "unsupported version 2"},
		{"unsupported image", "version: 1\nvm:\n  image: debian\n", "vm.image", "unsupported image"},
		{"negative cpus", "version: 1\nvm:\n  cpus: -1\n", "vm.cpus", "between 1 and 64"},
		{"too many cpus", "version: 1\nvm:\n  cpus: 65\n", "vm.cpus", "between 1 and 64"},
		{"bad memory unit", "version: 1\nvm:\n  memory: 8GB\n", "vm.memory", "512MiB or 8GiB"},
		{"too little memory", "version: 1\nvm:\n  memory: 100MiB\n", "vm.memory", "at least 512MiB"},
		{"overflowing memory", "version: 1\nvm:\n  memory: 17179869185GiB\n", "vm.memory", "at most 256GiB"},
		{"bad guest port", "version: 1\nvm:\n  ports:\n    - guest: 0\n      host: 3000\n", "vm.ports[0].guest", "1 and 65535"},
		{"bad host ip", "version: 1\nvm:\n  ports:\n    - guest: 80\n      host: 8080\n      host_ip: nope\n", "vm.ports[0].host_ip", "invalid IP"},
		{"bad port forwarding mode", "version: 1\nvm:\n  port_forwarding: sometimes\n", "vm.port_forwarding", "must be"},
		{"duplicate host binding", "version: 1\nvm:\n  ports:\n    - guest: 80\n      host: 8080\n    - guest: 81\n      host: 8080\n", "vm.ports[1].host", "duplicates"},
		{"guest port 22", "version: 1\nvm:\n  ports:\n    - guest: 22\n      host: 2222\n", "vm.ports[0].guest", "reserved"},
		{"duplicate guest port", "version: 1\nvm:\n  ports:\n    - guest: 3000\n      host: 3000\n    - guest: 3000\n      host: 3001\n", "vm.ports[1].guest", "duplicates"},
		{"bad tool name", "version: 1\ntools:\n  Node: \"24\"\n", "tools.Node", "invalid tool name"},
		{"bad tool version", "version: 1\ntools:\n  node: \"24/1\"\n", "tools.node", "invalid version"},
		{"empty command", "version: 1\nrun:\n  targets:\n    dev:\n      command: []\n", "run.targets.dev.command", "at least one"},
		{"empty executable", "version: 1\nrun:\n  targets:\n    dev:\n      command: [\"\"]\n", "run.targets.dev.command[0]", "must not be empty"},
		{"unknown default", "version: 1\nrun:\n  default: nope\n  targets: {}\n", "run.default", "unknown target"},
		{"unknown field", "version: 1\nvm:\n  bogus: true\n", "", "field bogus not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.errSub) {
				t.Fatalf("error %q does not contain %q", err, tt.errSub)
			}
			if tt.path != "" && !strings.Contains(err.Error(), tt.path) {
				t.Fatalf("error %q does not contain field path %q", err, tt.path)
			}
		})
	}
}

func TestValidationErrorAggregatesFields(t *testing.T) {
	cfg := Default()
	cfg.Version = 99
	cfg.VM.CPUs = 0
	cfg.Run.Targets["bad target"] = RunTarget{}
	err := cfg.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	if len(ve.Errors) < 3 {
		t.Fatalf("expected at least 3 errors, got %#v", ve.Errors)
	}
}

func TestParseRejectsMultipleDocuments(t *testing.T) {
	_, err := Parse([]byte("version: 1\n---\nversion: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "multiple documents") {
		t.Fatalf("expected multiple-document error, got %v", err)
	}
}

func TestParseToolSpec(t *testing.T) {
	tests := []struct {
		spec, name, version, wantErr string
	}{
		{"node@24", "node", "24", ""},
		{"python@3.13", "python", "3.13", ""},
		{"codex@latest", "codex", "latest", ""},
		{"node", "", "", "expected name@version"},
		{"@24", "", "", "expected name@version"},
		{"node@", "", "", "expected name@version"},
		{"Node@24", "", "", "invalid tool name"},
		{"node@1/2", "", "", "invalid tool version"},
		{"npm:codex@1", "", "", "invalid tool name"},
	}
	for _, tt := range tests {
		name, version, err := ParseToolSpec(tt.spec)
		if tt.wantErr == "" {
			if err != nil {
				t.Errorf("%q: unexpected error %v", tt.spec, err)
				continue
			}
			if name != tt.name || version != tt.version {
				t.Errorf("%q: got %s@%s", tt.spec, name, version)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%q: expected error containing %q, got %v", tt.spec, tt.wantErr, err)
		}
	}
}

func TestCreateDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".boxt", "config.yaml")
	if err := Create(path); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Create(path); !errors.Is(err, ErrConfigExists) {
		t.Fatalf("expected ErrConfigExists, got %v", err)
	}
}

func TestAddAndRemoveToolPreservesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	initial := `# project header
version: 1
vm:
  image: ubuntu-24.04
  cpus: 2
  memory: 4GiB
# tools comment
tools:
  python: "3.13"
run:
  default: dev
  targets:
    dev:
      command: ["npm", "run", "dev"]
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddTool(path, "node", "24"); err != nil {
		t.Fatalf("AddTool: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# project header", "# tools comment", "python:", "node:", "run:", "default: dev"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("updated file lost %q:\n%s", want, data)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed to %o", info.Mode().Perm())
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load after add: %v", err)
	}
	if cfg.Tools["node"] != "24" || cfg.Tools["python"] != "3.13" {
		t.Fatalf("unexpected tools: %#v", cfg.Tools)
	}
	if cfg.VM.CPUs != 2 || cfg.Run.Default != "dev" {
		t.Fatalf("unrelated settings changed: %#v", cfg)
	}

	if err := AddTool(path, "node", "22"); err != nil {
		t.Fatalf("AddTool update: %v", err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools["node"] != "22" {
		t.Fatalf("tool version not updated: %#v", cfg.Tools)
	}

	if err := RemoveTool(path, "node"); err != nil {
		t.Fatalf("RemoveTool: %v", err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Tools["node"]; ok {
		t.Fatalf("node still declared: %#v", cfg.Tools)
	}
	if err := RemoveTool(path, "node"); !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("expected ErrToolNotFound, got %v", err)
	}
}

func TestAddToolCreatesToolsMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nrun:\n  targets: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AddTool(path, "go", "1.24"); err != nil {
		t.Fatalf("AddTool: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tools["go"] != "1.24" {
		t.Fatalf("tool missing: %#v", cfg.Tools)
	}
}

func TestAddToolInvalidLeavesFileUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Create(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := AddTool(path, "Node", "24"); err == nil {
		t.Fatal("expected invalid name error")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("invalid add modified the configuration")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	cfg := Default()
	cfg.Tools["node"] = "24"
	cfg.Run.Default = "dev"
	cfg.Run.Targets["dev"] = RunTarget{Command: []string{"npm", "run", "dev"}}
	cfg.VM.Ports = []PortForward{{Guest: 3000, Host: 3000, HostIP: "127.0.0.1"}}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse(marshal): %v\n%s", err, data)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v\n%s", got, cfg, data)
	}
}

func TestAddToolThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real-config.yaml")
	if err := Create(target); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := AddTool(link, "node", "24"); err != nil {
		t.Fatalf("AddTool: %v", err)
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
	if cfg.Tools["node"] != "24" {
		t.Fatalf("target not updated: %#v", cfg.Tools)
	}
}

func TestConcurrentAddTool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Create(path); err != nil {
		t.Fatal(err)
	}
	const n = 20
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- AddTool(path, fmt.Sprintf("tool%d", i), "1")
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent AddTool: %v", err)
		}
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Tools) != n {
		t.Fatalf("tools = %d, want %d: %#v", len(cfg.Tools), n, cfg.Tools)
	}
}

func TestAddToolTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Create(path); err != nil {
		t.Fatal(err)
	}
	target := RunTarget{Command: []string{"opencode"}, Interactive: true}
	if err := AddToolTarget(path, "opencode", "latest", "opencode", target); err != nil {
		t.Fatalf("AddToolTarget: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools["opencode"] != "latest" {
		t.Fatalf("tools = %#v", cfg.Tools)
	}
	got, ok := cfg.Run.Targets["opencode"]
	if !ok || !reflect.DeepEqual(got, target) {
		t.Fatalf("target = %#v", cfg.Run.Targets)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# boxt project configuration.") || !strings.Contains(string(data), "# Tools are installed") {
		t.Fatalf("comments were not preserved:\n%s", data)
	}
}

func TestAddToolTargetConflictIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Create(path); err != nil {
		t.Fatal(err)
	}
	existing := RunTarget{Command: []string{"npm", "test"}}
	if err := AddToolTarget(path, "node", "24", "test", existing); err != nil {
		t.Fatal(err)
	}
	err := AddToolTarget(path, "python", "3.13", "test", RunTarget{Command: []string{"pytest"}})
	var exists *ErrTargetExists
	if !errors.As(err, &exists) {
		t.Fatalf("expected ErrTargetExists, got %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Tools["python"]; ok {
		t.Fatalf("conflicting target still added the tool: %#v", cfg.Tools)
	}
	if got := cfg.Run.Targets["test"]; !reflect.DeepEqual(got, existing) {
		t.Fatalf("existing target changed: %#v", got)
	}
}

func TestAddToolTargetIdempotentAndUpdatesTool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Create(path); err != nil {
		t.Fatal(err)
	}
	target := RunTarget{Command: []string{"node"}, Interactive: true}
	if err := AddToolTarget(path, "node", "22", "node", target); err != nil {
		t.Fatal(err)
	}
	if err := AddToolTarget(path, "node", "24", "node", target); err != nil {
		t.Fatalf("idempotent target update: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools["node"] != "24" {
		t.Fatalf("tool version = %q", cfg.Tools["node"])
	}
	if len(cfg.Run.Targets) != 1 {
		t.Fatalf("targets = %#v", cfg.Run.Targets)
	}
}

func TestAddToolTargetNullRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nrun: null\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AddToolTarget(path, "node", "24", "node", RunTarget{Command: []string{"node"}}); err != nil {
		t.Fatalf("AddToolTarget with null run: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Run.Targets["node"]; !ok {
		t.Fatalf("target missing: %#v", cfg.Run.Targets)
	}
}

func TestAddToolTargetInvalidName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Create(path); err != nil {
		t.Fatal(err)
	}
	if err := AddToolTarget(path, "node", "24", "bad target", RunTarget{Command: []string{"node"}}); err == nil {
		t.Fatal("invalid target name accepted")
	}
}

func TestCreateWithHeaderAndNoOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := Default()
	cfg.Tools["node"] = "24"
	if err := CreateWith(path, cfg, "# generated header"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "# generated header\n\n") {
		t.Fatalf("header missing:\n%s", data)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Tools["node"] != "24" {
		t.Fatalf("tools = %#v", loaded.Tools)
	}
	cfg2 := Default()
	cfg2.Tools["python"] = "3.13"
	if err := CreateWith(path, cfg2, ""); !errors.Is(err, ErrConfigExists) {
		t.Fatalf("expected ErrConfigExists, got %v", err)
	}
	loaded, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Tools["python"]; ok {
		t.Fatal("existing config was overwritten")
	}
}
