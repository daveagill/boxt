package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/platform"
)

func initGitProject(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	run := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", dir}, args...)
		if out, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: full}); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("add", "-A")
	run("commit", "-q", "-m", "initial")
}

func newWorkspaceFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	f := newLifecycleFixture(t)
	initGitProject(t, f.project.Root)
	return f
}

func (f *lifecycleFixture) workspaceHostRoot() string {
	return platform.WorkspacesDir(f.dataDir, f.project.ID)
}

func TestWorkspaceCreateListPath(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.runner.status = "Stopped"
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	wantPath := filepath.Join(f.workspaceHostRoot(), "feature-a")
	if info, err := os.Stat(wantPath); err != nil || !info.IsDir() {
		t.Fatalf("workspace not created: %v", err)
	}
	for _, want := range []string{"Created workspace feature-a", wantPath, "/workspaces/" + f.project.ID + "/feature-a", "boxt/feature-a"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, f.out.String())
		}
	}
	if len(f.runner.commands) != 0 {
		t.Fatalf("workspace create invoked Lima: %#v", f.runner.commands)
	}

	f2 := f
	f2.out.Reset()
	if code := f2.run(t, "workspace", "list"); code != 0 {
		t.Fatalf("list exit %d: %s", code, f2.errOut.String())
	}
	for _, want := range []string{"NAME", "default", "feature-a", wantPath, "boxt/feature-a", "ready"} {
		if !strings.Contains(f2.out.String(), want) {
			t.Fatalf("list missing %q:\n%s", want, f2.out.String())
		}
	}

	f2.out.Reset()
	if code := f2.run(t, "workspace", "path", "feature-a"); code != 0 {
		t.Fatalf("path exit %d: %s", code, f2.errOut.String())
	}
	if strings.TrimSpace(f2.out.String()) != wantPath {
		t.Fatalf("path output = %q, want %q", f2.out.String(), wantPath)
	}

	f2.out.Reset()
	if code := f2.run(t, "workspace", "path", "default"); code != 0 {
		t.Fatalf("default path exit %d: %s", code, f2.errOut.String())
	}
	if strings.TrimSpace(f2.out.String()) != f.project.Root {
		t.Fatalf("default path = %q", f2.out.String())
	}
}

func TestWorkspaceCreateBranch(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "create", "one", "--branch", "feature/one"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "feature/one") {
		t.Fatalf("branch missing: %s", f.out.String())
	}
	out, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{
		Name: "git", Args: []string{"-C", f.project.Root, "show-ref", "--verify", "refs/heads/feature/one"},
	})
	if err != nil {
		t.Fatalf("branch not created: %v (%s)", err, out)
	}
}

func TestWorkspaceCreateDuplicate(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "create", "one"); code != 0 {
		t.Fatalf("first create failed")
	}
	f.errOut.Reset()
	if code := f.run(t, "workspace", "create", "one"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "Workspace already exists: one") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestWorkspaceCreateInvalidName(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "create", "../escape"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "invalid workspace name") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestWorkspaceCreateNonGit(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "workspace", "create", "one"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "require a Git repository") || !strings.Contains(f.errOut.String(), "git init") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestWorkspaceListNonGit(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "workspace", "list"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "default") || !strings.Contains(f.out.String(), "Note: named workspaces require a Git repository") {
		t.Fatalf("unexpected output:\n%s", f.out.String())
	}
}

func TestWorkspaceRemove(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "create", "one"); code != 0 {
		t.Fatalf("create failed: %s", f.errOut.String())
	}
	path := filepath.Join(f.workspaceHostRoot(), "one")
	f.out.Reset()
	if code := f.run(t, "workspace", "remove", "one"); code != 0 {
		t.Fatalf("remove exit %d: %s", code, f.errOut.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists: %v", err)
	}
	if !strings.Contains(f.out.String(), "Git branch boxt/one was kept") {
		t.Fatalf("unexpected output: %s", f.out.String())
	}
	out, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{
		Name: "git", Args: []string{"-C", f.project.Root, "show-ref", "--verify", "refs/heads/boxt/one"},
	})
	if err != nil {
		t.Fatalf("branch deleted: %v (%s)", err, out)
	}
}

func TestWorkspaceRemoveDirty(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "create", "one"); code != 0 {
		t.Fatalf("create failed")
	}
	path := filepath.Join(f.workspaceHostRoot(), "one")
	if err := os.WriteFile(filepath.Join(path, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.errOut.Reset()
	if code := f.run(t, "workspace", "remove", "one"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	for _, want := range []string{"uncommitted changes", "Commit or stash", "--force"} {
		if !strings.Contains(f.errOut.String(), want) {
			t.Fatalf("error missing %q:\n%s", want, f.errOut.String())
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("dirty workspace removed without force")
	}
	f.out.Reset()
	if code := f.run(t, "workspace", "remove", "one", "--force"); code != 0 {
		t.Fatalf("force remove exit %d: %s", code, f.errOut.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("forced remove left workspace")
	}
}

func TestWorkspaceRemoveDefault(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "remove", "default"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "default workspace") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestWorkspaceRemoveMissing(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "create", "gone"); code != 0 {
		t.Fatalf("create failed")
	}
	path := filepath.Join(f.workspaceHostRoot(), "gone")
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	f.out.Reset()
	if code := f.run(t, "workspace", "list"); code != 0 {
		t.Fatalf("list exit %d", code)
	}
	if !strings.Contains(f.out.String(), "missing") {
		t.Fatalf("missing status not shown:\n%s", f.out.String())
	}
	f.out.Reset()
	if code := f.run(t, "workspace", "remove", "gone"); code != 0 {
		t.Fatalf("remove exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "Pruned stale") {
		t.Fatalf("unexpected output: %s", f.out.String())
	}
}

func TestWorkspacePathMissing(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "create", "gone"); code != 0 {
		t.Fatalf("create failed")
	}
	if err := os.RemoveAll(filepath.Join(f.workspaceHostRoot(), "gone")); err != nil {
		t.Fatal(err)
	}
	if code := f.run(t, "workspace", "path", "gone"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "missing") || !strings.Contains(f.errOut.String(), "workspace remove gone") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestWorkspacePathUnknown(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "path", "nope"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "Workspace not found: nope") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestWorkspaceRun(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed: %s", f.errOut.String())
	}
	f.runner.commands = nil
	if code := f.run(t, "workspace", "run", "feature-a", "test"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	guest := "/workspaces/" + f.project.ID + "/feature-a"
	c := f.shellCommand(t)
	if !containsArg(c.Args, guest) {
		t.Fatalf("missing guest workdir %q: %#v", guest, c.Args)
	}
	if !endsWith(c.Args, []string{"--", "npm", "test"}) {
		t.Fatalf("unexpected args: %#v", c.Args)
	}
}

func TestWorkspaceRunArbitrary(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed")
	}
	if code := f.run(t, "workspace", "run", "feature-a", "--", "python", "script.py"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !endsWith(f.shellCommand(t).Args, []string{"--", "python", "script.py"}) {
		t.Fatalf("unexpected args: %#v", f.shellCommand(t).Args)
	}
}

func TestWorkspaceRunUnknownTargetHint(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed")
	}
	f.runner.commands = nil
	if code := f.run(t, "workspace", "run", "feature-a", "codex"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "boxt workspace run feature-a -- codex") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
	if f.countCommands("shell") != 0 {
		t.Fatal("unknown target reached the guest")
	}
}

func TestWorkspaceRunDefault(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "workspace", "run", "default", "dev"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !containsArg(f.shellCommand(t).Args, "/workspace") {
		t.Fatalf("unexpected args: %#v", f.shellCommand(t).Args)
	}
}

func TestWorkspaceRunAutoStart(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Stopped"
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed")
	}
	f.runner.commands = nil
	if code := f.run(t, "workspace", "run", "feature-a", "test"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if f.countCommands("start") != 1 {
		t.Fatalf("expected auto-start: %#v", f.runner.commands)
	}
}

func TestWorkspaceRunNoStart(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Stopped"
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed")
	}
	f.runner.commands = nil
	if code := f.run(t, "workspace", "run", "--no-start", "feature-a", "test"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if f.countCommands("start") != 0 || f.countCommands("shell") != 0 {
		t.Fatalf("--no-start invoked Lima: %#v", f.runner.commands)
	}
}

func TestWorkspaceRunTargetArguments(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed")
	}
	if code := f.run(t, "workspace", "run", "feature-a", "test", "--", "--watch"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	guest := "/workspaces/" + f.project.ID + "/feature-a"
	c := f.shellCommand(t)
	if !containsArg(c.Args, guest) {
		t.Fatalf("missing guest workdir %q: %#v", guest, c.Args)
	}
	if !endsWith(c.Args, []string{"--", "npm", "test", "--watch"}) {
		t.Fatalf("unexpected args: %#v", c.Args)
	}
}

func TestWorkspaceRunUnexpectedBeforeDash(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed")
	}
	if code := f.run(t, "workspace", "run", "feature-a", "dev", "extra", "--", "x"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "before `--`") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestWorkspaceMerge(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed: %s", f.errOut.String())
	}
	path := filepath.Join(f.workspaceHostRoot(), "feature-a")
	if err := os.WriteFile(filepath.Join(path, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "feature.txt"}, {"commit", "-q", "-m", "feature"}} {
		if out, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: append([]string{"-C", path}, args...)}); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	f.out.Reset()
	if code := f.run(t, "workspace", "merge", "feature-a"); code != 0 {
		t.Fatalf("merge exit %d: %s", code, f.errOut.String())
	}
	for _, want := range []string{"Fast-forwarded", "boxt/feature-a", "HEAD:"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("merge output missing %q:\n%s", want, f.out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(f.project.Root, "feature.txt")); err != nil {
		t.Fatalf("feature not merged into project root: %v", err)
	}
}

func TestWorkspaceMergeRemove(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed")
	}
	path := filepath.Join(f.workspaceHostRoot(), "feature-a")
	if err := os.WriteFile(filepath.Join(path, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "feature.txt"}, {"commit", "-q", "-m", "feature"}} {
		if out, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: append([]string{"-C", path}, args...)}); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	if code := f.run(t, "workspace", "merge", "feature-a", "--remove"); code != 0 {
		t.Fatalf("merge exit %d: %s", code, f.errOut.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("workspace not removed")
	}
	if !strings.Contains(f.out.String(), "Git branch boxt/feature-a was kept") {
		t.Fatalf("branch retention missing:\n%s", f.out.String())
	}
}

func TestWorkspaceMergeDefaultRefused(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "merge", "default"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "default workspace") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestWorkspaceShell(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.runner.status = "Running"
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed")
	}
	if code := f.run(t, "workspace", "shell", "feature-a"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	guest := "/workspaces/" + f.project.ID + "/feature-a"
	c := f.shellCommand(t)
	if !containsArg(c.Args, guest) {
		t.Fatalf("missing guest workdir: %#v", c.Args)
	}
	if containsArg(c.Args, "--") {
		t.Fatalf("plain shell should not pass a command: %#v", c.Args)
	}
}

func TestWorkspaceShellDefault(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.runner.status = "Running"
	if code := f.run(t, "workspace", "shell", "default"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !containsArg(f.shellCommand(t).Args, "/workspace") {
		t.Fatalf("unexpected args: %#v", f.shellCommand(t).Args)
	}
}

func TestWorkspaceRunNonGitNamed(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "workspace", "run", "nope", "test"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "require a Git repository") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestCommandsWorkFromInsideWorkspace(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.runner.status = "Running"
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed: %s", f.errOut.String())
	}
	wsPath := filepath.Join(f.workspaceHostRoot(), "feature-a")
	f.app.Getwd = func() (string, error) { return wsPath, nil }
	f.out.Reset()
	if code := f.run(t, "workspace", "path", "feature-a"); code != 0 {
		t.Fatalf("path exit %d: %s", code, f.errOut.String())
	}
	if strings.TrimSpace(f.out.String()) != wsPath {
		t.Fatalf("path = %q, want %q", f.out.String(), wsPath)
	}
	f.out.Reset()
	if code := f.run(t, "status"); code != 0 {
		t.Fatalf("status exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), f.project.Root) || !strings.Contains(f.out.String(), f.project.VMName()) {
		t.Fatalf("status from workspace did not resolve project:\n%s", f.out.String())
	}
}
