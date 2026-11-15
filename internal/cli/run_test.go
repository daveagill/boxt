package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/project"
	"github.com/daveagill/boxt/internal/toolchain"
)

func (f *lifecycleFixture) writeConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(f.project.ConfigPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *lifecycleFixture) shellCommand(t *testing.T) command.Command {
	t.Helper()
	for i := len(f.runner.commands) - 1; i >= 0; i-- {
		c := f.runner.commands[i]
		if len(c.Args) > 0 && c.Args[0] == "shell" {
			return c
		}
	}
	t.Fatalf("no shell command recorded; commands: %#v", f.runner.commands)
	return command.Command{}
}

func (f *lifecycleFixture) countCommands(name string) int {
	n := 0
	for _, c := range f.runner.commands {
		if len(c.Args) > 0 && c.Args[0] == name {
			n++
		}
	}
	return n
}

const runConfig = `version: 1
run:
  default: dev
  targets:
    dev:
      command: ["npm", "run", "dev"]
    test:
      command: ["npm", "test"]
    python:
      command: ["python"]
      interactive: true
`

const runConfigNoAutoStart = `version: 1
vm:
  auto_start: false
run:
  default: dev
  targets:
    dev:
      command: ["npm", "run", "dev"]
    test:
      command: ["npm", "test"]
`

func TestRunDefaultTarget(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "run"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	c := f.shellCommand(t)
	if !containsArg(c.Args, "--workdir") || !containsArg(c.Args, "/workspace") {
		t.Fatalf("missing workdir: %#v", c.Args)
	}
	if !containsArg(c.Args, f.project.VMName()) {
		t.Fatalf("missing instance: %#v", c.Args)
	}
	wantTail := []string{"--", "npm", "run", "dev"}
	if !endsWith(c.Args, wantTail) {
		t.Fatalf("args = %#v, want tail %#v", c.Args, wantTail)
	}
	if f.countCommands("start") != 0 {
		t.Fatal("started a running VM")
	}
}

func TestRootCommandRunsDefaultTarget(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !endsWith(f.shellCommand(t).Args, []string{"--", "npm", "run", "dev"}) {
		t.Fatalf("root did not run default target: %#v", f.runner.commands)
	}
}

func TestRootCommandRunsNamedTarget(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "test"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !endsWith(f.shellCommand(t).Args, []string{"--", "npm", "test"}) {
		t.Fatalf("root did not run named target: %#v", f.runner.commands)
	}
}

func TestRootCommandArbitraryAfterDash(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "--", "echo", "hi"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !endsWith(f.shellCommand(t).Args, []string{"--", "echo", "hi"}) {
		t.Fatalf("root did not run arbitrary command: %#v", f.runner.commands)
	}
}

func TestRunNamedTarget(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "run", "test"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !endsWith(f.shellCommand(t).Args, []string{"--", "npm", "test"}) {
		t.Fatalf("unexpected args: %#v", f.shellCommand(t).Args)
	}
}

func TestRunArbitraryCommand(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "run", "--", "python", "script.py"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !endsWith(f.shellCommand(t).Args, []string{"--", "python", "script.py"}) {
		t.Fatalf("unexpected args: %#v", f.shellCommand(t).Args)
	}
}

func TestRunUnknownTarget(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "run", "codex"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	msg := f.errOut.String()
	for _, want := range []string{"unknown run target: codex", "Define it under run.targets", "boxt run -- codex"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
	}
	if f.countCommands("shell") != 0 {
		t.Fatal("unknown target reached the guest")
	}
}

func TestRunNoDefaultTarget(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, `version: 1
run:
  targets:
    test:
      command: ["npm", "test"]
`)
	if code := f.run(t, "run"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "no default run target") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
	if f.countCommands("shell") != 0 {
		t.Fatal("guest command ran without a target")
	}
}

func TestRunUnexpectedArguments(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "run", "dev", "extra"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "unexpected arguments") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
	if f.countCommands("shell") != 0 {
		t.Fatal("unexpected arguments reached the guest")
	}
}

func TestRunEmptyAfterDash(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	if code := f.run(t, "run", "--"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "no command specified after") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestRunAutoStartsStoppedVM(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Stopped"
	if code := f.run(t, "run", "test"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if f.countCommands("start") != 1 {
		t.Fatalf("expected auto-start, commands: %#v", f.runner.commands)
	}
	if f.countCommands("shell") != 1 {
		t.Fatal("command did not run after auto-start")
	}
}

func TestRunAutoStartProvisionsDeferredTools(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Stopped"
	called := false
	f.app.Provision = func(ctx context.Context, p *project.Project) (*toolchain.Report, error) {
		called = true
		return &toolchain.Report{MiseVersion: "2026.11.8"}, nil
	}
	if code := f.run(t, "run", "test"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !called {
		t.Fatal("auto-start did not provision the toolchain")
	}
	if !strings.Contains(f.errOut.String(), "Provisioning toolchain") {
		t.Fatalf("missing provisioning note: %s", f.errOut.String())
	}
}

func TestRunAutoStartProvisionFailure(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Stopped"
	f.app.Provision = func(ctx context.Context, p *project.Project) (*toolchain.Report, error) {
		return &toolchain.Report{Tools: []toolchain.ToolResult{{Name: "node", Declared: "24", Err: errors.New("offline")}}}, errors.New("offline")
	}
	if code := f.run(t, "run", "test"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if f.countCommands("shell") != 0 {
		t.Fatal("command ran after failed provisioning")
	}
	if !strings.Contains(f.errOut.String(), "provisioning failed") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestRunAutoStartRefusesConfigDrift(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("up exit %d: %s", code, f.errOut.String())
	}
	f.writeConfig(t, `version: 1
vm:
  image: ubuntu-24.04
  cpus: 2
  memory: 8GiB
run:
  default: dev
  targets:
    dev:
      command: ["npm", "run", "dev"]
`)
	f.runner.status = "Stopped"
	f.runner.commands = nil
	f.errOut.Reset()
	if code := f.run(t, "run", "dev"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if f.countCommands("start") != 0 || f.countCommands("shell") != 0 {
		t.Fatalf("drifted VM was started: %#v", f.runner.commands)
	}
	if !strings.Contains(f.errOut.String(), "different configuration") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestRunRefusesConfigDriftWhileRunning(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("up exit %d: %s", code, f.errOut.String())
	}
	f.writeConfig(t, `version: 1
vm:
  image: ubuntu-24.04
  cpus: 2
  memory: 8GiB
run:
  default: dev
  targets:
    dev:
      command: ["npm", "run", "dev"]
`)
	f.runner.status = "Running"
	f.runner.commands = nil
	f.errOut.Reset()
	if code := f.run(t, "run", "dev"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if f.countCommands("shell") != 0 {
		t.Fatalf("drifted VM ran a command: %#v", f.runner.commands)
	}
	if !strings.Contains(f.errOut.String(), "different configuration") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestRunNoStart(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Stopped"
	if code := f.run(t, "run", "--no-start", "test"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if f.countCommands("start") != 0 || f.countCommands("shell") != 0 {
		t.Fatalf("--no-start invoked Lima: %#v", f.runner.commands)
	}
	if !strings.Contains(f.errOut.String(), "is stopped") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestRunMissingVMCreatesVM(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	if code := f.run(t, "run", "test"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if f.countCommands("start") != 1 {
		t.Fatalf("expected VM creation, commands: %#v", f.runner.commands)
	}
	if f.countCommands("shell") != 1 {
		t.Fatal("command did not run after VM creation")
	}
	if !strings.Contains(f.out.String(), "Creating VM") {
		t.Fatalf("missing creation message: %s", f.out.String())
	}
}

func TestRunMissingVMNoStart(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	if code := f.run(t, "run", "--no-start", "test"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if f.countCommands("start") != 0 || f.countCommands("shell") != 0 {
		t.Fatalf("--no-start invoked Lima: %#v", f.runner.commands)
	}
	if !strings.Contains(f.errOut.String(), "does not exist") || !strings.Contains(f.errOut.String(), "boxt up") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestRunAutoStartDisabledInConfig(t *testing.T) {
	for _, status := range []string{"", "Stopped"} {
		t.Run(status, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.writeConfig(t, runConfigNoAutoStart)
			f.runner.status = status
			if code := f.run(t, "run", "test"); code != 1 {
				t.Fatalf("exit = %d, want 1", code)
			}
			if f.countCommands("start") != 0 || f.countCommands("shell") != 0 {
				t.Fatalf("auto_start false invoked Lima: %#v", f.runner.commands)
			}
			if !strings.Contains(f.errOut.String(), "boxt up") || !strings.Contains(f.errOut.String(), "vm.auto_start") {
				t.Fatalf("unexpected error: %s", f.errOut.String())
			}
		})
	}
}

func TestRunAfterDestroyCanCreateWithNewConfig(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("up exit %d: %s", code, f.errOut.String())
	}
	if code := f.run(t, "destroy", "--yes"); code != 0 {
		t.Fatalf("destroy exit %d: %s", code, f.errOut.String())
	}
	f.writeConfig(t, `version: 1
vm:
  cpus: 2
run:
  targets:
    test:
      command: ["npm", "test"]
`)
	f.runner.commands = nil
	if code := f.run(t, "run", "test"); code != 0 {
		t.Fatalf("run exit %d: %s", code, f.errOut.String())
	}
	if f.countCommands("start") != 1 {
		t.Fatalf("VM was not recreated after destroy: %#v", f.runner.commands)
	}
}

func TestShellMissingVMCreatesVM(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "shell"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if f.countCommands("start") != 1 || f.countCommands("shell") != 1 {
		t.Fatalf("shell did not auto-create VM: %#v", f.runner.commands)
	}
}

func TestRunInteractiveTargetWarnsWithoutTerminal(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	if code := f.run(t, "run", "python"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.errOut.String(), "will not receive a PTY") {
		t.Fatalf("missing PTY warning: %s", f.errOut.String())
	}
}

func TestRunPropagatesExitStatus(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, runConfig)
	f.runner.status = "Running"
	f.runner.shellErr = &command.Error{Name: "limactl", ExitCode: 7, Stderr: "tests failed"}
	if code := f.run(t, "run", "test"); code != 7 {
		t.Fatalf("exit = %d, want 7; stderr: %s", code, f.errOut.String())
	}
}

func TestShellDefaultWorkspace(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	if code := f.run(t, "shell"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	c := f.shellCommand(t)
	if !containsArg(c.Args, "/workspace") {
		t.Fatalf("shell missing workdir: %#v", c.Args)
	}
	if containsArg(c.Args, "--") {
		t.Fatalf("plain shell should not pass a command: %#v", c.Args)
	}
}

func TestShellUsesConfiguredTools(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	f.writeConfig(t, `version: 1
tools:
  node: "24"
run:
  targets: {}
`)
	called := false
	f.app.PrepareCommand = func(tools map[string]string, argv []string) []string {
		called = true
		if tools["node"] != "24" {
			t.Fatalf("tools = %#v", tools)
		}
		return append([]string{"mise", "exec", "node@24", "--"}, argv...)
	}
	if code := f.run(t, "shell"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !called {
		t.Fatal("shell did not use the toolchain environment")
	}
	if !endsWith(f.shellCommand(t).Args, []string{"--", "mise", "exec", "node@24", "--", "/bin/bash"}) {
		t.Fatalf("unexpected shell args: %#v", f.shellCommand(t).Args)
	}
}

func TestResolveRun(t *testing.T) {
	cfg := &config.Config{
		Version: 1,
		Run: config.RunConfig{
			Default: "dev",
			Targets: map[string]config.RunTarget{
				"dev":    {Command: []string{"npm", "run", "dev"}},
				"python": {Command: []string{"python"}, Interactive: true},
			},
		},
	}
	tests := []struct {
		name        string
		args        []string
		dash        int
		terminal    bool
		want        []string
		interactive bool
		wantErr     string
	}{
		{"default", nil, -1, true, []string{"npm", "run", "dev"}, false, ""},
		{"named", []string{"dev"}, -1, true, []string{"npm", "run", "dev"}, false, ""},
		{"interactive target", []string{"python"}, -1, false, []string{"python"}, true, ""},
		{"arbitrary terminal", []string{"python", "x.py"}, 0, true, []string{"python", "x.py"}, true, ""},
		{"arbitrary non-terminal", []string{"python", "x.py"}, 0, false, []string{"python", "x.py"}, false, ""},
		{"unknown", []string{"nope"}, -1, false, nil, false, "unknown run target"},
		{"too many", []string{"dev", "x"}, -1, false, nil, false, "unexpected arguments"},
		{"dash with target", []string{"dev", "x"}, 1, false, nil, false, "before `--`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := resolveRun(cfg, tt.args, tt.dash, tt.terminal, "boxt run")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !equalStrings(inv.Command, tt.want) || inv.Interactive != tt.interactive {
				t.Fatalf("got %#v / interactive=%v, want %#v / %v", inv.Command, inv.Interactive, tt.want, tt.interactive)
			}
		})
	}
}

func TestResolveRunCopiesCommand(t *testing.T) {
	cfg := &config.Config{Run: config.RunConfig{Targets: map[string]config.RunTarget{
		"dev": {Command: []string{"one"}},
	}}}
	inv, err := resolveRun(cfg, []string{"dev"}, -1, false, "boxt run")
	if err != nil {
		t.Fatal(err)
	}
	inv.Command[0] = "mutated"
	if cfg.Run.Targets["dev"].Command[0] != "one" {
		t.Fatal("resolved command aliases the configuration slice")
	}
}

func TestNoDefaultTargetError(t *testing.T) {
	_, err := resolveRun(&config.Config{}, nil, -1, false, "boxt run")
	if err == nil || !strings.Contains(err.Error(), "no default") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUnknownTargetWorkspaceHint(t *testing.T) {
	cfg := &config.Config{}
	_, err := resolveRun(cfg, []string{"x"}, -1, false, "boxt workspace run feature-a")
	hint := err.(interface{ Hint() string }).Hint()
	if !strings.Contains(hint, "boxt workspace run feature-a -- x") {
		t.Fatalf("unexpected hint: %s", hint)
	}
}

func TestNoDefaultTargetWorkspaceHint(t *testing.T) {
	cfg := &config.Config{}
	_, err := resolveRun(cfg, nil, -1, false, "boxt workspace run feature-a")
	hint := err.(interface{ Hint() string }).Hint()
	if !strings.Contains(hint, "boxt workspace run feature-a <target>") {
		t.Fatalf("unexpected hint: %s", hint)
	}
}

func TestUnexpectedArgumentsWorkspaceHint(t *testing.T) {
	cfg := &config.Config{Run: config.RunConfig{Targets: map[string]config.RunTarget{"dev": {Command: []string{"true"}}}}}
	_, err := resolveRun(cfg, []string{"dev", "extra"}, -1, false, "boxt workspace run feature-a")
	if err == nil || !strings.Contains(err.Error(), "boxt workspace run feature-a <target>") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunWithPrepare(t *testing.T) {
	f := newLifecycleFixture(t)
	f.writeConfig(t, `version: 1
tools:
  node: "24"
run:
  targets:
    dev:
      command: ["npm", "run", "dev"]
`)
	f.runner.status = "Running"
	f.app.PrepareCommand = func(tools map[string]string, argv []string) []string {
		return append([]string{"mise", "exec", "node@24", "--"}, argv...)
	}
	if code := f.run(t, "run", "dev"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !endsWith(f.shellCommand(t).Args, []string{"--", "mise", "exec", "node@24", "--", "npm", "run", "dev"}) {
		t.Fatalf("unexpected args: %#v", f.shellCommand(t).Args)
	}
}

func endsWith(args, tail []string) bool {
	if len(args) < len(tail) {
		return false
	}
	offset := len(args) - len(tail)
	for i := range tail {
		if args[offset+i] != tail[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
