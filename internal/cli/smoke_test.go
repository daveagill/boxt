package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/platform"
	"github.com/daveagill/boxt/internal/project"
)

func TestEndToEndWithFakeLimactl(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	projectDir := filepath.Join(base, "project")
	fakeDir := filepath.Join(base, "bin")
	stateDir := filepath.Join(base, "state")
	home := filepath.Join(base, "home")
	dataDir := filepath.Join(base, "data")
	for _, dir := range []string{fakeDir, stateDir, home, dataDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stateFile := filepath.Join(stateDir, "state")
	script := `#!/bin/sh
state="` + stateFile + `"
cmd="$1"
shift
case "$cmd" in
  list)
    if [ -f "$state" ]; then
      printf '{"name":"%s","status":"%s","vmType":"qemu","arch":"aarch64","cpus":4,"memory":8589934592}\n' "$FAKE_NAME" "$(cat "$state")"
    fi
    ;;
  start)
    printf 'Running' > "$state"
    ;;
  stop)
    printf 'Stopped' > "$state"
    ;;
  delete)
    rm -f "$state"
    ;;
  shell)
    case "$*" in
      *"mise --version"*) echo "mise 2026.11.8 linux-arm64";;
      *"mise ls --json"*) echo '{"node":[{"version":"24.22.3","requested_version":"24","installed":true,"install_path":"/home/boxt/.local/share/mise/installs/node/24.22.3"}]}';;
      *"sh -c"*) exit 0;;
      *"mise install"*) exit 0;;
      *"mise exec"*) exit 0;;
      *) echo "FAKE_SHELL $*";;
    esac
    ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(fakeDir, "limactl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", dataDir)

	app := New()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	app.In = strings.NewReader("")
	app.Out = out
	app.Err = errOut
	app.Getwd = func() (string, error) { return projectDir, nil }
	run := func(args ...string) int {
		t.Helper()
		out.Reset()
		errOut.Reset()
		return Run(context.Background(), app, args)
	}

	if code := run("init"); code != 0 {
		t.Fatalf("init exit %d: %s", code, errOut.String())
	}
	p, err := project.Open(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_NAME", p.VMName())

	if code := run("up"); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"Creating VM", "Provisioning toolchain", "Environment ready"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("up output missing %q:\n%s", want, out.String())
		}
	}

	if code := run("tool", "add", "node@24"); code != 0 {
		t.Fatalf("tool add exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Installed node@24") {
		t.Fatalf("tool add output:\n%s", out.String())
	}
	if code := run("tool", "list"); code != 0 {
		t.Fatalf("tool list exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"node", "24.22.3", "installed"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("tool list missing %q:\n%s", want, out.String())
		}
	}

	cfg, err := config.Load(p.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Run.Default = "dev"
	cfg.Run.Targets["dev"] = config.RunTarget{Command: []string{"npm", "run", "dev"}}
	data, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run("run", "dev"); code != 0 {
		t.Fatalf("run exit %d: %s", code, errOut.String())
	}

	gitRunCLI(t, projectDir, "init", "-q")
	gitRunCLI(t, projectDir, "config", "user.email", "test@example.com")
	gitRunCLI(t, projectDir, "config", "user.name", "Test")
	gitRunCLI(t, projectDir, "add", "-A")
	gitRunCLI(t, projectDir, "commit", "-q", "-m", "initial")
	if code := run("workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("workspace create exit %d: %s", code, errOut.String())
	}
	if code := run("workspace", "run", "feature-a", "dev"); code != 0 {
		t.Fatalf("workspace run exit %d: %s", code, errOut.String())
	}
	if code := run("status"); code != 0 {
		t.Fatalf("status exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"feature-a", "boxt/feature-a", "ready"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status missing %q:\n%s", want, out.String())
		}
	}

	if code := run("down"); code != 0 {
		t.Fatalf("down exit %d: %s", code, errOut.String())
	}
	if code := run("destroy", "--yes"); code != 0 {
		t.Fatalf("destroy exit %d: %s", code, errOut.String())
	}
	actualDataDir, err := platform.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	workspacePath := filepath.Join(actualDataDir, "workspaces", p.ID, "feature-a")
	if resolved, err := filepath.EvalSymlinks(workspacePath); err == nil {
		workspacePath = resolved
	}
	if _, err := os.Stat(workspacePath); err != nil {
		t.Fatalf("destroy affected workspace: %v", err)
	}
}

func gitRunCLI(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	if out, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: full}); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}
