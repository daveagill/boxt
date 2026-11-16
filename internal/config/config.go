// Package config implements loading, validating and atomically editing the
// project configuration stored in .boxt/config.yaml.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/daveagill/boxt/internal/fsutil"
)

const (
	// CurrentVersion is the only configuration version supported by this MVP.
	CurrentVersion = 1
	// DefaultImage is the only guest image supported by the MVP.
	DefaultImage = "ubuntu-24.04"

	DefaultCPUs   = 4
	DefaultMemory = "8GiB"

	// PortForwardingAuto lets Lima forward guest loopback services to host
	// loopback automatically. PortForwardingExplicit forwards only declared
	// ports and ignores everything else.
	PortForwardingAuto     = "auto"
	PortForwardingExplicit = "explicit"
)

// ErrConfigExists is returned when initialization would overwrite a project.
var ErrConfigExists = errors.New("project configuration already exists")

// Config is the root of .boxt/config.yaml.
type Config struct {
	Version int               `yaml:"version"`
	VM      VMConfig          `yaml:"vm"`
	Seed    *SeedConfig       `yaml:"seed,omitempty"`
	Tools   map[string]string `yaml:"tools,omitempty"`
	Run     RunConfig         `yaml:"run"`
	Hooks   *HooksConfig      `yaml:"hooks,omitempty"`
}

// SeedConfig controls which Git-ignored files may be copied into a workspace
// and whether removal may discard ignored files that match the same rules.
type SeedConfig struct {
	Include     []string `yaml:"include,omitempty"`
	Exclude     []string `yaml:"exclude,omitempty"`
	AllowRemove bool     `yaml:"allow_remove,omitempty"`
}

// SeedRules returns the configured seed rules, or empty rules when no seed
// section is present. Empty rules keep the historical copy-all-ignored behavior
// and never allow removal without --force.
func (c *Config) SeedRules() SeedConfig {
	if c == nil || c.Seed == nil {
		return SeedConfig{}
	}
	return *c.Seed
}

// Hook lifecycle events.
const (
	HookWorkspaceCreate = "workspace_create"
	HookWorkspaceMerge  = "workspace_merge"
	HookWorkspaceRemove = "workspace_remove"

	HookWhereGuest = "guest"
	HookWhereHost  = "host"
)

// HooksConfig holds pre/post hooks for workspace lifecycle events.
type HooksConfig struct {
	WorkspaceCreate HookSet `yaml:"workspace_create,omitempty"`
	WorkspaceMerge  HookSet `yaml:"workspace_merge,omitempty"`
	WorkspaceRemove HookSet `yaml:"workspace_remove,omitempty"`
}

// HookSet is the pre/post pair for one event.
type HookSet struct {
	Pre  []Hook `yaml:"pre,omitempty"`
	Post []Hook `yaml:"post,omitempty"`
}

// Hook is one argv command run on the host or in the guest.
type Hook struct {
	Command []string `yaml:"command"`
	Where   string   `yaml:"where,omitempty"`
}

// HooksFor returns the hook set for an event. Unknown events return an empty set.
func (c *Config) HooksFor(event string) HookSet {
	if c == nil || c.Hooks == nil {
		return HookSet{}
	}
	switch event {
	case HookWorkspaceCreate:
		return c.Hooks.WorkspaceCreate
	case HookWorkspaceMerge:
		return c.Hooks.WorkspaceMerge
	case HookWorkspaceRemove:
		return c.Hooks.WorkspaceRemove
	default:
		return HookSet{}
	}
}

// HasHooks reports whether any lifecycle hooks are configured.
func (c *Config) HasHooks() bool {
	for _, event := range []string{HookWorkspaceCreate, HookWorkspaceMerge, HookWorkspaceRemove} {
		set := c.HooksFor(event)
		if len(set.Pre) > 0 || len(set.Post) > 0 {
			return true
		}
	}
	return false
}

// VMConfig describes the persistent project VM.
type VMConfig struct {
	Image          string        `yaml:"image"`
	CPUs           int           `yaml:"cpus"`
	Memory         string        `yaml:"memory"`
	PortForwarding string        `yaml:"port_forwarding,omitempty"`
	AutoStart      *bool         `yaml:"auto_start,omitempty"`
	Ports          []PortForward `yaml:"ports,omitempty"`
}

// PortForward requests a controlled guest-to-host port forward. It is
// default-off; boxt never exposes a guest service unless it is listed here.
type PortForward struct {
	Guest  int    `yaml:"guest"`
	Host   int    `yaml:"host"`
	HostIP string `yaml:"host_ip,omitempty"`
}

// RunConfig contains the default target and all named targets.
type RunConfig struct {
	Default string               `yaml:"default,omitempty"`
	Targets map[string]RunTarget `yaml:"targets,omitempty"`
}

// RunTarget is a named executable and argument list executed in the guest.
type RunTarget struct {
	Command     []string `yaml:"command"`
	Interactive bool     `yaml:"interactive,omitempty"`
	// PortBase, when set, asks boxt to inject PortEnv with a deterministic
	// per-workspace offset applied. The guest service must honor the variable.
	PortBase int    `yaml:"port_base,omitempty"`
	PortEnv  string `yaml:"port_env,omitempty"`
}

// Default returns a fully populated default configuration.
func Default() *Config {
	return &Config{
		Version: CurrentVersion,
		VM: VMConfig{
			Image:          DefaultImage,
			CPUs:           DefaultCPUs,
			Memory:         DefaultMemory,
			PortForwarding: PortForwardingAuto,
			AutoStart:      boolPtr(true),
		},
		Tools: map[string]string{},
		Run: RunConfig{
			Targets: map[string]RunTarget{},
		},
	}
}

func boolPtr(v bool) *bool { return &v }

// AutoStartEnabled reports whether run/shell commands may create or start the
// VM automatically. Omitting auto_start means enabled.
func (v VMConfig) AutoStartEnabled() bool {
	return v.AutoStart == nil || *v.AutoStart
}

// DefaultYAML is the initial project file written by boxt init. It is kept as
// a readable template; Load(DefaultYAML) must equal Default().
const DefaultYAML = `# boxt project configuration.
# This file is the source of truth for this project's VM, tools and run targets.
version: 1

vm:
  image: ubuntu-24.04
  cpus: 4
  memory: 8GiB
  # auto: forward guest loopback services to host loopback (vite, servers)
  # explicit: forward only the ports listed under ports
  port_forwarding: auto
  # create/start the VM automatically before run and shell commands
  auto_start: true

# Tools are installed into the project VM. Example:
# tools:
#   node: "lts"
tools: {}

run:
  # default: dev
  targets: {}
`

// ApplyDefaults fills omitted optional fields with sensible defaults. It is
// called by Load before validation.
func (c *Config) ApplyDefaults() {
	if c.VM.Image == "" {
		c.VM.Image = DefaultImage
	}
	if c.VM.CPUs == 0 {
		c.VM.CPUs = DefaultCPUs
	}
	if c.VM.Memory == "" {
		c.VM.Memory = DefaultMemory
	}
	if c.VM.PortForwarding == "" {
		c.VM.PortForwarding = PortForwardingAuto
	}
	if c.VM.AutoStart == nil {
		c.VM.AutoStart = boolPtr(true)
	}
	if c.Tools == nil {
		c.Tools = map[string]string{}
	}
	if c.Run.Targets == nil {
		c.Run.Targets = map[string]RunTarget{}
	}
	for i := range c.VM.Ports {
		if c.VM.Ports[i].HostIP == "" {
			c.VM.Ports[i].HostIP = "127.0.0.1"
		}
	}
	if c.Hooks != nil {
		normalizeHooks(c.Hooks)
	}
}

func normalizeHooks(h *HooksConfig) {
	for _, set := range []*HookSet{&h.WorkspaceCreate, &h.WorkspaceMerge, &h.WorkspaceRemove} {
		for i := range set.Pre {
			if set.Pre[i].Where == "" {
				set.Pre[i].Where = HookWhereGuest
			}
		}
		for i := range set.Post {
			if set.Post[i].Where == "" {
				set.Post[i].Where = HookWhereGuest
			}
		}
	}
}

// Load reads and validates a configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read configuration: %w", err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes and validates configuration bytes. Unknown fields are
// rejected so typos never silently change behavior.
func Parse(data []byte) (*Config, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		return nil, errors.New("invalid YAML: multiple documents are not supported")
	case !errors.Is(err, io.EOF):
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ToolNames returns declared tool names in stable order.
func (c *Config) ToolNames() []string {
	names := make([]string, 0, len(c.Tools))
	for name := range c.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TargetNames returns configured target names in stable order.
func (c *Config) TargetNames() []string {
	names := make([]string, 0, len(c.Run.Targets))
	for name := range c.Run.Targets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ResolveTarget looks up a named target. The boolean is false when the target
// is not configured; callers must never fall back to PATH.
func (c *Config) ResolveTarget(name string) (RunTarget, bool) {
	t, ok := c.Run.Targets[name]
	return t, ok
}

// Create writes a new default configuration. It never overwrites an existing
// project configuration.
func Create(path string) error {
	return CreateData(path, []byte(DefaultYAML))
}

// CreateWith validates and writes a generated configuration. It never
// overwrites an existing project configuration.
func CreateWith(path string, cfg *Config, header string) error {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := MarshalWithHeader(cfg, header)
	if err != nil {
		return err
	}
	return CreateData(path, data)
}

// CreateData atomically creates a configuration file and fails if it already
// exists.
func CreateData(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create project directory: %w", err)
	}
	if err := fsutil.WriteFileExclusive(path, data, 0o644); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrConfigExists
		}
		return err
	}
	return nil
}

// Marshal serializes a configuration with two-space indentation.
func Marshal(cfg *Config) ([]byte, error) {
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return nil, fmt.Errorf("encode configuration: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode configuration: %w", err)
	}
	return []byte(b.String()), nil
}

// MarshalWithHeader serializes a configuration with an optional comment header.
func MarshalWithHeader(cfg *Config, header string) ([]byte, error) {
	body, err := Marshal(cfg)
	if err != nil {
		return nil, err
	}
	header = strings.TrimSpace(header)
	if header == "" {
		return body, nil
	}
	return []byte(header + "\n\n" + string(body)), nil
}
