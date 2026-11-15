package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/boxt/boxt/internal/config"
	"github.com/boxt/boxt/internal/executor"
	"github.com/boxt/boxt/internal/lima"
	"github.com/boxt/boxt/internal/project"
)

type invocation struct {
	Command     []string
	Interactive bool
}

func newRunCmd(app *App) *cobra.Command {
	var noStart bool
	cmd := &cobra.Command{
		Use:   "run [target] [-- command [args...]]",
		Short: "Run the default target, a named target, or an arbitrary command",
		Long: `Run a configured target in the project VM.

  boxt run                 run the configured default target
  boxt run test            run a named target
  boxt run -- npm test     run an arbitrary command`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.run(cmd.Context(), args, cmd.ArgsLenAtDash(), noStart)
		},
	}
	cmd.Flags().BoolVar(&noStart, "no-start", false, "do not start a stopped VM automatically")
	return cmd
}

func (a *App) run(ctx context.Context, args []string, dash int, noStart bool) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	inv, err := resolveRun(p.Config, args, dash, a.interactiveTerminal(), "boxt run")
	if err != nil {
		return err
	}
	if err := a.requireLima(); err != nil {
		return err
	}
	if err := a.ensureVMForExecution(ctx, p, !noStart); err != nil {
		return err
	}
	return a.execute(ctx, p, "default", lima.GuestProjectPath, inv)
}

func (a *App) execute(ctx context.Context, p *project.Project, workspace, workDir string, inv invocation) error {
	ex := a.executorFor(p)
	return ex.Execute(ctx, executor.Request{
		ProjectID:   p.ID,
		Workspace:   workspace,
		WorkDir:     workDir,
		Command:     inv.Command,
		Interactive: inv.Interactive,
	})
}

func (a *App) executorFor(p *project.Project) *executor.Executor {
	ex := &executor.Executor{
		Guest: a.Lima,
		In:    a.In,
		Out:   a.Out,
		Err:   a.Err,
	}
	if len(p.Config.Tools) > 0 && a.PrepareCommand != nil {
		tools := p.Config.Tools
		ex.Prepare = func(argv []string) []string {
			return a.PrepareCommand(tools, argv)
		}
	}
	return ex
}

func newShellCmd(app *App) *cobra.Command {
	var noStart bool
	cmd := &cobra.Command{
		Use:   "shell",
		Short: "Open an interactive shell in the default workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.shell(cmd.Context(), noStart)
		},
	}
	cmd.Flags().BoolVar(&noStart, "no-start", false, "do not start a stopped VM automatically")
	return cmd
}

func (a *App) shell(ctx context.Context, noStart bool) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if err := a.requireLima(); err != nil {
		return err
	}
	if err := a.ensureVMForExecution(ctx, p, !noStart); err != nil {
		return err
	}
	return a.execute(ctx, p, "default", lima.GuestProjectPath, a.shellInvocation(p))
}

// shellInvocation returns an empty command for a plain guest login shell, or
// an explicit shell when a prepared environment must be activated.
func (a *App) shellInvocation(p *project.Project) invocation {
	var argv []string
	if len(p.Config.Tools) > 0 && a.PrepareCommand != nil {
		argv = []string{lima.GuestShell}
	}
	return invocation{Command: argv, Interactive: true}
}

// resolveRun implements target resolution. It never falls back from an unknown
// target name to an executable on PATH.
func resolveRun(cfg *config.Config, args []string, dash int, terminal bool, commandPrefix string) (invocation, error) {
	if dash > 0 {
		return invocation{}, errors.New("unexpected arguments before `--`; use `boxt run <target>` or `boxt run -- <command>`")
	}
	if dash == 0 {
		if len(args) == 0 || args[0] == "" {
			return invocation{}, errors.New("no command specified after `--`")
		}
		return invocation{
			Command:     append([]string(nil), args...),
			Interactive: terminal,
		}, nil
	}
	switch len(args) {
	case 0:
		if cfg.Run.Default == "" {
			return invocation{}, &noDefaultTargetError{}
		}
		target, ok := cfg.ResolveTarget(cfg.Run.Default)
		if !ok {
			return invocation{}, &unknownTargetError{
				Name:          cfg.Run.Default,
				CommandPrefix: commandPrefix,
			}
		}
		return invocation{
			Command:     append([]string(nil), target.Command...),
			Interactive: target.Interactive,
		}, nil
	case 1:
		target, ok := cfg.ResolveTarget(args[0])
		if !ok {
			return invocation{}, &unknownTargetError{Name: args[0], CommandPrefix: commandPrefix}
		}
		return invocation{
			Command:     append([]string(nil), target.Command...),
			Interactive: target.Interactive,
		}, nil
	default:
		return invocation{}, fmt.Errorf(
			"unexpected arguments: %v\n\nUse `boxt run <target>` for a named target, or `boxt run -- <command> [args...]` for an arbitrary command",
			args[1:],
		)
	}
}

type unknownTargetError struct {
	Name          string
	CommandPrefix string
}

func (e *unknownTargetError) Error() string { return "unknown run target: " + e.Name }

func (e *unknownTargetError) Hint() string {
	prefix := e.CommandPrefix
	if prefix == "" {
		prefix = "boxt run"
	}
	return fmt.Sprintf(
		"Define it under run.targets in .boxt/config.yaml,\nor use `%s -- %s` to execute the command directly.",
		prefix, e.Name,
	)
}

type noDefaultTargetError struct{}

func (e *noDefaultTargetError) Error() string { return "no default run target configured" }
func (e *noDefaultTargetError) Hint() string {
	return "Set run.default in .boxt/config.yaml, or invoke a target with `boxt run <target>`."
}

func (a *App) interactiveTerminal() bool {
	return isTerminal(a.In) && isTerminal(a.Out)
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
