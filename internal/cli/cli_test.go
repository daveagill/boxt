package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/git"
	"github.com/daveagill/boxt/internal/preset"
	"github.com/daveagill/boxt/internal/project"
)

func testApp(t *testing.T, cwd string) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	runner := command.ExecRunner{}
	app := &App{
		In:       strings.NewReader(""),
		Out:      out,
		Err:      errOut,
		Getwd:    func() (string, error) { return cwd, nil },
		Runner:   runner,
		Git:      git.New(runner),
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return app, out, errOut
}

func TestInitCommand(t *testing.T) {
	dir := t.TempDir()
	app, out, errOut := testApp(t, dir)
	code := Run(context.Background(), app, []string{"init"})
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Initialized boxt project") {
		t.Fatalf("unexpected output: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, project.ConfigRelPath)); err != nil {
		t.Fatalf("config not created: %v", err)
	}
	if !strings.Contains(out.String(), "not a Git repository") {
		t.Fatalf("expected non-Git note, got: %s", out.String())
	}
	if !strings.Contains(out.String(), "boxt up") {
		t.Fatalf("expected next-step hint, got: %s", out.String())
	}
}

func TestInitCommandExplicitPath(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	app, _, errOut := testApp(t, base)
	code := Run(context.Background(), app, []string{"init", target})
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(target, project.ConfigRelPath)); err != nil {
		t.Fatalf("config not created: %v", err)
	}
}

func TestInitCommandDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	app, _, _ := testApp(t, dir)
	if code := Run(context.Background(), app, []string{"init"}); code != 0 {
		t.Fatalf("first init failed")
	}
	cfgPath := filepath.Join(dir, project.ConfigRelPath)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	app2, _, errOut2 := testApp(t, dir)
	if code := Run(context.Background(), app2, []string{"init"}); code != 1 {
		t.Fatalf("second init exit = %d, want 1", code)
	}
	if !strings.Contains(errOut2.String(), "already initialized") {
		t.Fatalf("unexpected error: %s", errOut2.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("configuration was overwritten")
	}
}

func TestInitCommandGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if _, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: []string{"-C", dir, "init", "-q"}}); err != nil {
		t.Fatal(err)
	}
	app, out, errOut := testApp(t, dir)
	if code := Run(context.Background(), app, []string{"init"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "not a Git repository") {
		t.Fatalf("incorrect non-Git note: %s", out.String())
	}
}

func TestInitCommandGitMissing(t *testing.T) {
	dir := t.TempDir()
	app, out, _ := testApp(t, dir)
	app.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if code := Run(context.Background(), app, []string{"init"}); code != 0 {
		t.Fatalf("init with missing Git should still succeed")
	}
	if !strings.Contains(out.String(), "Git was not found") {
		t.Fatalf("expected Git note, got: %s", out.String())
	}
}

func TestInitCommandTooManyArgs(t *testing.T) {
	app, _, errOut := testApp(t, t.TempDir())
	if code := Run(context.Background(), app, []string{"init", "a", "b"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "accepts at most 1 arg") {
		t.Fatalf("unexpected error: %s", errOut.String())
	}
}

func TestInitWithAgentPreset(t *testing.T) {
	dir := t.TempDir()
	app, out, errOut := testApp(t, dir)
	if code := Run(context.Background(), app, []string{"init", "--preset", "opencode"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	cfg, err := config.Load(filepath.Join(dir, project.ConfigRelPath))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools["node"] != "lts" || cfg.Tools["opencode"] != "latest" {
		t.Fatalf("tools = %#v", cfg.Tools)
	}
	target, ok := cfg.Run.Targets["opencode"]
	if !ok || !target.Interactive {
		t.Fatalf("target = %#v", cfg.Run.Targets)
	}
	if cfg.Run.Default != "opencode" {
		t.Fatalf("default = %q", cfg.Run.Default)
	}
	for _, want := range []string{"Presets: opencode", "Default target: opencode"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, project.ConfigRelPath))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Generated with presets: opencode") {
		t.Fatalf("preset header missing:\n%s", data)
	}
}

func TestInitComposePresetsNoDefault(t *testing.T) {
	dir := t.TempDir()
	app, out, errOut := testApp(t, dir)
	if code := Run(context.Background(), app, []string{"init", "--preset", "node", "--preset", "opencode"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	cfg, err := config.Load(filepath.Join(dir, project.ConfigRelPath))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Run.Default != "" {
		t.Fatalf("default = %q, want empty", cfg.Run.Default)
	}
	for _, target := range []string{"dev", "test", "opencode"} {
		if _, ok := cfg.Run.Targets[target]; !ok {
			t.Fatalf("missing target %q: %#v", target, cfg.Run.Targets)
		}
	}
	for _, want := range []string{"different default targets", "dev (node)", "opencode (opencode)", "No default run target"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestInitExplicitDefaultTarget(t *testing.T) {
	dir := t.TempDir()
	app, out, errOut := testApp(t, dir)
	if code := Run(context.Background(), app, []string{"init", "--preset", "node", "--preset", "opencode", "--default-target", "opencode"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	cfg, err := config.Load(filepath.Join(dir, project.ConfigRelPath))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Run.Default != "opencode" {
		t.Fatalf("default = %q", cfg.Run.Default)
	}
	if strings.Contains(out.String(), "different default targets") {
		t.Fatalf("unexpected conflict warning:\n%s", out.String())
	}
}

func TestInitUnknownPresetWritesNothing(t *testing.T) {
	dir := t.TempDir()
	app, _, errOut := testApp(t, dir)
	if code := Run(context.Background(), app, []string{"init", "--preset", "node", "--preset", "nope"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "unknown preset") || !strings.Contains(errOut.String(), "opencode") {
		t.Fatalf("unexpected error: %s", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, project.ConfigRelPath)); !os.IsNotExist(err) {
		t.Fatal("invalid preset created configuration")
	}
}

func TestInitInvalidDefaultTargetWritesNothing(t *testing.T) {
	dir := t.TempDir()
	app, _, errOut := testApp(t, dir)
	if code := Run(context.Background(), app, []string{"init", "--preset", "node", "--default-target", "nope"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "not defined") || !strings.Contains(errOut.String(), "dev") {
		t.Fatalf("unexpected error: %s", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, project.ConfigRelPath)); !os.IsNotExist(err) {
		t.Fatal("invalid default created configuration")
	}
}

func TestInitDefaultTargetWithoutPresetWritesNothing(t *testing.T) {
	dir := t.TempDir()
	app, _, _ := testApp(t, dir)
	if code := Run(context.Background(), app, []string{"init", "--default-target", "dev"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if _, err := os.Stat(filepath.Join(dir, project.ConfigRelPath)); !os.IsNotExist(err) {
		t.Fatal("invalid default created configuration")
	}
}

func TestInitPresetsDoNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	app, _, _ := testApp(t, dir)
	if code := Run(context.Background(), app, []string{"init", "--preset", "node"}); code != 0 {
		t.Fatalf("first init failed")
	}
	path := filepath.Join(dir, project.ConfigRelPath)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	app2, _, errOut2 := testApp(t, dir)
	if code := Run(context.Background(), app2, []string{"init", "--preset", "opencode"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut2.String(), "already initialized") {
		t.Fatalf("unexpected error: %s", errOut2.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("preset init overwrote existing configuration")
	}
}

func TestInitEveryPresetWritesValidConfig(t *testing.T) {
	for _, name := range preset.Names() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			app, out, errOut := testApp(t, dir)
			if code := Run(context.Background(), app, []string{"init", "--preset", name}); code != 0 {
				t.Fatalf("exit %d: %s", code, errOut.String())
			}
			if !strings.Contains(out.String(), "Presets: "+name) {
				t.Fatalf("output missing preset name:\n%s", out.String())
			}
			p, err := project.Open(dir)
			if err != nil {
				t.Fatalf("open project: %v", err)
			}
			want, err := preset.Resolve([]string{name}, "")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(p.Config, want.Config) {
				t.Fatalf("config mismatch:\n got %#v\nwant %#v", p.Config, want.Config)
			}
		})
	}
}

func TestInitHelpShowsPresets(t *testing.T) {
	app, out, _ := testApp(t, t.TempDir())
	if code := Run(context.Background(), app, []string{"init", "--help"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, name := range preset.Names() {
		if !strings.Contains(out.String(), name) {
			t.Fatalf("help missing preset %q:\n%s", name, out.String())
		}
	}
	if !strings.Contains(out.String(), "--default-target") {
		t.Fatalf("help missing --default-target:\n%s", out.String())
	}
}

func TestVersionCommand(t *testing.T) {
	app, out, _ := testApp(t, t.TempDir())
	if code := Run(context.Background(), app, []string{"version"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "boxt dev") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestDoctorCommandMissingDependencies(t *testing.T) {
	app, out, errOut := testApp(t, t.TempDir())
	app.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if code := Run(context.Background(), app, []string{"doctor"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	for _, want := range []string{"FAIL", "git", "limactl", "qemu", "doctor found problems"} {
		if !strings.Contains(out.String()+errOut.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s\n%s", want, out.String(), errOut.String())
		}
	}
}

func TestDoctorHelp(t *testing.T) {
	app, out, _ := testApp(t, t.TempDir())
	if code := Run(context.Background(), app, []string{"doctor", "--help"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "never starts or creates") {
		t.Fatalf("unexpected help:\n%s", out.String())
	}
}

func TestHelp(t *testing.T) {
	app, out, _ := testApp(t, t.TempDir())
	if code := Run(context.Background(), app, []string{"--help"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"init", "doctor", "hooks", "version", "persistent Linux"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help missing %q:\n%s", want, out.String())
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "nope"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "unknown run target: nope") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestPrintErrorNotInitialized(t *testing.T) {
	var b bytes.Buffer
	PrintError(&b, &project.ErrNotInitialized{Start: "/tmp/x"})
	want := "Project not initialized.\n\nRun `boxt init` to initialize this directory.\n"
	if b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}

func TestPrintErrorHint(t *testing.T) {
	var b bytes.Buffer
	PrintError(&b, &hintedError{})
	if !strings.Contains(b.String(), "Error: boom") || !strings.Contains(b.String(), "do this") {
		t.Fatalf("unexpected output: %s", b.String())
	}
}

func TestDefaultConfirm(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"yes\n", true},
		{"YES\n", true},
		{" yes \n", true},
		{"y\n", false},
		{"no\n", false},
		{"", false},
	}
	for _, tt := range tests {
		var b bytes.Buffer
		confirm := defaultConfirm(strings.NewReader(tt.input), &b)
		got, err := confirm("Confirm: ")
		if err != nil {
			t.Fatalf("input %q: %v", tt.input, err)
		}
		if got != tt.want {
			t.Errorf("input %q: got %v, want %v", tt.input, got, tt.want)
		}
		if !strings.Contains(b.String(), "Confirm: ") {
			t.Errorf("prompt not written for %q", tt.input)
		}
	}

	file, err := os.CreateTemp(t.TempDir(), "stdin-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	confirm := defaultConfirm(file, &bytes.Buffer{})
	if _, err := confirm("Confirm: "); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected non-TTY error, got %v", err)
	}
}

type hintedError struct{}

func (hintedError) Error() string { return "boom" }
func (hintedError) Hint() string  { return "do this" }
