package hooks

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		Version: 1,
		Hooks: &config.HooksConfig{
			WorkspaceCreate: config.HookSet{
				Pre:  []config.Hook{{Command: []string{"npm", "install"}}},
				Post: []config.Hook{{Command: []string{"git", "status"}, Where: config.HookWhereHost}},
			},
		},
	}
}

func TestHashNormAndChanges(t *testing.T) {
	a := testConfig()
	b := testConfig()
	b.Hooks.WorkspaceCreate.Pre[0].Where = config.HookWhereGuest
	ha, err := Hash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := Hash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("default and explicit guest hashes differ: %s != %s", ha, hb)
	}
	b.Hooks.WorkspaceCreate.Pre[0].Command = []string{"npm", "ci"}
	hc, err := Hash(b)
	if err != nil {
		t.Fatal(err)
	}
	if hc == ha {
		t.Fatal("hook change did not change the hash")
	}
}

func TestEnsureTrusted(t *testing.T) {
	cfg := testConfig()
	data := t.TempDir()
	hash, err := Hash(cfg)
	if err != nil {
		t.Fatal(err)
	}
	err = EnsureTrusted(data, "project", cfg, config.HookWorkspaceCreate)
	var untrusted *ErrUntrusted
	if !errors.As(err, &untrusted) {
		t.Fatalf("expected ErrUntrusted, got %v", err)
	}
	if !strings.Contains(untrusted.Error(), "npm install") || !strings.Contains(untrusted.Hint(), "boxt hooks trust") {
		t.Fatalf("untrusted error = %v / %s", err, untrusted.Hint())
	}
	if err := Trust(data, "project", hash); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTrusted(data, "project", cfg, config.HookWorkspaceCreate); err != nil {
		t.Fatalf("trusted hooks rejected: %v", err)
	}
	cfg.Hooks.WorkspaceCreate.Pre[0].Command = []string{"rm", "-rf", "/"}
	if err := EnsureTrusted(data, "project", cfg, config.HookWorkspaceCreate); err == nil {
		t.Fatal("changed hooks remained trusted")
	}
	if err := Clear(data, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(TrustPath(data, "project")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("approval still exists: %v", err)
	}
}

func TestEnsureTrustedIgnoresUnrelatedEvents(t *testing.T) {
	cfg := testConfig()
	data := t.TempDir()
	if err := EnsureTrusted(data, "project", cfg, config.HookWorkspaceMerge); err != nil {
		t.Fatalf("unconfigured event required trust: %v", err)
	}
}

type captureRunner struct {
	commands []struct {
		Name, Dir string
		Args, Env []string
	}
	failName string
}

func (c *captureRunner) Run(ctx context.Context, cmd command.Command) error {
	c.commands = append(c.commands, struct {
		Name, Dir string
		Args, Env []string
	}{cmd.Name, cmd.Dir, cmd.Args, cmd.Env})
	if c.failName != "" && cmd.Name == c.failName {
		return errors.New("boom")
	}
	return nil
}

func TestRunHostAndGuest(t *testing.T) {
	runner := &captureRunner{}
	var guest []struct {
		dir string
		arg []string
		env []string
	}
	opts := Options{
		Env:        Env{ProjectRoot: "/project", Workspace: "agent", WorkspacePath: "/ws/agent", GuestPath: "/workspaces/p/agent"},
		HostDir:    "/ws/agent",
		GuestDir:   "/workspaces/p/agent",
		HostRunner: runner,
		GuestRunner: func(ctx context.Context, dir string, argv []string, env []string) error {
			guest = append(guest, struct {
				dir string
				arg []string
				env []string
			}{dir, argv, env})
			return nil
		},
	}
	list := []config.Hook{
		{Command: []string{"npm", "install"}},
		{Command: []string{"echo", "host"}, Where: config.HookWhereHost},
	}
	if err := Run(context.Background(), list, opts); err != nil {
		t.Fatal(err)
	}
	if len(guest) != 1 || guest[0].dir != "/workspaces/p/agent" || strings.Join(guest[0].arg, " ") != "npm install" {
		t.Fatalf("guest hook = %#v", guest)
	}
	if len(runner.commands) != 1 || runner.commands[0].Name != "echo" || runner.commands[0].Dir != "/ws/agent" {
		t.Fatalf("host hook = %#v", runner.commands)
	}
	env := strings.Join(runner.commands[0].Env, "\n")
	for _, want := range []string{"BOXT_PROJECT_ROOT=/project", "BOXT_WORKSPACE=agent", "BOXT_WORKSPACE_PATH=/ws/agent", "BOXT_GUEST_PATH=/workspaces/p/agent"} {
		if !strings.Contains(env, want) {
			t.Fatalf("host env missing %q:\n%s", want, env)
		}
	}
	guestEnv := strings.Join(guest[0].env, "\n")
	if !strings.Contains(guestEnv, "BOXT_WORKSPACE=agent") {
		t.Fatalf("guest env = %#v", guest[0].env)
	}
}

func TestRunStopsOnFailure(t *testing.T) {
	runner := &captureRunner{failName: "false"}
	opts := Options{HostRunner: runner, HostDir: "/tmp"}
	list := []config.Hook{
		{Command: []string{"false"}, Where: config.HookWhereHost},
		{Command: []string{"echo", "after"}, Where: config.HookWhereHost},
	}
	err := Run(context.Background(), list, opts)
	if err == nil || !strings.Contains(err.Error(), "hook \"false\"") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("hooks continued after failure: %#v", runner.commands)
	}
}

func TestRunTimeout(t *testing.T) {
	opts := Options{
		Timeout: time.Nanosecond,
		GuestRunner: func(ctx context.Context, workDir string, argv []string, env []string) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	err := Run(context.Background(), []config.Hook{{Command: []string{"sleep", "forever"}}}, opts)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCommands(t *testing.T) {
	set := testConfig().HooksFor(config.HookWorkspaceCreate)
	got := Commands(set)
	if len(got) != 2 || got[0] != "npm install" || got[1] != "git status" {
		t.Fatalf("commands = %#v", got)
	}
}
