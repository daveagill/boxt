package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
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

// ErrTargetNotFound is returned when removing or selecting an undeclared target.
type ErrTargetNotFound struct {
	Name string
}

func (e *ErrTargetNotFound) Error() string {
	return fmt.Sprintf("run target %q is not configured", e.Name)
}

// ErrPortNotFound is returned when removing an undeclared guest port.
type ErrPortNotFound struct {
	Guest int
}

func (e *ErrPortNotFound) Error() string {
	return fmt.Sprintf("guest port %d is not forwarded", e.Guest)
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
		return addTargetToRun(run, targetName, target)
	})
}

// AddTarget adds or updates a run target. An existing target with a different
// definition is an error; an identical one is a no-op. When setDefault is true
// run.default is pointed at the target in the same atomic write.
func AddTarget(path, name string, target RunTarget, setDefault bool) error {
	if !ValidTargetName(name) {
		return fmt.Errorf("invalid target name %q", name)
	}
	return mutateConfig(path, func(root *yaml.Node) error {
		run, err := getOrCreateMapping(root, "run")
		if err != nil {
			return err
		}
		if err := addTargetToRun(run, name, target); err != nil {
			return err
		}
		if setDefault {
			return setScalarValue(root, []string{"run", "default"}, name, "!!str")
		}
		return nil
	})
}

func addTargetToRun(run *yaml.Node, name string, target RunTarget) error {
	targets, err := getOrCreateMapping(run, "targets")
	if err != nil {
		return err
	}
	key, value := findMapEntry(targets, name)
	if key != nil {
		var existing RunTarget
		if err := value.Decode(&existing); err != nil {
			return fmt.Errorf("parse run target %q: %w", name, err)
		}
		if !reflect.DeepEqual(existing, target) {
			return &ErrTargetExists{Name: name}
		}
		return nil
	}
	node, err := runTargetNode(target)
	if err != nil {
		return err
	}
	targets.Content = append(targets.Content, stringNode(name), node)
	return nil
}

// RemoveTarget removes one run target. When it was the configured default,
// run.default is cleared in the same atomic write and the boolean is true.
func RemoveTarget(path, name string) (wasDefault bool, err error) {
	if !ValidTargetName(name) {
		return false, fmt.Errorf("invalid target name %q", name)
	}
	err = mutateConfig(path, func(root *yaml.Node) error {
		runKey, run := findMapEntry(root, "run")
		if runKey == nil || run.Kind != yaml.MappingNode {
			return &ErrTargetNotFound{Name: name}
		}
		targetsKey, targets := findMapEntry(run, "targets")
		if targetsKey == nil || targets.Kind != yaml.MappingNode {
			return &ErrTargetNotFound{Name: name}
		}
		if key, _ := findMapEntry(targets, name); key == nil {
			return &ErrTargetNotFound{Name: name}
		}
		removeKeyFrom(targets, name)
		if defKey, defValue := findMapEntry(run, "default"); defKey != nil && defValue.Value == name {
			removeKeyFrom(run, "default")
			wasDefault = true
		}
		return nil
	})
	return wasDefault, err
}

// SetDefaultTarget points run.default at an existing target.
func SetDefaultTarget(path, name string) error {
	if !ValidTargetName(name) {
		return fmt.Errorf("invalid target name %q", name)
	}
	return mutateConfig(path, func(root *yaml.Node) error {
		runKey, run := findMapEntry(root, "run")
		if runKey == nil || run.Kind != yaml.MappingNode {
			return &ErrTargetNotFound{Name: name}
		}
		targetsKey, targets := findMapEntry(run, "targets")
		if targetsKey == nil || targets.Kind != yaml.MappingNode {
			return &ErrTargetNotFound{Name: name}
		}
		if key, _ := findMapEntry(targets, name); key == nil {
			return &ErrTargetNotFound{Name: name}
		}
		return setScalarValue(root, []string{"run", "default"}, name, "!!str")
	})
}

// UnsetDefaultTarget clears run.default.
func UnsetDefaultTarget(path string) error {
	return mutateConfig(path, func(root *yaml.Node) error {
		removeKeyPath(root, []string{"run", "default"})
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
	return valueNode(target, "run target")
}

// AddPort appends one explicit guest-to-host port mapping.
func AddPort(path string, port PortForward) error {
	if port.HostIP == "" {
		port.HostIP = "127.0.0.1"
	}
	if err := validatePortForward(port); err != nil {
		return err
	}
	return mutateConfig(path, func(root *yaml.Node) error {
		vm, err := getOrCreateMapping(root, "vm")
		if err != nil {
			return err
		}
		key, ports := findMapEntry(vm, "ports")
		if key == nil {
			ports = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			vm.Content = append(vm.Content, stringNode("ports"), ports)
		} else if ports.Kind != yaml.SequenceNode {
			return errors.New("configuration field vm.ports must be a list")
		}
		node, err := valueNode(port, "port forward")
		if err != nil {
			return err
		}
		ports.Content = append(ports.Content, node)
		return nil
	})
}

// RemovePortByGuest removes the mapping for one guest port.
func RemovePortByGuest(path string, guest int) error {
	return mutateConfig(path, func(root *yaml.Node) error {
		vmKey, vm := findMapEntry(root, "vm")
		if vmKey == nil || vm.Kind != yaml.MappingNode {
			return &ErrPortNotFound{Guest: guest}
		}
		portsKey, ports := findMapEntry(vm, "ports")
		if portsKey == nil || ports.Kind != yaml.SequenceNode {
			return &ErrPortNotFound{Guest: guest}
		}
		for i, item := range ports.Content {
			var existing PortForward
			if err := item.Decode(&existing); err != nil {
				continue
			}
			if existing.Guest != guest {
				continue
			}
			ports.Content = append(ports.Content[:i], ports.Content[i+1:]...)
			if len(ports.Content) == 0 {
				removeKeyFrom(vm, "ports")
			}
			return nil
		}
		return &ErrPortNotFound{Guest: guest}
	})
}

func validatePortForward(port PortForward) error {
	if port.Guest < 1 || port.Guest > 65535 {
		return fmt.Errorf("guest port %d must be between 1 and 65535", port.Guest)
	}
	if port.Guest == 22 {
		return errors.New("guest port 22 is reserved for Lima SSH")
	}
	if port.Host < 1 || port.Host > 65535 {
		return fmt.Errorf("host port %d must be between 1 and 65535", port.Host)
	}
	if net.ParseIP(port.HostIP) == nil {
		return fmt.Errorf("invalid host IP %q", port.HostIP)
	}
	return nil
}

func valueNode(value any, label string) (*yaml.Node, error) {
	data, err := yaml.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", label, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("encode %s: %w", label, err)
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("encode %s: empty document", label)
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
