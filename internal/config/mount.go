package config

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Mount is an additional host directory or file mounted into the guest.
// Writable is mandatory in configuration; the pointer lets validation reject
// an omitted field instead of silently choosing a mode.
type Mount struct {
	Host     string `yaml:"host"`
	Guest    string `yaml:"guest"`
	Writable *bool  `yaml:"writable"`
}

// WritableEnabled reports whether the mount is writable. Configuration always
// requires an explicit value; the nil fallback exists for programmatic callers.
func (m Mount) WritableEnabled() bool { return m.Writable == nil || *m.Writable }

// GuestPath returns the cleaned guest mount point.
func (m Mount) GuestPath() string { return path.Clean(m.Guest) }

// reservedGuestPaths are guest locations boxt or the guest system rely on.
// Custom mounts may live anywhere else, including elsewhere under /mnt.
var reservedGuestPaths = []string{
	"/workspace",
	"/workspaces",
	"/home",
	"/bin",
	"/boot",
	"/dev",
	"/etc",
	"/lib",
	"/lib64",
	"/proc",
	"/root",
	"/run",
	"/sbin",
	"/sys",
	"/usr",
}

// ExpandMountHost expands a leading ~ or ~/ in a host path. Other ~user forms
// are rejected rather than guessed.
func ExpandMountHost(host string) (string, error) {
	if host == "~" || strings.HasPrefix(host, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("determine home directory: %w", err)
		}
		if host == "~" {
			return home, nil
		}
		return filepath.Join(home, host[2:]), nil
	}
	if strings.HasPrefix(host, "~") {
		return "", fmt.Errorf("~user expansion is not supported: %s", host)
	}
	return host, nil
}

// ValidateMountSources checks that every configured host path exists and is a
// regular file or directory. It is run before a VM is created or started, not
// while loading configuration, so configurations remain portable.
func ValidateMountSources(mounts []Mount) error {
	for _, m := range mounts {
		info, err := os.Stat(m.Host)
		if err != nil {
			return fmt.Errorf("mount %s: cannot access host path %s: %w", m.GuestPath(), m.Host, err)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("mount %s: host path %s must be a regular file or directory", m.GuestPath(), m.Host)
		}
	}
	return nil
}

func validateMounts(v *validator, mounts []Mount) {
	type entry struct {
		index      int
		host       string
		hostValid  bool
		guest      string
		guestValid bool
	}
	home := ""
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		if resolved, err := filepath.EvalSymlinks(h); err == nil {
			h = resolved
		}
		home = filepath.Clean(h)
	}

	entries := make([]entry, 0, len(mounts))
	for i, m := range mounts {
		p := fmt.Sprintf("vm.mounts[%d]", i)
		e := entry{index: i}

		host := m.Host
		switch {
		case host == "":
			v.add(p+".host", "must not be empty")
		case strings.ContainsRune(host, 0):
			v.add(p+".host", "must not contain NUL")
		case !filepath.IsAbs(host):
			v.add(p+".host", "must be an absolute host path (expand ~ with `boxt mount add`)")
		default:
			clean := filepath.Clean(host)
			resolved := resolveMountHost(clean)
			e.host = resolved
			e.hostValid = true
			switch {
			case resolved == string(filepath.Separator):
				v.add(p+".host", "must not be the host root directory")
			case filepath.Base(resolved) == "docker.sock":
				v.add(p+".host", "must not mount a Docker socket")
			case home != "" && (resolved == home || isHostAncestor(resolved, home)):
				v.add(p+".host", "must not be the home directory or an ancestor of it")
			}
		}

		if m.Writable == nil {
			v.add(p+".writable", "is required (true or false)")
		}

		guest := m.Guest
		switch {
		case guest == "":
			v.add(p+".guest", "must not be empty")
		case strings.ContainsRune(guest, 0):
			v.add(p+".guest", "must not contain NUL")
		case !path.IsAbs(guest):
			v.add(p+".guest", "must be an absolute guest path")
		default:
			clean := path.Clean(guest)
			e.guest = clean
			e.guestValid = true
			switch reserved := reservedGuestPath(clean); {
			case clean == "/":
				v.add(p+".guest", "must not be the guest root directory")
			case reserved != "":
				v.add(p+".guest", "must not shadow the reserved guest path %s", reserved)
			}
		}
		entries = append(entries, e)
	}

	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			a, b := entries[i], entries[j]
			if a.hostValid && b.hostValid && a.host == b.host {
				v.add(fmt.Sprintf("vm.mounts[%d].host", b.index), "duplicates vm.mounts[%d] host path %s", a.index, b.host)
			}
			if !a.guestValid || !b.guestValid {
				continue
			}
			switch {
			case a.guest == b.guest:
				v.add(fmt.Sprintf("vm.mounts[%d].guest", b.index), "duplicates vm.mounts[%d] guest path %s", a.index, a.guest)
			case guestContains(a.guest, b.guest):
				v.add(fmt.Sprintf("vm.mounts[%d].guest", b.index), "overlaps vm.mounts[%d] guest path %s", a.index, a.guest)
			case guestContains(b.guest, a.guest):
				v.add(fmt.Sprintf("vm.mounts[%d].guest", a.index), "overlaps vm.mounts[%d] guest path %s", b.index, b.guest)
			}
		}
	}
}

func resolveMountHost(host string) string {
	if resolved, err := filepath.EvalSymlinks(host); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(host)
}

func isHostAncestor(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func guestContains(parent, child string) bool {
	return parent != child && strings.HasPrefix(child, strings.TrimSuffix(parent, "/")+"/")
}

func reservedGuestPath(guest string) string {
	for _, reserved := range reservedGuestPaths {
		if guest == reserved || guestContains(reserved, guest) {
			return reserved
		}
	}
	return ""
}
