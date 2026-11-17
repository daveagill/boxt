package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/workspace"
)

// newRunWorkspaceFixture creates two named workspaces and clears the recorded
// Lima commands so assertions only see the execution under test.
func newRunWorkspaceFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	f := newWorkspaceFixture(t)
	f.writeConfig(t, portConfig)
	f.runner.status = "Running"
	for _, name := range []string{"feature-a", "feature-b"} {
		if code := f.run(t, "workspace", "create", name); code != 0 {
			t.Fatalf("create %s: %s", name, f.errOut.String())
		}
	}
	f.runner.commands = nil
	f.out.Reset()
	f.errOut.Reset()
	return f
}

func (f *lifecycleFixture) setCwd(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	f.app.Getwd = func() (string, error) { return path, nil }
}

func (f *lifecycleFixture) guestPath(name string) string {
	if name == workspace.DefaultName {
		return "/workspace"
	}
	return "/workspaces/" + f.project.ID + "/" + name
}

func portArg(t *testing.T, args []string, want int) bool {
	t.Helper()
	prefix := "PORT=" + strconv.Itoa(want)
	for _, arg := range args {
		if arg == prefix {
			return true
		}
	}
	return false
}

func TestRunFollowsCurrentWorkspace(t *testing.T) {
	f := newRunWorkspaceFixture(t)
	f.setCwd(t, filepath.Join(f.workspaceHostRoot(), "feature-a"))

	if code := f.run(t, "run"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	c := f.shellCommand(t)
	if !containsArg(c.Args, f.guestPath("feature-a")) {
		t.Fatalf("missing workspace workdir %q: %#v", f.guestPath("feature-a"), c.Args)
	}
	wantPort := 3000 + workspace.PortOffset(f.project.ID, "feature-a")
	if !portArg(t, c.Args, wantPort) {
		t.Fatalf("missing PORT=%d for named workspace: %#v", wantPort, c.Args)
	}
	if !endsWith(c.Args, []string{"--", "/usr/bin/env", "PORT=" + strconv.Itoa(wantPort), "npm", "run", "dev"}) {
		t.Fatalf("unexpected args: %#v", c.Args)
	}
}

func TestRunFollowsWorkspaceSubdirectory(t *testing.T) {
	f := newRunWorkspaceFixture(t)
	nested := filepath.Join(f.workspaceHostRoot(), "feature-a", "src", "deep")
	f.setCwd(t, nested)

	if code := f.run(t, "run", "dev"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !containsArg(f.shellCommand(t).Args, f.guestPath("feature-a")) {
		t.Fatalf("unexpected args: %#v", f.shellCommand(t).Args)
	}
}

func TestBareBoxtFollowsCurrentWorkspace(t *testing.T) {
	f := newRunWorkspaceFixture(t)
	f.setCwd(t, filepath.Join(f.workspaceHostRoot(), "feature-b"))

	if code := f.run(t, "dev"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !containsArg(f.shellCommand(t).Args, f.guestPath("feature-b")) {
		t.Fatalf("unexpected args: %#v", f.shellCommand(t).Args)
	}
}

func TestRunFromProjectRootUsesDefaultWorkspace(t *testing.T) {
	f := newRunWorkspaceFixture(t)
	f.setCwd(t, f.project.Root)

	if code := f.run(t, "run"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	c := f.shellCommand(t)
	if !containsArg(c.Args, "/workspace") {
		t.Fatalf("missing default workdir: %#v", c.Args)
	}
	if !portArg(t, c.Args, 3000) {
		t.Fatalf("missing default PORT=3000: %#v", c.Args)
	}
}

func TestShellFollowsCurrentWorkspace(t *testing.T) {
	f := newRunWorkspaceFixture(t)
	f.setCwd(t, filepath.Join(f.workspaceHostRoot(), "feature-a"))

	if code := f.run(t, "shell"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !containsArg(f.shellCommand(t).Args, f.guestPath("feature-a")) {
		t.Fatalf("unexpected args: %#v", f.shellCommand(t).Args)
	}
}

func TestWorkspaceRunDefaultEscapesCurrentWorkspace(t *testing.T) {
	f := newRunWorkspaceFixture(t)
	f.setCwd(t, filepath.Join(f.workspaceHostRoot(), "feature-a"))

	if code := f.run(t, "workspace", "run", "default", "dev"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	c := f.shellCommand(t)
	if !containsArg(c.Args, "/workspace") {
		t.Fatalf("default escape did not use /workspace: %#v", c.Args)
	}
	if !portArg(t, c.Args, 3000) {
		t.Fatalf("default escape did not reset the port offset: %#v", c.Args)
	}
}

func TestWorkspaceRunDefaultArbitraryCommandEscapes(t *testing.T) {
	f := newRunWorkspaceFixture(t)
	f.setCwd(t, filepath.Join(f.workspaceHostRoot(), "feature-a"))

	if code := f.run(t, "workspace", "run", "default", "--", "pwd"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	c := f.shellCommand(t)
	if !containsArg(c.Args, "/workspace") {
		t.Fatalf("default escape did not use /workspace: %#v", c.Args)
	}
	if !endsWith(c.Args, []string{"--", "pwd"}) {
		t.Fatalf("unexpected args: %#v", c.Args)
	}
}

func TestWorkspaceShellDefaultEscapesCurrentWorkspace(t *testing.T) {
	f := newRunWorkspaceFixture(t)
	f.setCwd(t, filepath.Join(f.workspaceHostRoot(), "feature-a"))

	if code := f.run(t, "workspace", "shell", "default"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !containsArg(f.shellCommand(t).Args, "/workspace") {
		t.Fatalf("unexpected args: %#v", f.shellCommand(t).Args)
	}
}

func TestWorkspaceRunExplicitNameWinsOverCwd(t *testing.T) {
	f := newRunWorkspaceFixture(t)
	f.setCwd(t, filepath.Join(f.workspaceHostRoot(), "feature-a"))

	if code := f.run(t, "workspace", "run", "feature-b", "dev"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !containsArg(f.shellCommand(t).Args, f.guestPath("feature-b")) {
		t.Fatalf("explicit workspace did not win: %#v", f.shellCommand(t).Args)
	}
}

func TestRunUnknownTargetFromWorkspaceHint(t *testing.T) {
	f := newRunWorkspaceFixture(t)
	f.setCwd(t, filepath.Join(f.workspaceHostRoot(), "feature-a"))

	if code := f.run(t, "run", "codex"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "boxt run -- codex") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
	if f.countCommands("shell") != 0 {
		t.Fatalf("unknown target reached the guest: %#v", f.runner.commands)
	}
}
