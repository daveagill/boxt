package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellInitPrintsWrapper(t *testing.T) {
	cases := map[string][]string{
		"sh":   {"boxt()", `command boxt "$@"`, "workspace", "switch", "cd "},
		"bash": {"boxt()", `command boxt "$@"`, "workspace", "switch", "cd "},
		"zsh":  {"boxt()", `command boxt "$@"`, "workspace", "switch", "cd "},
		"fish": {"function boxt", "command boxt $argv", "workspace", "switch", "cd "},
	}
	for shell, want := range cases {
		t.Run(shell, func(t *testing.T) {
			app, out, errOut := testApp(t, t.TempDir())
			if code := Run(context.Background(), app, []string{"shell-init", shell}); code != 0 {
				t.Fatalf("exit %d: %s", code, errOut.String())
			}
			for _, w := range want {
				if !strings.Contains(out.String(), w) {
					t.Fatalf("%s script missing %q:\n%s", shell, w, out.String())
				}
			}
		})
	}
}

func TestShellInitUnsupportedShell(t *testing.T) {
	app, _, errOut := testApp(t, t.TempDir())
	if code := Run(context.Background(), app, []string{"shell-init", "powershell"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), `unsupported shell "powershell"`) {
		t.Fatalf("unexpected error: %s", errOut.String())
	}
	for _, shell := range []string{"sh", "bash", "zsh", "fish"} {
		if !strings.Contains(errOut.String(), shell) {
			t.Fatalf("error does not list %q: %s", shell, errOut.String())
		}
	}
}

func TestShellInitScriptSyntax(t *testing.T) {
	shScript, err := shellInitScript("zsh")
	if err != nil {
		t.Fatal(err)
	}
	fishScript, err := shellInitScript("fish")
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		shell  string
		flag   string
		script string
	}{
		{shell: "bash", flag: "-n", script: shScript},
		{shell: "zsh", flag: "-n", script: shScript},
		{shell: "fish", flag: "--no-execute", script: fishScript},
	}
	for _, c := range checks {
		t.Run(c.shell, func(t *testing.T) {
			bin, err := exec.LookPath(c.shell)
			if err != nil {
				t.Skipf("%s not installed", c.shell)
			}
			path := filepath.Join(t.TempDir(), "boxt-init")
			if err := os.WriteFile(path, []byte(c.script), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(bin, c.flag, path).CombinedOutput(); err != nil {
				t.Fatalf("%s %s failed: %v\n%s", c.shell, c.flag, err, out)
			}
		})
	}
}

// TestShellInitBashSwitch runs the emitted wrapper in a real bash and checks
// that switch changes the shell's directory, that a failed lookup leaves it
// untouched, and that every other invocation reaches the binary unchanged.
func TestShellInitBashSwitch(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	script, err := shellInitScript("bash")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	target := filepath.Join(root, "feature-a")
	start := filepath.Join(root, "start")
	binDir := filepath.Join(root, "bin")
	for _, dir := range []string{target, start, binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub := `#!/bin/sh
if [ "$1" = workspace ] && [ "$2" = path ]; then
    if [ "$4" = feature-a ]; then
        printf '%s\n' "$BOXT_TARGET"
        exit 0
    fi
    exit 1
fi
for arg in "$@"; do
    printf '<%s>\n' "$arg"
done
`
	if err := os.WriteFile(filepath.Join(binDir, "boxt"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, body string) string {
		t.Helper()
		cmd := exec.Command(bash, "-c", body)
		cmd.Dir = start
		cmd.Env = append(os.Environ(),
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"BOXT_INIT="+script,
			"BOXT_TARGET="+target,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bash failed: %v\n%s", err, out)
		}
		return string(out)
	}

	t.Run("switches directory", func(t *testing.T) {
		out := run(t, `eval "$BOXT_INIT"
boxt workspace switch feature-a || exit $?
pwd
`)
		if got := strings.TrimSpace(out); got != target {
			t.Fatalf("pwd = %q, want %q", got, target)
		}
	})

	t.Run("failed lookup keeps directory", func(t *testing.T) {
		out := run(t, `eval "$BOXT_INIT"
boxt workspace switch gone
printf 'status=%s\n' "$?"
pwd
`)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 2 || lines[0] != "status=1" || lines[1] != start {
			t.Fatalf("unexpected output:\n%s", out)
		}
	})

	t.Run("other calls pass through", func(t *testing.T) {
		out := run(t, `eval "$BOXT_INIT"
boxt a "b c"
`)
		want := "<a>\n<b c>\n"
		if out != want {
			t.Fatalf("output = %q, want %q", out, want)
		}
	})

	t.Run("help passes through", func(t *testing.T) {
		out := run(t, `eval "$BOXT_INIT"
boxt workspace switch --help
`)
		if out != "<workspace>\n<switch>\n<--help>\n" {
			t.Fatalf("output = %q", out)
		}
	})

	t.Run("nounset", func(t *testing.T) {
		out := run(t, `set -u
eval "$BOXT_INIT"
boxt
boxt a
`)
		if out != "<a>\n" {
			t.Fatalf("output = %q", out)
		}
	})

	t.Run("usage error", func(t *testing.T) {
		out := run(t, `eval "$BOXT_INIT"
boxt workspace switch
printf 'status=%s\n' "$?"
`)
		if !strings.Contains(out, "usage: boxt workspace switch <name>") || !strings.Contains(out, "status=2") {
			t.Fatalf("unexpected output:\n%s", out)
		}
	})
}
