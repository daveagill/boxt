package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
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

// ErrMountNotFound is returned when removing an undeclared mount.
var ErrMountNotFound = errors.New("mount is not declared")

// ErrMountAmbiguous is returned when a remove target matches both a guest path
// and a different host path.
type ErrMountAmbiguous struct {
	Target string
}

func (e *ErrMountAmbiguous) Error() string {
	return fmt.Sprintf("%q matches both a guest path and a different host path; remove it by the guest path", e.Target)
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

// AddMount adds an additional host mount while preserving unrelated YAML. The
// write is atomic and the result must still validate.
func AddMount(configPath string, mount Mount) error {
	if mount.Writable == nil {
		return errors.New("mount writable is required (true or false)")
	}
	return mutateConfig(configPath, func(root *yaml.Node) error {
		vm, err := getOrCreateMapping(root, "vm")
		if err != nil {
			return err
		}
		mounts, err := getOrCreateSequence(vm, "mounts")
		if err != nil {
			return err
		}
		guest := path.Clean(mount.Guest)
		host := filepath.Clean(mount.Host)
		for _, node := range mounts.Content {
			var existing Mount
			if err := node.Decode(&existing); err != nil {
				return fmt.Errorf("parse mount entry: %w", err)
			}
			if existing.GuestPath() == guest {
				return fmt.Errorf("guest mount path %s is already configured", guest)
			}
			if filepath.Clean(existing.Host) == host {
				return fmt.Errorf("host path %s is already mounted at %s", host, existing.GuestPath())
			}
		}
		node, err := mountNode(mount)
		if err != nil {
			return err
		}
		mounts.Content = append(mounts.Content, node)
		return nil
	})
}

// RemoveMount removes a configured mount by guest path or by host path and
// returns the removed entry. Removed mounts stay active in an existing VM until
// it is recreated.
func RemoveMount(configPath, target string) (Mount, error) {
	var removed Mount
	err := mutateConfig(configPath, func(root *yaml.Node) error {
		_, vm := findMapEntry(root, "vm")
		if vm == nil {
			return ErrMountNotFound
		}
		mounts, err := getOrCreateSequence(vm, "mounts")
		if err != nil {
			return err
		}

		guestIdx, guestMount := findMount(mounts, func(m Mount) bool {
			return m.GuestPath() == path.Clean(target)
		})
		hostIdx, hostMount := findMount(mounts, func(m Mount) bool {
			return filepath.Clean(m.Host) == filepath.Clean(target)
		})
		switch {
		case guestIdx < 0 && hostIdx < 0:
			return ErrMountNotFound
		case guestIdx >= 0 && hostIdx >= 0 && guestIdx != hostIdx:
			return &ErrMountAmbiguous{Target: target}
		}

		idx := guestIdx
		if idx < 0 {
			idx = hostIdx
		}
		removed = guestMount
		if idx == hostIdx {
			removed = hostMount
		}
		mounts.Content = append(mounts.Content[:idx], mounts.Content[idx+1:]...)
		if len(mounts.Content) == 0 {
			removeMapEntry(vm, "mounts")
		}
		return nil
	})
	if err != nil {
		return Mount{}, err
	}
	return removed, nil
}

func findMount(mounts *yaml.Node, match func(Mount) bool) (int, Mount) {
	for i, node := range mounts.Content {
		var mount Mount
		if err := node.Decode(&mount); err != nil {
			continue
		}
		if match(mount) {
			return i, mount
		}
	}
	return -1, Mount{}
}

func mountNode(mount Mount) (*yaml.Node, error) {
	data, err := yaml.Marshal(&mount)
	if err != nil {
		return nil, fmt.Errorf("encode mount: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("encode mount: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("encode mount: empty document")
	}
	return doc.Content[0], nil
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

func getOrCreateSequence(parent *yaml.Node, keyName string) (*yaml.Node, error) {
	key, value := findMapEntry(parent, keyName)
	if key == nil {
		key = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: keyName}
		value = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		parent.Content = append(parent.Content, key, value)
		return value, nil
	}
	if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
		value.Kind = yaml.SequenceNode
		value.Tag = "!!seq"
		value.Value = ""
		return value, nil
	}
	if value.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("configuration field %s must be a list", keyName)
	}
	return value, nil
}

func removeMapEntry(parent *yaml.Node, keyName string) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == keyName {
			parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
			return
		}
	}
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
