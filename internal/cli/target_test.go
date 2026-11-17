package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/project"
)

func loadFixtureConfig(t *testing.T, dir string) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join(dir, project.ConfigRelPath))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestTargetAddListRemove(t *testing.T) {
	app, dir, out, errOut := newConfigFixture(t)

	if code := Run(context.Background(), app, []string{"target", "add", "dev", "--set-default", "--", "npm", "run", "dev"}); code != 0 {
		t.Fatalf("add exit %d: %s", code, errOut.String())
	}
	cfg := loadFixtureConfig(t, dir)
	if cfg.Run.Default != "dev" {
		t.Fatalf("default = %q", cfg.Run.Default)
	}
	if got := strings.Join(cfg.Run.Targets["dev"].Command, " "); got != "npm run dev" {
		t.Fatalf("command = %q", got)
	}

	out.Reset()
	if code := Run(context.Background(), app, []string{"target", "list"}); code != 0 {
		t.Fatalf("list exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"NAME", "DEFAULT", "dev", "*", "npm run dev"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list missing %q:\n%s", want, out.String())
		}
	}

	errOut.Reset()
	if code := Run(context.Background(), app, []string{"target", "add", "dev", "--", "other"}); code != 1 {
		t.Fatalf("conflicting add exit = %d", code)
	}
	if !strings.Contains(errOut.String(), "already exists") {
		t.Fatalf("unexpected error:\n%s", errOut.String())
	}

	out.Reset()
	if code := Run(context.Background(), app, []string{"target", "remove", "dev"}); code != 0 {
		t.Fatalf("remove exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Cleared run.default") {
		t.Fatalf("default clearing missing:\n%s", out.String())
	}
	cfg = loadFixtureConfig(t, dir)
	if cfg.Run.Default != "" || len(cfg.Run.Targets) != 0 {
		t.Fatalf("config after remove = %#v", cfg.Run)
	}
}

func TestTargetAddPortFlags(t *testing.T) {
	app, dir, _, errOut := newConfigFixture(t)
	if code := Run(context.Background(), app, []string{"target", "add", "dev", "--port-base", "3000", "--port-env", "APP_PORT", "--", "serve"}); code != 0 {
		t.Fatalf("add exit %d: %s", code, errOut.String())
	}
	cfg := loadFixtureConfig(t, dir)
	target := cfg.Run.Targets["dev"]
	if target.PortBase != 3000 || target.PortEnv != "APP_PORT" {
		t.Fatalf("target = %#v", target)
	}

	errOut.Reset()
	if code := Run(context.Background(), app, []string{"target", "add", "bad", "--port-env", "PORT", "--", "serve"}); code != 1 {
		t.Fatalf("expected failure, exit = %d", code)
	}
	if !strings.Contains(errOut.String(), "requires --port-base") {
		t.Fatalf("unexpected error:\n%s", errOut.String())
	}
}

func TestTargetAddRequiresDash(t *testing.T) {
	app, _, _, errOut := newConfigFixture(t)
	if code := Run(context.Background(), app, []string{"target", "add", "dev", "npm"}); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(errOut.String(), "expected: boxt target add") {
		t.Fatalf("unexpected error:\n%s", errOut.String())
	}
}

func TestTargetDefaultCommands(t *testing.T) {
	app, dir, out, errOut := newConfigFixture(t)
	if code := Run(context.Background(), app, []string{"target", "add", "dev", "--", "npm", "run", "dev"}); code != 0 {
		t.Fatalf("add exit %d: %s", code, errOut.String())
	}
	if code := Run(context.Background(), app, []string{"target", "set-default", "dev"}); code != 0 {
		t.Fatalf("set-default exit %d: %s", code, errOut.String())
	}
	if cfg := loadFixtureConfig(t, dir); cfg.Run.Default != "dev" {
		t.Fatalf("default = %q", cfg.Run.Default)
	}
	out.Reset()
	if code := Run(context.Background(), app, []string{"target", "unset-default"}); code != 0 {
		t.Fatalf("unset-default exit %d: %s", code, errOut.String())
	}
	if cfg := loadFixtureConfig(t, dir); cfg.Run.Default != "" {
		t.Fatalf("default = %q", cfg.Run.Default)
	}

	errOut.Reset()
	if code := Run(context.Background(), app, []string{"target", "set-default", "missing"}); code != 1 {
		t.Fatalf("missing target exit = %d", code)
	}
	if !strings.Contains(errOut.String(), "not configured") {
		t.Fatalf("unexpected error:\n%s", errOut.String())
	}
}
