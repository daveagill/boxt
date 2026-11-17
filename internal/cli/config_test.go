package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/project"
)

func newConfigFixture(t *testing.T) (*App, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	if _, err := project.Init(dir); err != nil {
		t.Fatal(err)
	}
	app, out, errOut := testApp(t, dir)
	app.DataDir = func() (string, error) { return t.TempDir(), nil }
	return app, dir, out, errOut
}

func TestConfigGetAndSet(t *testing.T) {
	app, _, out, errOut := newConfigFixture(t)

	if code := Run(context.Background(), app, []string{"config", "get", "version"}); code != 0 {
		t.Fatalf("get exit %d: %s", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != "1" {
		t.Fatalf("version = %q", out.String())
	}

	out.Reset()
	if code := Run(context.Background(), app, []string{"config", "set", "vm.cpus", "8"}); code != 0 {
		t.Fatalf("set exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "recreate") {
		t.Fatalf("fingerprint hint missing:\n%s", out.String())
	}

	out.Reset()
	if code := Run(context.Background(), app, []string{"config", "get", "vm.cpus"}); code != 0 {
		t.Fatalf("get exit %d: %s", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != "8" {
		t.Fatalf("cpus = %q", out.String())
	}

	out.Reset()
	if code := Run(context.Background(), app, []string{"config", "set", "workspace.merge", "rebase"}); code != 0 {
		t.Fatalf("set exit %d: %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "recreate") {
		t.Fatalf("execution policy printed a recreate hint:\n%s", out.String())
	}
}

func TestConfigSeedLists(t *testing.T) {
	app, _, out, errOut := newConfigFixture(t)
	for _, value := range []string{".env", "node_modules/**"} {
		if code := Run(context.Background(), app, []string{"config", "set", "seed.include", value, "--append"}); code != 0 {
			t.Fatalf("append exit %d: %s", code, errOut.String())
		}
	}
	out.Reset()
	if code := Run(context.Background(), app, []string{"config", "get", "seed.include"}); code != 0 {
		t.Fatalf("get exit %d: %s", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != ".env\nnode_modules/**" {
		t.Fatalf("include = %q", got)
	}
	if code := Run(context.Background(), app, []string{"config", "unset", "seed.include", ".env"}); code != 0 {
		t.Fatalf("unset exit %d: %s", code, errOut.String())
	}
	out.Reset()
	if code := Run(context.Background(), app, []string{"config", "get", "seed.include"}); code != 0 {
		t.Fatalf("get exit %d: %s", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != "node_modules/**" {
		t.Fatalf("include after removal = %q", out.String())
	}
}

func TestConfigShowAndValidate(t *testing.T) {
	app, dir, out, errOut := newConfigFixture(t)
	if code := Run(context.Background(), app, []string{"config", "show"}); code != 0 {
		t.Fatalf("show exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "version: 1") || !strings.Contains(out.String(), "vm:") {
		t.Fatalf("show output:\n%s", out.String())
	}
	out.Reset()
	if code := Run(context.Background(), app, []string{"config", "validate"}); code != 0 {
		t.Fatalf("validate exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Configuration is valid") {
		t.Fatalf("validate output:\n%s", out.String())
	}

	cfgPath := filepath.Join(dir, project.ConfigRelPath)
	if err := os.WriteFile(cfgPath, []byte("version: 1\nvm:\n  cpus: 65\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := Run(context.Background(), app, []string{"config", "validate"}); code != 1 {
		t.Fatalf("invalid validate exit = %d", code)
	}
	if !strings.Contains(errOut.String(), "vm.cpus") {
		t.Fatalf("field path missing:\n%s", errOut.String())
	}
}

func TestConfigOwnedAndReadOnlyKeys(t *testing.T) {
	app, _, out, errOut := newConfigFixture(t)
	errOut.Reset()
	if code := Run(context.Background(), app, []string{"config", "set", "tools", "x"}); code != 1 {
		t.Fatalf("tools set exit = %d", code)
	}
	if !strings.Contains(errOut.String(), "boxt tool") {
		t.Fatalf("owner hint missing:\n%s", errOut.String())
	}
	out.Reset()
	if code := Run(context.Background(), app, []string{"config", "get", "vm.image"}); code != 0 {
		t.Fatalf("image get exit = %d: %s", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != "ubuntu-24.04" {
		t.Fatalf("image = %q", out.String())
	}
}
