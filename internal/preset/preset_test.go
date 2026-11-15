package preset

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/config"
)

func TestCatalog(t *testing.T) {
	want := []string{"minimal", "node", "python", "go", "rust", "opencode", "codex", "claude"}
	if got := Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %#v, want %#v", got, want)
	}
	if len(All()) != len(want) {
		t.Fatalf("All() returned %d presets", len(All()))
	}
	p, ok := Lookup("opencode")
	if !ok || p.Category != CategoryAgent || p.Tools["node"] != "lts" || p.Tools["opencode"] != "latest" {
		t.Fatalf("unexpected opencode preset: %#v", p)
	}
	p.Tools["node"] = "99"
	again, _ := Lookup("opencode")
	if again.Tools["node"] != "lts" {
		t.Fatal("Lookup did not return a clone")
	}
	if !strings.Contains(HelpText(), "opencode") || !strings.Contains(HelpText(), "node") {
		t.Fatalf("unexpected help: %s", HelpText())
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("unknown preset found")
	}
}

func TestResolveOpencode(t *testing.T) {
	result, err := Resolve([]string{"opencode"}, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := result.Config
	if cfg.Tools["node"] != "lts" || cfg.Tools["opencode"] != "latest" {
		t.Fatalf("tools = %#v", cfg.Tools)
	}
	target, ok := cfg.Run.Targets["opencode"]
	if !ok || !target.Interactive || !reflect.DeepEqual(target.Command, []string{"opencode"}) {
		t.Fatalf("target = %#v", target)
	}
	if cfg.Run.Default != "opencode" || result.DefaultTarget != "opencode" {
		t.Fatalf("default = %q / %q", cfg.Run.Default, result.DefaultTarget)
	}
	if !reflect.DeepEqual(result.Presets, []string{"opencode"}) {
		t.Fatalf("presets = %#v", result.Presets)
	}
}

func TestResolveNode(t *testing.T) {
	result, err := Resolve([]string{"node"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.Run.Default != "dev" {
		t.Fatalf("default = %q", result.Config.Run.Default)
	}
	if len(result.Config.TargetNames()) != 2 {
		t.Fatalf("targets = %#v", result.Config.Run.Targets)
	}
}

func TestResolveCompositionHasNoDefault(t *testing.T) {
	result, err := Resolve([]string{"node", "opencode"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.Run.Default != "" || result.DefaultTarget != "" {
		t.Fatalf("default should be empty: %#v", result)
	}
	if len(result.Suggestions) != 2 {
		t.Fatalf("suggestions = %#v", result.Suggestions)
	}
	if _, ok := result.Config.Run.Targets["dev"]; !ok {
		t.Fatal("missing node dev target")
	}
	if _, ok := result.Config.Run.Targets["opencode"]; !ok {
		t.Fatal("missing opencode target")
	}
}

func TestResolveExplicitDefault(t *testing.T) {
	for _, defaultTarget := range []string{"dev", "opencode"} {
		result, err := Resolve([]string{"node", "opencode"}, defaultTarget)
		if err != nil {
			t.Fatalf("%s: %v", defaultTarget, err)
		}
		if result.Config.Run.Default != defaultTarget || result.DefaultTarget != defaultTarget {
			t.Fatalf("%s: default = %q/%q", defaultTarget, result.Config.Run.Default, result.DefaultTarget)
		}
	}
}

func TestResolveInvalidDefault(t *testing.T) {
	_, err := Resolve([]string{"node"}, "opencode")
	var targetErr *DefaultTargetError
	if !errors.As(err, &targetErr) {
		t.Fatalf("expected DefaultTargetError, got %v", err)
	}
	if !strings.Contains(targetErr.Hint(), "dev") {
		t.Fatalf("hint = %q", targetErr.Hint())
	}
}

func TestResolveNoTargetsWithDefault(t *testing.T) {
	_, err := Resolve([]string{"minimal"}, "dev")
	var targetErr *DefaultTargetError
	if !errors.As(err, &targetErr) {
		t.Fatalf("expected DefaultTargetError, got %v", err)
	}
	if !strings.Contains(targetErr.Hint(), "no run targets") {
		t.Fatalf("hint = %q", targetErr.Hint())
	}
}

func TestResolveUnknownPreset(t *testing.T) {
	_, err := Resolve([]string{"node", "nope"}, "")
	var unknown *UnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected UnknownError, got %v", err)
	}
	if !strings.Contains(unknown.Hint(), "opencode") {
		t.Fatalf("hint = %q", unknown.Hint())
	}
}

func TestResolveDuplicatePreset(t *testing.T) {
	result, err := Resolve([]string{"node", "node", "node"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Presets, []string{"node"}) {
		t.Fatalf("presets = %#v", result.Presets)
	}
}

func TestResolveOrderIndependent(t *testing.T) {
	a, err := Resolve([]string{"node", "opencode"}, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Resolve([]string{"opencode", "node"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Config, b.Config) || a.DefaultTarget != b.DefaultTarget {
		t.Fatalf("order changed result:\na=%#v\nb=%#v", a, b)
	}
}

func TestMergeToolConflict(t *testing.T) {
	_, err := merge([]Preset{
		{Name: "one", Tools: map[string]string{"node": "22"}},
		{Name: "two", Tools: map[string]string{"node": "24"}},
	}, "")
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !strings.Contains(err.Error(), "one") || !strings.Contains(err.Error(), "two") {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestMergeTargetConflict(t *testing.T) {
	_, err := merge([]Preset{
		{Name: "one", Targets: map[string]config.RunTarget{"test": {Command: []string{"one"}}}},
		{Name: "two", Targets: map[string]config.RunTarget{"test": {Command: []string{"two"}}}},
	}, "")
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestMergeSameTargetIsAllowed(t *testing.T) {
	target := config.RunTarget{Command: []string{"test"}}
	result, err := merge([]Preset{
		{Name: "one", Targets: map[string]config.RunTarget{"test": target}},
		{Name: "two", Targets: map[string]config.RunTarget{"test": target}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Config.Run.Targets) != 1 {
		t.Fatalf("targets = %#v", result.Config.Run.Targets)
	}
}

func TestMergeMissingSuggestedDefault(t *testing.T) {
	_, err := merge([]Preset{{Name: "bad", DefaultTarget: "nope"}}, "")
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestMergeSameSuggestedDefault(t *testing.T) {
	target := map[string]config.RunTarget{"dev": {Command: []string{"dev"}}}
	result, err := merge([]Preset{
		{Name: "one", Targets: target, DefaultTarget: "dev"},
		{Name: "two", Targets: target, DefaultTarget: "dev"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.Run.Default != "dev" {
		t.Fatalf("default = %q", result.Config.Run.Default)
	}
}

func TestMergeMinimal(t *testing.T) {
	result, err := Resolve([]string{"minimal"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Config.Tools) != 0 || len(result.Config.Run.Targets) != 0 || result.Config.Run.Default != "" {
		t.Fatalf("minimal config = %#v", result.Config)
	}
	if result.Config.VM.Image != config.DefaultImage {
		t.Fatalf("image = %q", result.Config.VM.Image)
	}
}
