//go:build linux || darwin

package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestSeedClassifiesSpecialFiles covers the special-file skip. Git does not
// list FIFOs or sockets as untracked paths, so the classification Seed uses for
// defensive handling is exercised directly, together with a seed run that must
// leave the special file out of the workspace.
func TestSeedClassifiesSpecialFiles(t *testing.T) {
	repo := initRepo(t)
	pipe := filepath.Join(repo, "pipe")
	if err := unix.Mkfifo(pipe, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	info, err := os.Lstat(pipe)
	if err != nil {
		t.Fatal(err)
	}
	if got := classifySeedSource(info.Mode()); got != seedSourceSpecial {
		t.Fatalf("FIFO classified as %v, want special", got)
	}

	m := newManager(t, repo)
	ctx := context.Background()
	host := seedWorkspace(t, m, ctx, "special")
	if _, err := m.Seed(ctx, "special"); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(host, "pipe")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("special file was seeded: %v", err)
	}
}

func TestCloneFileWhenSupported(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	dst := filepath.Join(dir, "clone")
	content := []byte("reflink content\n")
	if err := os.WriteFile(src, content, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := cloneFile(src, dst, 0o640); err != nil {
		t.Skipf("filesystem does not support reflink: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("cloned content = %q, want %q", got, content)
	}
}
