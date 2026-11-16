package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// PortOffsetMax is the largest deterministic named-workspace offset. The
// default workspace always uses offset 0.
const PortOffsetMax = 999

// PortOffset returns the deterministic per-workspace port offset. The default
// workspace is always 0; named workspaces use 1..999 derived from the project ID
// and workspace name. One offset is shared by all targets in a workspace.
func PortOffset(projectID, name string) int {
	if name == DefaultName {
		return 0
	}
	sum := sha256.Sum256([]byte(projectID + "\x00" + name))
	return 1 + int(binary.BigEndian.Uint32(sum[:4])%PortOffsetMax)
}

// CheckPortOffset computes the offset for name and rejects a collision with any
// other current workspace. boxt never silently reuses a port.
func CheckPortOffset(projectID, name string, existing []string) (int, error) {
	offset := PortOffset(projectID, name)
	for _, other := range existing {
		if other == "" || other == name {
			continue
		}
		if PortOffset(projectID, other) == offset {
			return 0, &ErrPortOffsetCollision{Workspace: name, Other: other, Offset: offset}
		}
	}
	return offset, nil
}

// PortOffsetFor checks the current managed workspaces and returns the offset for
// name.
func (m *Manager) PortOffsetFor(ctx context.Context, name string) (int, error) {
	if name != DefaultName {
		if err := ValidateName(name); err != nil {
			return 0, err
		}
	}
	names := []string{DefaultName}
	list, err := m.List(ctx)
	if err != nil {
		return 0, err
	}
	for _, ws := range list {
		names = append(names, ws.Name)
	}
	return CheckPortOffset(m.ProjectID, name, names)
}

// ErrPortOffsetCollision reports two workspaces that hash to the same guest port
// offset.
type ErrPortOffsetCollision struct {
	Workspace string
	Other     string
	Offset    int
}

func (e *ErrPortOffsetCollision) Error() string {
	return fmt.Sprintf("workspace %s and workspace %s both use port offset %d", e.Workspace, e.Other, e.Offset)
}
func (e *ErrPortOffsetCollision) Hint() string {
	return fmt.Sprintf("Rename one of the workspaces, or choose a different port_base in .boxt/config.yaml. boxt does not silently reuse a guest port.")
}
