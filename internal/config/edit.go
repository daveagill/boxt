package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/boxt/boxt/internal/fsutil"
)

// ErrToolNotFound is returned when removing an undeclared tool.
var ErrToolNotFound = errors.New("tool is not declared")

// AddTool adds or updates a tool declaration while preserving unrelated YAML
// (including comments and keys boxt does not manage). The write is atomic.
func AddTool(path, name, version string) error {
	if !ValidToolName(name) {
		return fmt.Errorf("invalid tool name %q", name)
	}
	if !ValidToolVersion(version) {
		return fmt.Errorf("invalid tool version %q", version)
	}
	return mutateTools(path, func(m *yaml.Node) error {
		key, value := findMapEntry(m, name)
		val := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: version}
		if key != nil {
			*value = *val
			return nil
		}
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
			val,
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
	return mutateTools(path, func(m *yaml.Node) error {
		key, _ := findMapEntry(m, name)
		if key == nil {
			return ErrToolNotFound
		}
		for i := 0; i < len(m.Content); i += 2 {
			if m.Content[i] == key {
				m.Content = append(m.Content[:i], m.Content[i+2:]...)
				return nil
			}
		}
		return ErrToolNotFound
	})
}

func mutateTools(path string, mutate func(*yaml.Node) error) error {
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
	root := doc.Content[0]

	toolsKey, toolsValue := findMapEntry(root, "tools")
	if toolsKey == nil {
		toolsKey = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "tools"}
		toolsValue = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, toolsKey, toolsValue)
	} else if toolsValue.Kind == yaml.ScalarNode && toolsValue.Tag == "!!null" {
		toolsValue.Kind = yaml.MappingNode
		toolsValue.Tag = "!!map"
		toolsValue.Value = ""
	} else if toolsValue.Kind != yaml.MappingNode {
		return errors.New("configuration field tools must be a mapping")
	}

	if err := mutate(toolsValue); err != nil {
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

func findMapEntry(m *yaml.Node, name string) (key, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}
