package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boxt/boxt/internal/command"
	"github.com/boxt/boxt/internal/git"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	out, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: full})
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func initRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func newManager(t *testing.T, repo string) *Manager {
	t.Helper()
	return New(repo, "0123456789abcdef", filepath.Join(t.TempDir(), "workspaces", "0123456789abcdef"), git.New(command.ExecRunner{}))
}

func TestValidateName(t *testing.T) {
	valid := []string{"a", "feature-a", "feature_1", "a.b", strings.Repeat("a", 64)}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v", name, err)
		}
	}
	invalid := []string{"", "default", "..", ".", "a/b", "/abs", ".hidden", "-dash", "Upper", "a b", "a\x00b", strings.Repeat("a", 65)}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) accepted invalid name", name)
		}
	}
}

func TestPaths(t *testing.T) {
	m := New("/repo", "abc", "/data/workspaces/abc", git.New(command.ExecRunner{}))
	if got, _ := m.HostPath(DefaultName); got != "/repo" {
		t.Fatalf("default host path = %q", got)
	}
	if got, _ := m.HostPath("feature-a"); got != "/data/workspaces/abc/feature-a" {
		t.Fatalf("host path = %q", got)
	}
	if _, err := m.HostPath("../x"); err == nil {
		t.Fatal("traversal accepted")
	}
	if got := m.GuestPath(DefaultName); got != "/workspace" {
		t.Fatalf("default guest path = %q", got)
	}
	if got := m.GuestPath("feature-a"); got != "/workspaces/abc/feature-a" {
		t.Fatalf("guest path = %q", got)
	}
}

func TestCreateListRemove(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ctx := context.Background()

	ws, err := m.Create(ctx, "feature-a", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := canonicalPath(filepath.Join(m.HostRoot, "feature-a"))
	if canonicalPath(ws.HostPath) != want {
		t.Fatalf("host path = %q, want %q", ws.HostPath, want)
	}
	if ws.Branch != "boxt/feature-a" || ws.GuestPath != "/workspaces/0123456789abcdef/feature-a" {
		t.Fatalf("unexpected workspace: %#v", ws)
	}
	if _, err := os.Stat(filepath.Join(ws.HostPath, "README.md")); err != nil {
		t.Fatalf("checkout missing: %v", err)
	}
	if top := runGit(t, ws.HostPath, "rev-parse", "--show-toplevel"); canonicalPath(top) != canonicalPath(ws.HostPath) {
		t.Fatalf("unexpected top level %q", top)
	}
	if branch := runGit(t, ws.HostPath, "rev-parse", "--abbrev-ref", "HEAD"); branch != "boxt/feature-a" {
		t.Fatalf("branch = %q", branch)
	}
	if status := runGit(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("parent repository reports external worktree: %q", status)
	}

	list, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "feature-a" || list[0].Branch != "boxt/feature-a" || list[0].Missing {
		t.Fatalf("list = %#v", list)
	}
	got, err := m.Get(ctx, "feature-a")
	if err != nil || got.Name != "feature-a" {
		t.Fatalf("Get = %#v, %v", got, err)
	}
	def := m.Default(ctx)
	if def.Name != "default" || def.HostPath != repo || def.GuestPath != "/workspace" || def.Branch == "" {
		t.Fatalf("default = %#v", def)
	}

	if _, err := m.Create(ctx, "feature-a", ""); err == nil {
		t.Fatal("duplicate workspace accepted")
	} else {
		var exists *ErrExists
		if !errors.As(err, &exists) {
			t.Fatalf("expected ErrExists, got %v", err)
		}
	}

	if _, err := m.Remove(ctx, "feature-a", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(ws.HostPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace still exists: %v", err)
	}
	if _, err := m.List(ctx); err != nil {
		t.Fatal(err)
	} else if list, _ := m.List(ctx); len(list) != 0 {
		t.Fatalf("workspace still listed: %#v", list)
	}
	// Branch is deliberately retained.
	runGit(t, repo, "show-ref", "--verify", "refs/heads/boxt/feature-a")
	if status := runGit(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("parent repository dirty after remove: %q", status)
	}
}

func TestCreateBranchOverride(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "one", "feature/one")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Branch != "feature/one" {
		t.Fatalf("branch = %q", ws.Branch)
	}
	runGit(t, repo, "show-ref", "--verify", "refs/heads/feature/one")
}

func TestCreateExistingBranch(t *testing.T) {
	repo := initRepo(t)
	runGit(t, repo, "branch", "existing")
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "one", "existing")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Branch != "existing" {
		t.Fatalf("branch = %q", ws.Branch)
	}
	if _, err := m.Create(context.Background(), "two", "existing"); err == nil {
		t.Fatal("same branch checked out twice")
	} else if !strings.Contains(err.Error(), "already checked out") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateUnbornRepository(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "first", "")
	if err != nil {
		t.Fatalf("Create in unborn repo: %v", err)
	}
	if branch := runGit(t, ws.HostPath, "symbolic-ref", "--short", "HEAD"); branch != "boxt/first" {
		t.Fatalf("branch = %q", branch)
	}
}

func TestCreateInvalidBranch(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	if _, err := m.Create(context.Background(), "one", "bad..branch"); err == nil {
		t.Fatal("invalid branch accepted")
	}
}

func TestCreateNonGit(t *testing.T) {
	dir := t.TempDir()
	m := newManager(t, dir)
	_, err := m.Create(context.Background(), "one", "")
	var notGit *ErrNotGitRepository
	if !errors.As(err, &notGit) {
		t.Fatalf("expected ErrNotGitRepository, got %v", err)
	}
	list, err := m.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("non-Git list = %#v, %v", list, err)
	}
}

func TestCreateProjectSubdirectoryOfRepository(t *testing.T) {
	repo := initRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newManager(t, sub)
	_, err := m.Create(context.Background(), "one", "")
	var rootErr *ErrRepositoryRoot
	if !errors.As(err, &rootErr) {
		t.Fatalf("expected ErrRepositoryRoot, got %v", err)
	}
}

func TestListIgnoresUnmanagedWorktrees(t *testing.T) {
	repo := initRepo(t)
	outside := filepath.Join(t.TempDir(), "outside")
	runGit(t, repo, "worktree", "add", outside, "-b", "outside-branch", "-q")
	runGit(t, repo, "worktree", "add", filepath.Join(repo, "inside"), "-b", "inside-branch", "-q")
	m := newManager(t, repo)
	list, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("unmanaged worktrees listed: %#v", list)
	}
}

func TestRemoveDirtyRequiresForce(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "dirty", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = m.Remove(context.Background(), "dirty", false)
	var dirty *ErrDirty
	if !errors.As(err, &dirty) {
		t.Fatalf("expected ErrDirty, got %v", err)
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(ws.HostPath); statErr != nil {
		t.Fatal("dirty workspace was removed without force")
	}
	if _, err := m.Remove(context.Background(), "dirty", true); err != nil {
		t.Fatalf("forced remove: %v", err)
	}
	if _, err := os.Stat(ws.HostPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("forced remove left the workspace")
	}
	runGit(t, repo, "show-ref", "--verify", "refs/heads/boxt/dirty")
}

func TestRemoveMissingDirectoryPrunesMetadata(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "gone", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ws.HostPath); err != nil {
		t.Fatal(err)
	}
	list, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Missing {
		t.Fatalf("missing workspace not reported: %#v", list)
	}
	got, err := m.Remove(context.Background(), "gone", false)
	if err != nil {
		t.Fatalf("Remove missing: %v", err)
	}
	if !got.Missing {
		t.Fatalf("expected missing result: %#v", got)
	}
	list, err = m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("stale metadata remains: %#v", list)
	}
}

func TestCreateReusesNameAfterMissingDirectory(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "reuse", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ws.HostPath); err != nil {
		t.Fatal(err)
	}
	ws2, err := m.Create(context.Background(), "reuse", "")
	if err != nil {
		t.Fatalf("Create after missing directory: %v", err)
	}
	if canonicalPath(ws2.HostPath) != canonicalPath(ws.HostPath) {
		t.Fatalf("paths differ: %q %q", ws.HostPath, ws2.HostPath)
	}
}

func TestRemoveLocked(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "locked", "")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "worktree", "lock", "--reason", "busy", ws.HostPath)
	list, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Locked || list[0].LockReason != "busy" {
		t.Fatalf("lock not reported: %#v", list)
	}
	_, err = m.Remove(context.Background(), "locked", true)
	var locked *ErrLocked
	if !errors.As(err, &locked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
	if _, statErr := os.Stat(ws.HostPath); statErr != nil {
		t.Fatal("locked workspace was removed")
	}
}

func TestRemoveDefaultAndMissing(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	if _, err := m.Remove(context.Background(), "default", false); err == nil {
		t.Fatal("removed default workspace")
	} else if _, ok := err.(*ErrDefaultWorkspace); !ok {
		t.Fatalf("unexpected error: %T %v", err, err)
	}
	if _, err := m.Remove(context.Background(), "nope", false); err == nil {
		t.Fatal("removed missing workspace")
	} else {
		var notFound *ErrNotFound
		if !errors.As(err, &notFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	}
}

func TestParseWorktrees(t *testing.T) {
	out := "worktree /repo\x00HEAD abc\x00branch refs/heads/master\x00\x00" +
		"worktree /ws/one\x00HEAD def\x00branch refs/heads/boxt/one\x00locked busy\x00\x00" +
		"worktree /ws/two\x00HEAD 000\x00detached\x00prunable gitdir file points to non-existent location\x00\x00"
	got := parseWorktrees(out)
	if len(got) != 3 {
		t.Fatalf("parsed %d records: %#v", len(got), got)
	}
	if got[0].Path != "/repo" || got[0].Branch != "master" {
		t.Fatalf("main = %#v", got[0])
	}
	if !got[1].Locked || got[1].LockReason != "busy" || got[1].Branch != "boxt/one" {
		t.Fatalf("locked = %#v", got[1])
	}
	if !got[2].Prunable || got[2].Branch != "" {
		t.Fatalf("prunable = %#v", got[2])
	}
}

func TestCanonicalHostRootSymlink(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	repo := initRepo(t)
	m := New(repo, "abc", link, git.New(command.ExecRunner{}))
	ws, err := m.Create(context.Background(), "one", "")
	if err != nil {
		t.Fatal(err)
	}
	if canonicalPath(ws.HostPath) != canonicalPath(filepath.Join(real, "one")) {
		t.Fatalf("host path = %q", ws.HostPath)
	}
	list, err := m.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %#v, %v", list, err)
	}
}

func TestGetMissingWorkspace(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "gone", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(ws.HostPath); err != nil {
		t.Fatal(err)
	}
	_, err = m.Get(context.Background(), "gone")
	var missing *ErrMissing
	if !errors.As(err, &missing) {
		t.Fatalf("expected ErrMissing, got %v", err)
	}
}
