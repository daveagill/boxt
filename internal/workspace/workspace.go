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
	"strconv"
	"strings"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/config"
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
	Head       string
	HostPath   string
	GuestPath  string
	Default    bool
	Missing    bool
	Broken     bool
	Locked     bool
	LockReason string
	Dirty      bool
	Ahead      int
	Behind     int
	Compared   bool
	// Operation names an in-progress Git operation in the worktree, such as
	// "rebase", "merge" or "cherry-pick". Empty when the worktree is idle.
	Operation string
	// NewBranch is set only on the result of Create when the branch did not
	// exist before. It lets callers roll back setup failures safely.
	NewBranch bool
}

// Manager operates on one project's named workspaces.
type Manager struct {
	ProjectRoot string
	ProjectID   string
	HostRoot    string
	Git         *git.Client

	// copier overrides the seeding copy engine in tests; nil uses the
	// production reflink/byte-copy engine.
	copier *seedCopier
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

// NameForHostPath reports the named workspace whose checkout contains
// hostPath. It never returns DefaultName: callers fall back to the default
// workspace when the path is outside every managed worktree. The check is
// purely host-side and never starts the VM.
func (m *Manager) NameForHostPath(hostPath string) (string, bool) {
	if hostPath == "" || m.HostRoot == "" {
		return "", false
	}
	root, err := project.Canonical(m.HostRoot)
	if err != nil {
		return "", false
	}
	canonical, err := project.Canonical(hostPath)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, canonical)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	name := strings.Split(rel, string(filepath.Separator))[0]
	if err := ValidateName(name); err != nil {
		return "", false
	}
	info, err := os.Stat(filepath.Join(root, name))
	if err != nil || !info.IsDir() {
		return "", false
	}
	return name, true
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
		if head, err := m.Git.Output(ctx, m.ProjectRoot, "rev-parse", "--short", "HEAD"); err == nil {
			ws.Head = strings.TrimSpace(head)
		}
		if dirty, err := m.dirty(ctx, m.ProjectRoot); err == nil {
			ws.Dirty = dirty
		}
	}
	return ws
}

// List returns named workspaces managed by boxt, sorted by name. A non-Git
// project has no named workspaces rather than an error. Dirty state and
// ahead/behind counts are best-effort so one missing or detached worktree does
// not fail the whole list.
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
			Head:       shortHash(raw.Head),
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
		if !ws.Missing && !ws.Broken {
			if dirty, err := m.dirty(ctx, ws.HostPath); err == nil {
				ws.Dirty = dirty
			}
			if op, err := m.pendingOperation(ctx, ws.HostPath); err == nil {
				ws.Operation = op
			}
			if ws.Operation == "rebase" && ws.Branch == "" {
				// A rebase detaches HEAD. Recover the branch being rebased so
				// list output and ahead/behind stay meaningful.
				if branch, err := m.rebaseBranch(ctx, ws.HostPath); err == nil {
					ws.Branch = branch
				}
			}
		}
		workspaces = append(workspaces, ws)
	}
	sort.Slice(workspaces, func(i, j int) bool { return workspaces[i].Name < workspaces[j].Name })

	defaultBranch := m.Default(ctx).Branch
	for i := range workspaces {
		ws := &workspaces[i]
		if ws.Missing || ws.Broken || ws.Branch == "" || defaultBranch == "" {
			continue
		}
		ahead, behind, err := m.aheadBehind(ctx, root, defaultBranch, ws.Branch)
		if err == nil {
			ws.Ahead = ahead
			ws.Behind = behind
			ws.Compared = true
		}
	}
	return workspaces, nil
}

func (m *Manager) dirty(ctx context.Context, dir string) (bool, error) {
	status, err := m.Git.Output(ctx, dir, "status", "--porcelain", "--ignore-submodules=none")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) != "" {
			return true, nil
		}
	}
	return false, nil
}

func (m *Manager) aheadBehind(ctx context.Context, root, target, branch string) (ahead, behind int, err error) {
	out, err := m.Git.Output(ctx, root, "rev-list", "--left-right", "--count", target+"..."+branch)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", out)
	}
	behind, err = strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, err
	}
	ahead, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, err
	}
	return ahead, behind, nil
}

func shortHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
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
		NewBranch: createdBranch,
	}, nil
}

// Rollback removes a worktree created by Create and deletes its branch only when
// Create created that branch. It is used when post-create setup such as a
// pre-hook fails.
func (m *Manager) Rollback(ctx context.Context, ws Workspace) error {
	root, err := m.repoRoot(ctx)
	if err != nil {
		return err
	}
	if ws.HostPath != "" {
		if _, statErr := os.Stat(ws.HostPath); statErr == nil {
			if _, err := m.Git.Output(ctx, root, "worktree", "remove", "--force", ws.HostPath); err != nil && !isGoneWorktree(err) {
				return fmt.Errorf("roll back worktree %s: %w", ws.HostPath, err)
			}
		}
	}
	if ws.NewBranch && ws.Branch != "" {
		if _, err := m.Git.Output(ctx, root, "branch", "-D", ws.Branch); err != nil {
			return fmt.Errorf("roll back branch %s: %w", ws.Branch, err)
		}
	}
	return nil
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

// RebaseResult describes a successful workspace branch rebase.
type RebaseResult struct {
	Workspace      Workspace
	SourceBranch   string
	TargetBranch   string
	OldHead        string
	NewHead        string
	AlreadyCurrent bool
	// FastForwarded reports that the branch had no commits of its own and was
	// simply moved to the target tip.
	FastForwarded bool
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
	if err := m.requireNoOperation(ctx, name, ws.HostPath); err != nil {
		return MergeResult{}, err
	}
	if ws.Branch == "" {
		return MergeResult{}, &ErrMergeDetached{Role: "workspace " + name}
	}

	target, err := m.resolveTarget(ctx, root, name, into, ws.Branch)
	if err != nil {
		return MergeResult{}, err
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

// Rebase replays the named workspace's branch onto the branch checked out in
// the project root. It never moves the target, removes the worktree or creates
// merge commits. A conflict is left in the workspace for the caller to resolve
// or abort; the target is never touched.
func (m *Manager) Rebase(ctx context.Context, name, into string) (RebaseResult, error) {
	if name == DefaultName {
		return RebaseResult{}, &ErrDefaultWorkspace{}
	}
	if err := ValidateName(name); err != nil {
		return RebaseResult{}, err
	}
	root, err := m.repoRoot(ctx)
	if err != nil {
		return RebaseResult{}, err
	}
	ws, err := m.Get(ctx, name)
	if err != nil {
		return RebaseResult{}, err
	}
	if ws.Locked {
		return RebaseResult{}, &ErrLocked{Name: name, Path: ws.HostPath, Reason: ws.LockReason}
	}
	if err := m.requireNoOperation(ctx, name, ws.HostPath); err != nil {
		return RebaseResult{}, err
	}
	if ws.Branch == "" {
		return RebaseResult{}, &ErrMergeDetached{Role: "workspace " + name}
	}
	target, err := m.resolveTarget(ctx, root, name, into, ws.Branch)
	if err != nil {
		return RebaseResult{}, err
	}
	if err := m.requireClean(ctx, ws.HostPath, "workspace "+name); err != nil {
		return RebaseResult{}, err
	}

	oldHead, err := m.Git.Output(ctx, root, "rev-parse", "refs/heads/"+ws.Branch)
	if err != nil {
		return RebaseResult{}, fmt.Errorf("inspect workspace branch: %w", err)
	}
	result := RebaseResult{
		Workspace:    ws,
		SourceBranch: ws.Branch,
		TargetBranch: target,
		OldHead:      strings.TrimSpace(oldHead),
		NewHead:      strings.TrimSpace(oldHead),
	}

	if err := m.isAncestor(ctx, root, target, ws.Branch); err == nil {
		result.AlreadyCurrent = true
		return result, nil
	} else if !isNotFastForward(err) {
		return RebaseResult{}, err
	}
	if _, err := m.Git.Output(ctx, root, "merge-base", target, ws.Branch); err != nil {
		if exitStatus(err) == 1 {
			return RebaseResult{}, fmt.Errorf("cannot rebase %s onto %s: the branches share no history", ws.Branch, target)
		}
		return RebaseResult{}, fmt.Errorf("check common history: %w", err)
	}
	fastForward := false
	if err := m.isAncestor(ctx, root, ws.Branch, target); err == nil {
		fastForward = true
	} else if !isNotFastForward(err) {
		return RebaseResult{}, err
	}

	if _, err := m.Git.Output(ctx, ws.HostPath, "-c", "core.editor=true", "rebase", target); err != nil {
		if op, probeErr := m.pendingOperation(ctx, ws.HostPath); probeErr == nil && op == "rebase" {
			return RebaseResult{}, &ErrRebaseConflict{
				Name:    name,
				Path:    ws.HostPath,
				Files:   m.unmergedPaths(ctx, ws.HostPath),
				OldHead: result.OldHead,
			}
		}
		return RebaseResult{}, fmt.Errorf("rebase %s onto %s: %w", ws.Branch, target, err)
	}
	newHead, err := m.Git.Output(ctx, ws.HostPath, "rev-parse", "HEAD")
	if err != nil {
		return RebaseResult{}, fmt.Errorf("inspect rebased branch: %w", err)
	}
	result.NewHead = strings.TrimSpace(newHead)
	result.FastForwarded = fastForward
	return result, nil
}

// RebaseContinue resumes a rebase left in the workspace after conflicts were
// resolved. It reports the same conflict error while conflicts remain.
func (m *Manager) RebaseContinue(ctx context.Context, name string) (RebaseResult, error) {
	ws, root, target, err := m.requireRebaseInProgress(ctx, name)
	if err != nil {
		return RebaseResult{}, err
	}
	oldHead, _ := m.Git.Output(ctx, root, "rev-parse", "refs/heads/"+ws.Branch)
	if _, err := m.Git.Output(ctx, ws.HostPath, "-c", "core.editor=true", "rebase", "--continue"); err != nil {
		if op, probeErr := m.pendingOperation(ctx, ws.HostPath); probeErr == nil && op == "rebase" {
			return RebaseResult{}, &ErrRebaseConflict{
				Name:    name,
				Path:    ws.HostPath,
				Files:   m.unmergedPaths(ctx, ws.HostPath),
				OldHead: strings.TrimSpace(oldHead),
			}
		}
		return RebaseResult{}, fmt.Errorf("continue rebase in %s: %w", ws.HostPath, err)
	}
	newHead, err := m.Git.Output(ctx, ws.HostPath, "rev-parse", "HEAD")
	if err != nil {
		return RebaseResult{}, fmt.Errorf("inspect rebased branch: %w", err)
	}
	return RebaseResult{
		Workspace:    ws,
		SourceBranch: ws.Branch,
		TargetBranch: target,
		OldHead:      strings.TrimSpace(oldHead),
		NewHead:      strings.TrimSpace(newHead),
	}, nil
}

// RebaseAbort abandons an in-progress rebase and restores the workspace branch
// to its pre-rebase tip.
func (m *Manager) RebaseAbort(ctx context.Context, name string) (RebaseResult, error) {
	ws, root, target, err := m.requireRebaseInProgress(ctx, name)
	if err != nil {
		return RebaseResult{}, err
	}
	abandoned, _ := m.Git.Output(ctx, ws.HostPath, "rev-parse", "HEAD")
	if _, err := m.Git.Output(ctx, ws.HostPath, "rebase", "--abort"); err != nil {
		return RebaseResult{}, fmt.Errorf("abort rebase in %s: %w", ws.HostPath, err)
	}
	restored, err := m.Git.Output(ctx, root, "rev-parse", "refs/heads/"+ws.Branch)
	if err != nil {
		return RebaseResult{}, fmt.Errorf("inspect restored branch: %w", err)
	}
	return RebaseResult{
		Workspace:    ws,
		SourceBranch: ws.Branch,
		TargetBranch: target,
		OldHead:      strings.TrimSpace(abandoned),
		NewHead:      strings.TrimSpace(restored),
	}, nil
}

// RequireCleanRoot reports an error when the project root has uncommitted
// changes. Callers that are about to rewrite a workspace branch use it to fail
// before changing any refs.
func (m *Manager) RequireCleanRoot(ctx context.Context) error {
	root, err := m.repoRoot(ctx)
	if err != nil {
		return err
	}
	return m.requireClean(ctx, root, "project root")
}

// requireRebaseInProgress resolves a workspace and refuses unless a rebase is
// currently in progress in it.
func (m *Manager) requireRebaseInProgress(ctx context.Context, name string) (Workspace, string, string, error) {
	if name == DefaultName {
		return Workspace{}, "", "", &ErrDefaultWorkspace{}
	}
	if err := ValidateName(name); err != nil {
		return Workspace{}, "", "", err
	}
	root, err := m.repoRoot(ctx)
	if err != nil {
		return Workspace{}, "", "", err
	}
	ws, err := m.Get(ctx, name)
	if err != nil {
		return Workspace{}, "", "", err
	}
	op, err := m.pendingOperation(ctx, ws.HostPath)
	if err != nil {
		return Workspace{}, "", "", fmt.Errorf("inspect workspace %s: %w", name, err)
	}
	if op != "rebase" {
		return Workspace{}, "", "", &ErrNoRebaseInProgress{Name: name}
	}
	target, err := m.Git.Output(ctx, root, "symbolic-ref", "--short", "HEAD")
	if err != nil || strings.TrimSpace(target) == "" {
		return Workspace{}, "", "", &ErrMergeDetached{Role: "project root"}
	}
	return ws, root, strings.TrimSpace(target), nil
}

func (m *Manager) requireNoOperation(ctx context.Context, name, dir string) error {
	op, err := m.pendingOperation(ctx, dir)
	if err != nil {
		return fmt.Errorf("inspect workspace %s: %w", name, err)
	}
	if op != "" {
		return &ErrOperationInProgress{Name: name, Operation: op, Path: dir}
	}
	return nil
}

// resolveTarget returns the branch checked out in the project root, validating
// an explicit --into request.
func (m *Manager) resolveTarget(ctx context.Context, root, name, into, workspaceBranch string) (string, error) {
	target, err := m.Git.Output(ctx, root, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", &ErrMergeDetached{Role: "project root"}
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return "", &ErrMergeDetached{Role: "project root"}
	}
	if into != "" && into != target {
		return "", &ErrMergeTarget{Requested: into, Current: target}
	}
	if workspaceBranch == target {
		return "", fmt.Errorf("workspace %s already uses the target branch %s", name, target)
	}
	return target, nil
}

// pendingOperation names an in-progress Git operation in dir, or "" when the
// worktree is idle. It is best-effort for listing and authoritative for
// mutating commands.
func (m *Manager) pendingOperation(ctx context.Context, dir string) (string, error) {
	probes := []struct{ op, file string }{
		{"rebase", "rebase-merge"},
		{"rebase", "rebase-apply"},
		{"merge", "MERGE_HEAD"},
		{"cherry-pick", "CHERRY_PICK_HEAD"},
	}
	for _, probe := range probes {
		path, err := m.Git.Output(ctx, dir, "rev-parse", "--git-path", probe.file)
		if err != nil {
			return "", fmt.Errorf("locate Git metadata: %w", err)
		}
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if _, err := os.Stat(path); err == nil {
			return probe.op, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", nil
}

// rebaseBranch recovers the branch being rebased while HEAD is detached.
func (m *Manager) rebaseBranch(ctx context.Context, dir string) (string, error) {
	gitDir, err := m.Git.Output(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	gitDir = strings.TrimSpace(gitDir)
	for _, state := range []string{"rebase-merge", "rebase-apply"} {
		data, err := os.ReadFile(filepath.Join(gitDir, state, "head-name"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		branch := strings.TrimSpace(string(data))
		branch = strings.TrimPrefix(branch, "refs/heads/")
		if branch != "" {
			return branch, nil
		}
	}
	return "", errors.New("rebase state has no branch name")
}

// unmergedPaths lists paths with unresolved conflicts. Failures are ignored
// because the conflict error is still useful without them.
func (m *Manager) unmergedPaths(ctx context.Context, dir string) []string {
	out, err := m.Git.Output(ctx, dir, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil
	}
	var files []string
	for _, rel := range strings.Split(out, "\x00") {
		if rel != "" {
			files = append(files, rel)
		}
	}
	return files
}

func isNotFastForward(err error) bool {
	var notFF *ErrNotFastForward
	return errors.As(err, &notFF)
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

// RemoveOptions controls safety checks for RemoveWithOptions.
type RemoveOptions struct {
	// Force discards uncommitted changes and ignored files.
	Force bool
	// Seed supplies the configured seed include/exclude rules and allow_remove
	// policy evaluated against the workspace's current ignored files.
	Seed config.SeedConfig
}

// Remove deletes a named worktree using the default safety policy: dirty
// worktrees and every ignored file require force. Branch refs are always
// retained and the directory is never removed with a manual rm.
func (m *Manager) Remove(ctx context.Context, name string, force bool) (Workspace, error) {
	return m.RemoveWithOptions(ctx, name, RemoveOptions{Force: force})
}

// RemoveWithOptions deletes a named worktree. Dirty tracked and non-ignored
// untracked files always require force. When Seed.AllowRemove is false, any
// ignored file requires force. When it is true, ignored files are allowed only
// if every one matches the current include/exclude rules; otherwise removal is
// refused with the unmatched paths. Force remains the complete override. Branch
// refs are always retained and the directory is never removed with a manual rm.
func (m *Manager) RemoveWithOptions(ctx context.Context, name string, opts RemoveOptions) (Workspace, error) {
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

	if !opts.Force {
		if err := m.requireNoOperation(ctx, name, ws.HostPath); err != nil {
			return Workspace{}, err
		}
	}

	if !opts.Force {
		if err := m.checkRemovable(ctx, name, ws.HostPath, opts.Seed); err != nil {
			return Workspace{}, err
		}
	}

	args := []string{"worktree", "remove"}
	if opts.Force {
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

// checkRemovable enforces the non-force removal policy. Dirty tracked and
// non-ignored untracked files always block. Ignored files block unless
// allow_remove is set and every current ignored path matches the seed rules.
func (m *Manager) checkRemovable(ctx context.Context, name, dir string, rules config.SeedConfig) error {
	status, err := m.Git.Output(ctx, dir, "status", "--porcelain", "--ignore-submodules=none")
	if err != nil {
		return fmt.Errorf("check workspace status: %w", err)
	}
	if hasStatusChanges(status) {
		return &ErrDirty{Name: name}
	}

	ignored, err := m.ignoredPaths(ctx, dir)
	if err != nil {
		return err
	}
	if len(ignored) == 0 {
		return nil
	}
	if !rules.AllowRemove {
		return &ErrIgnoredFiles{Name: name}
	}
	matcher, err := newSeedMatcher(rules)
	if err != nil {
		return err
	}
	if unmatched := matcher.Unmatched(ignored); len(unmatched) > 0 {
		return &ErrIgnoredFiles{Name: name, Paths: unmatched}
	}
	return nil
}

// ignoredPaths lists the workspace's Git-ignored files, relative to its root.
func (m *Manager) ignoredPaths(ctx context.Context, dir string) ([]string, error) {
	out, err := m.Git.Output(ctx, dir, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("list ignored files: %w", err)
	}
	var paths []string
	for _, rel := range strings.Split(out, "\x00") {
		if rel != "" {
			paths = append(paths, rel)
		}
	}
	return paths, nil
}

func hasStatusChanges(status string) bool {
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

type rawWorktree struct {
	Path        string
	Head        string
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
			cur.Head = strings.TrimPrefix(field, "HEAD ")
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
	return "the default workspace is implicit and cannot be created, merged, rebased, seeded or removed"
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
	return "No refs were changed. Run `boxt workspace merge <workspace> --rebase`, run `boxt workspace rebase <workspace>` and merge again, or recreate the workspace from the current target branch."
}

// ErrRebaseConflict reports a rebase stopped by conflicts. The rebase remains
// open in the workspace until it is continued or aborted.
type ErrRebaseConflict struct {
	Name    string
	Path    string
	Files   []string
	OldHead string
}

func (e *ErrRebaseConflict) Error() string {
	msg := fmt.Sprintf("Rebase of workspace %s stopped with conflicts", e.Name)
	if len(e.Files) > 0 {
		msg += ":\n  " + formatIgnoredPaths(e.Files)
	}
	return msg
}
func (e *ErrRebaseConflict) Hint() string {
	hint := fmt.Sprintf("Resolve the conflicts in %s, then run `boxt workspace rebase %s --continue`.\nTo discard the rebase and restore the previous tip, run `boxt workspace rebase %s --abort`.", e.Path, e.Name, e.Name)
	if e.OldHead != "" {
		hint += fmt.Sprintf("\nThe previous tip was %s; the branch reflog keeps it recoverable.", e.OldHead)
	}
	return hint
}

// ErrOperationInProgress refuses workspace mutations while Git has an unfinished
// operation in the worktree.
type ErrOperationInProgress struct {
	Name      string
	Operation string
	Path      string
}

func (e *ErrOperationInProgress) Error() string {
	return fmt.Sprintf("Workspace %s has a %s in progress", e.Name, e.Operation)
}
func (e *ErrOperationInProgress) Hint() string {
	if e.Operation == "rebase" {
		return fmt.Sprintf("Finish it in %s with `git rebase --continue`, or run `boxt workspace rebase %s --continue` or `boxt workspace rebase %s --abort`.", e.Path, e.Name, e.Name)
	}
	return fmt.Sprintf("Finish or abort the %s in %s before retrying.", e.Operation, e.Path)
}

// ErrNoRebaseInProgress means --continue or --abort was requested without an
// open rebase.
type ErrNoRebaseInProgress struct{ Name string }

func (e *ErrNoRebaseInProgress) Error() string {
	return fmt.Sprintf("Workspace %s has no rebase in progress", e.Name)
}
func (e *ErrNoRebaseInProgress) Hint() string {
	return fmt.Sprintf("Run `boxt workspace rebase %s` to start one.", e.Name)
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
// Paths is set when allow_remove is enabled but some ignored paths do not match
// the configured include/exclude rules.
type ErrIgnoredFiles struct {
	Name  string
	Paths []string
}

func (e *ErrIgnoredFiles) Error() string {
	return fmt.Sprintf("Cannot remove workspace %s:\nit contains ignored files that would be deleted.", e.Name)
}
func (e *ErrIgnoredFiles) Hint() string {
	if len(e.Paths) > 0 {
		return fmt.Sprintf("These ignored paths do not match seed.include/seed.exclude:\n  %s\n\nNarrow seed.include or add seed.exclude patterns in .boxt/config.yaml, or use `boxt workspace remove %s --force` to discard them.",
			formatIgnoredPaths(e.Paths), e.Name)
	}
	return fmt.Sprintf("Use `boxt workspace remove %s --force` to discard ignored files such as .env, caches or build output.", e.Name)
}

func formatIgnoredPaths(paths []string) string {
	const max = 10
	if len(paths) <= max {
		return strings.Join(paths, "\n  ")
	}
	return strings.Join(paths[:max], "\n  ") + fmt.Sprintf("\n  ... and %d more", len(paths)-max)
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
