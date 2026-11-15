package toolchain

import (
	"context"
	"errors"
	"fmt"

	"github.com/boxt/boxt/internal/lima"
	"github.com/boxt/boxt/internal/project"
)

// ToolResult is one tool's provisioning outcome.
type ToolResult struct {
	Name      string
	Declared  string
	Installed string
	Err       error
}

// Report summarizes a provisioning run.
type Report struct {
	MiseVersion string
	Tools       []ToolResult
}

// Failed reports whether any tool failed.
func (r *Report) Failed() bool {
	for _, t := range r.Tools {
		if t.Err != nil {
			return true
		}
	}
	return false
}

// Provision reconciles the VM against .boxt/config.yaml. It is idempotent:
// missing tools are installed, declared versions are installed, and existing
// correct installations are left alone. Failures are reported per tool.
func (m *Manager) Provision(ctx context.Context, p *project.Project) (*Report, error) {
	if err := m.EnsureDeps(ctx, p); err != nil {
		return nil, fmt.Errorf("prepare system dependencies: %w", err)
	}
	miseVersion, err := m.EnsureMise(ctx, p)
	if err != nil {
		return nil, err
	}
	report := &Report{MiseVersion: miseVersion}
	var failures []error

	// Reuse satisfied installations. If the inventory cannot be read, fall
	// back to installing every declaration and let verification report errors.
	existing, _ := m.List(ctx, p)
	installed := make(map[string]ToolStatus, len(existing))
	for _, s := range existing {
		installed[s.Name] = s
	}

	for _, name := range p.Config.ToolNames() {
		declared := p.Config.Tools[name]
		result := ToolResult{Name: name, Declared: declared, Installed: declared}
		if s, ok := installed[name]; ok && s.State == StateInstalled && declared != "latest" {
			result.Installed = s.Installed
			report.Tools = append(report.Tools, result)
			continue
		}
		if err := m.installSpec(ctx, p, name, declared); err != nil {
			result.Err = err
			failures = append(failures, err)
		}
		report.Tools = append(report.Tools, result)
	}

	statuses, listErr := m.List(ctx, p)
	if listErr != nil {
		failures = append(failures, fmt.Errorf("verify installed tools: %w", listErr))
	} else {
		byName := make(map[string]ToolStatus, len(statuses))
		for _, s := range statuses {
			byName[s.Name] = s
		}
		for i := range report.Tools {
			result := &report.Tools[i]
			if s, ok := byName[result.Name]; ok {
				result.Installed = s.Installed
				if result.Err == nil && s.State != StateInstalled {
					result.Err = fmt.Errorf("%s %s: tool is %s after provisioning", result.Name, result.Declared, s.State)
					failures = append(failures, result.Err)
				}
			}
		}
	}

	if len(p.Config.Tools) > 0 && len(failures) == 0 {
		if _, err := m.run(ctx, p, lima.GuestUserHome, WrapArgv(p.Config.Tools, []string{"/usr/bin/true"})); err != nil {
			failures = append(failures, fmt.Errorf("verify tool execution environment: %w", err))
		}
	}
	if len(failures) > 0 {
		return report, errors.Join(failures...)
	}
	return report, nil
}
