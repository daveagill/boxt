package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSetKeyScalars(t *testing.T) {
	cases := []struct {
		key   string
		value string
		check func(*Config) bool
	}{
		{"vm.cpus", "12", func(c *Config) bool { return c.VM.CPUs == 12 }},
		{"vm.memory", "16GiB", func(c *Config) bool { return c.VM.Memory == "16GiB" }},
		{"vm.port_forwarding", "explicit", func(c *Config) bool { return c.VM.PortForwarding == PortForwardingExplicit }},
		{"vm.auto_start", "false", func(c *Config) bool { return !c.VM.AutoStartEnabled() }},
		{"workspace.merge", "rebase", func(c *Config) bool { return c.WorkspaceMergeMode() == MergeModeRebase }},
		{"seed.allow_remove", "true", func(c *Config) bool { return c.SeedRules().AllowRemove }},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			path := writeTestConfig(t, DefaultYAML)
			if err := SetKey(path, tc.key, tc.value, false); err != nil {
				t.Fatalf("SetKey(%s): %v", tc.key, err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.check(cfg) {
				t.Fatalf("%s did not persist, got %#v", tc.key, cfg)
			}
		})
	}
}

func TestSetAndUnsetDefaultTarget(t *testing.T) {
	path := writeTestConfig(t, `version: 1
run:
  targets:
    dev:
      command: ["npm", "run", "dev"]
`)
	if err := SetKey(path, "run.default", "dev", false); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Run.Default != "dev" {
		t.Fatalf("default = %q", cfg.Run.Default)
	}
	if err := SetKey(path, "run.default", "missing", false); err == nil {
		t.Fatal("unknown target was accepted")
	}
	if err := UnsetKey(path, "run.default", "", false); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Run.Default != "" {
		t.Fatalf("default = %q", cfg.Run.Default)
	}
}

func TestSeedLists(t *testing.T) {
	path := writeTestConfig(t, DefaultYAML)
	if err := SetKey(path, "seed.include", ".env", true); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(path, "seed.include", "node_modules/**", true); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(path, "seed.exclude", "node_modules/.cache/**", true); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.SeedRules().Include, ","); got != ".env,node_modules/**" {
		t.Fatalf("include = %q", got)
	}
	if err := UnsetKey(path, "seed.include", ".env", true); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.SeedRules().Include, ","); got != "node_modules/**" {
		t.Fatalf("include after remove = %q", got)
	}
	if err := SetKey(path, "seed.include", "src/**", false); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.SeedRules().Include, ","); got != "src/**" {
		t.Fatalf("include after replace = %q", got)
	}
	if err := UnsetKey(path, "seed.include", "", false); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SeedRules().Include) != 0 {
		t.Fatalf("include after unset = %v", cfg.SeedRules().Include)
	}
}

func TestSetKeyRefusals(t *testing.T) {
	path := writeTestConfig(t, DefaultYAML)
	cases := []struct {
		key   string
		value string
		want  string
	}{
		{"nope", "x", "unknown configuration key"},
		{"tools", "x", "managed by boxt tool"},
		{"vm.ports", "x", "managed by boxt port"},
		{"vm.mounts", "x", "managed by boxt mount"},
		{"run.targets", "x", "managed by boxt target"},
		{"hooks", "x", "hand-edited"},
		{"version", "2", "read-only"},
		{"vm.image", "debian", "read-only"},
		{"vm.cpus", "many", "expected an integer"},
		{"vm.memory", "4GB", "must be a size"},
		{"vm.auto_start", "maybe", "expected true or false"},
		{"vm.port_forwarding", "sometimes", "must be one of"},
		{"workspace.merge", "squash", "must be one of"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			err := SetKey(path, tc.key, tc.value, false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SetKey(%s) = %v, want %q", tc.key, err, tc.want)
			}
		})
	}
}

func TestSetKeyInvalidLeavesFileUnchanged(t *testing.T) {
	path := writeTestConfig(t, DefaultYAML)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetKey(path, "vm.cpus", "0", false); err == nil {
		t.Fatal("expected validation failure")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("invalid value modified the configuration")
	}
}

func TestSetKeyPreservesComments(t *testing.T) {
	path := writeTestConfig(t, `version: 1
vm:
  # keep this comment
  cpus: 4 # inline
  memory: 8GiB
run:
  targets: {}
`)
	if err := SetKey(path, "vm.cpus", "8", false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	for _, want := range []string{"# keep this comment", "# inline", "cpus: 8"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSetKeyThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.yaml")
	if err := os.WriteFile(realPath, []byte(DefaultYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(realPath, link); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(link, "vm.cpus", "6", false); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VM.CPUs != 6 {
		t.Fatalf("cpus = %d", cfg.VM.CPUs)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config symlink was replaced")
	}
}

func TestConfigKeys(t *testing.T) {
	all := ConfigKeys(false)
	writable := ConfigKeys(true)
	for _, key := range []string{"vm.cpus", "seed.include", "run.default"} {
		if !containsKey(writable, key) {
			t.Fatalf("%s missing from writable keys", key)
		}
	}
	for _, key := range []string{"version", "tools", "vm.ports"} {
		if containsKey(writable, key) {
			t.Fatalf("%s should not be writable", key)
		}
		if !containsKey(all, key) {
			t.Fatalf("%s missing from all keys", key)
		}
	}
	if values := KeyValues("workspace.merge"); len(values) != 2 {
		t.Fatalf("merge values = %v", values)
	}
}

func containsKey(keys []string, want string) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}
