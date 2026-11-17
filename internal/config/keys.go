package config

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// KeyKind describes how a scalar configuration key is parsed and rendered.
type KeyKind int

const (
	KeyString KeyKind = iota
	KeyInt
	KeyBool
	KeyEnum
	KeyList
	KeyTarget
)

// KeySpec describes one dotted configuration key exposed by `boxt config`.
// Owner is set for structured sections that belong to a typed command; those
// keys are readable through that command, not through `config set/get` beyond
// the top-level entry.
type KeySpec struct {
	Path        string
	Kind        KeyKind
	Values      []string
	Fingerprint bool
	ReadOnly    bool
	Owner       string
	Scalar      func(*Config) string
	List        func(*Config) []string
}

// Editable reports whether `config set` and `config unset` may write this key.
func (k KeySpec) Editable() bool { return !k.ReadOnly && k.Owner == "" }

var keySpecs = []KeySpec{
	{
		Path:     "version",
		Kind:     KeyInt,
		ReadOnly: true,
		Scalar:   func(c *Config) string { return strconv.Itoa(c.Version) },
	},
	{
		Path:     "vm.image",
		Kind:     KeyString,
		ReadOnly: true,
		Scalar:   func(c *Config) string { return c.VM.Image },
	},
	{
		Path:        "vm.cpus",
		Kind:        KeyInt,
		Fingerprint: true,
		Scalar:      func(c *Config) string { return strconv.Itoa(c.VM.CPUs) },
	},
	{
		Path:        "vm.memory",
		Kind:        KeyString,
		Fingerprint: true,
		Scalar:      func(c *Config) string { return c.VM.Memory },
	},
	{
		Path:        "vm.port_forwarding",
		Kind:        KeyEnum,
		Values:      []string{PortForwardingAuto, PortForwardingExplicit},
		Fingerprint: true,
		Scalar:      func(c *Config) string { return c.VM.PortForwarding },
	},
	{
		Path:   "vm.auto_start",
		Kind:   KeyBool,
		Scalar: func(c *Config) string { return strconv.FormatBool(c.VM.AutoStartEnabled()) },
	},
	{
		Path:   "workspace.merge",
		Kind:   KeyEnum,
		Values: []string{MergeModeFF, MergeModeRebase},
		Scalar: func(c *Config) string { return c.WorkspaceMergeMode() },
	},
	{
		Path:   "seed.allow_remove",
		Kind:   KeyBool,
		Scalar: func(c *Config) string { return strconv.FormatBool(c.SeedRules().AllowRemove) },
	},
	{
		Path: "seed.include",
		Kind: KeyList,
		List: func(c *Config) []string { return append([]string(nil), c.SeedRules().Include...) },
	},
	{
		Path: "seed.exclude",
		Kind: KeyList,
		List: func(c *Config) []string { return append([]string(nil), c.SeedRules().Exclude...) },
	},
	{
		Path:   "run.default",
		Kind:   KeyTarget,
		Scalar: func(c *Config) string { return c.Run.Default },
	},
	{
		Path:  "vm.ports",
		Owner: "boxt port",
	},
	{
		Path:  "vm.mounts",
		Owner: "boxt mount",
	},
	{
		Path:  "tools",
		Owner: "boxt tool",
	},
	{
		Path:  "run.targets",
		Owner: "boxt target",
	},
	{
		Path:  "hooks",
		Owner: "hand-edited configuration (`boxt hooks status` shows the current hooks)",
	},
}

// LookupKey returns the spec for a dotted configuration key.
func LookupKey(key string) (KeySpec, bool) {
	for _, spec := range keySpecs {
		if spec.Path == key {
			return spec, true
		}
	}
	return KeySpec{}, false
}

// ConfigKeys returns known keys in stable order. When writable is true only
// keys that `config set`/`config unset` accept are returned.
func ConfigKeys(writable bool) []string {
	keys := make([]string, 0, len(keySpecs))
	for _, spec := range keySpecs {
		if writable && !spec.Editable() {
			continue
		}
		keys = append(keys, spec.Path)
	}
	sort.Strings(keys)
	return keys
}

// KeyValues returns the allowed values for an enum key.
func KeyValues(key string) []string {
	spec, ok := LookupKey(key)
	if !ok {
		return nil
	}
	return append([]string(nil), spec.Values...)
}

// SetKey writes one scalar or list key through the atomic, locked,
// comment-preserving configuration editor. Lists replace their contents unless
// appendValue is set.
func SetKey(path, key, value string, appendValue bool) error {
	spec, err := writableKey(key)
	if err != nil {
		return err
	}
	segments := strings.Split(key, ".")
	switch spec.Kind {
	case KeyList:
		normalized, err := NormalizeSeedPattern(value)
		if err != nil {
			return fmt.Errorf("invalid value for %s: %w", key, err)
		}
		return mutateConfig(path, func(root *yaml.Node) error {
			if appendValue {
				return appendListValue(root, segments, normalized)
			}
			return setListValue(root, segments, []string{normalized})
		})
	default:
		tag, normalized, err := normalizeScalar(spec, value)
		if err != nil {
			return err
		}
		return mutateConfig(path, func(root *yaml.Node) error {
			return setScalarValue(root, segments, normalized, tag)
		})
	}
}

// UnsetKey removes a scalar key or a list element. Without listValue it removes
// the whole key. Required and read-only keys are refused.
func UnsetKey(path, key, listValue string, hasListValue bool) error {
	spec, err := writableKey(key)
	if err != nil {
		return err
	}
	segments := strings.Split(key, ".")
	return mutateConfig(path, func(root *yaml.Node) error {
		if spec.Kind == KeyList && hasListValue {
			normalized, err := NormalizeSeedPattern(listValue)
			if err != nil {
				return fmt.Errorf("invalid value for %s: %w", key, err)
			}
			return removeListValue(root, segments, normalized)
		}
		removeKeyPath(root, segments)
		return nil
	})
}

func writableKey(key string) (KeySpec, error) {
	spec, ok := LookupKey(key)
	if !ok {
		return KeySpec{}, fmt.Errorf("unknown configuration key %q (supported: %s)", key, strings.Join(ConfigKeys(false), ", "))
	}
	if spec.Owner != "" {
		return KeySpec{}, fmt.Errorf("%s is managed by %s", key, spec.Owner)
	}
	if spec.ReadOnly {
		return KeySpec{}, fmt.Errorf("%s is read-only", key)
	}
	return spec, nil
}

func normalizeScalar(spec KeySpec, value string) (tag, normalized string, err error) {
	switch spec.Kind {
	case KeyInt:
		n, convErr := strconv.Atoi(strings.TrimSpace(value))
		if convErr != nil {
			return "", "", fmt.Errorf("invalid value for %s: expected an integer, got %q", spec.Path, value)
		}
		if spec.Path == "vm.cpus" && (n < 1 || n > 64) {
			return "", "", fmt.Errorf("invalid value for %s: must be between 1 and 64", spec.Path)
		}
		return "!!int", strconv.Itoa(n), nil
	case KeyBool:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true":
			return "!!bool", "true", nil
		case "false":
			return "!!bool", "false", nil
		}
		return "", "", fmt.Errorf("invalid value for %s: expected true or false, got %q", spec.Path, value)
	case KeyEnum:
		for _, allowed := range spec.Values {
			if value == allowed {
				return "!!str", value, nil
			}
		}
		return "", "", fmt.Errorf("invalid value for %s: must be one of %s", spec.Path, strings.Join(spec.Values, ", "))
	case KeyTarget:
		if !ValidTargetName(value) {
			return "", "", fmt.Errorf("invalid value for %s: %q is not a valid target name", spec.Path, value)
		}
		return "!!str", value, nil
	case KeyString:
		if spec.Path == "vm.memory" {
			if _, err := MemoryBytes(value); err != nil {
				return "", "", fmt.Errorf("invalid value for %s: %s", spec.Path, err)
			}
		}
		return "!!str", value, nil
	}
	return "", "", errors.New("unsupported configuration key type")
}

// setScalarValue walks or creates the mapping path and updates the final scalar
// in place so head and line comments survive.
func setScalarValue(root *yaml.Node, segments []string, value, tag string) error {
	parent := root
	for _, segment := range segments[:len(segments)-1] {
		next, err := getOrCreateMapping(parent, segment)
		if err != nil {
			return err
		}
		parent = next
	}
	last := segments[len(segments)-1]
	key, existing := findMapEntry(parent, last)
	if key == nil {
		parent.Content = append(parent.Content, stringNode(last), &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
		return nil
	}
	head, line, foot := existing.HeadComment, existing.LineComment, existing.FootComment
	*existing = yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
	existing.HeadComment, existing.LineComment, existing.FootComment = head, line, foot
	return nil
}

func setListValue(root *yaml.Node, segments []string, values []string) error {
	parent, key, existing, err := listParent(root, segments, true)
	if err != nil {
		return err
	}
	seq := stringSequence(values)
	if key == nil {
		parent.Content = append(parent.Content, stringNode(segments[len(segments)-1]), seq)
		return nil
	}
	head, line, foot := existing.HeadComment, existing.LineComment, existing.FootComment
	*existing = *seq
	existing.HeadComment, existing.LineComment, existing.FootComment = head, line, foot
	return nil
}

func appendListValue(root *yaml.Node, segments []string, value string) error {
	parent, key, existing, err := listParent(root, segments, true)
	if err != nil {
		return err
	}
	if key == nil {
		parent.Content = append(parent.Content, stringNode(segments[len(segments)-1]), stringSequence([]string{value}))
		return nil
	}
	for _, item := range existing.Content {
		if item.Value == value {
			return nil
		}
	}
	existing.Content = append(existing.Content, stringNode(value))
	return nil
}

func removeListValue(root *yaml.Node, segments []string, value string) error {
	parent, key, existing, err := listParent(root, segments, false)
	if err != nil {
		return err
	}
	if key == nil {
		return fmt.Errorf("%s has no value %q", strings.Join(segments, "."), value)
	}
	kept := existing.Content[:0]
	for _, item := range existing.Content {
		if item.Value != value {
			kept = append(kept, item)
		}
	}
	if len(kept) == len(existing.Content) {
		return fmt.Errorf("%s has no value %q", strings.Join(segments, "."), value)
	}
	if len(kept) == 0 {
		removeKeyFrom(parent, segments[len(segments)-1])
		return nil
	}
	existing.Content = kept
	return nil
}

// listParent resolves the mapping containing a list key. When create is true it
// creates missing intermediate mappings and the list itself.
func listParent(root *yaml.Node, segments []string, create bool) (*yaml.Node, *yaml.Node, *yaml.Node, error) {
	parent := root
	for _, segment := range segments[:len(segments)-1] {
		var next *yaml.Node
		var err error
		if create {
			next, err = getOrCreateMapping(parent, segment)
		} else {
			key, value := findMapEntry(parent, segment)
			if key == nil {
				return parent, nil, nil, nil
			}
			next = value
			if next.Kind != yaml.MappingNode {
				err = fmt.Errorf("configuration field %s must be a mapping", segment)
			}
		}
		if err != nil {
			return nil, nil, nil, err
		}
		parent = next
	}
	last := segments[len(segments)-1]
	key, existing := findMapEntry(parent, last)
	if key == nil {
		if !create {
			return parent, nil, nil, nil
		}
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		return parent, nil, seq, nil
	}
	if existing.Kind != yaml.SequenceNode {
		return nil, nil, nil, fmt.Errorf("configuration field %s must be a list", last)
	}
	return parent, key, existing, nil
}

func removeKeyPath(root *yaml.Node, segments []string) {
	parent := root
	for _, segment := range segments[:len(segments)-1] {
		key, value := findMapEntry(parent, segment)
		if key == nil || value.Kind != yaml.MappingNode {
			return
		}
		parent = value
	}
	removeKeyFrom(parent, segments[len(segments)-1])
}

func removeKeyFrom(parent *yaml.Node, name string) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == name {
			parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
			return
		}
	}
}

func stringSequence(values []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, value := range values {
		seq.Content = append(seq.Content, stringNode(value))
	}
	return seq
}

func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
