package cli

import (
	"strings"
	"testing"
)

func TestWorkspaceSwitchNeedsShellInit(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.out.Reset()
	if code := f.run(t, "workspace", "switch", "feature-a"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if f.out.Len() != 0 {
		t.Fatalf("switch wrote to stdout (must never be eval-able): %q", f.out.String())
	}
	for _, want := range []string{
		"boxt cannot change the working directory of your shell",
		`eval "$(boxt shell-init zsh)"`,
		`eval "$(boxt shell-init bash)"`,
		"boxt shell-init fish | source",
		`cd "$(boxt workspace path feature-a)"`,
	} {
		if !strings.Contains(f.errOut.String(), want) {
			t.Fatalf("error missing %q:\n%s", want, f.errOut.String())
		}
	}
}

func TestWorkspaceSwitchCompletion(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed: %s", f.errOut.String())
	}
	f.out.Reset()
	if code := f.run(t, "__complete", "workspace", "switch", ""); code != 0 {
		t.Fatalf("completion exit %d: %s", code, f.errOut.String())
	}
	for _, want := range []string{"default", "feature-a"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("completion missing %q:\n%s", want, f.out.String())
		}
	}
}

func TestWorkspacePathCompletion(t *testing.T) {
	f := newWorkspaceFixture(t)
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed: %s", f.errOut.String())
	}
	f.out.Reset()
	if code := f.run(t, "__complete", "workspace", "path", ""); code != 0 {
		t.Fatalf("completion exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "feature-a") {
		t.Fatalf("completion missing workspace:\n%s", f.out.String())
	}
}
