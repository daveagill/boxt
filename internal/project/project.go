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
	"github.com/boxt/boxt/internal/fsutil"
)

// ConfigRelPath is the project marker file, relative to the project root.
const ConfigRelPath = ".boxt/config.yaml"

// WorktreeMetadataFile is stored inside a linked worktree's Git directory so
// commands run from that workspace resolve to the owning project.
const WorktreeMetadataFile = "boxt-project"

// Project is an initialized boxt project.
type Project struct {
	Root       string
	ConfigPath string
	ID         string
	Config     *config.Config
}

// ErrUnsafeProjectRoot refuses to mount the host home directory or root.
type ErrUnsafeProjectRoot struct {
	Root string
	Home string
}

func (e *ErrUnsafeProjectRoot) Error() string {
	return fmt.Sprintf("refusing to use %s as a project root: it is the host home directory or an ancestor of it", e.Root)
}
func (e *ErrUnsafeProjectRoot) Hint() string {
	return fmt.Sprintf("The project root is mounted writable into the VM. Create the project in a subdirectory (for example %s/projects/myapp).", e.Home)
}

func validateProjectRoot(root string) error {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	canonicalHome, err := Canonical(home)
	if err != nil {
		return nil
	}
	if root == "/" || root == canonicalHome || isSubpath(root, canonicalHome) {
		return &ErrUnsafeProjectRoot{Root: root, Home: canonicalHome}
	}
	return nil
}

func isSubpath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ErrUnmanagedWorktree refuses to treat a linked Git worktree without boxt
// metadata as a separate project.
type ErrUnmanagedWorktree struct{ Path string }

func (e *ErrUnmanagedWorktree) Error() string {
	return fmt.Sprintf("refusing to treat %s as a separate project: it is a linked Git worktree without boxt metadata", e.Path)
}
func (e *ErrUnmanagedWorktree) Hint() string {
	return "Run commands from the main project root, or create a managed workspace with `boxt workspace create <name>`."
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
	if err := validateProjectRoot(canonical); err != nil {
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
// It recognizes boxt-managed linked worktrees, which store their owning project
// root in Git's per-worktree metadata. A nested project inside a workspace wins
// over the workspace's own metadata. It never initializes a project implicitly.
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
	cfgDir, err := findConfigDir(canonical)
	if err != nil {
		return nil, err
	}
	workspaceRoot, projectRoot, hasMetadata := findWorktreeMetadata(canonical)
	if !hasMetadata && cfgDir != "" && isLinkedWorktree(cfgDir) {
		return nil, &ErrUnmanagedWorktree{Path: cfgDir}
	}
	switch {
	case hasMetadata && cfgDir == "":
		return Open(projectRoot)
	case hasMetadata && cfgDir == workspaceRoot:
		return Open(projectRoot)
	case hasMetadata && isSubpath(workspaceRoot, cfgDir):
		// A nested project inside the workspace checkout.
		return Open(cfgDir)
	case hasMetadata:
		return Open(projectRoot)
	case cfgDir != "":
		return Open(cfgDir)
	default:
		return nil, &ErrNotInitialized{Start: canonical}
	}
}

func findConfigDir(start string) (string, error) {
	dir := start
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	for {
		cfgPath := filepath.Join(dir, filepath.FromSlash(ConfigRelPath))
		if info, err := os.Stat(cfgPath); err == nil {
			if info.IsDir() {
				return "", fmt.Errorf("%s is a directory, expected a configuration file", cfgPath)
			}
			return dir, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect %s: %w", cfgPath, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// WriteWorktreeMetadata records the owning project root inside a linked
// worktree's Git directory. Git removes this directory with the worktree.
func WriteWorktreeMetadata(gitDir, projectRoot string) error {
	if gitDir == "" {
		return errors.New("Git directory is required")
	}
	root, err := Canonical(projectRoot)
	if err != nil {
		return err
	}
	path := filepath.Join(gitDir, WorktreeMetadataFile)
	return fsutil.WriteFileAtomic(path, []byte(root+"\n"), 0o644)
}

// findWorktreeMetadata checks whether start is inside a linked Git worktree
// with boxt metadata. It returns the worktree root, owning project root and
// whether metadata was found.
func findWorktreeMetadata(start string) (workspaceRoot, projectRoot string, ok bool) {
	dir := start
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	for {
		gitPath := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			if info.IsDir() {
				return "", "", false
			}
			gitDir, valid := readGitDirFile(gitPath, dir)
			if !valid {
				return "", "", false
			}
			data, err := os.ReadFile(filepath.Join(gitDir, WorktreeMetadataFile))
			if err == nil {
				root := strings.TrimSpace(string(data))
				if root != "" {
					canonicalRoot, err := Canonical(root)
					if err == nil && canonicalRoot != "" {
						return dir, canonicalRoot, true
					}
				}
			}
			// Keep walking: a nested project inside a workspace or submodule
			// may still resolve to an outer workspace's metadata.
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
}

func readGitDirFile(path, base string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return "", false
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if gitDir == "" {
		return "", false
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(base, gitDir)
	}
	return filepath.Clean(gitDir), true
}

func isLinkedWorktree(dir string) bool {
	gitDir, ok := readGitDirFile(filepath.Join(dir, ".git"), dir)
	if !ok {
		return false
	}
	return strings.Contains(filepath.ToSlash(gitDir), "/worktrees/")
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
	if err := validateProjectRoot(canonical); err != nil {
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
