// Package hooks validates, trusts and executes project lifecycle hooks. Hooks
// reuse the normal command/executor seams and never use a shell.
package hooks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/config"
)

// DefaultTimeout is the generous per-hook limit that prevents an infinite hang.
const DefaultTimeout = 10 * time.Minute

// Env describes the workspace context exposed to every hook.
type Env struct {
	ProjectRoot   string
	Workspace     string
	WorkspacePath string
	GuestPath     string
}

// Vars returns the BOXT_* environment assignments.
func (e Env) Vars() []string {
	return []string{
		"BOXT_PROJECT_ROOT=" + e.ProjectRoot,
		"BOXT_WORKSPACE=" + e.Workspace,
		"BOXT_WORKSPACE_PATH=" + e.WorkspacePath,
		"BOXT_GUEST_PATH=" + e.GuestPath,
	}
}

// Options controls hook execution.
type Options struct {
	Env         Env
	HostDir     string
	GuestDir    string
	HostRunner  command.Runner
	GuestRunner func(ctx context.Context, workDir string, argv []string, env []string) error
	Timeout     time.Duration
	Out         io.Writer
	Err         io.Writer
}

// Run executes hooks in order. Extra environment assignments are appended to
// the BOXT_* context. A pre-hook failure stops the caller; post-hook failures
// are returned after the operation and must not undo completed mutations.
func Run(ctx context.Context, list []config.Hook, opts Options) error {
	if len(list) == 0 {
		return nil
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	env := opts.Env.Vars()
	for _, hook := range list {
		argv := append([]string(nil), hook.Command...)
		if len(argv) == 0 {
			continue
		}
		where := hook.Where
		if where == "" {
			where = config.HookWhereGuest
		}
		hookCtx, cancel := context.WithTimeout(ctx, timeout)
		var err error
		switch where {
		case config.HookWhereGuest:
			if opts.GuestRunner == nil {
				err = errors.New("guest hook runner is unavailable")
			} else {
				err = opts.GuestRunner(hookCtx, opts.GuestDir, argv, env)
			}
		case config.HookWhereHost:
			if opts.HostRunner == nil {
				err = errors.New("host hook runner is unavailable")
			} else {
				err = opts.HostRunner.Run(hookCtx, command.Command{
					Name:   argv[0],
					Args:   argv[1:],
					Dir:    opts.HostDir,
					Env:    append(os.Environ(), env...),
					Stdout: opts.Out,
					Stderr: opts.Err,
				})
			}
		default:
			err = fmt.Errorf("unsupported hook location %q", where)
		}
		cancel()
		if err != nil {
			if hookCtx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("hook %q (%s) timed out after %s", display(argv), where, timeout)
			}
			return fmt.Errorf("hook %q (%s) failed: %w", display(argv), where, err)
		}
	}
	return nil
}

func display(argv []string) string {
	return strings.Join(argv, " ")
}

// Commands returns readable command strings for one hook set.
func Commands(set config.HookSet) []string {
	out := make([]string, 0, len(set.Pre)+len(set.Post))
	for _, hook := range set.Pre {
		if len(hook.Command) > 0 {
			out = append(out, display(hook.Command))
		}
	}
	for _, hook := range set.Post {
		if len(hook.Command) > 0 {
			out = append(out, display(hook.Command))
		}
	}
	return out
}
