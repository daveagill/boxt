package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/command"
)

const createHookConfig = `version: 1
hooks:
  workspace_create:
    pre:
      - command: ["pre-guest"]
    post:
      - command: ["post-host"]
        where: host
`

const hostHookConfig = `version: 1
hooks:
  workspace_create:
    pre:
      - command: ["pre-host"]
        where: host
    post:
      - command: ["post-host"]
        where: host
  workspace_merge:
    pre:
      - command: ["merge-pre"]
        where: host
    post:
      - command: ["merge-post"]
        where: host
  workspace_remove:
    pre:
      - command: ["remove-pre"]
        where: host
    post:
      - command: ["remove-post"]
        where: host
`

type recordingRunner struct {
	commands []command.Command
	fail     map[string]error
}

func (r *recordingRunner) Run(ctx context.Context, cmd command.Command) error {
	r.commands = append(r.commands, cmd)
	if err := r.fail[cmd.Name]; err != nil {
		return err
	}
	return nil
}

func (r *recordingRunner) find(name string) (command.Command, bool) {
	for _, c := range r.commands {
		if c.Name == name {
			return c, true
		}
	}
	return command.Command{}, false
}

func trustProjectHooks(t *testing.T, f *lifecycleFixture) {
	t.Helper()
	if code := f.run(t, "hooks", "trust"); code != 0 {
		t.Fatalf("hooks trust exit %d: %s", code, f.errOut.String())
	}
	f.out.Reset()
	f.errOut.Reset()
}

func TestHooksStatusTrustClear(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.writeConfig(t, createHookConfig)
	if code := f.run(t, "hooks", "status"); code != 0 {
		t.Fatalf("status exit %d: %s", code, f.errOut.String())
	}
	for _, want := range []string{"Hooks:    untrusted", "Current:", "not approved", "workspace_create", "pre-guest"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("status missing %q:\n%s", want, f.out.String())
		}
	}
	f.out.Reset()
	if code := f.run(t, "hooks", "trust"); code != 0 {
		t.Fatalf("trust exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "Trusted lifecycle hooks") {
		t.Fatalf("unexpected trust output:\n%s", f.out.String())
	}
	f.out.Reset()
	if code := f.run(t, "hooks", "status"); code != 0 {
		t.Fatalf("status exit %d", code)
	}
	if !strings.Contains(f.out.String(), "Hooks:    trusted") {
		t.Fatalf("hooks not trusted:\n%s", f.out.String())
	}
	f.writeConfig(t, strings.Replace(createHookConfig, "pre-guest", "pre-changed", 1))
	f.out.Reset()
	if code := f.run(t, "hooks", "status"); code != 0 {
		t.Fatalf("status exit %d", code)
	}
	if !strings.Contains(f.out.String(), "Hooks:    untrusted") {
		t.Fatalf("changed hooks stayed trusted:\n%s", f.out.String())
	}
	f.out.Reset()
	if code := f.run(t, "hooks", "clear"); code != 0 {
		t.Fatalf("clear exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "Cleared hook approval") {
		t.Fatalf("unexpected clear output:\n%s", f.out.String())
	}
}

func TestHooksTrustWithoutHooks(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "hooks", "trust"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "No lifecycle hooks") {
		t.Fatalf("unexpected output:\n%s", f.out.String())
	}
}

func TestUntrustedHooksFailBeforeMutation(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.writeConfig(t, createHookConfig)
	f.runner.status = "Running"
	if code := f.run(t, "workspace", "create", "agent-1"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	msg := f.errOut.String()
	for _, want := range []string{"workspace_create hooks are not trusted", "pre-guest", "post-host", "boxt hooks trust"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
	}
	if _, err := os.Stat(filepath.Join(f.workspaceHostRoot(), "agent-1")); !os.IsNotExist(err) {
		t.Fatal("untrusted hooks created a workspace")
	}
	if f.countCommands("shell") != 0 {
		t.Fatal("untrusted hooks reached the guest")
	}
}

func TestCreateHooks(t *testing.T) {
	f := newWorkspaceFixture(t)
	runner := &recordingRunner{}
	f.app.Runner = runner
	f.writeConfig(t, createHookConfig)
	trustProjectHooks(t, f)
	f.runner.status = "Running"
	if code := f.run(t, "workspace", "create", "agent-1"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !endsWith(f.shellCommand(t).Args, []string{"--", "/usr/bin/env", "BOXT_PROJECT_ROOT=" + f.project.Root, "BOXT_WORKSPACE=agent-1", "BOXT_WORKSPACE_PATH=" + filepath.Join(f.workspaceHostRoot(), "agent-1"), "BOXT_GUEST_PATH=/workspaces/" + f.project.ID + "/agent-1", "pre-guest"}) {
		t.Fatalf("guest hook args = %#v", f.shellCommand(t).Args)
	}
	post, ok := runner.find("post-host")
	if !ok {
		t.Fatalf("post hook did not run: %#v", runner.commands)
	}
	if post.Dir != filepath.Join(f.workspaceHostRoot(), "agent-1") {
		t.Fatalf("post hook dir = %q", post.Dir)
	}
	if !strings.Contains(strings.Join(post.Env, "\n"), "BOXT_WORKSPACE=agent-1") {
		t.Fatalf("post hook env = %#v", post.Env)
	}
}

func TestCreatePreHookFailureRollsBack(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.writeConfig(t, `version: 1
hooks:
  workspace_create:
    pre:
      - command: ["pre-guest"]
`)
	trustProjectHooks(t, f)
	f.runner.status = "Running"
	f.runner.shellErr = &command.Error{Name: "limactl", ExitCode: 5, Stderr: "hook failed"}
	if code := f.run(t, "workspace", "create", "agent-1"); code != 5 {
		t.Fatalf("exit = %d, want 5", code)
	}
	if !strings.Contains(f.errOut.String(), "aborted by pre-hook") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
	if _, err := os.Stat(filepath.Join(f.workspaceHostRoot(), "agent-1")); !os.IsNotExist(err) {
		t.Fatal("pre-hook failure left the workspace")
	}
	if _, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{
		Name: "git", Args: []string{"-C", f.project.Root, "show-ref", "--verify", "--quiet", "refs/heads/boxt/agent-1"},
	}); err == nil {
		t.Fatal("pre-hook failure left the newly-created branch")
	}
}

func TestCreatePostHookFailureKeepsWorkspace(t *testing.T) {
	f := newWorkspaceFixture(t)
	runner := &recordingRunner{fail: map[string]error{"post-host": errors.New("post boom")}}
	f.app.Runner = runner
	f.writeConfig(t, hostHookConfig)
	trustProjectHooks(t, f)
	if code := f.run(t, "workspace", "create", "agent-1"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "operation completed") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
	if _, err := os.Stat(filepath.Join(f.workspaceHostRoot(), "agent-1")); err != nil {
		t.Fatal("post-hook failure removed the workspace")
	}
}

func TestGuestHookUsesToolchainEnvironment(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.runner.status = "Running"
	f.writeConfig(t, `version: 1
tools:
  node: "24"
hooks:
  workspace_create:
    pre:
      - command: ["echo", "hook"]
`)
	trustProjectHooks(t, f)
	f.app.PrepareCommand = func(tools map[string]string, argv []string) []string {
		return append([]string{"mise", "exec", "node@24", "--"}, argv...)
	}
	if code := f.run(t, "workspace", "create", "agent-1"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	args := f.shellCommand(t).Args
	for _, want := range []string{"mise", "exec", "node@24", "--", "/usr/bin/env", "BOXT_WORKSPACE=agent-1", "echo", "hook"} {
		if !containsArg(args, want) {
			t.Fatalf("hook args missing %q: %#v", want, args)
		}
	}
}

func TestRemoveHooksRunPostInProjectRoot(t *testing.T) {
	f := newWorkspaceFixture(t)
	runner := &recordingRunner{}
	f.app.Runner = runner
	f.writeConfig(t, hostHookConfig)
	trustProjectHooks(t, f)
	if code := f.run(t, "workspace", "create", "agent-1"); code != 0 {
		t.Fatalf("create failed: %s", f.errOut.String())
	}
	wsPath := filepath.Join(f.workspaceHostRoot(), "agent-1")
	if code := f.run(t, "workspace", "remove", "agent-1"); code != 0 {
		t.Fatalf("remove exit %d: %s", code, f.errOut.String())
	}
	pre, ok := runner.find("remove-pre")
	if !ok || pre.Dir != wsPath {
		t.Fatalf("remove pre hook = %#v", pre)
	}
	post, ok := runner.find("remove-post")
	if !ok || post.Dir != f.project.Root {
		t.Fatalf("remove post hook = %#v", post)
	}
	if _, err := os.Stat(wsPath); !os.IsNotExist(err) {
		t.Fatal("workspace still exists")
	}
}

func TestRemovePreHookFailureAborts(t *testing.T) {
	f := newWorkspaceFixture(t)
	runner := &recordingRunner{fail: map[string]error{"remove-pre": errors.New("no")}}
	f.app.Runner = runner
	f.writeConfig(t, hostHookConfig)
	trustProjectHooks(t, f)
	if code := f.run(t, "workspace", "create", "agent-1"); code != 0 {
		t.Fatalf("create failed")
	}
	if code := f.run(t, "workspace", "remove", "agent-1"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "aborted by pre-hook") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
	if _, err := os.Stat(filepath.Join(f.workspaceHostRoot(), "agent-1")); err != nil {
		t.Fatal("pre-hook failure removed the workspace")
	}
}

func TestRemovePostHookFailureKeepsRemoval(t *testing.T) {
	f := newWorkspaceFixture(t)
	runner := &recordingRunner{fail: map[string]error{"remove-post": errors.New("post")}}
	f.app.Runner = runner
	f.writeConfig(t, hostHookConfig)
	trustProjectHooks(t, f)
	if code := f.run(t, "workspace", "create", "agent-1"); code != 0 {
		t.Fatalf("create failed")
	}
	if code := f.run(t, "workspace", "remove", "agent-1"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "operation completed") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
	if _, err := os.Stat(filepath.Join(f.workspaceHostRoot(), "agent-1")); !os.IsNotExist(err) {
		t.Fatal("post-hook failure undid the removal")
	}
}

func TestUntrustedRemoveFailsBeforeMutation(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "create", "agent-1"); code != 0 {
		t.Fatalf("create failed")
	}
	f.writeConfig(t, hostHookConfig)
	if code := f.run(t, "workspace", "remove", "agent-1"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "workspace_remove hooks are not trusted") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
	if _, err := os.Stat(filepath.Join(f.workspaceHostRoot(), "agent-1")); err != nil {
		t.Fatal("untrusted hooks removed the workspace")
	}
}

func TestMergeHooks(t *testing.T) {
	f := newWorkspaceFixture(t)
	runner := &recordingRunner{}
	f.app.Runner = runner
	f.writeConfig(t, hostHookConfig)
	trustProjectHooks(t, f)
	gitRunInTest(t, f.project.Root, "add", ".boxt/config.yaml")
	gitRunInTest(t, f.project.Root, "commit", "-q", "-m", "hooks config")
	if code := f.run(t, "workspace", "create", "agent-1"); code != 0 {
		t.Fatalf("create failed")
	}
	wsPath := filepath.Join(f.workspaceHostRoot(), "agent-1")
	if err := os.WriteFile(filepath.Join(wsPath, "feature.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunInTest(t, wsPath, "add", "feature.txt")
	gitRunInTest(t, wsPath, "commit", "-q", "-m", "feature")
	if code := f.run(t, "workspace", "merge", "agent-1"); code != 0 {
		t.Fatalf("merge exit %d: %s", code, f.errOut.String())
	}
	pre, ok := runner.find("merge-pre")
	if !ok || pre.Dir != wsPath {
		t.Fatalf("merge pre hook = %#v", pre)
	}
	post, ok := runner.find("merge-post")
	if !ok || post.Dir != wsPath {
		t.Fatalf("merge post hook = %#v", post)
	}
	if _, err := os.Stat(filepath.Join(f.project.Root, "feature.txt")); err != nil {
		t.Fatal("merge did not happen")
	}
}

func TestMergePreHookFailureAbortsBeforeRefChange(t *testing.T) {
	f := newWorkspaceFixture(t)
	runner := &recordingRunner{fail: map[string]error{"merge-pre": errors.New("no")}}
	f.app.Runner = runner
	f.writeConfig(t, hostHookConfig)
	trustProjectHooks(t, f)
	if code := f.run(t, "workspace", "create", "agent-1"); code != 0 {
		t.Fatalf("create failed")
	}
	wsPath := filepath.Join(f.workspaceHostRoot(), "agent-1")
	if err := os.WriteFile(filepath.Join(wsPath, "feature.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunInTest(t, wsPath, "add", "feature.txt")
	gitRunInTest(t, wsPath, "commit", "-q", "-m", "feature")
	before, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: []string{"-C", f.project.Root, "rev-parse", "HEAD"}})
	if err != nil {
		t.Fatal(err)
	}
	if code := f.run(t, "workspace", "merge", "agent-1"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	after, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: []string{"-C", f.project.Root, "rev-parse", "HEAD"}})
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("merge changed refs despite pre-hook failure")
	}
	if _, err := os.Stat(filepath.Join(f.project.Root, "feature.txt")); !os.IsNotExist(err) {
		t.Fatal("merge changed the working tree despite pre-hook failure")
	}
}

func TestMergePostHookFailureKeepsMerge(t *testing.T) {
	f := newWorkspaceFixture(t)
	runner := &recordingRunner{fail: map[string]error{"merge-post": errors.New("post")}}
	f.app.Runner = runner
	f.writeConfig(t, hostHookConfig)
	trustProjectHooks(t, f)
	gitRunInTest(t, f.project.Root, "add", ".boxt/config.yaml")
	gitRunInTest(t, f.project.Root, "commit", "-q", "-m", "hooks config")
	if code := f.run(t, "workspace", "create", "agent-1"); code != 0 {
		t.Fatalf("create failed")
	}
	wsPath := filepath.Join(f.workspaceHostRoot(), "agent-1")
	if err := os.WriteFile(filepath.Join(wsPath, "feature.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunInTest(t, wsPath, "add", "feature.txt")
	gitRunInTest(t, wsPath, "commit", "-q", "-m", "feature")
	if code := f.run(t, "workspace", "merge", "agent-1"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "operation completed") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
	if _, err := os.Stat(filepath.Join(f.project.Root, "feature.txt")); err != nil {
		t.Fatal("post-hook failure undid the merge")
	}
}

func TestMergeRebasePreHookSeesRebasedTree(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.writeConfig(t, `version: 1
hooks:
  workspace_merge:
    pre:
      - command: ["test", "-f", "a.txt"]
        where: host
`)
	trustProjectHooks(t, f)
	gitRunInTest(t, f.project.Root, "add", ".boxt/config.yaml")
	gitRunInTest(t, f.project.Root, "commit", "-q", "-m", "hooks config")
	parallelWorkspaces(t, f)
	if code := f.run(t, "workspace", "merge", "feature-b", "--rebase"); code != 0 {
		t.Fatalf("merge exit %d: %s", code, f.errOut.String())
	}
	if _, err := os.Stat(filepath.Join(f.project.Root, "b.txt")); err != nil {
		t.Fatalf("b.txt not merged: %v", err)
	}
}

func TestMergeRebasePreHookFailureKeepsTargetButRebasesWorkspace(t *testing.T) {
	f := newWorkspaceFixture(t)
	for _, name := range []string{"feature-a", "feature-b"} {
		if code := f.run(t, "workspace", "create", name); code != 0 {
			t.Fatalf("create %s failed: %s", name, f.errOut.String())
		}
	}
	a := filepath.Join(f.workspaceHostRoot(), "feature-a")
	b := filepath.Join(f.workspaceHostRoot(), "feature-b")
	writeAndCommit(t, a, "a.txt", "a\n", "feature a")
	writeAndCommit(t, b, "b.txt", "b\n", "feature b")
	if code := f.run(t, "workspace", "merge", "feature-a"); code != 0 {
		t.Fatalf("merge feature-a exit %d: %s", code, f.errOut.String())
	}
	f.out.Reset()
	f.errOut.Reset()

	f.writeConfig(t, `version: 1
hooks:
  workspace_merge:
    pre:
      - command: ["sh", "-c", "exit 1"]
        where: host
`)
	trustProjectHooks(t, f)
	gitRunInTest(t, f.project.Root, "add", ".boxt/config.yaml")
	gitRunInTest(t, f.project.Root, "commit", "-q", "-m", "failing hook")
	rootBefore, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: []string{"-C", f.project.Root, "rev-parse", "HEAD"}})
	if err != nil {
		t.Fatal(err)
	}
	bBefore, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: []string{"-C", b, "rev-parse", "HEAD"}})
	if err != nil {
		t.Fatal(err)
	}

	if code := f.run(t, "workspace", "merge", "feature-b", "--rebase"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	rootAfter, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: []string{"-C", f.project.Root, "rev-parse", "HEAD"}})
	if err != nil {
		t.Fatal(err)
	}
	if rootAfter != rootBefore {
		t.Fatal("pre-hook failure changed the target")
	}
	bAfter, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: []string{"-C", b, "rev-parse", "HEAD"}})
	if err != nil {
		t.Fatal(err)
	}
	if bAfter == bBefore {
		t.Fatal("pre-hook ran before the rebase")
	}
	if _, err := os.Stat(filepath.Join(f.project.Root, "b.txt")); !os.IsNotExist(err) {
		t.Fatal("pre-hook failure merged files into the project root")
	}
}
