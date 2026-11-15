// Package preset provides static, composable starter configurations for
// boxt init. Presets are one-shot configuration fragments, not runtime state.
package preset

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/daveagill/boxt/internal/config"
)

// Category is documentation metadata for a preset.
type Category string

const (
	CategoryStack Category = "stack"
	CategoryAgent Category = "agent"
)

// Preset is a starter configuration fragment.
type Preset struct {
	Name          string
	Category      Category
	Summary       string
	Tools         map[string]string
	Targets       map[string]config.RunTarget
	DefaultTarget string
	Ports         []config.PortForward
}

var builtins = []Preset{
	{
		Name:     "minimal",
		Category: CategoryStack,
		Summary:  "VM defaults only",
	},
	{
		Name:     "node",
		Category: CategoryStack,
		Summary:  "Node 24 with dev/test targets",
		Tools:    map[string]string{"node": "24"},
		Targets: map[string]config.RunTarget{
			"dev":  {Command: []string{"npm", "run", "dev"}},
			"test": {Command: []string{"npm", "test"}},
		},
		DefaultTarget: "dev",
	},
	{
		Name:     "python",
		Category: CategoryStack,
		Summary:  "Python 3.13 with an interactive python target",
		Tools:    map[string]string{"python": "3.13"},
		Targets: map[string]config.RunTarget{
			"python": {Command: []string{"python"}, Interactive: true},
		},
	},
	{
		Name:     "go",
		Category: CategoryStack,
		Summary:  "Go 1.24 with build/test targets",
		Tools:    map[string]string{"go": "1.24"},
		Targets: map[string]config.RunTarget{
			"build": {Command: []string{"go", "build", "./..."}},
			"test":  {Command: []string{"go", "test", "./..."}},
		},
	},
	{
		Name:     "rust",
		Category: CategoryStack,
		Summary:  "Rust with build/test targets",
		Tools:    map[string]string{"rust": "latest"},
		Targets: map[string]config.RunTarget{
			"build": {Command: []string{"cargo", "build"}},
			"test":  {Command: []string{"cargo", "test"}},
		},
	},
	{
		Name:          "opencode",
		Category:      CategoryAgent,
		Summary:       "OpenCode with Node, interactive target",
		Tools:         map[string]string{"node": "24", "opencode": "latest"},
		Targets:       map[string]config.RunTarget{"opencode": {Command: []string{"opencode"}, Interactive: true}},
		DefaultTarget: "opencode",
	},
	{
		Name:          "codex",
		Category:      CategoryAgent,
		Summary:       "Codex CLI, interactive target",
		Tools:         map[string]string{"codex": "latest"},
		Targets:       map[string]config.RunTarget{"codex": {Command: []string{"codex"}, Interactive: true}},
		DefaultTarget: "codex",
	},
	{
		Name:          "claude",
		Category:      CategoryAgent,
		Summary:       "Claude Code, interactive target",
		Tools:         map[string]string{"claude": "latest"},
		Targets:       map[string]config.RunTarget{"claude": {Command: []string{"claude"}, Interactive: true}},
		DefaultTarget: "claude",
	},
}

// All returns the built-in presets in help order.
func All() []Preset {
	out := make([]Preset, 0, len(builtins))
	for _, p := range builtins {
		out = append(out, clone(p))
	}
	return out
}

// Names returns the built-in preset names in help order.
func Names() []string {
	out := make([]string, 0, len(builtins))
	for _, p := range builtins {
		out = append(out, p.Name)
	}
	return out
}

// Lookup returns a clone of a built-in preset.
func Lookup(name string) (Preset, bool) {
	for _, p := range builtins {
		if p.Name == name {
			return clone(p), true
		}
	}
	return Preset{}, false
}

// HelpText describes the available presets for command help.
func HelpText() string {
	var b strings.Builder
	b.WriteString("Presets (repeat --preset to compose):")
	for _, p := range builtins {
		fmt.Fprintf(&b, "\n  %-10s %s", p.Name, p.Summary)
	}
	return b.String()
}

func clone(p Preset) Preset {
	out := p
	out.Tools = make(map[string]string, len(p.Tools))
	for k, v := range p.Tools {
		out.Tools[k] = v
	}
	out.Targets = make(map[string]config.RunTarget, len(p.Targets))
	for k, v := range p.Targets {
		out.Targets[k] = config.RunTarget{
			Command:     append([]string(nil), v.Command...),
			Interactive: v.Interactive,
		}
	}
	out.Ports = append([]config.PortForward(nil), p.Ports...)
	return out
}

// DefaultSuggestion records a preset's preferred default target.
type DefaultSuggestion struct {
	Preset string
	Target string
}

// Result is the merged configuration produced by a preset selection.
type Result struct {
	Config        *config.Config
	Presets       []string
	Suggestions   []DefaultSuggestion
	DefaultTarget string
}

// UnknownError reports an unknown preset name.
type UnknownError struct{ Name string }

func (e *UnknownError) Error() string { return fmt.Sprintf("unknown preset %q", e.Name) }
func (e *UnknownError) Hint() string  { return "Available presets: " + strings.Join(Names(), ", ") }

// ConflictError reports two presets that cannot be composed.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return "preset conflict: " + e.Message }

// DefaultTargetError reports an explicit default target that the merged
// configuration does not define.
type DefaultTargetError struct {
	Name      string
	Available []string
}

func (e *DefaultTargetError) Error() string {
	return fmt.Sprintf("default target %q is not defined by the selected presets", e.Name)
}
func (e *DefaultTargetError) Hint() string {
	if len(e.Available) == 0 {
		return "The selected presets define no run targets."
	}
	return "Available targets: " + strings.Join(e.Available, ", ")
}

// Resolve validates names and merges them into a new configuration.
func Resolve(names []string, defaultTarget string) (*Result, error) {
	presets := make([]Preset, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		p, ok := Lookup(name)
		if !ok {
			return nil, &UnknownError{Name: name}
		}
		seen[name] = true
		presets = append(presets, p)
	}
	return merge(presets, defaultTarget)
}

func merge(presets []Preset, defaultTarget string) (*Result, error) {
	cfg := config.Default()
	result := &Result{Config: cfg}
	toolOwner := map[string]string{}
	targetOwner := map[string]string{}

	for _, p := range presets {
		result.Presets = append(result.Presets, p.Name)
		for _, name := range sortedKeys(p.Tools) {
			version := p.Tools[name]
			if existing, ok := cfg.Tools[name]; ok && existing != version {
				return nil, &ConflictError{Message: fmt.Sprintf(
					"preset %s requires %s@%s but preset %s requires %s@%s",
					p.Name, name, version, toolOwner[name], name, existing,
				)}
			}
			cfg.Tools[name] = version
			if _, ok := toolOwner[name]; !ok {
				toolOwner[name] = p.Name
			}
		}
		for _, name := range sortedKeys(p.Targets) {
			target := p.Targets[name]
			if existing, ok := cfg.Run.Targets[name]; ok && !reflect.DeepEqual(existing, target) {
				return nil, &ConflictError{Message: fmt.Sprintf(
					"preset %s and preset %s define run target %q differently",
					targetOwner[name], p.Name, name,
				)}
			}
			cfg.Run.Targets[name] = target
			if _, ok := targetOwner[name]; !ok {
				targetOwner[name] = p.Name
			}
		}
		cfg.VM.Ports = append(cfg.VM.Ports, p.Ports...)
		if p.DefaultTarget != "" {
			result.Suggestions = append(result.Suggestions, DefaultSuggestion{
				Preset: p.Name,
				Target: p.DefaultTarget,
			})
		}
	}

	for _, s := range result.Suggestions {
		if _, ok := cfg.Run.Targets[s.Target]; !ok {
			return nil, &ConflictError{Message: fmt.Sprintf(
				"preset %s suggests default target %q, but does not define it",
				s.Preset, s.Target,
			)}
		}
	}
	sort.Slice(cfg.VM.Ports, func(i, j int) bool {
		if cfg.VM.Ports[i].Guest != cfg.VM.Ports[j].Guest {
			return cfg.VM.Ports[i].Guest < cfg.VM.Ports[j].Guest
		}
		if cfg.VM.Ports[i].Host != cfg.VM.Ports[j].Host {
			return cfg.VM.Ports[i].Host < cfg.VM.Ports[j].Host
		}
		return cfg.VM.Ports[i].HostIP < cfg.VM.Ports[j].HostIP
	})

	if defaultTarget != "" {
		if _, ok := cfg.Run.Targets[defaultTarget]; !ok {
			return nil, &DefaultTargetError{Name: defaultTarget, Available: targetNames(cfg)}
		}
		cfg.Run.Default = defaultTarget
		result.DefaultTarget = defaultTarget
	} else {
		distinct := map[string]bool{}
		for _, s := range result.Suggestions {
			distinct[s.Target] = true
		}
		if len(distinct) == 1 {
			for target := range distinct {
				cfg.Run.Default = target
				result.DefaultTarget = target
			}
		}
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid preset configuration: %w", err)
	}
	return result, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func targetNames(cfg *config.Config) []string {
	return cfg.TargetNames()
}
