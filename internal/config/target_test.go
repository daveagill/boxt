package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestAddTarget(t *testing.T) {
	path := writeTestConfig(t, DefaultYAML)
	target := RunTarget{Command: []string{"npm", "run", "dev"}, PortBase: 3000}
	if err := AddTarget(path, "dev", target, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Run.Default != "dev" {
		t.Fatalf("default = %q", cfg.Run.Default)
	}
	if got := cfg.Run.Targets["dev"]; got.PortBase != 3000 || len(got.Command) != 3 {
		t.Fatalf("target = %#v", got)
	}
	if err := AddTarget(path, "dev", target, false); err != nil {
		t.Fatalf("identical add: %v", err)
	}
	other := RunTarget{Command: []string{"npm", "test"}}
	var exists *ErrTargetExists
	if err := AddTarget(path, "dev", other, false); !errors.As(err, &exists) {
		t.Fatalf("expected ErrTargetExists, got %v", err)
	}
	if err := AddTarget(path, "Bad Name", target, false); err == nil {
		t.Fatal("invalid target name accepted")
	}
}

func TestRemoveTargetClearsDefault(t *testing.T) {
	path := writeTestConfig(t, `version: 1
run:
  default: dev
  targets:
    dev:
      command: ["npm", "run", "dev"]
    test:
      command: ["npm", "test"]
`)
	wasDefault, err := RemoveTarget(path, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if !wasDefault {
		t.Fatal("default removal not reported")
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Run.Default != "" {
		t.Fatalf("default = %q", cfg.Run.Default)
	}
	if _, ok := cfg.Run.Targets["dev"]; ok {
		t.Fatal("target still present")
	}
	if _, ok := cfg.Run.Targets["test"]; !ok {
		t.Fatal("unrelated target removed")
	}
	wasDefault, err = RemoveTarget(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	if wasDefault {
		t.Fatal("non-default removal reported as default")
	}
	var missing *ErrTargetNotFound
	if _, err := RemoveTarget(path, "nope"); !errors.As(err, &missing) {
		t.Fatalf("expected ErrTargetNotFound, got %v", err)
	}
}

func TestSetAndUnsetDefaultTargetHelpers(t *testing.T) {
	path := writeTestConfig(t, `version: 1
run:
  default: dev
  targets:
    dev:
      command: ["npm", "run", "dev"]
    test:
      command: ["npm", "test"]
`)
	if err := SetDefaultTarget(path, "test"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Run.Default != "test" {
		t.Fatalf("default = %q", cfg.Run.Default)
	}
	var missing *ErrTargetNotFound
	if err := SetDefaultTarget(path, "nope"); !errors.As(err, &missing) {
		t.Fatalf("expected ErrTargetNotFound, got %v", err)
	}
	if err := UnsetDefaultTarget(path); err != nil {
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

func TestAddTargetPreservesCommentsAndUnrelatedKeys(t *testing.T) {
	path := writeTestConfig(t, `version: 1
# keep me
run:
  targets:
    dev:
      command: ["npm", "run", "dev"]
  # unrelated
  default: dev
tools:
  node: "lts"
`)
	if err := AddTarget(path, "test", RunTarget{Command: []string{"npm", "test"}}, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	for _, want := range []string{"# keep me", "# unrelated", "node:", "lts", "test:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}
