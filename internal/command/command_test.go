package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecRunnerSuccess(t *testing.T) {
	var out bytes.Buffer
	err := (ExecRunner{}).Run(context.Background(), Command{
		Name:   "sh",
		Args:   []string{"-c", "printf hello"},
		Stdout: &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.String() != "hello" {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestExecRunnerExitCode(t *testing.T) {
	err := (ExecRunner{}).Run(context.Background(), Command{Name: "sh", Args: []string{"-c", "exit 7"}})
	if err == nil {
		t.Fatal("expected error")
	}
	var cmdErr *Error
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if cmdErr.ExitStatus() != 7 {
		t.Fatalf("exit status = %d, want 7", cmdErr.ExitStatus())
	}
	if cmdErr.Error() == "" {
		t.Fatal("empty error message")
	}
}

func TestExecRunnerMissingBinary(t *testing.T) {
	err := (ExecRunner{}).Run(context.Background(), Command{Name: "definitely-not-a-real-binary-boxt"})
	if err == nil {
		t.Fatal("expected error")
	}
	var cmdErr *Error
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if cmdErr.ExitStatus() != 1 {
		t.Fatalf("exit status = %d, want 1", cmdErr.ExitStatus())
	}
}

func TestExecRunnerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := (ExecRunner{}).Run(ctx, Command{Name: "sh", Args: []string{"-c", "sleep 5"}})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
	var cmdErr *Error
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if cmdErr.ExitStatus() != 130 {
		t.Fatalf("exit status = %d, want 130", cmdErr.ExitStatus())
	}
}

func TestExecRunnerCancellationKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- (ExecRunner{}).Run(ctx, Command{
			Name:            "sh",
			Args:            []string{"-c", "sleep 5 & echo $! > " + pidFile + "; wait"},
			NewProcessGroup: true,
		})
	}()
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(pidFile)
		if err == nil && strings.TrimSpace(string(data)) != "" {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("grandchild PID was not written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-errCh; err == nil {
		t.Fatal("expected cancellation error")
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild process %d survived cancellation (err=%v)", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestOutputCapturesStreams(t *testing.T) {
	out, err := Output(context.Background(), ExecRunner{}, Command{
		Name: "sh",
		Args: []string{"-c", "printf stdout; printf stderr >&2; exit 3"},
	})
	if out != "stdout" {
		t.Fatalf("stdout = %q", out)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	var cmdErr *Error
	if !errors.As(err, &cmdErr) || !strings.Contains(cmdErr.Stderr, "stderr") {
		t.Fatalf("stderr not attached: %v", err)
	}
}

type recordingRunner struct {
	cmds []Command
	err  error
	out  string
}

func (r *recordingRunner) Run(ctx context.Context, c Command) error {
	r.cmds = append(r.cmds, c)
	if c.Stdout != nil && r.out != "" {
		_, _ = c.Stdout.Write([]byte(r.out))
	}
	return r.err
}

func TestArgumentsAreNotJoinedInTests(t *testing.T) {
	rr := &recordingRunner{out: "  value  \n"}
	got, err := Output(context.Background(), rr, Command{Name: "git", Args: []string{"-C", "/tmp/x", "status"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "  value  " {
		t.Fatalf("output trimming = %q", got)
	}
	if len(rr.cmds) != 1 || len(rr.cmds[0].Args) != 3 || rr.cmds[0].Args[1] != "/tmp/x" {
		t.Fatalf("args were not preserved: %#v", rr.cmds)
	}
}

func TestErrorDisplay(t *testing.T) {
	err := &Error{Name: "git", Args: []string{"status"}, ExitCode: 128, Stderr: "fatal: nope\n"}
	msg := err.Error()
	if !strings.Contains(msg, "git status") || !strings.Contains(msg, "exit status 128") || !strings.Contains(msg, "fatal: nope") {
		t.Fatalf("unexpected message %q", msg)
	}
}

func TestEmptyCommand(t *testing.T) {
	if err := (ExecRunner{}).Run(context.Background(), Command{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestOutputWithProvidedWriter(t *testing.T) {
	var stdout bytes.Buffer
	out, err := Output(context.Background(), ExecRunner{}, Command{
		Name:   "sh",
		Args:   []string{"-c", "printf captured"},
		Stdout: &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "" || stdout.String() != "captured" {
		t.Fatalf("provided writer not used: out=%q writer=%q", out, stdout.String())
	}
}
