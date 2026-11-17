package config

import (
	"errors"
	"fmt"
	"net"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	toolNameRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]*$`)
	toolVersionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	targetNameRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	portEnvRE     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	memoryRE      = regexp.MustCompile(`^([1-9][0-9]*)(MiB|GiB)$`)
)

// MaxPortBase is the highest port_base that still leaves room for every
// per-workspace offset without exceeding port 65535.
const MaxPortBase = 65535 - 999

// FieldError is one validation failure with a stable field path.
type FieldError struct {
	Path    string
	Message string
}

func (e FieldError) String() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

// ValidationError aggregates all configuration problems.
type ValidationError struct {
	Errors []FieldError
}

func (e *ValidationError) Error() string {
	if len(e.Errors) == 0 {
		return "invalid configuration"
	}
	if len(e.Errors) == 1 {
		return "invalid configuration: " + e.Errors[0].String()
	}
	var b strings.Builder
	b.WriteString("invalid configuration:")
	for _, fe := range e.Errors {
		b.WriteString("\n  - ")
		b.WriteString(fe.String())
	}
	return b.String()
}

type validator struct {
	errs []FieldError
}

func (v *validator) add(path, format string, args ...any) {
	v.errs = append(v.errs, FieldError{Path: path, Message: fmt.Sprintf(format, args...)})
}

// Validate checks required fields, supported versions and value ranges.
func (c *Config) Validate() error {
	v := &validator{}

	if c.Version == 0 {
		v.add("version", "is required")
	} else if c.Version != CurrentVersion {
		v.add("version", "unsupported version %d (supported: %d)", c.Version, CurrentVersion)
	}

	switch c.VM.Image {
	case DefaultImage:
	case "":
		v.add("vm.image", "is required")
	default:
		v.add("vm.image", "unsupported image %q (supported: %s)", c.VM.Image, DefaultImage)
	}

	if c.VM.CPUs < 1 || c.VM.CPUs > 64 {
		v.add("vm.cpus", "must be between 1 and 64")
	}
	if _, err := parseMemory(c.VM.Memory); err != nil {
		v.add("vm.memory", "%s", err)
	}
	switch c.VM.PortForwarding {
	case PortForwardingAuto, PortForwardingExplicit:
	default:
		v.add("vm.port_forwarding", "must be %q or %q", PortForwardingAuto, PortForwardingExplicit)
	}

	validateMounts(v, c.VM.Mounts)

	switch c.Workspace.Merge {
	case MergeModeFF, MergeModeRebase:
	default:
		v.add("workspace.merge", "must be %q or %q", MergeModeFF, MergeModeRebase)
	}

	seenHostPorts := map[string]int{}
	seenGuestPorts := map[int]int{}
	for i, p := range c.VM.Ports {
		path := fmt.Sprintf("vm.ports[%d]", i)
		if p.Guest < 1 || p.Guest > 65535 {
			v.add(path+".guest", "must be between 1 and 65535")
		}
		if p.Guest == 22 {
			v.add(path+".guest", "port 22 is reserved for Lima SSH")
		}
		if p.Host < 1 || p.Host > 65535 {
			v.add(path+".host", "must be between 1 and 65535")
		}
		ip := p.HostIP
		if ip == "" {
			ip = "127.0.0.1"
		}
		if net.ParseIP(ip) == nil {
			v.add(path+".host_ip", "invalid IP address %q", ip)
		}
		if p.Host >= 1 && p.Host <= 65535 {
			key := net.JoinHostPort(ip, strconv.Itoa(p.Host))
			if prev, ok := seenHostPorts[key]; ok {
				v.add(path+".host", "duplicates vm.ports[%d] host binding %s", prev, key)
			} else {
				seenHostPorts[key] = i
			}
		}
		if p.Guest >= 1 && p.Guest <= 65535 {
			if prev, ok := seenGuestPorts[p.Guest]; ok {
				v.add(path+".guest", "duplicates vm.ports[%d] guest port %d", prev, p.Guest)
			} else {
				seenGuestPorts[p.Guest] = i
			}
		}
	}

	for _, name := range c.ToolNames() {
		if !toolNameRE.MatchString(name) {
			v.add("tools."+name, "invalid tool name (use lowercase letters, digits, '.', '_', '+' or '-')")
		}
		version := c.Tools[name]
		switch {
		case version == "":
			v.add("tools."+name, "version is required")
		case !toolVersionRE.MatchString(version):
			v.add("tools."+name, "invalid version %q", version)
		}
	}

	for _, name := range c.TargetNames() {
		path := "run.targets." + name
		if !targetNameRE.MatchString(name) {
			v.add(path, "invalid target name (use letters, digits, '.', '_' or '-')")
		}
		target := c.Run.Targets[name]
		if len(target.Command) == 0 {
			v.add(path+".command", "must contain at least one argument")
		} else {
			for i, arg := range target.Command {
				if strings.ContainsRune(arg, 0) {
					v.add(fmt.Sprintf("%s.command[%d]", path, i), "must not contain NUL")
				}
				if i == 0 && arg == "" {
					v.add(path+".command[0]", "executable must not be empty")
				}
			}
		}
		if target.PortBase < 0 {
			v.add(path+".port_base", "must be between 1 and %d", MaxPortBase)
		} else if target.PortBase > MaxPortBase {
			v.add(path+".port_base", "must be between 1 and %d so per-workspace offsets stay below port 65535", MaxPortBase)
		}
		if target.PortEnv != "" {
			if target.PortBase == 0 {
				v.add(path+".port_env", "requires port_base")
			}
			if !portEnvRE.MatchString(target.PortEnv) {
				v.add(path+".port_env", "invalid environment variable name %q", target.PortEnv)
			}
		}
	}

	if c.Run.Default != "" {
		if _, ok := c.Run.Targets[c.Run.Default]; !ok {
			v.add("run.default", "unknown target %q", c.Run.Default)
		}
	}

	if c.Hooks != nil {
		for _, event := range []struct {
			name string
			set  HookSet
		}{
			{HookWorkspaceCreate, c.Hooks.WorkspaceCreate},
			{HookWorkspaceMerge, c.Hooks.WorkspaceMerge},
			{HookWorkspaceRemove, c.Hooks.WorkspaceRemove},
		} {
			validateHookSet(v, event.name, "pre", event.set.Pre)
			validateHookSet(v, event.name, "post", event.set.Post)
		}
	}

	if c.Seed != nil {
		validateSeedPatterns(v, "seed.include", c.Seed.Include)
		validateSeedPatterns(v, "seed.exclude", c.Seed.Exclude)
	}

	if len(v.errs) == 0 {
		return nil
	}
	sort.SliceStable(v.errs, func(i, j int) bool { return v.errs[i].Path < v.errs[j].Path })
	return &ValidationError{Errors: v.errs}
}

func validateHookSet(v *validator, event, phase string, hooks []Hook) {
	for i, hook := range hooks {
		path := fmt.Sprintf("hooks.%s.%s[%d]", event, phase, i)
		if len(hook.Command) == 0 {
			v.add(path+".command", "must contain at least one argument")
		} else {
			for j, arg := range hook.Command {
				if strings.ContainsRune(arg, 0) {
					v.add(fmt.Sprintf("%s.command[%d]", path, j), "must not contain NUL")
				}
				if j == 0 && arg == "" {
					v.add(path+".command[0]", "executable must not be empty")
				}
			}
		}
		switch hook.Where {
		case "", HookWhereGuest, HookWhereHost:
		default:
			v.add(path+".where", "must be %q or %q", HookWhereGuest, HookWhereHost)
		}
	}
}

func validateSeedPatterns(v *validator, field string, patterns []string) {
	for i, pattern := range patterns {
		if _, err := NormalizeSeedPattern(pattern); err != nil {
			v.add(fmt.Sprintf("%s[%d]", field, i), "%s", err)
		}
	}
}

// NormalizeSeedPattern validates a root-relative, slash-based seed pattern and
// returns its canonical form. Patterns must be non-empty, relative, free of NUL
// and '..' path segments, and use valid glob syntax. A trailing slash marks a
// directory pattern and is preserved.
func NormalizeSeedPattern(pattern string) (string, error) {
	if pattern == "" {
		return "", errors.New("must not be empty")
	}
	if strings.ContainsRune(pattern, 0) {
		return "", errors.New("must not contain NUL")
	}
	if strings.ContainsRune(pattern, '\\') {
		return "", errors.New("must use '/' separators; backslash escapes are not supported")
	}
	if isAbsoluteSeedPattern(pattern) {
		return "", errors.New("must be relative")
	}

	trailingSlash := strings.HasSuffix(pattern, "/")
	trimmed := strings.TrimSuffix(pattern, "/")
	if trimmed == "" {
		return "", errors.New("must not be empty")
	}

	parts := strings.Split(trimmed, "/")
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "":
			return "", errors.New("must not contain empty path segments")
		case ".":
			continue
		case "..":
			return "", errors.New("must not contain '..' path segments")
		}
		if _, err := path.Match(part, ""); err != nil {
			return "", fmt.Errorf("invalid glob syntax in %q", part)
		}
		normalized = append(normalized, part)
	}
	if len(normalized) == 0 {
		return "", errors.New("must not be empty")
	}
	result := strings.Join(normalized, "/")
	if trailingSlash {
		result += "/"
	}
	return result, nil
}

// ValidateSeedPattern reports whether pattern is an acceptable seed pattern.
func ValidateSeedPattern(pattern string) error {
	_, err := NormalizeSeedPattern(pattern)
	return err
}

func isAbsoluteSeedPattern(pattern string) bool {
	if path.IsAbs(pattern) {
		return true
	}
	// Reject Windows drive prefixes on every host so a shared config behaves
	// the same when the project moves between operating systems.
	return len(pattern) >= 2 && pattern[1] == ':'
}

// MemoryBytes validates and converts a memory size such as 8GiB to bytes.
func MemoryBytes(s string) (int64, error) { return parseMemory(s) }

func parseMemory(s string) (int64, error) {
	m := memoryRE.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("must be a size such as 512MiB or 8GiB")
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("must be a valid size")
	}
	mult := int64(1024 * 1024)
	if m[2] == "GiB" {
		mult = 1024 * 1024 * 1024
	}
	const maxBytes = int64(256) * 1024 * 1024 * 1024
	if n > maxBytes/mult {
		return 0, fmt.Errorf("must be at most 256GiB")
	}
	bytes := n * mult
	if bytes < 512*1024*1024 {
		return 0, fmt.Errorf("must be at least 512MiB")
	}
	return bytes, nil
}

// ValidToolName reports whether name is an acceptable boxt tool identifier.
func ValidToolName(name string) bool { return toolNameRE.MatchString(name) }

// ValidTargetName reports whether name is an acceptable run-target name.
func ValidTargetName(name string) bool { return targetNameRE.MatchString(name) }

// ValidToolVersion reports whether version is an acceptable version string.
func ValidToolVersion(version string) bool { return toolVersionRE.MatchString(version) }

// ParseToolSpec splits a name@version declaration at the last '@'.
func ParseToolSpec(spec string) (name, version string, err error) {
	i := strings.LastIndex(spec, "@")
	if i < 1 || i == len(spec)-1 {
		return "", "", fmt.Errorf("invalid tool %q: expected name@version", spec)
	}
	name, version = spec[:i], spec[i+1:]
	if !ValidToolName(name) {
		return "", "", fmt.Errorf("invalid tool name %q", name)
	}
	if !ValidToolVersion(version) {
		return "", "", fmt.Errorf("invalid tool version %q", version)
	}
	return name, version, nil
}
