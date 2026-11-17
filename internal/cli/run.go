package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/executor"
	"github.com/daveagill/boxt/internal/lima"
	"github.com/daveagill/boxt/internal/project"
	"github.com/daveagill/boxt/internal/workspace"
)

type invocation struct {
	Command     []string
	Interactive bool
	PortBase    int
	PortEnv     string
}

func newRunCmd(app *App) *cobra.Command {
	var noStart bool
	cmd := &cobra.Command{
		Use:   "run [target] [-- args...] [-- command [args...]]",
		Short: "Run the default target, a named target, or an arbitrary command",
		Long: `Run a configured target in the project VM.

  boxt run                    run the configured default target
  boxt run test               run a named target
  boxt run test -- --watch    append arguments to a named target
  boxt run -- npm test        run an arbitrary command`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.run(cmd.Context(), args, cmd.ArgsLenAtDash(), noStart)
		},
	}
	cmd.Flags().BoolVar(&noStart, "no-start", false, "do not create or start the VM automatically")
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
	workspace, workDir, err := a.executionWorkspace(ctx, p)
	if err != nil {
		return err
	}
	return a.execute(ctx, p, workspace, workDir, inv, !noStart && p.Config.VM.AutoStartEnabled())
}

// executionWorkspace returns the workspace that run and shell execute in: the
// managed workspace whose checkout contains the current directory, or the
// default workspace when the current directory is elsewhere. A command cannot
// change its parent shell's directory, so this is how `boxt run` and
// `boxt shell` follow `cd`. It is derived per invocation and never persisted;
// `boxt workspace run|shell <name>` remains the explicit form.
func (a *App) executionWorkspace(ctx context.Context, p *project.Project) (string, string, error) {
	cwd, err := a.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("determine working directory: %w", err)
	}
	mgr, _, err := a.workspaceManager(p)
	if err != nil {
		return "", "", err
	}
	if name, ok := mgr.NameForHostPath(cwd); ok {
		return name, mgr.GuestPath(name), nil
	}
	return workspace.DefaultName, lima.GuestProjectPath, nil
}

// execute is the single CLI path that starts the VM when allowed and runs a
// request through the shared executor.
func (a *App) execute(ctx context.Context, p *project.Project, workspace, workDir string, inv invocation, allowStart bool) error {
	if inv.PortBase > 0 {
		mgr, _, err := a.workspaceManager(p)
		if err != nil {
			return err
		}
		offset, err := mgr.PortOffsetFor(ctx, workspace)
		if err != nil {
			return err
		}
		guestPort := inv.PortBase + offset
		if guestPort > 65535 {
			return fmt.Errorf("guest port %d for workspace %s exceeds 65535", guestPort, workspace)
		}
		envName := inv.PortEnv
		if envName == "" {
			envName = "PORT"
		}
		inv.Command = executor.WithEnv(inv.Command, envName+"="+strconv.Itoa(guestPort))
	}
	if inv.Interactive && !isTerminal(a.Out) {
		fmt.Fprintln(a.Err, "Warning: interactive target requested, but stdout is not a terminal; the guest will not receive a PTY.")
	}
	if err := a.requireLima(); err != nil {
		return err
	}
	if err := a.ensureVMForExecution(ctx, p, allowStart); err != nil {
		return err
	}
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
		Short: "Open an interactive shell in the current workspace",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.shell(cmd.Context(), noStart)
		},
	}
	cmd.Flags().BoolVar(&noStart, "no-start", false, "do not create or start the VM automatically")
	return cmd
}

func (a *App) shell(ctx context.Context, noStart bool) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	workspace, workDir, err := a.executionWorkspace(ctx, p)
	if err != nil {
		return err
	}
	return a.execute(ctx, p, workspace, workDir, a.shellInvocation(p), !noStart && p.Config.VM.AutoStartEnabled())
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
// target name to an executable on PATH. Arguments after `--` either form an
// arbitrary command (when there is no target before the dash) or are appended
// to the configured target command.
func resolveRun(cfg *config.Config, args []string, dash int, terminal bool, commandPrefix string) (invocation, error) {
	if dash == 0 {
		if len(args) == 0 || args[0] == "" {
			return invocation{}, errors.New("no command specified after `--`")
		}
		return invocation{
			Command:     append([]string(nil), args...),
			Interactive: terminal,
		}, nil
	}
	if dash > 0 {
		if dash != 1 {
			prefix := commandPrefix
			if prefix == "" {
				prefix = "boxt run"
			}
			return invocation{}, fmt.Errorf(
				"unexpected arguments before `--`: %v\n\nUse `%s <target> -- <args...>` to pass arguments to a target, or `%s -- <command> [args...]` for an arbitrary command",
				args[:dash], prefix, prefix,
			)
		}
		name := args[0]
		target, ok := cfg.ResolveTarget(name)
		if !ok {
			return invocation{}, &unknownTargetError{Name: name, CommandPrefix: commandPrefix}
		}
		command := append([]string(nil), target.Command...)
		command = append(command, args[dash:]...)
		inv := targetInvocation(target)
		inv.Command = command
		return inv, nil
	}
	switch len(args) {
	case 0:
		if cfg.Run.Default == "" {
			return invocation{}, &noDefaultTargetError{CommandPrefix: commandPrefix}
		}
		target, ok := cfg.ResolveTarget(cfg.Run.Default)
		if !ok {
			return invocation{}, &unknownTargetError{
				Name:          cfg.Run.Default,
				CommandPrefix: commandPrefix,
			}
		}
		return targetInvocation(target), nil
	case 1:
		target, ok := cfg.ResolveTarget(args[0])
		if !ok {
			return invocation{}, &unknownTargetError{Name: args[0], CommandPrefix: commandPrefix}
		}
		return targetInvocation(target), nil
	default:
		prefix := commandPrefix
		if prefix == "" {
			prefix = "boxt run"
		}
		return invocation{}, fmt.Errorf(
			"unexpected arguments: %v\n\nUse `%s <target>` for a named target, `%s <target> -- <args...>` to pass arguments, or `%s -- <command> [args...]` for an arbitrary command",
			args[1:], prefix, prefix, prefix,
		)
	}
}

func targetInvocation(target config.RunTarget) invocation {
	return invocation{
		Command:     append([]string(nil), target.Command...),
		Interactive: target.Interactive,
		PortBase:    target.PortBase,
		PortEnv:     target.PortEnv,
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
		"Add it with `boxt target add %[1]s -- <command>` or edit run.targets in .boxt/config.yaml,\nor use `%[2]s -- %[1]s` to execute the command directly.",
		e.Name, prefix,
	)
}

type noDefaultTargetError struct {
	CommandPrefix string
}

func (e *noDefaultTargetError) Error() string { return "no default run target configured" }
func (e *noDefaultTargetError) Hint() string {
	prefix := e.CommandPrefix
	if prefix == "" {
		prefix = "boxt run"
	}
	return fmt.Sprintf("Set run.default in .boxt/config.yaml, or invoke a target with `%s <target>`.", prefix)
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
