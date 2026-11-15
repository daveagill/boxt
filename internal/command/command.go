// Package command provides the single subprocess seam used by all boxt
// adapters. External tools are never invoked directly from CLI code.
package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
)

// Command describes an external process invocation. Arguments are always
// passed separately, never as a shell string.
type Command struct {
	Name            string
	Args            []string
	Dir             string
	Env             []string
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	NewProcessGroup bool
}

// Runner executes external commands. Tests substitute a fake.
type Runner interface {
	Run(ctx context.Context, cmd Command) error
}

// ExecRunner runs commands with os/exec.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, c Command) error {
	if c.Name == "" {
		return &Error{Name: c.Name, Args: c.Args, ExitCode: -1, Err: errors.New("empty command name")}
	}
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	if c.Env != nil {
		cmd.Env = c.Env
	}
	cmd.Stdin = c.Stdin
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	if c.NewProcessGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return nil
			}
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return &Error{Name: c.Name, Args: c.Args, ExitCode: 130, Err: ctxErr}
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &Error{
				Name:     c.Name,
				Args:     c.Args,
				ExitCode: exitErr.ExitCode(),
				Err:      err,
			}
		}
		return &Error{Name: c.Name, Args: c.Args, ExitCode: -1, Err: err}
	}
	return nil
}

// Error describes a failed external command. It preserves the exit code so the
// CLI can return the same status as a guest command.
type Error struct {
	Name     string
	Args     []string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%s failed", e.display())
	if e.ExitCode > 0 {
		msg += fmt.Sprintf(" (exit status %d)", e.ExitCode)
	}
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += ": " + s
	} else if e.Err != nil && e.ExitCode <= 0 {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) display() string {
	parts := append([]string{e.Name}, e.Args...)
	return strings.Join(parts, " ")
}

func (e *Error) Unwrap() error { return e.Err }

// ExitStatus returns the process exit status, or 1 for failures that never
// produced a process (for example a missing executable).
func (e *Error) ExitStatus() int {
	if e.ExitCode > 0 {
		return e.ExitCode
	}
	return 1
}

// ExitCoder is implemented by errors that carry a process exit status.
type ExitCoder interface {
	ExitStatus() int
}

// Output runs a command and captures stdout. On failure, stderr is attached to
// the returned error when the underlying runner reports one.
func Output(ctx context.Context, r Runner, c Command) (string, error) {
	var stdout, stderr bytes.Buffer
	if c.Stdout == nil {
		c.Stdout = &stdout
	}
	if c.Stderr == nil {
		c.Stderr = &stderr
	}
	if err := r.Run(ctx, c); err != nil {
		var cmdErr *Error
		if errors.As(err, &cmdErr) && cmdErr.Stderr == "" {
			cmdErr.Stderr = stderr.String()
		}
		return strings.TrimRight(stdout.String(), "\n"), err
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}
