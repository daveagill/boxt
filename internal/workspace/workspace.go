// Package workspace manages named workspaces backed by Git worktrees.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/git"
	"github.com/daveagill/boxt/internal/lima"
	"github.com/daveagill/boxt/internal/project"
)

// DefaultName is reserved for the implicit project-root workspace.
const DefaultName = "default"

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidateName rejects path traversal, absolute paths, reserved names and
// names that are not safe as single directory components.
func ValidateName(name string) error {
	if name == DefaultName {
		return fmt.Errorf("%q is reserved for the implicit default workspace", name)
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid workspace name %q (use 1-64 lowercase letters, digits, '.', '_' or '-', starting with a letter or digit)", name)
	}
	return nil
}

// Workspace is one working directory inside a project.
type Workspace struct {
	Name       string
	Branch     string
	HostPath   string
	GuestPath  string
	Default    bool
	Missing    bool
	Broken     bool
	Locked     bool
	LockReason string
}

// Manager operates on one project's named workspaces.
type Manager struct {
	ProjectRoot string
	ProjectID   string
	HostRoot    string
	Git         *git.Client
}

// New returns a workspace manager. HostRoot is the project-scoped directory
// under the boxt data directory.
func New(projectRoot, projectID, hostRoot string, gitClient *git.Client) *Manager {
	return &Manager{
		ProjectRoot: projectRoot,
		ProjectID:   projectID,
		HostRoot:    hostRoot,
		Git:         gitClient,
	}
}

// HostPath returns the host path for a workspace name without touching disk.
func (m *Manager) HostPath(name string) (string, error) {
	if name == DefaultName {
		return m.ProjectRoot, nil
	}
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return filepath.Join(m.HostRoot, name), nil
}

// GuestPath returns the deterministic guest path for a workspace name.
func (m *Manager) GuestPath(name string) string {
	if name == DefaultName {
		return lima.GuestProjectPath
	}
	return filepath.ToSlash(filepath.Join(lima.WorkspacesGuestDir(m.ProjectID), name))
}

// Default returns the implicit project-root workspace.
func (m *Manager) Default(ctx context.Context) Workspace {
	ws := Workspace{
		Name:      DefaultName,
		HostPath:  m.ProjectRoot,
		GuestPath: lima.GuestProjectPath,
		Default:   true,
	}
	if _, err := m.repoRoot(ctx); err == nil {
		if branch, err := m.Git.Output(ctx, m.ProjectRoot, "symbolic-ref", "--short", "HEAD"); err == nil {
			ws.Branch = strings.TrimSpace(branch)
		}
	}
	return ws
}

// List returns named workspaces managed by boxt, sorted by name. A non-Git
// project has no named workspaces rather than an error.
func (m *Manager) List(ctx context.Context) ([]Workspace, error) {
	root, err := m.repoRoot(ctx)
	if err != nil {
		var notGit *ErrNotGitRepository
		if errors.As(err, &notGit) {
			return nil, nil
		}
		return nil, err
	}
	hostRoot := m.canonicalHostRoot()
	if _, err := os.Stat(hostRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect workspace root: %w", err)
	}
	raws, err := m.worktrees(ctx)
	if err != nil {
		return nil, err
	}
	workspaces := make([]Workspace, 0, len(raws))
	for _, raw := range raws {
		name := filepath.Base(raw.Path)
		if err := ValidateName(name); err != nil {
			continue
		}
		if filepath.Dir(canonicalPath(raw.Path)) != hostRoot {
			continue
		}
		if canonicalPath(raw.Path) == canonicalPath(root) {
			continue
		}
		ws := Workspace{
			Name:       name,
			Branch:     raw.Branch,
			HostPath:   raw.Path,
			GuestPath:  m.GuestPath(name),
			Missing:    raw.Prunable,
			Locked:     raw.Locked,
			LockReason: raw.LockReason,
		}
		info, statErr := os.Stat(raw.Path)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			ws.Missing = true
			ws.Broken = false
		case statErr != nil:
			return nil, fmt.Errorf("inspect workspace %s: %w", raw.Path, statErr)
		case !info.IsDir():
			ws.Missing = false
			ws.Broken = true
		case raw.Prunable:
			ws.Missing = false
			ws.Broken = true
		}
		workspaces = append(workspaces, ws)
	}
	sort.Slice(workspaces, func(i, j int) bool { return workspaces[i].Name < workspaces[j].Name })
	return workspaces, nil
}

// Get resolves a workspace by name. The default workspace is implicit.
func (m *Manager) Get(ctx context.Context, name string) (Workspace, error) {
	if name == DefaultName {
		return m.Default(ctx), nil
	}
	if err := ValidateName(name); err != nil {
		return Workspace{}, err
	}
	if _, err := m.repoRoot(ctx); err != nil {
		return Workspace{}, err
	}
	list, err := m.List(ctx)
	if err != nil {
		return Workspace{}, err
	}
	for _, ws := range list {
		if ws.Name == name {
			if ws.Missing {
				return Workspace{}, &ErrMissing{Name: name, Path: ws.HostPath}
			}
			if ws.Broken {
				return Workspace{}, &ErrBroken{Name: name, Path: ws.HostPath}
			}
			return ws, nil
		}
	}
	return Workspace{}, &ErrNotFound{Name: name}
}

// IsGit reports whether named workspaces are supported for this project.
func (m *Manager) IsGit(ctx context.Context) (bool, error) {
	_, err := m.repoRoot(ctx)
	if err == nil {
		return true, nil
	}
	var notGit *ErrNotGitRepository
	if errors.As(err, &notGit) {
		return false, nil
	}
	return false, err
}

// Create adds a Git worktree under the project-scoped workspace root. The
// default branch name is boxt/<name>; an explicit branch may be supplied.
func (m *Manager) Create(ctx context.Context, name, branch string) (Workspace, error) {
	if err := ValidateName(name); err != nil {
		return Workspace{}, err
	}
	root, err := m.repoRoot(ctx)
	if err != nil {
		return Workspace{}, err
	}
	if branch == "" {
		branch = "boxt/" + name
	}
	if strings.HasPrefix(branch, "-") {
		return Workspace{}, fmt.Errorf("invalid branch name %q: must not start with '-'", branch)
	}
	if _, err := m.Git.Output(ctx, root, "check-ref-format", "--branch", branch); err != nil {
		return Workspace{}, fmt.Errorf("invalid branch name %q: %w", branch, err)
	}

	if err := os.MkdirAll(m.canonicalHostRoot(), 0o755); err != nil {
		return Workspace{}, fmt.Errorf("create workspace root: %w", err)
	}
	hostRoot := m.canonicalHostRoot()
	path := filepath.Join(hostRoot, name)
	if _, err := os.Stat(path); err == nil {
		return Workspace{}, &ErrExists{Name: name}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Workspace{}, fmt.Errorf("inspect workspace path: %w", err)
	}

	// Stale worktree metadata for this exact workspace must not block reuse of
	// its name. Remove only that entry; never prune unrelated worktrees.
	if list, err := m.List(ctx); err != nil {
		return Workspace{}, err
	} else {
		for _, ws := range list {
			if ws.Name != name {
				continue
			}
			if !ws.Missing {
				return Workspace{}, &ErrExists{Name: name}
			}
			if err := m.removeStale(ctx, root, ws.HostPath); err != nil {
				return Workspace{}, fmt.Errorf("clean stale metadata for workspace %s: %w", name, err)
			}
		}
	}
	if list, err := m.List(ctx); err != nil {
		return Workspace{}, err
	} else {
		for _, ws := range list {
			if ws.Name == name {
				return Workspace{}, &ErrExists{Name: name}
			}
		}
	}

	branchExists, err := m.branchExists(ctx, root, branch)
	if err != nil {
		return Workspace{}, err
	}
	unborn, err := m.unbornHEAD(ctx, root)
	if err != nil {
		return Workspace{}, err
	}
	if unborn && branchExists {
		return Workspace{}, fmt.Errorf("branch %q already exists; choose another branch name", branch)
	}

	args := []string{"worktree", "add"}
	if unborn {
		args = append(args, "--orphan")
	}
	args = append(args, path)
	createdBranch := false
	if branchExists {
		args = append(args, branch)
	} else {
		args = append(args, "-b", branch)
		createdBranch = true
	}
	if _, err := m.Git.Output(ctx, root, args...); err != nil {
		// Git can leak a branch created by -b when the worktree add fails.
		if createdBranch {
			if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
				_, _ = m.Git.Output(ctx, root, "branch", "-D", branch)
			}
		}
		return Workspace{}, m.mapCreateError(name, path, err)
	}

	top, err := m.Git.TopLevel(ctx, path)
	if err != nil {
		return m.rollbackCreated(ctx, root, path, branch, createdBranch, fmt.Errorf("verify workspace checkout: %w", err))
	}
	if canonicalPath(top) != canonicalPath(path) {
		return m.rollbackCreated(ctx, root, path, branch, createdBranch, fmt.Errorf("verify workspace checkout: Git reports %s for %s", top, path))
	}
	gitDir, err := m.Git.Output(ctx, path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return m.rollbackCreated(ctx, root, path, branch, createdBranch, fmt.Errorf("locate workspace Git metadata: %w", err))
	}
	if err := project.WriteWorktreeMetadata(gitDir, m.ProjectRoot); err != nil {
		return m.rollbackCreated(ctx, root, path, branch, createdBranch, fmt.Errorf("record project metadata for workspace %s: %w", name, err))
	}
	return Workspace{
		Name:      name,
		Branch:    branch,
		HostPath:  path,
		GuestPath: m.GuestPath(name),
	}, nil
}

func (m *Manager) rollbackCreated(ctx context.Context, root, path, branch string, createdBranch bool, cause error) (Workspace, error) {
	_, rmErr := m.Git.Output(ctx, root, "worktree", "remove", "--force", path)
	if createdBranch {
		_, _ = m.Git.Output(ctx, root, "branch", "-D", branch)
	}
	if rmErr != nil && !isGoneWorktree(rmErr) {
		return Workspace{}, fmt.Errorf("%w (rollback failed: %v)", cause, rmErr)
	}
	return Workspace{}, cause
}

// MergeResult describes a successful fast-forward merge.
type MergeResult struct {
	Workspace      Workspace
	SourceBranch   string
	TargetBranch   string
	OldHead        string
	NewHead        string
	AlreadyCurrent bool
	Removed        bool
	RemoveErr      error
}

// Merge fast-forwards the branch checked out in the project root to the named
// workspace's branch. It never creates merge commits, rebases, squashes or
// forces refs. When remove is set, the worktree is removed through the same safe
// path as Remove after the merge, keeping the branch.
func (m *Manager) Merge(ctx context.Context, name, into string, remove bool) (MergeResult, error) {
	if name == DefaultName {
		return MergeResult{}, &ErrDefaultWorkspace{}
	}
	if err := ValidateName(name); err != nil {
		return MergeResult{}, err
	}
	root, err := m.repoRoot(ctx)
	if err != nil {
		return MergeResult{}, err
	}
	ws, err := m.Get(ctx, name)
	if err != nil {
		return MergeResult{}, err
	}
	if ws.Locked {
		return MergeResult{}, &ErrLocked{Name: name, Path: ws.HostPath, Reason: ws.LockReason}
	}
	if ws.Branch == "" {
		return MergeResult{}, &ErrMergeDetached{Role: "workspace " + name}
	}

	target, err := m.Git.Output(ctx, root, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return MergeResult{}, &ErrMergeDetached{Role: "project root"}
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return MergeResult{}, &ErrMergeDetached{Role: "project root"}
	}
	if into != "" && into != target {
		return MergeResult{}, &ErrMergeTarget{Requested: into, Current: target}
	}
	if ws.Branch == target {
		return MergeResult{}, fmt.Errorf("workspace %s already uses the target branch %s", name, target)
	}

	if err := m.requireClean(ctx, ws.HostPath, "workspace "+name); err != nil {
		return MergeResult{}, err
	}
	if err := m.requireClean(ctx, root, "project root"); err != nil {
		return MergeResult{}, err
	}

	if err := m.isAncestor(ctx, root, target, ws.Branch); err != nil {
		return MergeResult{}, err
	}
	oldHead, err := m.Git.Output(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return MergeResult{}, fmt.Errorf("inspect target branch: %w", err)
	}
	if strings.HasPrefix(ws.Branch, "-") {
		return MergeResult{}, fmt.Errorf("invalid source branch %q", ws.Branch)
	}
	if _, err := m.Git.Output(ctx, root, "merge", "--ff-only", ws.Branch); err != nil {
		return MergeResult{}, fmt.Errorf("fast-forward %s into %s: %w", ws.Branch, target, err)
	}
	newHead, err := m.Git.Output(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return MergeResult{}, fmt.Errorf("inspect merged branch: %w", err)
	}

	result := MergeResult{
		Workspace:      ws,
		SourceBranch:   ws.Branch,
		TargetBranch:   target,
		OldHead:        strings.TrimSpace(oldHead),
		NewHead:        strings.TrimSpace(newHead),
		AlreadyCurrent: strings.TrimSpace(oldHead) == strings.TrimSpace(newHead),
	}
	if remove {
		if _, err := m.Remove(ctx, name, false); err != nil {
			result.RemoveErr = err
		} else {
			result.Removed = true
		}
	}
	return result, nil
}

func (m *Manager) requireClean(ctx context.Context, dir, role string) error {
	status, err := m.Git.Output(ctx, dir, "status", "--porcelain", "--ignore-submodules=none")
	if err != nil {
		return fmt.Errorf("check %s status: %w", role, err)
	}
	if status != "" {
		return &ErrMergeDirty{Role: role, Path: dir}
	}
	return nil
}

func (m *Manager) isAncestor(ctx context.Context, root, ancestor, descendant string) error {
	_, err := m.Git.Output(ctx, root, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return nil
	}
	if exitStatus(err) == 1 {
		return &ErrNotFastForward{Source: descendant, Target: ancestor}
	}
	return fmt.Errorf("check fast-forward ancestry: %w", err)
}

// Remove deletes a named worktree. Dirty worktrees require force. Branch refs
// are always retained and the directory is never removed with a manual rm.
func (m *Manager) Remove(ctx context.Context, name string, force bool) (Workspace, error) {
	if name == DefaultName {
		return Workspace{}, &ErrDefaultWorkspace{}
	}
	if err := ValidateName(name); err != nil {
		return Workspace{}, err
	}
	root, err := m.repoRoot(ctx)
	if err != nil {
		return Workspace{}, err
	}
	ws, err := m.find(ctx, name)
	if err != nil {
		return Workspace{}, err
	}
	if ws.Locked {
		return Workspace{}, &ErrLocked{Name: name, Path: ws.HostPath, Reason: ws.LockReason}
	}
	if ws.Broken {
		return Workspace{}, &ErrBroken{Name: name, Path: ws.HostPath}
	}
	if ws.Missing {
		if err := m.removeStale(ctx, root, ws.HostPath); err != nil {
			return Workspace{}, fmt.Errorf("clean stale workspace metadata: %w", err)
		}
		return ws, nil
	}

	status, err := m.Git.Output(ctx, ws.HostPath, "status", "--porcelain", "--ignored=matching", "--ignore-submodules=none")
	if err != nil {
		return Workspace{}, fmt.Errorf("check workspace status: %w", err)
	}
	dirty, ignored := workspaceChanges(status)
	if !force {
		switch {
		case dirty:
			return Workspace{}, &ErrDirty{Name: name}
		case ignored:
			return Workspace{}, &ErrIgnoredFiles{Name: name}
		}
	}

	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, ws.HostPath)
	if _, err := m.Git.Output(ctx, root, args...); err != nil {
		if isGoneWorktree(err) {
			return ws, nil
		}
		return Workspace{}, m.mapRemoveError(name, ws.HostPath, err)
	}
	if _, statErr := os.Stat(ws.HostPath); statErr == nil {
		return Workspace{}, fmt.Errorf("Git removed the worktree metadata, but the directory still exists: %s\n\nboxt does not delete directories outside Git's worktree command", ws.HostPath)
	}
	return ws, nil
}

type rawWorktree struct {
	Path        string
	Branch      string
	Locked      bool
	LockReason  string
	Prunable    bool
	PruneReason string
}

func (m *Manager) find(ctx context.Context, name string) (Workspace, error) {
	list, err := m.List(ctx)
	if err != nil {
		return Workspace{}, err
	}
	for _, ws := range list {
		if ws.Name == name {
			return ws, nil
		}
	}
	return Workspace{}, &ErrNotFound{Name: name}
}

// removeStale removes Git metadata for one missing worktree. It never prunes
// unrelated worktrees or deletes a directory.
func (m *Manager) removeStale(ctx context.Context, root, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("workspace directory still exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := m.Git.Output(ctx, root, "worktree", "remove", path); err != nil {
		if isGoneWorktree(err) {
			return nil
		}
		return err
	}
	return nil
}

func (m *Manager) worktrees(ctx context.Context) ([]rawWorktree, error) {
	out, err := m.Git.Output(ctx, m.ProjectRoot, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("list Git worktrees: %w", err)
	}
	return parseWorktrees(out), nil
}

func parseWorktrees(out string) []rawWorktree {
	var result []rawWorktree
	var cur rawWorktree
	flush := func() {
		if cur.Path != "" {
			result = append(result, cur)
			cur = rawWorktree{}
		}
	}
	for _, field := range strings.Split(out, "\x00") {
		if field == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(field, "worktree "):
			cur.Path = strings.TrimPrefix(field, "worktree ")
		case strings.HasPrefix(field, "HEAD "):
		case strings.HasPrefix(field, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(field, "branch "), "refs/heads/")
		case field == "detached", field == "bare":
		case field == "locked":
			cur.Locked = true
		case strings.HasPrefix(field, "locked "):
			cur.Locked = true
			cur.LockReason = strings.TrimPrefix(field, "locked ")
		case strings.HasPrefix(field, "prunable"):
			cur.Prunable = true
			cur.PruneReason = strings.TrimSpace(strings.TrimPrefix(field, "prunable"))
		}
	}
	flush()
	return result
}

func (m *Manager) repoRoot(ctx context.Context) (string, error) {
	if m.Git == nil {
		return "", errors.New("workspace manager has no Git client")
	}
	top, err := m.Git.TopLevel(ctx, m.ProjectRoot)
	if err != nil {
		if errors.Is(err, git.ErrNotRepository) {
			return "", &ErrNotGitRepository{Root: m.ProjectRoot}
		}
		return "", err
	}
	top = canonicalPath(top)
	root := canonicalPath(m.ProjectRoot)
	if top != root {
		return "", &ErrRepositoryRoot{Root: root, Top: top}
	}
	return root, nil
}

func (m *Manager) canonicalHostRoot() string {
	return canonicalPath(m.HostRoot)
}

func (m *Manager) branchExists(ctx context.Context, root, branch string) (bool, error) {
	_, err := m.Git.Output(ctx, root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	if exitStatus(err) == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect branch %q: %w", branch, err)
}

func (m *Manager) unbornHEAD(ctx context.Context, root string) (bool, error) {
	_, err := m.Git.Output(ctx, root, "rev-parse", "--quiet", "--verify", "HEAD")
	if err == nil {
		return false, nil
	}
	if exitStatus(err) == 1 {
		return true, nil
	}
	return false, fmt.Errorf("inspect repository HEAD: %w", err)
}

func (m *Manager) mapCreateError(name, path string, err error) error {
	var cmdErr *command.Error
	if errors.As(err, &cmdErr) {
		msg := cmdErr.Stderr
		switch {
		case strings.Contains(msg, "a branch named") && strings.Contains(msg, "already exists"):
			return fmt.Errorf("the branch for workspace %s already exists; choose a different `--branch` or workspace name", name)
		case strings.Contains(msg, "is already used by worktree"), strings.Contains(msg, "is already checked out at"):
			return fmt.Errorf("the selected branch is already checked out in another worktree; choose a different branch")
		case strings.Contains(msg, "already exists"):
			return &ErrExists{Name: name}
		}
	}
	return fmt.Errorf("create worktree %s: %w", path, err)
}

func (m *Manager) mapRemoveError(name, path string, err error) error {
	var cmdErr *command.Error
	if errors.As(err, &cmdErr) {
		msg := cmdErr.Stderr
		switch {
		case strings.Contains(msg, "contains modified or untracked files"):
			return &ErrDirty{Name: name}
		case strings.Contains(msg, "locked working tree"):
			return &ErrLocked{Name: name, Path: path}
		case strings.Contains(msg, "submodules"):
			return fmt.Errorf("workspace %s contains submodules; re-run with --force to remove it: %w", name, err)
		}
	}
	return fmt.Errorf("remove worktree %s: %w", path, err)
}

func isGoneWorktree(err error) bool {
	var cmdErr *command.Error
	if !errors.As(err, &cmdErr) {
		return false
	}
	msg := strings.ToLower(cmdErr.Stderr)
	return strings.Contains(msg, "is not a working tree") || strings.Contains(msg, "does not exist")
}

func exitStatus(err error) int {
	var coder command.ExitCoder
	if errors.As(err, &coder) {
		return coder.ExitStatus()
	}
	return -1
}

func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

// ErrNotGitRepository means named workspaces are unavailable.
type ErrNotGitRepository struct{ Root string }

func (e *ErrNotGitRepository) Error() string {
	return "named workspaces require a Git repository"
}
func (e *ErrNotGitRepository) Hint() string {
	return fmt.Sprintf("Initialize Git in %s with `git init`, make an initial commit, then try again.", e.Root)
}

// ErrRepositoryRoot means the project root is not the repository root.
type ErrRepositoryRoot struct{ Root, Top string }

func (e *ErrRepositoryRoot) Error() string {
	return "named workspaces require the project root to be the Git repository root"
}
func (e *ErrRepositoryRoot) Hint() string {
	return fmt.Sprintf("The Git repository root is %s, but the project root is %s.\nMove .boxt/config.yaml to the repository root or create workspaces from there.", e.Top, e.Root)
}

// ErrExists means the workspace name is already used.
type ErrExists struct{ Name string }

func (e *ErrExists) Error() string { return "Workspace already exists: " + e.Name }

// ErrNotFound means no such named workspace exists.
type ErrNotFound struct{ Name string }

func (e *ErrNotFound) Error() string { return "Workspace not found: " + e.Name }

// ErrMissing indicates stale Git metadata for a workspace whose directory is
// gone.
type ErrMissing struct{ Name, Path string }

func (e *ErrMissing) Error() string {
	return fmt.Sprintf("Workspace %s is missing at %s", e.Name, e.Path)
}
func (e *ErrMissing) Hint() string {
	return "The Git worktree metadata is stale. Run `boxt workspace remove " + e.Name + "` to prune it."
}

// ErrBroken indicates a workspace directory or Git link that needs manual
// repair; boxt does not delete it automatically.
type ErrBroken struct{ Name, Path string }

func (e *ErrBroken) Error() string {
	return fmt.Sprintf("Workspace %s is broken at %s", e.Name, e.Path)
}
func (e *ErrBroken) Hint() string {
	return fmt.Sprintf("Inspect %s and its .git file, or repair it with `git worktree repair %s`. boxt did not delete anything.", e.Path, e.Path)
}

// ErrDefaultWorkspace means an operation attempted to create or remove the
// implicit default workspace.
type ErrDefaultWorkspace struct{}

func (e *ErrDefaultWorkspace) Error() string {
	return "the default workspace is implicit and cannot be created, merged, seeded or removed"
}

// ErrMergeDetached refuses to merge without a branch to integrate.
type ErrMergeDetached struct{ Role string }

func (e *ErrMergeDetached) Error() string {
	return fmt.Sprintf("cannot merge: %s has a detached HEAD", e.Role)
}
func (e *ErrMergeDetached) Hint() string {
	return "Check out a branch first; boxt only fast-forwards named branches."
}

// ErrMergeDirty refuses to merge with uncommitted tracked or untracked files.
type ErrMergeDirty struct {
	Role string
	Path string
}

func (e *ErrMergeDirty) Error() string {
	return fmt.Sprintf("cannot merge: %s has uncommitted changes", e.Role)
}
func (e *ErrMergeDirty) Hint() string {
	return fmt.Sprintf("Commit or stash changes in %s, then try again. Ignored files may remain.", e.Path)
}

// ErrMergeTarget refuses --into a branch that is not checked out in the project
// root.
type ErrMergeTarget struct{ Requested, Current string }

func (e *ErrMergeTarget) Error() string {
	return fmt.Sprintf("cannot merge into %s: the project root has %s checked out", e.Requested, e.Current)
}
func (e *ErrMergeTarget) Hint() string {
	return fmt.Sprintf("Check out %s in the project root, or merge into the current branch %s.", e.Requested, e.Current)
}

// ErrNotFastForward refuses a merge that would need a merge commit.
type ErrNotFastForward struct{ Source, Target string }

func (e *ErrNotFastForward) Error() string {
	return fmt.Sprintf("cannot fast-forward %s to %s: their histories have diverged", e.Target, e.Source)
}
func (e *ErrNotFastForward) Hint() string {
	return "No refs were changed. Rebase or merge the branch manually with Git, or recreate the workspace from the current target branch."
}

func workspaceChanges(status string) (dirty, ignored bool) {
	for _, line := range strings.Split(status, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "!!") {
			ignored = true
		} else {
			dirty = true
		}
	}
	return dirty, ignored
}

// ErrDirty refuses to delete uncommitted work.
type ErrDirty struct{ Name string }

func (e *ErrDirty) Error() string {
	return fmt.Sprintf("Cannot remove workspace %s:\nit contains uncommitted changes.", e.Name)
}
func (e *ErrDirty) Hint() string {
	return fmt.Sprintf("Commit or stash your changes before removing it.\n\nUse `boxt workspace remove %s --force` to discard them.", e.Name)
}

// ErrIgnoredFiles refuses to delete ignored files such as .env or caches.
type ErrIgnoredFiles struct{ Name string }

func (e *ErrIgnoredFiles) Error() string {
	return fmt.Sprintf("Cannot remove workspace %s:\nit contains ignored files that would be deleted.", e.Name)
}
func (e *ErrIgnoredFiles) Hint() string {
	return fmt.Sprintf("Use `boxt workspace remove %s --force` to discard ignored files such as .env, caches or build output.", e.Name)
}

// ErrLocked refuses to remove a Git-locked worktree.
type ErrLocked struct {
	Name   string
	Path   string
	Reason string
}

func (e *ErrLocked) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("Cannot remove workspace %s: it is locked by Git (%s)", e.Name, e.Reason)
	}
	return fmt.Sprintf("Cannot remove workspace %s: it is locked by Git", e.Name)
}
func (e *ErrLocked) Hint() string {
	return fmt.Sprintf("Unlock it first with `git worktree unlock %s`.", e.Path)
}
