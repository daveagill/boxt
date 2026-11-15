// Package lima wraps the supported limactl command interface and generates
// project VM configuration. It never depends on undocumented Lima internals.
package lima

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/boxt/boxt/internal/config"
	"github.com/boxt/boxt/internal/fsutil"
)

// ConfigFileName is the generated Lima YAML file name inside the VM directory.
const ConfigFileName = "lima.yaml"

// FingerprintFileName stores the fingerprint of user-controlled VM settings
// that Lima only applies at creation time.
const FingerprintFileName = "config.sha256"

// Ubuntu 24.04 cloud image snapshot. The snapshot and digests are pinned for
// reproducible downloads; update them when refreshing the supported image.
// Pinned to the images published with Lima v2.3.0.
var ubuntuImages = []limaImage{
	{
		Location: "https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-amd64.img",
		Arch:     "x86_64",
		Digest:   "sha256:04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94",
		Variant:  "server",
	},
	{
		Location: "https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-arm64.img",
		Arch:     "aarch64",
		Digest:   "sha256:50e24e1fb79e89116e939d90aa7738f7af48c442fa1bc2510131e0b39c50348e",
		Variant:  "server",
	},
}

var projectIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// VMOptions describes the machine boxt should create for a project.
type VMOptions struct {
	ProjectID      string
	ProjectRoot    string
	WorkspacesDir  string
	CPUs           int
	Memory         string
	PortForwarding string
	Ports          []config.PortForward
}

type limaImage struct {
	Location string `yaml:"location"`
	Arch     string `yaml:"arch"`
	Digest   string `yaml:"digest,omitempty"`
	Variant  string `yaml:"variant,omitempty"`
}

type limaUser struct {
	Name             string `yaml:"name"`
	Home             string `yaml:"home"`
	Shell            string `yaml:"shell"`
	PasswordlessSudo bool   `yaml:"passwordlessSudo"`
}

type limaMount struct {
	Location   string `yaml:"location"`
	MountPoint string `yaml:"mountPoint"`
	Writable   bool   `yaml:"writable"`
}

type limaContainerd struct {
	System bool `yaml:"system"`
	User   bool `yaml:"user"`
}

type limaPortForward struct {
	GuestPort         int    `yaml:"guestPort,omitempty"`
	HostPort          int    `yaml:"hostPort,omitempty"`
	HostIP            string `yaml:"hostIP,omitempty"`
	GuestIP           string `yaml:"guestIP,omitempty"`
	GuestIPMustBeZero *bool  `yaml:"guestIPMustBeZero,omitempty"`
	Proto             string `yaml:"proto,omitempty"`
	Ignore            bool   `yaml:"ignore,omitempty"`
}

type limaYAML struct {
	MinimumLimaVersion string            `yaml:"minimumLimaVersion"`
	Images             []limaImage       `yaml:"images"`
	CPUs               int               `yaml:"cpus"`
	Memory             string            `yaml:"memory"`
	User               limaUser          `yaml:"user"`
	Mounts             []limaMount       `yaml:"mounts"`
	Containerd         limaContainerd    `yaml:"containerd"`
	PropagateProxyEnv  *bool             `yaml:"propagateProxyEnv,omitempty"`
	PortForwards       []limaPortForward `yaml:"portForwards,omitempty"`
}

// GuestPath constants define the single host-to-guest mapping.
const (
	GuestProjectPath  = "/workspace"
	GuestWorkspaces   = "/workspaces"
	GuestUser         = "boxt"
	GuestUserHome     = "/home/boxt"
	GuestShell        = "/bin/bash"
	GuestMiseBin      = "/home/boxt/.local/bin/mise"
	GuestMiseDataDir  = "/home/boxt/.local/share/mise"
	GuestMiseCacheDir = "/home/boxt/.cache/mise"
)

// WorkspacesGuestDir is the project-scoped guest mount root.
func WorkspacesGuestDir(projectID string) string {
	return filepath.ToSlash(filepath.Join(GuestWorkspaces, projectID))
}

// GenerateConfig renders the Lima YAML for a project. It mounts only the
// project root and the project-scoped workspace root; the host home directory
// and Docker socket are never mounted.
func GenerateConfig(opts VMOptions) ([]byte, error) {
	if !projectIDRE.MatchString(opts.ProjectID) {
		return nil, fmt.Errorf("invalid project ID %q", opts.ProjectID)
	}
	if !filepath.IsAbs(opts.ProjectRoot) {
		return nil, fmt.Errorf("project root must be absolute: %q", opts.ProjectRoot)
	}
	if !filepath.IsAbs(opts.WorkspacesDir) {
		return nil, fmt.Errorf("workspaces directory must be absolute: %q", opts.WorkspacesDir)
	}
	projectRoot := canonicalCompare(opts.ProjectRoot)
	workspacesRoot := canonicalCompare(opts.WorkspacesDir)
	if projectRoot == workspacesRoot || pathContains(projectRoot, workspacesRoot) || pathContains(workspacesRoot, projectRoot) {
		return nil, fmt.Errorf("workspace data directory %s must be outside the project root %s", opts.WorkspacesDir, opts.ProjectRoot)
	}
	if opts.CPUs < 1 {
		return nil, fmt.Errorf("cpus must be positive")
	}
	if opts.Memory == "" {
		return nil, fmt.Errorf("memory is required")
	}

	doc := limaYAML{
		MinimumLimaVersion: "2.0.0",
		Images:             ubuntuImages,
		CPUs:               opts.CPUs,
		Memory:             opts.Memory,
		User: limaUser{
			Name:             GuestUser,
			Home:             GuestUserHome,
			Shell:            GuestShell,
			PasswordlessSudo: true,
		},
		Mounts: []limaMount{
			{Location: filepath.Clean(opts.ProjectRoot), MountPoint: GuestProjectPath, Writable: true},
			{Location: filepath.Clean(opts.WorkspacesDir), MountPoint: WorkspacesGuestDir(opts.ProjectID), Writable: true},
		},
		Containerd: limaContainerd{System: false, User: false},
	}
	propagateProxy := false
	doc.PropagateProxyEnv = &propagateProxy
	for _, p := range opts.Ports {
		hostIP := p.HostIP
		if hostIP == "" {
			hostIP = "127.0.0.1"
		}
		doc.PortForwards = append(doc.PortForwards, limaPortForward{
			GuestPort: p.Guest,
			HostPort:  p.Host,
			HostIP:    hostIP,
		})
	}
	// Lima appends an implicit catch-all that forwards every guest localhost
	// port to host localhost. In explicit mode, add an ignore-all rule before
	// that fallback so guest services are only exposed when configured.
	if opts.PortForwarding == config.PortForwardingExplicit {
		allowAny := false
		doc.PortForwards = append(doc.PortForwards, limaPortForward{
			GuestIP:           "0.0.0.0",
			GuestIPMustBeZero: &allowAny,
			Proto:             "any",
			Ignore:            true,
		})
	}

	var buf bytes.Buffer
	buf.WriteString("# Generated by boxt. Do not edit; this file is overwritten.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("encode Lima configuration: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode Lima configuration: %w", err)
	}
	return buf.Bytes(), nil
}

// WriteConfig atomically writes the generated Lima YAML and returns its path.
func WriteConfig(vmDir string, data []byte) (string, error) {
	path := filepath.Join(vmDir, ConfigFileName)
	if err := fsutil.WriteFileAtomic(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Fingerprint hashes the user-controlled VM settings that Lima applies only at
// instance creation. Image/version changes are intentionally excluded so
// upgrading boxt does not force every VM to be recreated.
func Fingerprint(opts VMOptions) string {
	portForwarding := opts.PortForwarding
	if portForwarding == "" {
		portForwarding = config.PortForwardingAuto
	}
	memory := opts.Memory
	if b, err := config.MemoryBytes(memory); err == nil {
		memory = fmt.Sprintf("%d", b)
	}
	type port struct {
		Guest  int
		Host   int
		HostIP string
	}
	ports := make([]port, 0, len(opts.Ports))
	for _, p := range opts.Ports {
		ip := p.HostIP
		if ip == "" {
			ip = "127.0.0.1"
		}
		ports = append(ports, port{Guest: p.Guest, Host: p.Host, HostIP: ip})
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Guest != ports[j].Guest {
			return ports[i].Guest < ports[j].Guest
		}
		return ports[i].Host < ports[j].Host
	})
	payload := struct {
		ProjectID     string
		ProjectRoot   string
		WorkspacesDir string
		CPUs          int
		Memory        string
		Forwarding    string
		Ports         []port
	}{
		ProjectID:     opts.ProjectID,
		ProjectRoot:   filepath.Clean(opts.ProjectRoot),
		WorkspacesDir: filepath.Clean(opts.WorkspacesDir),
		CPUs:          opts.CPUs,
		Memory:        memory,
		Forwarding:    portForwarding,
		Ports:         ports,
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// WriteFingerprint records the applied VM configuration fingerprint.
func WriteFingerprint(vmDir, fingerprint string) error {
	path := filepath.Join(vmDir, FingerprintFileName)
	return fsutil.WriteFileAtomic(path, []byte(fingerprint+"\n"), 0o644)
}

// MountLocations returns the exact host paths mounted by a generated config.
// It is used by tests and diagnostics.
func MountLocations(data []byte) ([]string, error) {
	var doc limaYAML
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(doc.Mounts))
	for _, m := range doc.Mounts {
		out = append(out, m.Location)
	}
	return out, nil
}

func canonicalCompare(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ValidateGenerated performs basic structural checks independent of limactl.
func ValidateGenerated(data []byte) error {
	var doc limaYAML
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("invalid generated YAML: %w", err)
	}
	if len(doc.Images) == 0 {
		return fmt.Errorf("generated config has no images")
	}
	if doc.User.Name != GuestUser {
		return fmt.Errorf("generated config guest user = %q, want %q", doc.User.Name, GuestUser)
	}
	if doc.CPUs < 1 || doc.Memory == "" {
		return fmt.Errorf("generated config has invalid resources")
	}
	foundProject, foundWorkspaces := false, false
	for _, m := range doc.Mounts {
		if m.Location == "" || !filepath.IsAbs(m.Location) {
			return fmt.Errorf("generated mount location is not absolute: %q", m.Location)
		}
		switch m.MountPoint {
		case GuestProjectPath:
			foundProject = true
		case "/workspaces":
			// not used; project-scoped mount is required
		default:
			if strings.HasPrefix(m.MountPoint, GuestWorkspaces+"/") {
				foundWorkspaces = true
			}
		}
		if !m.Writable {
			return fmt.Errorf("generated mount %s is not writable", m.MountPoint)
		}
	}
	if !foundProject || !foundWorkspaces {
		return fmt.Errorf("generated config is missing project or workspace mount")
	}
	return nil
}
