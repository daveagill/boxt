package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daveagill/boxt/internal/config"
)

// commitGitignore adds a tracked .gitignore so seeding has ignored paths to
// find.
func commitGitignore(t *testing.T, repo, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".gitignore")
	runGit(t, repo, "commit", "-q", "-m", "ignore seeded paths")
}

// mustSymlink creates a symlink or skips the test when the host cannot.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// seedWorkspace creates a named workspace for seeding tests.
func seedWorkspace(t *testing.T, m *Manager, ctx context.Context, name string) string {
	t.Helper()
	ws, err := m.Create(ctx, name, "")
	if err != nil {
		t.Fatalf("Create %s: %v", name, err)
	}
	return ws.HostPath
}

func TestSeedPreservesSymlinks(t *testing.T) {
	repo := initRepo(t)
	commitGitignore(t, repo, "link-rel\nlink-abs\nlink-escape\nlink-dangling\n")

	outsideName := "seed-outside-target.txt"
	outside := filepath.Join(filepath.Dir(repo), outsideName)
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	targets := map[string]string{
		"link-rel":      "README.md",
		"link-abs":      filepath.Join(repo, "README.md"),
		"link-escape":   ".." + string(filepath.Separator) + outsideName,
		"link-dangling": "does-not-exist",
	}
	for name, target := range targets {
		mustSymlink(t, target, filepath.Join(repo, name))
	}

	m := newManager(t, repo)
	host := seedWorkspace(t, m, context.Background(), "links")
	result, err := m.Seed(context.Background(), "links")
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if result.Copied != len(targets) {
		t.Fatalf("copied = %d, want %d (%#v)", result.Copied, len(targets), result)
	}
	if len(result.Conflicts) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("unexpected conflicts/skips: %#v", result)
	}
	for name, target := range targets {
		link := filepath.Join(host, name)
		info, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("%s not seeded: %v", name, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is not a symlink (mode %v)", name, info.Mode())
		}
		got, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("readlink %s: %v", name, err)
		}
		if got != target {
			t.Fatalf("%s target = %q, want %q", name, got, target)
		}
	}
	if _, err := os.Stat(filepath.Join(host, "link-dangling")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling symlink unexpectedly resolves: %v", err)
	}
}

func TestSeedWithRulesFiltersIgnoredFiles(t *testing.T) {
	repo := initRepo(t)
	commitGitignore(t, repo, ".env\nnode_modules/\n*.log\n")
	for rel, content := range map[string]string{
		".env":                      "SECRET=1\n",
		"node_modules/pkg/index.js": "module\n",
		"node_modules/.cache/tmp":   "cache\n",
		"app.log":                   "app\n",
		"debug.log":                 "debug\n",
	} {
		path := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := newManager(t, repo)
	ctx := context.Background()
	host := seedWorkspace(t, m, ctx, "filtered")
	result, err := m.SeedWithRules(ctx, "filtered", config.SeedConfig{
		Include: []string{".env", "node_modules/**", "**/*.log"},
		Exclude: []string{"node_modules/.cache/**", "**/debug.log"},
	})
	if err != nil {
		t.Fatalf("SeedWithRules: %v", err)
	}
	if result.Copied != 3 {
		t.Fatalf("copied = %d, want 3 (%#v)", result.Copied, result)
	}
	if len(result.Conflicts) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("unexpected conflicts/skips: %#v", result)
	}
	for _, rel := range []string{".env", "node_modules/pkg/index.js", "app.log"} {
		if _, err := os.Stat(filepath.Join(host, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("included file %s missing: %v", rel, err)
		}
	}
	for _, rel := range []string{"node_modules/.cache/tmp", "debug.log"} {
		if _, err := os.Stat(filepath.Join(host, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("excluded file %s was seeded: %v", rel, err)
		}
	}
}

func TestSeedWithRulesEmptyIncludeStillAppliesExcludes(t *testing.T) {
	repo := initRepo(t)
	commitGitignore(t, repo, "*.log\n")
	for _, rel := range []string{"app.log", "debug.log"} {
		if err := os.WriteFile(filepath.Join(repo, rel), []byte(rel+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := newManager(t, repo)
	ctx := context.Background()
	host := seedWorkspace(t, m, ctx, "empty-include")
	result, err := m.SeedWithRules(ctx, "empty-include", config.SeedConfig{
		Exclude: []string{"debug.log"},
	})
	if err != nil {
		t.Fatalf("SeedWithRules: %v", err)
	}
	if result.Copied != 1 {
		t.Fatalf("copied = %d, want 1 (%#v)", result.Copied, result)
	}
	if _, err := os.Stat(filepath.Join(host, "app.log")); err != nil {
		t.Fatalf("app.log missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(host, "debug.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("debug.log was seeded: %v", err)
	}
}

func TestSeedRejectsDestinationParentSymlinkEscape(t *testing.T) {
	repo := initRepo(t)
	commitGitignore(t, repo, "node_modules/\n")
	if err := os.MkdirAll(filepath.Join(repo, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "node_modules", "pkg", "index.js"), []byte("module\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newManager(t, repo)
	ctx := context.Background()

	host := seedWorkspace(t, m, ctx, "escape")
	outside := t.TempDir()
	mustSymlink(t, outside, filepath.Join(host, "node_modules"))
	if _, err := m.Seed(ctx, "escape"); err == nil {
		t.Fatal("seed followed a destination parent symlink outside the workspace")
	} else if !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "pkg", "index.js")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("seed wrote outside the workspace: %v", err)
	}

	danglingHost := seedWorkspace(t, m, ctx, "dangling")
	danglingTarget := filepath.Join(danglingHost, "missing-dir")
	mustSymlink(t, danglingTarget, filepath.Join(danglingHost, "node_modules"))
	if _, err := m.Seed(ctx, "dangling"); err == nil {
		t.Fatal("seed accepted a dangling destination parent symlink")
	} else if !strings.Contains(err.Error(), "dangling symlink") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Lstat(danglingTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("seed created a dangling symlink target: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(danglingHost, "node_modules")); err != nil || target != danglingTarget {
		t.Fatalf("destination parent symlink changed: %q %v", target, err)
	}
}

func TestSeedAllowsDestinationParentSymlinkInsideWorkspace(t *testing.T) {
	repo := initRepo(t)
	commitGitignore(t, repo, "cache/\n")
	if err := os.MkdirAll(filepath.Join(repo, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "cache", "a"), []byte("cache\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newManager(t, repo)
	ctx := context.Background()
	host := seedWorkspace(t, m, ctx, "inside")
	if err := os.MkdirAll(filepath.Join(host, "real-cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, "real-cache", filepath.Join(host, "cache"))

	result, err := m.Seed(ctx, "inside")
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if result.Copied != 1 {
		t.Fatalf("copied = %d, want 1 (%#v)", result.Copied, result)
	}
	data, err := os.ReadFile(filepath.Join(host, "real-cache", "a"))
	if err != nil || string(data) != "cache\n" {
		t.Fatalf("file not seeded through in-workspace symlink: %q %v", data, err)
	}
	if target, err := os.Readlink(filepath.Join(host, "cache")); err != nil || target != "real-cache" {
		t.Fatalf("destination parent symlink replaced: %q %v", target, err)
	}
}

func TestSeedNeverOverwritesExistingDestination(t *testing.T) {
	repo := initRepo(t)
	commitGitignore(t, repo, ".env\nlink\nfile\n")
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "file"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, "README.md", filepath.Join(repo, "link"))

	m := newManager(t, repo)
	ctx := context.Background()
	host := seedWorkspace(t, m, ctx, "conflicts")
	if err := os.WriteFile(filepath.Join(host, ".env"), []byte("KEEP=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, "elsewhere", filepath.Join(host, "link"))
	if err := os.MkdirAll(filepath.Join(host, "file"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := m.Seed(ctx, "conflicts")
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if result.Copied != 0 {
		t.Fatalf("copied = %d, want 0", result.Copied)
	}
	conflicts := map[string]bool{}
	for _, rel := range result.Conflicts {
		conflicts[rel] = true
	}
	for _, want := range []string{".env", "link", "file"} {
		if !conflicts[want] {
			t.Fatalf("missing conflict %q: %#v", want, result.Conflicts)
		}
	}
	data, err := os.ReadFile(filepath.Join(host, ".env"))
	if err != nil || string(data) != "KEEP=1\n" {
		t.Fatalf("existing file overwritten: %q %v", data, err)
	}
	if target, err := os.Readlink(filepath.Join(host, "link")); err != nil || target != "elsewhere" {
		t.Fatalf("existing symlink overwritten: %q %v", target, err)
	}
	if info, err := os.Lstat(filepath.Join(host, "file")); err != nil || !info.IsDir() {
		t.Fatalf("existing directory replaced: %v %v", info, err)
	}
}

func TestSeedReflinkSeam(t *testing.T) {
	setup := func(t *testing.T, ignored string) (*Manager, string, []byte) {
		t.Helper()
		repo := initRepo(t)
		commitGitignore(t, repo, ignored)
		content := []byte(strings.Repeat("reflink-me\n", 64))
		if err := os.WriteFile(filepath.Join(repo, ignored), content, 0o640); err != nil {
			t.Fatal(err)
		}
		m := newManager(t, repo)
		host := seedWorkspace(t, m, context.Background(), "copied")
		return m, host, content
	}

	t.Run("clone success skips byte copy", func(t *testing.T) {
		m, host, content := setup(t, "data.bin")
		var cloneCalls, copyCalls int
		m.copier = &seedCopier{
			clone: func(src, dst string, perm os.FileMode) error {
				cloneCalls++
				return copyFile(src, dst, perm)
			},
			copy: func(src, dst string, perm os.FileMode) error {
				copyCalls++
				return errors.New("byte copy should not run when cloning succeeds")
			},
		}
		result, err := m.Seed(context.Background(), "copied")
		if err != nil {
			t.Fatalf("Seed: %v", err)
		}
		if cloneCalls != 1 || copyCalls != 0 {
			t.Fatalf("clone calls = %d, copy calls = %d", cloneCalls, copyCalls)
		}
		if result.Copied != 1 {
			t.Fatalf("copied = %d (%#v)", result.Copied, result)
		}
		got, err := os.ReadFile(filepath.Join(host, "data.bin"))
		if err != nil || string(got) != string(content) {
			t.Fatalf("content = %q, %v", got, err)
		}
	})

	t.Run("clone failure falls back to byte copy", func(t *testing.T) {
		m, host, content := setup(t, "data.bin")
		var cloneCalls, copyCalls int
		m.copier = &seedCopier{
			clone: func(src, dst string, perm os.FileMode) error {
				cloneCalls++
				return errors.New("reflink: unsupported filesystem")
			},
			copy: func(src, dst string, perm os.FileMode) error {
				copyCalls++
				return copyFile(src, dst, perm)
			},
		}
		result, err := m.Seed(context.Background(), "copied")
		if err != nil {
			t.Fatalf("Seed: %v", err)
		}
		if cloneCalls != 1 || copyCalls != 1 {
			t.Fatalf("clone calls = %d, copy calls = %d", cloneCalls, copyCalls)
		}
		if result.Copied != 1 {
			t.Fatalf("copied = %d (%#v)", result.Copied, result)
		}
		got, err := os.ReadFile(filepath.Join(host, "data.bin"))
		if err != nil || string(got) != string(content) {
			t.Fatalf("content = %q, %v", got, err)
		}
	})

	t.Run("environment override forces byte copy", func(t *testing.T) {
		m, host, content := setup(t, "data.bin")
		t.Setenv(reflinkDisableEnv, "1")
		var cloneCalls, copyCalls int
		m.copier = &seedCopier{
			clone: func(src, dst string, perm os.FileMode) error {
				cloneCalls++
				return copyFile(src, dst, perm)
			},
			copy: func(src, dst string, perm os.FileMode) error {
				copyCalls++
				return copyFile(src, dst, perm)
			},
		}
		result, err := m.Seed(context.Background(), "copied")
		if err != nil {
			t.Fatalf("Seed: %v", err)
		}
		if cloneCalls != 0 || copyCalls != 1 {
			t.Fatalf("clone calls = %d, copy calls = %d", cloneCalls, copyCalls)
		}
		if result.Copied != 1 {
			t.Fatalf("copied = %d (%#v)", result.Copied, result)
		}
		got, err := os.ReadFile(filepath.Join(host, "data.bin"))
		if err != nil || string(got) != string(content) {
			t.Fatalf("content = %q, %v", got, err)
		}
	})
}

func TestSeedPreservesFileModeAndModTime(t *testing.T) {
	for _, tc := range []struct {
		name           string
		disableReflink bool
	}{
		{name: "default"},
		{name: "byte-copy", disableReflink: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.disableReflink {
				t.Setenv(reflinkDisableEnv, "1")
			}
			repo := initRepo(t)
			commitGitignore(t, repo, "tool.bin\n")
			src := filepath.Join(repo, "tool.bin")
			if err := os.WriteFile(src, []byte("mode and time\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			modTime := time.Unix(1_600_000_000, 0)
			if err := os.Chtimes(src, modTime, modTime); err != nil {
				t.Fatal(err)
			}

			m := newManager(t, repo)
			host := seedWorkspace(t, m, context.Background(), "metadata")
			if _, err := m.Seed(context.Background(), "metadata"); err != nil {
				t.Fatalf("Seed: %v", err)
			}
			info, err := os.Lstat(filepath.Join(host, "tool.bin"))
			if err != nil {
				t.Fatalf("seeded file missing: %v", err)
			}
			if info.Mode().Perm() != 0o640 {
				t.Fatalf("mode = %v, want 0640", info.Mode().Perm())
			}
			if !info.ModTime().Equal(modTime) {
				t.Fatalf("mtime = %v, want %v", info.ModTime(), modTime)
			}
		})
	}
}

func TestSeedPreservesCreatedDirectoryPermissions(t *testing.T) {
	repo := initRepo(t)
	commitGitignore(t, repo, "cache/\n")
	dir := filepath.Join(repo, "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newManager(t, repo)
	host := seedWorkspace(t, m, context.Background(), "dir-mode")
	if _, err := m.Seed(context.Background(), "dir-mode"); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	info, err := os.Stat(filepath.Join(host, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("created directory mode = %v, want 0750", info.Mode().Perm())
	}
}

func TestSeedPreservesDirectoryModTimes(t *testing.T) {
	repo := initRepo(t)
	commitGitignore(t, repo, "cache/\nkeep/\nquiet/\nnested/\n")
	for _, dir := range []string{"cache", "keep", "quiet", "nested/one/two"} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "cache", "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "keep", "b"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "nested", "one", "two", "file"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcCacheTime := time.Unix(1_500_000_000, 0)
	srcKeepTime := time.Unix(1_500_100_000, 0)
	srcQuietTime := time.Unix(1_500_200_000, 0)
	srcNestedOneTime := time.Unix(1_500_300_000, 0)
	srcNestedTwoTime := time.Unix(1_500_400_000, 0)
	for dir, ts := range map[string]time.Time{
		"cache":          srcCacheTime,
		"keep":           srcKeepTime,
		"quiet":          srcQuietTime,
		"nested/one":     srcNestedOneTime,
		"nested/one/two": srcNestedTwoTime,
	} {
		if err := os.Chtimes(filepath.Join(repo, dir), ts, ts); err != nil {
			t.Fatal(err)
		}
	}

	m := newManager(t, repo)
	ctx := context.Background()
	host := seedWorkspace(t, m, ctx, "dir-times")

	preKeepTime := time.Unix(1_400_000_000, 0)
	if err := os.MkdirAll(filepath.Join(host, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(host, "keep"), preKeepTime, preKeepTime); err != nil {
		t.Fatal(err)
	}
	preQuietTime := time.Unix(1_400_100_000, 0)
	if err := os.MkdirAll(filepath.Join(host, "quiet"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(host, "quiet"), preQuietTime, preQuietTime); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(host, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Seed(ctx, "dir-times"); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	info, err := os.Stat(filepath.Join(host, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(srcCacheTime) {
		t.Fatalf("created directory mtime = %v, want %v", info.ModTime(), srcCacheTime)
	}

	info, err = os.Stat(filepath.Join(host, "keep"))
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Equal(srcKeepTime) {
		t.Fatalf("pre-existing directory was stamped with the source mtime %v", srcKeepTime)
	}
	if !info.ModTime().After(preKeepTime) {
		t.Fatalf("pre-existing directory should be updated by writing its child, got %v", info.ModTime())
	}

	info, err = os.Stat(filepath.Join(host, "quiet"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(preQuietTime) {
		t.Fatalf("untouched pre-existing directory mtime = %v, want %v", info.ModTime(), preQuietTime)
	}

	for dir, want := range map[string]time.Time{
		"nested/one":     srcNestedOneTime,
		"nested/one/two": srcNestedTwoTime,
	} {
		info, err := os.Stat(filepath.Join(host, filepath.FromSlash(dir)))
		if err != nil {
			t.Fatalf("seeded directory %s missing: %v", dir, err)
		}
		if !info.ModTime().Equal(want) {
			t.Fatalf("%s mtime = %v, want %v", dir, info.ModTime(), want)
		}
	}
}
