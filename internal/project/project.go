// Package project handles project discovery and stable project identity.
package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/boxt/boxt/internal/config"
)

// ConfigRelPath is the project marker file, relative to the project root.
const ConfigRelPath = ".boxt/config.yaml"

// Project is an initialized boxt project.
type Project struct {
	Root       string
	ConfigPath string
	ID         string
	Config     *config.Config
}

// ErrNotInitialized indicates that no .boxt/config.yaml was found.
type ErrNotInitialized struct {
	Start string
}

func (e *ErrNotInitialized) Error() string {
	return "project not initialized: " + e.Start
}

// IsNotInitialized reports whether err means no project marker was found.
func IsNotInitialized(err error) bool {
	var target *ErrNotInitialized
	return errors.As(err, &target)
}

// Canonical returns an absolute, symlink-resolved, clean path.
func Canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return filepath.Clean(abs), nil
		}
		return "", fmt.Errorf("resolve path %s: %w", path, err)
	}
	return filepath.Clean(resolved), nil
}

// IDForRoot derives a stable, collision-resistant project ID from a canonical
// project root. The directory name is deliberately not used.
func IDForRoot(canonicalRoot string) string {
	sum := sha256.Sum256([]byte(canonicalRoot))
	return hex.EncodeToString(sum[:])[:16]
}

// VMName returns the stable Lima instance name for the project.
func (p *Project) VMName() string { return "boxt-" + p.ID }

// Open loads an initialized project rooted at root.
func Open(root string) (*Project, error) {
	canonical, err := Canonical(root)
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(canonical, filepath.FromSlash(ConfigRelPath))
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	return &Project{
		Root:       canonical,
		ConfigPath: cfgPath,
		ID:         IDForRoot(canonical),
		Config:     cfg,
	}, nil
}

// Lookup finds the nearest ancestor of start containing .boxt/config.yaml.
// It never initializes a project implicitly.
func Lookup(start string) (*Project, error) {
	if start == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("determine working directory: %w", err)
		}
	}
	canonical, err := Canonical(start)
	if err != nil {
		return nil, err
	}
	dir := canonical
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	for {
		cfgPath := filepath.Join(dir, filepath.FromSlash(ConfigRelPath))
		if info, err := os.Stat(cfgPath); err == nil {
			if info.IsDir() {
				return nil, fmt.Errorf("%s is a directory, expected a configuration file", cfgPath)
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			return &Project{
				Root:       dir,
				ConfigPath: cfgPath,
				ID:         IDForRoot(dir),
				Config:     cfg,
			}, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect %s: %w", cfgPath, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, &ErrNotInitialized{Start: canonical}
		}
		dir = parent
	}
}

// Init creates a new project configuration at path. Existing configuration is
// never overwritten.
func Init(path string) (*Project, error) {
	if path == "" {
		var err error
		path, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("determine working directory: %w", err)
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create project directory: %w", err)
	}
	canonical, err := Canonical(abs)
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(canonical, filepath.FromSlash(ConfigRelPath))
	if err := config.Create(cfgPath); err != nil {
		if errors.Is(err, config.ErrConfigExists) {
			return nil, fmt.Errorf("already initialized: %s (existing configuration was not overwritten)", cfgPath)
		}
		return nil, err
	}
	return Open(canonical)
}

// String is useful in status and tests.
func (p *Project) String() string {
	return fmt.Sprintf("%s (%s)", p.Root, p.ID)
}

// DescribeRoot is a helper for user-facing messages.
func (p *Project) DescribeRoot() string {
	return strings.TrimRight(p.Root, string(filepath.Separator))
}
