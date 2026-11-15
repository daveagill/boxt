package project

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/boxt/boxt/internal/config"
)

func TestInitAndLookup(t *testing.T) {
	dir := t.TempDir()
	p, err := Init(dir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if p.Root != dir {
		t.Fatalf("root = %q, want %q", p.Root, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, ConfigRelPath)); err != nil {
		t.Fatalf("config not created: %v", err)
	}
	if p.Config.VM.Image != config.DefaultImage {
		t.Fatalf("unexpected config: %#v", p.Config)
	}

	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Lookup(nested)
	if err != nil {
		t.Fatalf("Lookup nested: %v", err)
	}
	if got.Root != p.Root || got.ID != p.ID {
		t.Fatalf("lookup = %#v, want root %q id %q", got, p.Root, p.ID)
	}
}

func TestInitDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ConfigRelPath)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Init(dir); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("expected already-initialized error, got %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("existing configuration was modified")
	}
}

func TestInitCreatesMissingPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deep", "project")
	p, err := Init(dir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if p.Root != dir {
		t.Fatalf("root = %q, want %q", p.Root, dir)
	}
}

func TestLookupNotInitialized(t *testing.T) {
	dir := t.TempDir()
	_, err := Lookup(dir)
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsNotInitialized(err) {
		t.Fatalf("expected ErrNotInitialized, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("error does not mention start path: %v", err)
	}
}

func TestLookupNestedProjects(t *testing.T) {
	outer := t.TempDir()
	if _, err := Init(outer); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(outer, "inner")
	if _, err := Init(inner); err != nil {
		t.Fatal(err)
	}
	got, err := Lookup(inner)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != inner {
		t.Fatalf("Lookup returned %q, want inner project %q", got.Root, inner)
	}
	got, err = Lookup(filepath.Join(inner, "src"))
	if err != nil {
		t.Fatalf("Lookup missing nested path: %v", err)
	}
	if got.Root != inner {
		t.Fatalf("Lookup returned %q, want inner project %q", got.Root, inner)
	}
}

func TestSymlinkResolvesToSameProject(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if _, err := Init(real); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	p1, err := Lookup(real)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := Lookup(link)
	if err != nil {
		t.Fatal(err)
	}
	if p1.ID != p2.ID || p1.Root != p2.Root {
		t.Fatalf("symlinked lookup differs: %#v vs %#v", p1, p2)
	}
}

func TestIDForRoot(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{16}$`)
	a := IDForRoot("/tmp/one")
	b := IDForRoot("/tmp/two")
	if !re.MatchString(a) || !re.MatchString(b) {
		t.Fatalf("unexpected ID format: %q %q", a, b)
	}
	if a == b {
		t.Fatal("different roots produced the same ID")
	}
	if a != IDForRoot("/tmp/one") {
		t.Fatal("ID is not deterministic")
	}
	if got := (&Project{ID: a}).VMName(); got != "boxt-"+a {
		t.Fatalf("VMName = %q", got)
	}
}

func TestLookupInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".boxt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigRelPath), []byte("version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Lookup(dir)
	if err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("expected validation error, got %v", err)
	}
	if IsNotInitialized(err) {
		t.Fatal("invalid config must not be reported as uninitialized")
	}
}

func TestLookupAcceptsFilePath(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Lookup(file)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != dir {
		t.Fatalf("root = %q, want %q", got.Root, dir)
	}
}

func TestCanonicalMissingPath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does", "not", "exist")
	got, err := Canonical(missing)
	if err != nil {
		t.Fatalf("Canonical missing: %v", err)
	}
	if !filepath.IsAbs(got) || got != missing {
		t.Fatalf("got %q, want %q", got, missing)
	}
}

func TestProjectString(t *testing.T) {
	p := &Project{Root: "/tmp/x", ID: "abc"}
	if !strings.Contains(p.String(), "/tmp/x") || !strings.Contains(p.String(), "abc") {
		t.Fatalf("unexpected string %q", p.String())
	}
	if p.DescribeRoot() != "/tmp/x" {
		t.Fatalf("DescribeRoot = %q", p.DescribeRoot())
	}
}

func TestIsNotInitializedOtherError(t *testing.T) {
	if IsNotInitialized(errors.New("boom")) {
		t.Fatal("unrelated error classified as uninitialized")
	}
}

func TestRejectsHomeAndAncestors(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	for _, root := range []string{home, base, "/"} {
		_, err := Init(root)
		var unsafe *ErrUnsafeProjectRoot
		if !errors.As(err, &unsafe) {
			t.Fatalf("Init(%q) = %v, want ErrUnsafeProjectRoot", root, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ConfigRelPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsafe init created configuration")
	}

	inside := filepath.Join(home, "projects", "app")
	if _, err := Init(inside); err != nil {
		t.Fatalf("Init inside home: %v", err)
	}
	if _, err := Lookup(inside); err != nil {
		t.Fatalf("Lookup inside home: %v", err)
	}
}

func TestLookupRejectsHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgPath := filepath.Join(home, ConfigRelPath)
	if err := config.Create(cfgPath); err != nil {
		t.Fatal(err)
	}
	_, err := Lookup(home)
	var unsafe *ErrUnsafeProjectRoot
	if !errors.As(err, &unsafe) {
		t.Fatalf("Lookup(home) = %v, want ErrUnsafeProjectRoot", err)
	}
}

func TestLookupRejectsUnmanagedWorktree(t *testing.T) {
	base := t.TempDir()
	worktree := filepath.Join(base, "manual-worktree")
	gitDir := filepath.Join(base, "repo", ".git", "worktrees", "manual-worktree")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.Create(filepath.Join(worktree, ConfigRelPath)); err != nil {
		t.Fatal(err)
	}
	_, err := Lookup(worktree)
	var unmanaged *ErrUnmanagedWorktree
	if !errors.As(err, &unmanaged) {
		t.Fatalf("Lookup(manual worktree) = %v, want ErrUnmanagedWorktree", err)
	}
}
