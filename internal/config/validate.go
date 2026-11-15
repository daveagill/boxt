package config

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	toolNameRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]*$`)
	toolVersionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	targetNameRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	memoryRE      = regexp.MustCompile(`^([1-9][0-9]*)(MiB|GiB)$`)
)

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

	seenHostPorts := map[string]int{}
	for i, p := range c.VM.Ports {
		path := fmt.Sprintf("vm.ports[%d]", i)
		if p.Guest < 1 || p.Guest > 65535 {
			v.add(path+".guest", "must be between 1 and 65535")
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
	}

	if c.Run.Default != "" {
		if _, ok := c.Run.Targets[c.Run.Default]; !ok {
			v.add("run.default", "unknown target %q", c.Run.Default)
		}
	}

	if len(v.errs) == 0 {
		return nil
	}
	sort.SliceStable(v.errs, func(i, j int) bool { return v.errs[i].Path < v.errs[j].Path })
	return &ValidationError{Errors: v.errs}
}

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
	bytes := n * mult
	if bytes < 512*1024*1024 {
		return 0, fmt.Errorf("must be at least 512MiB")
	}
	if bytes > 256*1024*1024*1024 {
		return 0, fmt.Errorf("must be at most 256GiB")
	}
	return bytes, nil
}

// ValidToolName reports whether name is an acceptable boxt tool identifier.
func ValidToolName(name string) bool { return toolNameRE.MatchString(name) }

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
