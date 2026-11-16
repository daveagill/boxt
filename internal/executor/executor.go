// Package executor implements the single execution engine used by every
// boxt command that runs something inside the project VM.
package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/lima"
)

// Request is one guest execution. All commands differ only by working
// directory, command line and whether a PTY is required.
type Request struct {
	ProjectID   string
	Workspace   string
	WorkDir     string
	Command     []string
	Interactive bool
}

// Guest is the guest execution seam (implemented by the Lima client).
type Guest interface {
	Shell(ctx context.Context, req lima.ShellRequest) error
}

// Executor runs commands through one guest interface.
type Executor struct {
	Guest   Guest
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
	Prepare func(argv []string) []string
}

// VMNameFor returns the Lima instance name for a project ID.
func VMNameFor(projectID string) string { return "boxt-" + projectID }

// Execute runs a request and returns the guest exit status through the error
// chain when it is non-zero. An empty command is only valid for an interactive
// shell.
func (e *Executor) Execute(ctx context.Context, req Request) error {
	if e.Guest == nil {
		return errors.New("executor has no guest")
	}
	if req.ProjectID == "" {
		return errors.New("execution request is missing the project ID")
	}
	if req.WorkDir == "" {
		return errors.New("execution request is missing the guest working directory")
	}
	if len(req.Command) == 0 && !req.Interactive {
		return errors.New("execution request has no command")
	}
	if req.Interactive && len(req.Command) == 0 && e.Prepare != nil {
		// A prepared environment needs a process to activate it. Start the
		// guest shell explicitly so mise-provided tools are on PATH.
		req.Command = []string{lima.GuestShell}
	}

	argv := req.Command
	if len(argv) > 0 && e.Prepare != nil {
		argv = e.Prepare(argv)
	}

	streams := lima.ShellRequest{
		Instance:        VMNameFor(req.ProjectID),
		WorkDir:         req.WorkDir,
		Command:         argv,
		Stdin:           e.In,
		Stdout:          e.Out,
		Stderr:          e.Err,
		NewProcessGroup: !req.Interactive,
	}
	if !req.Interactive {
		// Wrapping with a non-*os.File writer makes os/exec create a pipe,
		// so limactl does not allocate a remote PTY for non-interactive
		// commands even when boxt itself is attached to a terminal.
		streams.Stdout = nonFileWriter{e.Out}
		streams.Stderr = nonFileWriter{e.Err}
	}

	return runWithSignals(ctx, req.Interactive, func(runCtx context.Context) error {
		if err := e.Guest.Shell(runCtx, streams); err != nil {
			return commandError(req.Command, err)
		}
		return nil
	})
}

// WithEnv prefixes argv with /usr/bin/env assignments. It never constructs a
// shell string and leaves the original slice untouched.
func WithEnv(argv []string, assignments ...string) []string {
	if len(assignments) == 0 {
		return argv
	}
	out := make([]string, 0, 1+len(assignments)+len(argv))
	out = append(out, "/usr/bin/env")
	out = append(out, assignments...)
	return append(out, argv...)
}

func commandError(argv []string, err error) error {
	display := strings.Join(argv, " ")
	if display == "" {
		display = "interactive shell"
	}
	var cmdErr *command.Error
	if errors.As(err, &cmdErr) {
		return &command.Error{
			Name:     display,
			ExitCode: cmdErr.ExitCode,
			Stderr:   cmdErr.Stderr,
			Err:      cmdErr.Err,
		}
	}
	return fmt.Errorf("%s: %w", display, err)
}

// runWithSignals lets an interactive SSH child own terminal signals (Ctrl-C,
// Ctrl-\), while a direct SIGTERM cancels boxt and its child. Non-interactive
// commands cancel on SIGINT or SIGTERM.
func runWithSignals(ctx context.Context, interactive bool, fn func(context.Context) error) error {
	if interactive {
		signal.Ignore(os.Interrupt, syscall.SIGQUIT)
		defer signal.Reset(os.Interrupt, syscall.SIGQUIT)
		ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM)
		defer stop()
		return fn(ctx)
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return fn(ctx)
}

// nonFileWriter hides the concrete *os.File type from os/exec.
type nonFileWriter struct{ io.Writer }

func (w nonFileWriter) Write(p []byte) (int, error) { return w.Writer.Write(p) }
