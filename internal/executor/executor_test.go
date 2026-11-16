package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/lima"
)

type fakeGuest struct {
	requests []lima.ShellRequest
	err      error
}

func (f *fakeGuest) Shell(ctx context.Context, req lima.ShellRequest) error {
	f.requests = append(f.requests, req)
	return f.err
}

func newExecutor(g *fakeGuest) (*Executor, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	return &Executor{
		Guest: g,
		In:    strings.NewReader("input"),
		Out:   out,
		Err:   errOut,
	}, out, errOut
}

func TestExecuteNonInteractive(t *testing.T) {
	g := &fakeGuest{}
	ex, out, errOut := newExecutor(g)
	err := ex.Execute(context.Background(), Request{
		ProjectID: "abc",
		Workspace: "default",
		WorkDir:   "/workspace",
		Command:   []string{"npm", "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.requests) != 1 {
		t.Fatalf("requests = %#v", g.requests)
	}
	req := g.requests[0]
	if req.Instance != "boxt-abc" || req.WorkDir != "/workspace" {
		t.Fatalf("unexpected request: %#v", req)
	}
	if len(req.Command) != 2 || req.Command[0] != "npm" || req.Command[1] != "test" {
		t.Fatalf("command = %#v", req.Command)
	}
	if _, ok := req.Stdout.(nonFileWriter); !ok {
		t.Fatalf("non-interactive stdout is %T, want nonFileWriter", req.Stdout)
	}
	if _, ok := req.Stderr.(nonFileWriter); !ok {
		t.Fatalf("non-interactive stderr is %T, want nonFileWriter", req.Stderr)
	}
	if !req.NewProcessGroup {
		t.Fatal("non-interactive command should use a separate process group")
	}
	_, _ = io.WriteString(req.Stdout, "hello stdout")
	_, _ = io.WriteString(req.Stderr, "hello stderr")
	if out.String() != "hello stdout" || errOut.String() != "hello stderr" {
		t.Fatalf("streams not connected: out=%q err=%q", out.String(), errOut.String())
	}
}

func TestExecuteInteractiveUsesTerminalStreams(t *testing.T) {
	g := &fakeGuest{}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	in := strings.NewReader("x")
	ex := &Executor{Guest: g, In: in, Out: out, Err: errOut}
	if err := ex.Execute(context.Background(), Request{
		ProjectID:   "abc",
		Workspace:   "default",
		WorkDir:     "/workspace",
		Command:     []string{"python"},
		Interactive: true,
	}); err != nil {
		t.Fatal(err)
	}
	req := g.requests[0]
	if req.Stdout != out || req.Stderr != errOut || req.Stdin != in {
		t.Fatalf("interactive streams were replaced: %#v", req)
	}
	if req.NewProcessGroup {
		t.Fatal("interactive command should stay in the terminal process group")
	}
}

func TestExecuteEmptyInteractiveShell(t *testing.T) {
	g := &fakeGuest{}
	ex, _, _ := newExecutor(g)
	if err := ex.Execute(context.Background(), Request{
		ProjectID:   "abc",
		Workspace:   "default",
		WorkDir:     "/workspace",
		Interactive: true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(g.requests[0].Command) != 0 {
		t.Fatalf("unprepared shell command = %#v, want empty", g.requests[0].Command)
	}

	g2 := &fakeGuest{}
	ex2, _, _ := newExecutor(g2)
	ex2.Prepare = func(argv []string) []string {
		return append([]string{"mise", "exec", "--"}, argv...)
	}
	if err := ex2.Execute(context.Background(), Request{
		ProjectID:   "abc",
		Workspace:   "default",
		WorkDir:     "/workspace",
		Interactive: true,
	}); err != nil {
		t.Fatal(err)
	}
	got := g2.requests[0].Command
	want := []string{"mise", "exec", "--", "/bin/bash"}
	if !equal(got, want) {
		t.Fatalf("prepared shell command = %#v, want %#v", got, want)
	}
}

func TestExecutePrepare(t *testing.T) {
	g := &fakeGuest{}
	ex, _, _ := newExecutor(g)
	ex.Prepare = func(argv []string) []string {
		return append([]string{"env", "MISE_NO_CONFIG=1", "mise", "exec", "node@24", "--"}, argv...)
	}
	if err := ex.Execute(context.Background(), Request{
		ProjectID: "abc",
		Workspace: "default",
		WorkDir:   "/workspace",
		Command:   []string{"npm", "test"},
	}); err != nil {
		t.Fatal(err)
	}
	got := g.requests[0].Command
	want := []string{"env", "MISE_NO_CONFIG=1", "mise", "exec", "node@24", "--", "npm", "test"}
	if !equal(got, want) {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

func TestExecuteValidation(t *testing.T) {
	g := &fakeGuest{}
	ex, _, _ := newExecutor(g)
	tests := []Request{
		{WorkDir: "/workspace", Command: []string{"true"}},
		{ProjectID: "abc", Command: []string{"true"}},
		{ProjectID: "abc", WorkDir: "/workspace"},
	}
	for i, req := range tests {
		if err := ex.Execute(context.Background(), req); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
	if len(g.requests) != 0 {
		t.Fatal("invalid requests reached the guest")
	}
}

func TestExecuteExitStatusPreserved(t *testing.T) {
	g := &fakeGuest{err: &command.Error{Name: "limactl", ExitCode: 7}}
	ex, _, _ := newExecutor(g)
	err := ex.Execute(context.Background(), Request{
		ProjectID: "abc",
		Workspace: "default",
		WorkDir:   "/workspace",
		Command:   []string{"false"},
	})
	var exitCoder command.ExitCoder
	if !errors.As(err, &exitCoder) || exitCoder.ExitStatus() != 7 {
		t.Fatalf("exit status not preserved: %v", err)
	}
	if strings.Contains(err.Error(), "limactl shell") {
		t.Fatalf("error leaks Lima argv: %v", err)
	}
	if !strings.Contains(err.Error(), "false failed") {
		t.Fatalf("error does not name the guest command: %v", err)
	}
	if strings.Contains(err.Error(), "default:") {
		t.Fatalf("error leaks workspace prefix: %v", err)
	}
}

func TestExecutorNoGuest(t *testing.T) {
	ex := &Executor{}
	err := ex.Execute(context.Background(), Request{ProjectID: "a", WorkDir: "/workspace", Command: []string{"true"}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestWithEnv(t *testing.T) {
	argv := []string{"npm", "run", "dev"}
	got := WithEnv(argv, "PORT=3000")
	want := []string{"/usr/bin/env", "PORT=3000", "npm", "run", "dev"}
	if !equal(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if !equal(argv, []string{"npm", "run", "dev"}) {
		t.Fatalf("WithEnv mutated its input: %#v", argv)
	}
	if got := WithEnv(argv); !equal(got, argv) {
		t.Fatalf("empty env changed argv: %#v", got)
	}
}

func equal(a, b []string) bool {
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
