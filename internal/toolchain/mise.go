// Package toolchain manages boxt-declared tools inside the project VM using
// mise as an internal implementation detail. Users never configure mise.
package toolchain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/boxt/boxt/internal/command"
	"github.com/boxt/boxt/internal/lima"
	"github.com/boxt/boxt/internal/project"
)

const (
	// MiseVersion is the pinned mise release installed into each VM.
	MiseVersion = "v2026.11.8"
	// MiseInstallURL is the official mise installer endpoint.
	MiseInstallURL = "https://mise.run"
	// MiseInstallerPath is the downloaded installer path inside the guest.
	MiseInstallerPath = "/tmp/boxt-mise-install.sh"
)

var basePackages = []string{"curl", "ca-certificates", "git", "tar", "xz-utils", "unzip"}

// Guest is the guest execution seam (implemented by the Lima client).
type Guest interface {
	Shell(ctx context.Context, req lima.ShellRequest) error
}

// Manager installs and inspects tools in one project VM.
type Manager struct {
	Guest Guest
}

// New returns a toolchain manager.
func New(guest Guest) *Manager { return &Manager{Guest: guest} }

// State is the reconciliation state of a declared tool.
type State string

const (
	StateInstalled      State = "installed"
	StateMissing        State = "missing"
	StateMismatch       State = "mismatch"
	StateNotProvisioned State = "not provisioned"
)

// ToolStatus describes one declared tool and its installed version.
type ToolStatus struct {
	Name      string
	Declared  string
	Installed string
	State     State
}

// Matches reports whether the installed version satisfies the declaration.
func (s ToolStatus) Matches() bool { return s.State == StateInstalled }

// miseEntry is the subset of `mise ls --json` boxt needs.
type miseEntry struct {
	Version          string `json:"version"`
	RequestedVersion string `json:"requested_version"`
	InstallPath      string `json:"install_path"`
	Installed        bool   `json:"installed"`
	Active           bool   `json:"active"`
}

// WrapArgv returns a guest argv that runs command with the declared tool
// versions on PATH. It uses mise's documented execution interface and does not
// rely on shell profiles or shims.
func WrapArgv(tools map[string]string, argv []string) []string {
	if len(tools) == 0 {
		return argv
	}
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)

	out := []string{
		"/usr/bin/env",
		"MISE_NO_CONFIG=1",
		"MISE_EXEC_AUTO_INSTALL=0",
		lima.GuestMiseBin,
		"exec",
	}
	for _, name := range names {
		out = append(out, name+"@"+tools[name])
	}
	out = append(out, "--")
	return append(out, argv...)
}

func miseArgv(args ...string) []string {
	base := []string{
		"/usr/bin/env",
		"MISE_NO_CONFIG=1",
		"MISE_EXEC_AUTO_INSTALL=0",
		lima.GuestMiseBin,
	}
	return append(base, args...)
}

// run executes a guest command and captures its output.
func (m *Manager) run(ctx context.Context, p *project.Project, workDir string, argv []string) (string, error) {
	if m.Guest == nil {
		return "", errors.New("toolchain has no guest")
	}
	if workDir == "" {
		workDir = lima.GuestUserHome
	}
	var stdout, stderr bytes.Buffer
	err := m.Guest.Shell(ctx, lima.ShellRequest{
		Instance: p.VMName(),
		WorkDir:  workDir,
		Command:  argv,
		Stdout:   &stdout,
		Stderr:   &stderr,
	})
	if err != nil {
		var cmdErr *command.Error
		if errors.As(err, &cmdErr) && cmdErr.Stderr == "" {
			cmdErr.Stderr = strings.TrimSpace(stderr.String())
		}
		return strings.TrimSpace(stdout.String()), err
	}
	return strings.TrimSpace(stdout.String()), nil
}

// EnsureDeps installs the small set of system packages mise and Git backends
// need. It uses apt only for system dependencies and sudo only here.
func (m *Manager) EnsureDeps(ctx context.Context, p *project.Project) error {
	if _, err := m.run(ctx, p, lima.GuestUserHome, []string{"sh", "-c", depsCheckScript}); err == nil {
		return nil
	}
	if _, err := m.run(ctx, p, lima.GuestUserHome, []string{
		"sudo", "-n", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update",
	}); err != nil {
		return fmt.Errorf("update guest packages: %w", err)
	}
	args := []string{
		"sudo", "-n", "env", "DEBIAN_FRONTEND=noninteractive",
		"apt-get", "install", "-y", "--no-install-recommends",
	}
	args = append(args, basePackages...)
	if _, err := m.run(ctx, p, lima.GuestUserHome, args); err != nil {
		return fmt.Errorf("install guest packages: %w", err)
	}
	if _, err := m.run(ctx, p, lima.GuestUserHome, []string{"sh", "-c", depsCheckScript}); err != nil {
		return fmt.Errorf("guest packages are still incomplete: %w", err)
	}
	return nil
}

const depsCheckScript = `for p in curl ca-certificates git tar xz-utils unzip; do dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q "install ok installed" || exit 1; done`

// EnsureMise installs or verifies the pinned mise release and returns its
// version string.
func (m *Manager) EnsureMise(ctx context.Context, p *project.Project) (string, error) {
	if out, err := m.run(ctx, p, lima.GuestUserHome, []string{lima.GuestMiseBin, "--version"}); err == nil {
		if v := parseMiseVersion(out); v != "" {
			return v, nil
		}
	}
	if _, err := m.run(ctx, p, lima.GuestUserHome, []string{
		"curl", "-fsSL", "-o", MiseInstallerPath, MiseInstallURL,
	}); err != nil {
		return "", fmt.Errorf("download mise installer: %w", err)
	}
	if _, err := m.run(ctx, p, lima.GuestUserHome, []string{
		"env", "MISE_VERSION=" + MiseVersion, "sh", MiseInstallerPath,
	}); err != nil {
		return "", fmt.Errorf("install mise: %w", err)
	}
	out, err := m.run(ctx, p, lima.GuestUserHome, []string{lima.GuestMiseBin, "--version"})
	if err != nil {
		return "", fmt.Errorf("verify mise installation: %w", err)
	}
	version := parseMiseVersion(out)
	if version == "" {
		return "", fmt.Errorf("verify mise installation: unexpected output %q", out)
	}
	return version, nil
}

func parseMiseVersion(out string) string {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "mise" && i+1 < len(fields) {
				return fields[i+1]
			}
		}
		if strings.HasPrefix(line, "mise ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "mise "))
		}
	}
	return ""
}

// installSpec installs one exact tool declaration.
func (m *Manager) installSpec(ctx context.Context, p *project.Project, name, version string) error {
	spec := name + "@" + version
	if _, err := m.run(ctx, p, lima.GuestUserHome, miseArgv("install", spec)); err != nil {
		return fmt.Errorf("install %s: %w", spec, err)
	}
	return nil
}

// Install installs or switches to one declared tool version and returns the
// resolved installed version when available.
func (m *Manager) Install(ctx context.Context, p *project.Project, name, version string) (string, error) {
	if err := m.EnsureDeps(ctx, p); err != nil {
		return "", err
	}
	if _, err := m.EnsureMise(ctx, p); err != nil {
		return "", err
	}
	if err := m.installSpec(ctx, p, name, version); err != nil {
		return "", err
	}
	data, err := m.queryInstalled(ctx, p)
	if err != nil {
		return version, nil
	}
	status := statusFor(ToolStatus{Name: name, Declared: version}, data[name])
	if status.Installed != "" {
		return status.Installed, nil
	}
	return version, nil
}

// List reports the installed state of every declared tool. A running VM
// without mise is reported as not provisioned rather than as an error.
func (m *Manager) List(ctx context.Context, p *project.Project) ([]ToolStatus, error) {
	names := p.Config.ToolNames()
	out := make([]ToolStatus, 0, len(names))
	for _, name := range names {
		out = append(out, ToolStatus{Name: name, Declared: p.Config.Tools[name], State: StateNotProvisioned})
	}
	if len(names) == 0 {
		return out, nil
	}
	if _, err := m.run(ctx, p, lima.GuestUserHome, []string{lima.GuestMiseBin, "--version"}); err != nil {
		return out, nil
	}
	data, err := m.queryInstalled(ctx, p)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i] = statusFor(out[i], data[out[i].Name])
	}
	return out, nil
}

func (m *Manager) queryInstalled(ctx context.Context, p *project.Project) (map[string][]miseEntry, error) {
	raw, err := m.run(ctx, p, lima.GuestUserHome, miseArgv("ls", "--json"))
	if err != nil {
		return nil, fmt.Errorf("query installed tools: %w", err)
	}
	return parseMiseLS(raw)
}

func parseMiseLS(raw string) (map[string][]miseEntry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string][]miseEntry{}, nil
	}
	var data map[string][]miseEntry
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, fmt.Errorf("parse mise ls output: %w", err)
	}
	if data == nil {
		data = map[string][]miseEntry{}
	}
	return data, nil
}

func statusFor(status ToolStatus, entries []miseEntry) ToolStatus {
	var fallback string
	for _, e := range entries {
		if !e.Installed && e.InstallPath == "" {
			continue
		}
		if fallback == "" {
			fallback = e.Version
		}
		if versionMatches(status.Declared, e.RequestedVersion) || versionMatches(status.Declared, e.Version) {
			status.Installed = e.Version
			status.State = StateInstalled
			return status
		}
	}
	if fallback != "" {
		status.Installed = fallback
		status.State = StateMismatch
		return status
	}
	status.State = StateMissing
	return status
}

func versionMatches(declared, candidate string) bool {
	if declared == "" || candidate == "" {
		return false
	}
	if declared == "latest" || candidate == "latest" {
		return true
	}
	if declared == candidate {
		return true
	}
	return strings.HasPrefix(candidate, declared+".")
}
