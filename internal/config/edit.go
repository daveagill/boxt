package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"gopkg.in/yaml.v3"

	"github.com/daveagill/boxt/internal/fsutil"
)

// ErrToolNotFound is returned when removing an undeclared tool.
var ErrToolNotFound = errors.New("tool is not declared")

// ErrTargetExists is returned when adding a target without replacing an
// existing, different definition.
type ErrTargetExists struct {
	Name string
}

func (e *ErrTargetExists) Error() string {
	return fmt.Sprintf("run target %q already exists with a different command", e.Name)
}

// AddTool adds or updates a tool declaration while preserving unrelated YAML
// (including comments and keys boxt does not manage). The write is atomic.
func AddTool(path, name, version string) error {
	if !ValidToolName(name) {
		return fmt.Errorf("invalid tool name %q", name)
	}
	if !ValidToolVersion(version) {
		return fmt.Errorf("invalid tool version %q", version)
	}
	return mutateConfig(path, func(root *yaml.Node) error {
		tools, err := getOrCreateMapping(root, "tools")
		if err != nil {
			return err
		}
		setTool(tools, name, version)
		return nil
	})
}

// AddToolTarget adds or updates a tool declaration and adds a run target in the
// same atomic configuration write. An existing target with a different
// definition is an error.
func AddToolTarget(path, name, version, targetName string, target RunTarget) error {
	if !ValidToolName(name) {
		return fmt.Errorf("invalid tool name %q", name)
	}
	if !ValidToolVersion(version) {
		return fmt.Errorf("invalid tool version %q", version)
	}
	if !ValidTargetName(targetName) {
		return fmt.Errorf("invalid target name %q", targetName)
	}
	return mutateConfig(path, func(root *yaml.Node) error {
		tools, err := getOrCreateMapping(root, "tools")
		if err != nil {
			return err
		}
		setTool(tools, name, version)

		run, err := getOrCreateMapping(root, "run")
		if err != nil {
			return err
		}
		targets, err := getOrCreateMapping(run, "targets")
		if err != nil {
			return err
		}
		key, value := findMapEntry(targets, targetName)
		if key != nil {
			var existing RunTarget
			if err := value.Decode(&existing); err != nil {
				return fmt.Errorf("parse run target %q: %w", targetName, err)
			}
			if !reflect.DeepEqual(existing, target) {
				return &ErrTargetExists{Name: targetName}
			}
			return nil
		}
		node, err := runTargetNode(target)
		if err != nil {
			return err
		}
		targets.Content = append(targets.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: targetName},
			node,
		)
		return nil
	})
}

// RemoveTool removes a tool declaration without uninstalling anything from
// the VM. Unrelated YAML is preserved and the write is atomic.
func RemoveTool(path, name string) error {
	if !ValidToolName(name) {
		return fmt.Errorf("invalid tool name %q", name)
	}
	return mutateConfig(path, func(root *yaml.Node) error {
		tools, err := getOrCreateMapping(root, "tools")
		if err != nil {
			return err
		}
		key, _ := findMapEntry(tools, name)
		if key == nil {
			return ErrToolNotFound
		}
		for i := 0; i < len(tools.Content); i += 2 {
			if tools.Content[i] == key {
				tools.Content = append(tools.Content[:i], tools.Content[i+2:]...)
				return nil
			}
		}
		return ErrToolNotFound
	})
}

func mutateConfig(path string, mutate func(root *yaml.Node) error) error {
	target, err := resolveConfigPath(path)
	if err != nil {
		return err
	}
	return fsutil.WithLock(target, func() error {
		return mutateConfigLocked(target, mutate)
	})
}

func resolveConfigPath(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect configuration: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve configuration symlink: %w", err)
	}
	return resolved, nil
}

func mutateConfigLocked(path string, mutate func(root *yaml.Node) error) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect configuration: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse configuration: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("parse configuration: expected a YAML mapping at the document root")
	}
	if err := mutate(doc.Content[0]); err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}

	// Never write a configuration that boxt itself cannot load.
	if _, err := Parse(buf.Bytes()); err != nil {
		return fmt.Errorf("refusing to write invalid configuration: %w", err)
	}

	return fsutil.WriteFileAtomic(path, buf.Bytes(), info.Mode().Perm())
}

func getOrCreateMapping(parent *yaml.Node, keyName string) (*yaml.Node, error) {
	key, value := findMapEntry(parent, keyName)
	if key == nil {
		key = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: keyName}
		value = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		parent.Content = append(parent.Content, key, value)
		return value, nil
	}
	if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
		value.Kind = yaml.MappingNode
		value.Tag = "!!map"
		value.Value = ""
		return value, nil
	}
	if value.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("configuration field %s must be a mapping", keyName)
	}
	return value, nil
}

func setTool(tools *yaml.Node, name, version string) {
	key, value := findMapEntry(tools, name)
	val := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: version}
	if key != nil {
		*value = *val
		return
	}
	tools.Content = append(tools.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
		val,
	)
}

func runTargetNode(target RunTarget) (*yaml.Node, error) {
	data, err := yaml.Marshal(&target)
	if err != nil {
		return nil, fmt.Errorf("encode run target: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("encode run target: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("encode run target: empty document")
	}
	return doc.Content[0], nil
}

func findMapEntry(m *yaml.Node, name string) (key, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}
