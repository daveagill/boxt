package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/boxt/boxt/internal/command"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	if out, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: full}); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}

func TestTopLevel(t *testing.T) {
	repo := initRepo(t)
	nested := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := New(command.ExecRunner{}).TopLevel(context.Background(), nested)
	if err != nil {
		t.Fatalf("TopLevel: %v", err)
	}
	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("TopLevel = %q, want %q", got, filepath.Clean(want))
	}
}

func TestNotRepository(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	_, err := New(command.ExecRunner{}).TopLevel(context.Background(), dir)
	if !errors.Is(err, ErrNotRepository) {
		t.Fatalf("expected ErrNotRepository, got %v", err)
	}
	ok, err := New(command.ExecRunner{}).IsRepository(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("IsRepository reported true for a non-repository")
	}
}

type fakeRunner struct {
	args []string
	out  string
	err  error
}

func (f *fakeRunner) Run(ctx context.Context, c command.Command) error {
	f.args = append([]string{c.Name}, c.Args...)
	if c.Stdout != nil && f.out != "" {
		_, _ = c.Stdout.Write([]byte(f.out))
	}
	return f.err
}

func TestCommandConstruction(t *testing.T) {
	fr := &fakeRunner{out: "/repo\n"}
	got, err := New(fr).TopLevel(context.Background(), "/repo/sub")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/repo" {
		t.Fatalf("TopLevel = %q", got)
	}
	want := []string{"git", "-C", "/repo/sub", "rev-parse", "--show-toplevel"}
	if len(fr.args) != len(want) {
		t.Fatalf("args = %#v, want %#v", fr.args, want)
	}
	for i := range want {
		if fr.args[i] != want[i] {
			t.Fatalf("args = %#v, want %#v", fr.args, want)
		}
	}
}

func TestExit128MapsToNotRepository(t *testing.T) {
	fr := &fakeRunner{err: &command.Error{
		Name:     "git",
		Args:     []string{"rev-parse"},
		ExitCode: 128,
		Stderr:   "fatal: not a git repository (or any of the parent directories): .git",
	}}
	_, err := New(fr).TopLevel(context.Background(), "/tmp")
	if !errors.Is(err, ErrNotRepository) {
		t.Fatalf("expected ErrNotRepository, got %v", err)
	}
}

func TestUnexpectedErrorIsNotMasked(t *testing.T) {
	fr := &fakeRunner{err: &command.Error{Name: "git", ExitCode: 127, Stderr: "permission denied"}}
	_, err := New(fr).TopLevel(context.Background(), "/tmp")
	if err == nil || errors.Is(err, ErrNotRepository) {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}
