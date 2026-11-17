package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/git"
	"github.com/daveagill/boxt/internal/project"
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

func TestNameForHostPath(t *testing.T) {
	root := t.TempDir()
	hostRoot := filepath.Join(root, "workspaces", "0123456789abcdef")
	if err := os.MkdirAll(filepath.Join(hostRoot, "feature-a", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hostRoot, "bad name"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "workspaces", "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(filepath.Join(root, "project"), "0123456789abcdef", hostRoot, git.New(command.ExecRunner{}))

	link := filepath.Join(root, "linked-workspace")
	if err := os.Symlink(filepath.Join(hostRoot, "feature-a"), link); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		want string
		ok   bool
	}{
		{name: "worktree root", path: filepath.Join(hostRoot, "feature-a"), want: "feature-a", ok: true},
		{name: "nested directory", path: filepath.Join(hostRoot, "feature-a", "sub"), want: "feature-a", ok: true},
		{name: "through symlink", path: link, want: "feature-a", ok: true},
		{name: "project root", path: m.ProjectRoot},
		{name: "workspace host root", path: hostRoot},
		{name: "sibling directory", path: filepath.Join(root, "workspaces", "other")},
		{name: "prefix trap", path: filepath.Join(hostRoot, "feature-abc")},
		{name: "invalid name", path: filepath.Join(hostRoot, "bad name")},
		{name: "empty path", path: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := m.NameForHostPath(tc.path)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("NameForHostPath(%q) = %q, %v; want %q, %v", tc.path, got, ok, tc.want, tc.ok)
			}
		})
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
	if _, err := m.Create(context.Background(), "two", "-option"); err == nil {
		t.Fatal("option-like branch accepted")
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

func TestRemoveIgnoredFilesRequiresForce(t *testing.T) {
	repo := initRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".gitignore")
	runGit(t, repo, "commit", "-q", "-m", "ignore env")
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "ignored", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, ".env"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = m.Remove(context.Background(), "ignored", false)
	var ignored *ErrIgnoredFiles
	if !errors.As(err, &ignored) {
		t.Fatalf("expected ErrIgnoredFiles, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(ws.HostPath, ".env")); statErr != nil {
		t.Fatal("ignored file was deleted without force")
	}
	if _, err := m.Remove(context.Background(), "ignored", true); err != nil {
		t.Fatalf("forced remove: %v", err)
	}
	if _, err := os.Stat(ws.HostPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("forced remove left workspace")
	}
}

// setupIgnoredWorkspace creates a workspace with committed ignore patterns.
func setupIgnoredWorkspace(t *testing.T, name string, patterns ...string) (*Manager, string) {
	t.Helper()
	repo := initRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(strings.Join(patterns, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".gitignore")
	runGit(t, repo, "commit", "-q", "-m", "ignore local files")
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), name, "")
	if err != nil {
		t.Fatal(err)
	}
	return m, ws.HostPath
}

func writeWorkspaceFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveWithOptionsAllowsMatchingIgnoredFiles(t *testing.T) {
	m, host := setupIgnoredWorkspace(t, "allowed", ".env", "node_modules/")
	writeWorkspaceFile(t, host, ".env", "SECRET=1\n")
	writeWorkspaceFile(t, host, "node_modules/pkg/index.js", "module\n")

	_, err := m.RemoveWithOptions(context.Background(), "allowed", RemoveOptions{
		Seed: config.SeedConfig{
			Include:     []string{".env", "node_modules/**"},
			AllowRemove: true,
		},
	})
	if err != nil {
		t.Fatalf("RemoveWithOptions: %v", err)
	}
	if _, err := os.Stat(host); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("workspace was not removed")
	}
}

func TestRemoveWithOptionsBlocksUnmatchedIgnoredFiles(t *testing.T) {
	m, host := setupIgnoredWorkspace(t, "unmatched", ".env", "cache/")
	writeWorkspaceFile(t, host, ".env", "SECRET=1\n")
	writeWorkspaceFile(t, host, "cache/data.bin", "cache\n")

	_, err := m.RemoveWithOptions(context.Background(), "unmatched", RemoveOptions{
		Seed: config.SeedConfig{
			Include:     []string{".env"},
			AllowRemove: true,
		},
	})
	var ignored *ErrIgnoredFiles
	if !errors.As(err, &ignored) {
		t.Fatalf("expected ErrIgnoredFiles, got %v", err)
	}
	if len(ignored.Paths) != 1 || ignored.Paths[0] != "cache/data.bin" {
		t.Fatalf("unmatched paths = %#v", ignored.Paths)
	}
	if !strings.Contains(ignored.Hint(), "seed.include") {
		t.Fatalf("hint lacks seed guidance: %s", ignored.Hint())
	}
	if _, statErr := os.Stat(host); statErr != nil {
		t.Fatal("blocked removal deleted the workspace")
	}
	if _, statErr := os.Stat(filepath.Join(host, "cache", "data.bin")); statErr != nil {
		t.Fatal("blocked removal deleted an ignored file")
	}
}

func TestRemoveWithOptionsBlocksExcludedIgnoredFiles(t *testing.T) {
	m, host := setupIgnoredWorkspace(t, "excluded", "node_modules/")
	writeWorkspaceFile(t, host, "node_modules/pkg/index.js", "module\n")
	writeWorkspaceFile(t, host, "node_modules/.cache/tmp", "cache\n")

	_, err := m.RemoveWithOptions(context.Background(), "excluded", RemoveOptions{
		Seed: config.SeedConfig{
			Include:     []string{"node_modules/**"},
			Exclude:     []string{"node_modules/.cache/**"},
			AllowRemove: true,
		},
	})
	var ignored *ErrIgnoredFiles
	if !errors.As(err, &ignored) {
		t.Fatalf("expected ErrIgnoredFiles, got %v", err)
	}
	if len(ignored.Paths) != 1 || ignored.Paths[0] != "node_modules/.cache/tmp" {
		t.Fatalf("unmatched paths = %#v", ignored.Paths)
	}
	if _, statErr := os.Stat(host); statErr != nil {
		t.Fatal("blocked removal deleted the workspace")
	}
}

func TestRemoveWithOptionsEmptyIncludeAllowsAllIgnored(t *testing.T) {
	m, host := setupIgnoredWorkspace(t, "empty-include", ".env", "cache/")
	writeWorkspaceFile(t, host, ".env", "SECRET=1\n")
	writeWorkspaceFile(t, host, "cache/data.bin", "cache\n")

	_, err := m.RemoveWithOptions(context.Background(), "empty-include", RemoveOptions{
		Seed: config.SeedConfig{AllowRemove: true},
	})
	if err != nil {
		t.Fatalf("RemoveWithOptions: %v", err)
	}
	if _, err := os.Stat(host); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("workspace was not removed")
	}
}

func TestRemoveWithOptionsStillBlocksDirtyTrackedFiles(t *testing.T) {
	m, host := setupIgnoredWorkspace(t, "dirty", ".env")
	writeWorkspaceFile(t, host, ".env", "SECRET=1\n")
	writeWorkspaceFile(t, host, "README.md", "changed\n")

	_, err := m.RemoveWithOptions(context.Background(), "dirty", RemoveOptions{
		Seed: config.SeedConfig{AllowRemove: true},
	})
	var dirty *ErrDirty
	if !errors.As(err, &dirty) {
		t.Fatalf("expected ErrDirty, got %v", err)
	}
	if _, statErr := os.Stat(host); statErr != nil {
		t.Fatal("dirty workspace was removed")
	}
}

func TestRemoveWithOptionsForceOverridesUnmatchedIgnored(t *testing.T) {
	m, host := setupIgnoredWorkspace(t, "forced", ".env", "cache/")
	writeWorkspaceFile(t, host, ".env", "SECRET=1\n")
	writeWorkspaceFile(t, host, "cache/data.bin", "cache\n")

	_, err := m.RemoveWithOptions(context.Background(), "forced", RemoveOptions{
		Force: true,
		Seed: config.SeedConfig{
			Include:     []string{".env"},
			AllowRemove: true,
		},
	})
	if err != nil {
		t.Fatalf("RemoveWithOptions --force: %v", err)
	}
	if _, err := os.Stat(host); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("forced removal left the workspace")
	}
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

func TestCreateDoesNotPruneUnrelatedStaleWorktrees(t *testing.T) {
	repo := initRepo(t)
	outside := filepath.Join(t.TempDir(), "unrelated")
	runGit(t, repo, "worktree", "add", outside, "-b", "unrelated", "-q")
	if err := os.RemoveAll(outside); err != nil {
		t.Fatal(err)
	}
	m := newManager(t, repo)
	if _, err := m.Create(context.Background(), "one", ""); err != nil {
		t.Fatal(err)
	}
	listed := runGit(t, repo, "worktree", "list", "--porcelain")
	if !strings.Contains(listed, outside) {
		t.Fatalf("unrelated stale worktree metadata was pruned:\n%s", listed)
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

func TestMergeFastForward(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws.HostPath, "add", "feature.txt")
	runGit(t, ws.HostPath, "commit", "-q", "-m", "feature")
	wantHead := runGit(t, ws.HostPath, "rev-parse", "HEAD")
	current := runGit(t, repo, "symbolic-ref", "--short", "HEAD")

	res, err := m.Merge(context.Background(), "feature-a", "", false)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if res.SourceBranch != "boxt/feature-a" || res.TargetBranch != current {
		t.Fatalf("result = %#v", res)
	}
	if res.OldHead == res.NewHead || res.AlreadyCurrent {
		t.Fatalf("expected new HEAD: %#v", res)
	}
	if got := runGit(t, repo, "rev-parse", "HEAD"); got != wantHead {
		t.Fatalf("target HEAD = %s, want %s", got, wantHead)
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err != nil {
		t.Fatalf("feature not merged: %v", err)
	}
	if _, err := os.Stat(ws.HostPath); err != nil {
		t.Fatal("merge removed the workspace without --remove")
	}
}

func TestMergeAlreadyUpToDate(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	if _, err := m.Create(context.Background(), "feature-a", ""); err != nil {
		t.Fatal(err)
	}
	before := runGit(t, repo, "rev-parse", "HEAD")
	res, err := m.Merge(context.Background(), "feature-a", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.AlreadyCurrent || res.OldHead != res.NewHead {
		t.Fatalf("result = %#v", res)
	}
	if got := runGit(t, repo, "rev-parse", "HEAD"); got != before {
		t.Fatalf("HEAD changed: %s -> %s", before, got)
	}
}

func TestMergeDivergedRefusedWithoutChanges(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, "ws.txt"), []byte("ws\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws.HostPath, "add", "ws.txt")
	runGit(t, ws.HostPath, "commit", "-q", "-m", "workspace change")
	if err := os.WriteFile(filepath.Join(repo, "root.txt"), []byte("root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "root.txt")
	runGit(t, repo, "commit", "-q", "-m", "root change")
	before := runGit(t, repo, "rev-parse", "HEAD")

	_, err = m.Merge(context.Background(), "feature-a", "", false)
	var notFF *ErrNotFastForward
	if !errors.As(err, &notFF) {
		t.Fatalf("expected ErrNotFastForward, got %v", err)
	}
	if got := runGit(t, repo, "rev-parse", "HEAD"); got != before {
		t.Fatalf("diverged merge changed HEAD: %s -> %s", before, got)
	}
	if _, err := os.Stat(filepath.Join(repo, "ws.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("diverged merge changed the working tree")
	}
}

func TestMergeDirtySourceRefused(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := runGit(t, repo, "rev-parse", "HEAD")
	_, err = m.Merge(context.Background(), "feature-a", "", false)
	var dirty *ErrMergeDirty
	if !errors.As(err, &dirty) || !strings.Contains(dirty.Role, "workspace") {
		t.Fatalf("expected workspace ErrMergeDirty, got %v", err)
	}
	if got := runGit(t, repo, "rev-parse", "HEAD"); got != before {
		t.Fatal("dirty merge changed HEAD")
	}
}

func TestMergeDirtyProjectRootRefused(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, "feature.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws.HostPath, "add", "feature.txt")
	runGit(t, ws.HostPath, "commit", "-q", "-m", "feature")
	if err := os.WriteFile(filepath.Join(repo, "root-untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = m.Merge(context.Background(), "feature-a", "", false)
	var dirty *ErrMergeDirty
	if !errors.As(err, &dirty) || dirty.Role != "project root" {
		t.Fatalf("expected project-root ErrMergeDirty, got %v", err)
	}
}

func TestMergeDetachedTargetRefused(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, "feature.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws.HostPath, "add", "feature.txt")
	runGit(t, ws.HostPath, "commit", "-q", "-m", "feature")
	runGit(t, repo, "checkout", "--detach", "-q")
	_, err = m.Merge(context.Background(), "feature-a", "", false)
	var detached *ErrMergeDetached
	if !errors.As(err, &detached) || detached.Role != "project root" {
		t.Fatalf("expected detached target, got %v", err)
	}
}

func TestMergeDetachedSourceRefused(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, ws.HostPath, "checkout", "--detach", "-q")
	_, err = m.Merge(context.Background(), "feature-a", "", false)
	var detached *ErrMergeDetached
	if !errors.As(err, &detached) {
		t.Fatalf("expected detached source, got %v", err)
	}
}

func TestMergeIntoTarget(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, "feature.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws.HostPath, "add", "feature.txt")
	runGit(t, ws.HostPath, "commit", "-q", "-m", "feature")
	current := runGit(t, repo, "symbolic-ref", "--short", "HEAD")
	if _, err := m.Merge(context.Background(), "feature-a", current, false); err != nil {
		t.Fatalf("merge into current: %v", err)
	}
	_, err = m.Merge(context.Background(), "feature-a", "other", false)
	var targetErr *ErrMergeTarget
	if !errors.As(err, &targetErr) {
		t.Fatalf("expected ErrMergeTarget, got %v", err)
	}
}

func TestMergeRemoveKeepsBranch(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, "feature.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws.HostPath, "add", "feature.txt")
	runGit(t, ws.HostPath, "commit", "-q", "-m", "feature")
	res, err := m.Merge(context.Background(), "feature-a", "", true)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if !res.Removed || res.RemoveErr != nil {
		t.Fatalf("result = %#v", res)
	}
	if _, err := os.Stat(ws.HostPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("workspace not removed")
	}
	runGit(t, repo, "show-ref", "--verify", "refs/heads/boxt/feature-a")
}

func TestMergeRemoveBlockedByIgnoredFiles(t *testing.T) {
	repo := initRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".gitignore")
	runGit(t, repo, "commit", "-q", "-m", "ignore")
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, ".env"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := m.Merge(context.Background(), "feature-a", "", true)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if res.Removed || res.RemoveErr == nil {
		t.Fatalf("expected removal to be blocked: %#v", res)
	}
	if _, err := os.Stat(ws.HostPath); err != nil {
		t.Fatal("blocked removal deleted the workspace")
	}
}

func TestMergeDefaultWorkspaceRefused(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	_, err := m.Merge(context.Background(), "default", "", false)
	var def *ErrDefaultWorkspace
	if !errors.As(err, &def) {
		t.Fatalf("expected ErrDefaultWorkspace, got %v", err)
	}
}

func TestListReportsDirtyHeadAndAheadBehind(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, "feature.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws.HostPath, "add", "feature.txt")
	runGit(t, ws.HostPath, "commit", "-q", "-m", "feature")
	if err := os.WriteFile(filepath.Join(ws.HostPath, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %#v", list)
	}
	got := list[0]
	if len(got.Head) != 7 || !got.Dirty || !got.Compared || got.Ahead != 1 || got.Behind != 0 {
		t.Fatalf("workspace summary = %#v", got)
	}
	def := m.Default(context.Background())
	if len(def.Head) != 7 || def.Dirty {
		t.Fatalf("default summary = %#v", def)
	}
	if err := os.WriteFile(filepath.Join(repo, "root-untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if def := m.Default(context.Background()); !def.Dirty {
		t.Fatalf("default dirty not reported: %#v", def)
	}
	if err := os.Remove(filepath.Join(repo, "root-untracked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "root.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "root.txt")
	runGit(t, repo, "commit", "-q", "-m", "root change")
	list, err = m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Compared || list[0].Behind != 1 {
		t.Fatalf("behind not reported: %#v", list)
	}
}

func TestListHandlesDetachedAndMissing(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "detached", "")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, ws.HostPath, "checkout", "--detach", "-q")
	if _, err := m.Create(context.Background(), "gone", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(m.HostRoot, "gone")); err != nil {
		t.Fatal(err)
	}
	list, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byName := map[string]Workspace{}
	for _, ws := range list {
		byName[ws.Name] = ws
	}
	if ws := byName["detached"]; ws.Branch != "" || ws.Head == "" || ws.Compared {
		t.Fatalf("detached = %#v", ws)
	}
	if ws := byName["gone"]; !ws.Missing {
		t.Fatalf("missing = %#v", ws)
	}
}

func TestSeedCopiesOnlyIgnoredFiles(t *testing.T) {
	repo := initRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".env\nnode_modules/\ncache/\nlink\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".gitignore")
	runGit(t, repo, "commit", "-q", "-m", "ignore")
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "node_modules", "pkg", "index.js"), []byte("module\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "cache", "a"), []byte("cache\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkCreated := os.Symlink(filepath.Join(repo, "README.md"), filepath.Join(repo, "link")) == nil

	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, ".env"), []byte("KEEP=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := m.Seed(context.Background(), "feature-a")
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	wantCopied := 2
	if linkCreated {
		wantCopied++
	}
	if result.Copied != wantCopied {
		t.Fatalf("copied = %d, want %d, result = %#v", result.Copied, wantCopied, result)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0] != ".env" {
		t.Fatalf("conflicts = %#v", result.Conflicts)
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("skipped = %#v", result.Skipped)
	}
	data, err := os.ReadFile(filepath.Join(ws.HostPath, ".env"))
	if err != nil || string(data) != "KEEP=1\n" {
		t.Fatalf("existing .env overwritten: %q %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(ws.HostPath, "node_modules", "pkg", "index.js"))
	if err != nil || string(data) != "module\n" {
		t.Fatalf("nested ignored file not copied: %q %v", data, err)
	}
	info, err := os.Stat(filepath.Join(ws.HostPath, "node_modules", "pkg", "index.js"))
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("permissions not preserved: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(ws.HostPath, "cache", "a")); err != nil {
		t.Fatalf("cache not copied: %v", err)
	}
	if linkCreated {
		got, err := os.Readlink(filepath.Join(ws.HostPath, "link"))
		if err != nil {
			t.Fatalf("symlink was not preserved: %v", err)
		}
		if got != filepath.Join(repo, "README.md") {
			t.Fatalf("symlink target = %q", got)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, ".env")); err != nil {
		t.Fatal("seed modified the default workspace")
	}
}

func TestSeedRefusesDefaultAndMissing(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	if _, err := m.Seed(context.Background(), "default"); err == nil {
		t.Fatal("seeded the default workspace")
	} else {
		var def *ErrDefaultWorkspace
		if !errors.As(err, &def) {
			t.Fatalf("expected ErrDefaultWorkspace, got %v", err)
		}
	}
	if _, err := m.Seed(context.Background(), "nope"); err == nil {
		t.Fatal("seeded a missing workspace")
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

func TestMapCreateError(t *testing.T) {
	m := New("/repo", "abc", "/data", git.New(command.ExecRunner{}))
	tests := []struct {
		stderr string
		want   string
	}{
		{"fatal: a branch named 'x' already exists", "branch for workspace one already exists"},
		{"fatal: 'x' is already used by worktree at '/w'", "already checked out"},
		{"fatal: 'x' is already checked out at '/w'", "already checked out"},
		{"fatal: '/path' already exists", "Workspace already exists"},
	}
	for _, tt := range tests {
		err := m.mapCreateError("one", "/path", &command.Error{Stderr: tt.stderr})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("stderr %q: got %v, want %q", tt.stderr, err, tt.want)
		}
	}
}

func TestProjectLookupFromWorkspace(t *testing.T) {
	repo := initRepo(t)
	if _, err := project.Init(repo); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "boxt config")
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws.HostPath, ".boxt", "config.yaml")); err != nil {
		t.Fatalf("tracked config missing from worktree: %v", err)
	}
	got, err := project.Lookup(ws.HostPath)
	if err != nil {
		t.Fatalf("Lookup from workspace: %v", err)
	}
	if got.Root != canonicalPath(repo) {
		t.Fatalf("project root = %q, want %q", got.Root, canonicalPath(repo))
	}
	if got.ID != project.IDForRoot(canonicalPath(repo)) {
		t.Fatalf("project ID = %q", got.ID)
	}
	nested := filepath.Join(ws.HostPath, "src")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = project.Lookup(nested)
	if err != nil {
		t.Fatalf("Lookup from nested workspace path: %v", err)
	}
	if got.Root != canonicalPath(repo) {
		t.Fatalf("nested lookup root = %q", got.Root)
	}
	gitDir := runGit(t, ws.HostPath, "rev-parse", "--absolute-git-dir")
	data, err := os.ReadFile(filepath.Join(gitDir, project.WorktreeMetadataFile))
	if err != nil {
		t.Fatalf("worktree metadata missing: %v", err)
	}
	if strings.TrimSpace(string(data)) != canonicalPath(repo) {
		t.Fatalf("metadata = %q", data)
	}

	nestedProject := filepath.Join(ws.HostPath, "nested-project")
	if _, err := project.Init(nestedProject); err != nil {
		t.Fatal(err)
	}
	got, err = project.Lookup(nestedProject)
	if err != nil {
		t.Fatalf("Lookup nested project inside workspace: %v", err)
	}
	if got.Root != nestedProject {
		t.Fatalf("nested lookup = %q, want %q", got.Root, nestedProject)
	}
}

func commitWorkspaceFile(t *testing.T, dir, rel, content, message string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", rel)
	runGit(t, dir, "commit", "-q", "-m", message)
	return runGit(t, dir, "rev-parse", "HEAD")
}

func TestRebaseThenMergeParallelWorkspaces(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ctx := context.Background()

	a, err := m.Create(ctx, "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create(ctx, "feature-b", "")
	if err != nil {
		t.Fatal(err)
	}
	commitWorkspaceFile(t, a.HostPath, "a.txt", "a\n", "feature a")
	commitWorkspaceFile(t, b.HostPath, "b.txt", "b\n", "feature b")

	if _, err := m.Merge(ctx, "feature-a", "", false); err != nil {
		t.Fatalf("merge feature-a: %v", err)
	}
	if _, err := m.Merge(ctx, "feature-b", "", false); err == nil {
		t.Fatal("merge feature-b unexpectedly fast-forwarded after feature-a")
	} else {
		var notFF *ErrNotFastForward
		if !errors.As(err, &notFF) {
			t.Fatalf("expected ErrNotFastForward, got %v", err)
		}
	}

	res, err := m.Rebase(ctx, "feature-b", "")
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if res.AlreadyCurrent || res.OldHead == res.NewHead || res.FastForwarded {
		t.Fatalf("unexpected rebase result: %#v", res)
	}
	if branchTip := runGit(t, b.HostPath, "rev-parse", "HEAD"); branchTip != res.NewHead {
		t.Fatalf("workspace head = %s, want %s", branchTip, res.NewHead)
	}

	if _, err := m.Merge(ctx, "feature-b", "", false); err != nil {
		t.Fatalf("merge after rebase: %v", err)
	}
	// The target history must be linear: the rebased tip has a single parent.
	parents := strings.Fields(runGit(t, repo, "rev-list", "--parents", "-n", "1", "HEAD"))
	if len(parents) != 2 {
		t.Fatalf("target is not a linear fast-forward: %v", parents)
	}
	for _, rel := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Fatalf("%s missing from project root: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err != nil {
		t.Fatalf("b.txt missing: %v", err)
	}
}

func TestRebaseAlreadyUpToDate(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ctx := context.Background()
	if _, err := m.Create(ctx, "feature-a", ""); err != nil {
		t.Fatal(err)
	}
	before := runGit(t, repo, "rev-parse", "HEAD")
	res, err := m.Rebase(ctx, "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.AlreadyCurrent || res.OldHead != before || res.NewHead != before {
		t.Fatalf("result = %#v", res)
	}
}

func TestRebaseFastForwardsBranchWithoutCommits(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ctx := context.Background()
	if _, err := m.Create(ctx, "feature-a", ""); err != nil {
		t.Fatal(err)
	}
	head := commitWorkspaceFile(t, repo, "root.txt", "root\n", "root change")

	res, err := m.Rebase(ctx, "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.FastForwarded || res.AlreadyCurrent {
		t.Fatalf("result = %#v", res)
	}
	if got := runGit(t, repo, "rev-parse", "boxt/feature-a"); got != head {
		t.Fatalf("branch = %s, want %s", got, head)
	}
}

func TestRebaseConflictLeavesRebaseOpen(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ctx := context.Background()

	a, err := m.Create(ctx, "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create(ctx, "feature-b", "")
	if err != nil {
		t.Fatal(err)
	}
	commitWorkspaceFile(t, a.HostPath, "README.md", "from a\n", "a change")
	commitWorkspaceFile(t, b.HostPath, "README.md", "from b\n", "b change")
	if _, err := m.Merge(ctx, "feature-a", "", false); err != nil {
		t.Fatalf("merge feature-a: %v", err)
	}
	rootBefore := runGit(t, repo, "rev-parse", "HEAD")
	bBefore := runGit(t, b.HostPath, "rev-parse", "HEAD")

	_, err = m.Rebase(ctx, "feature-b", "")
	var conflict *ErrRebaseConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ErrRebaseConflict, got %v", err)
	}
	if len(conflict.Files) != 1 || conflict.Files[0] != "README.md" {
		t.Fatalf("conflict files = %#v", conflict.Files)
	}
	if got := runGit(t, repo, "rev-parse", "HEAD"); got != rootBefore {
		t.Fatal("conflicted rebase changed the target")
	}

	list, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %#v", list)
	}
	var got Workspace
	for _, ws := range list {
		if ws.Name == "feature-b" {
			got = ws
		}
	}
	if got.Operation != "rebase" || got.Branch != "boxt/feature-b" || !got.Dirty {
		t.Fatalf("workspace state = %#v", got)
	}

	if _, err := m.Merge(ctx, "feature-b", "", false); err == nil {
		t.Fatal("merge ran during an open rebase")
	} else {
		var inProgress *ErrOperationInProgress
		if !errors.As(err, &inProgress) {
			t.Fatalf("expected ErrOperationInProgress, got %v", err)
		}
	}
	if _, err := m.Remove(ctx, "feature-b", false); err == nil {
		t.Fatal("remove ran during an open rebase")
	} else {
		var inProgress *ErrOperationInProgress
		if !errors.As(err, &inProgress) {
			t.Fatalf("expected ErrOperationInProgress, got %v", err)
		}
	}

	aborted, err := m.RebaseAbort(ctx, "feature-b")
	if err != nil {
		t.Fatalf("RebaseAbort: %v", err)
	}
	if aborted.NewHead != bBefore {
		t.Fatalf("abort restored %s, want %s", aborted.NewHead, bBefore)
	}
	if got := runGit(t, b.HostPath, "rev-parse", "HEAD"); got != bBefore {
		t.Fatalf("branch after abort = %s, want %s", got, bBefore)
	}
	if _, err := os.Stat(filepath.Join(b.HostPath, "README.md")); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Rebase(ctx, "feature-b", ""); err == nil {
		t.Fatal("expected conflict on retry")
	}
	if err := os.WriteFile(filepath.Join(b.HostPath, "README.md"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, b.HostPath, "add", "README.md")
	continued, err := m.RebaseContinue(ctx, "feature-b")
	if err != nil {
		t.Fatalf("RebaseContinue: %v", err)
	}
	if continued.NewHead == bBefore {
		t.Fatal("continue did not move the branch")
	}
	if _, err := m.Merge(ctx, "feature-b", "", false); err != nil {
		t.Fatalf("merge after continue: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "resolved" {
		t.Fatalf("README = %q", data)
	}
}

func TestRebaseRefusesDirtyWorkspaceAndDetached(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ctx := context.Background()
	ws, err := m.Create(ctx, "feature-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rebase(ctx, "feature-a", ""); err == nil {
		t.Fatal("rebase ran on a dirty workspace")
	} else {
		var dirty *ErrMergeDirty
		if !errors.As(err, &dirty) {
			t.Fatalf("expected ErrMergeDirty, got %v", err)
		}
	}
	if err := os.Remove(filepath.Join(ws.HostPath, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	runGit(t, ws.HostPath, "checkout", "--detach", "-q")
	if _, err := m.Rebase(ctx, "feature-a", ""); err == nil {
		t.Fatal("rebase ran with a detached HEAD")
	} else {
		var detached *ErrMergeDetached
		if !errors.As(err, &detached) {
			t.Fatalf("expected ErrMergeDetached, got %v", err)
		}
	}
}

func TestRequireCleanRoot(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	if err := m.RequireCleanRoot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := m.RequireCleanRoot(context.Background())
	var dirty *ErrMergeDirty
	if !errors.As(err, &dirty) || dirty.Role != "project root" {
		t.Fatalf("expected project root ErrMergeDirty, got %v", err)
	}
}

func TestRebaseNoRebaseInProgress(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ctx := context.Background()
	if _, err := m.Create(ctx, "feature-a", ""); err != nil {
		t.Fatal(err)
	}
	for _, fn := range []func(context.Context, string) (RebaseResult, error){m.RebaseContinue, m.RebaseAbort} {
		if _, err := fn(ctx, "feature-a"); err == nil {
			t.Fatal("operation ran without an open rebase")
		} else {
			var none *ErrNoRebaseInProgress
			if !errors.As(err, &none) {
				t.Fatalf("expected ErrNoRebaseInProgress, got %v", err)
			}
		}
	}
}
