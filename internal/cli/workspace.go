package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/project"
	"github.com/daveagill/boxt/internal/workspace"
)

func newWorkspaceCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage independent Git worktrees in the project VM",
		Long:  "Named workspaces are Git worktrees stored outside the main checkout and mounted into the shared project VM.",
	}
	cmd.AddCommand(newWorkspaceCreateCmd(app))
	cmd.AddCommand(newWorkspaceListCmd(app))
	cmd.AddCommand(newWorkspaceMergeCmd(app))
	cmd.AddCommand(newWorkspaceRemoveCmd(app))
	cmd.AddCommand(newWorkspacePathCmd(app))
	cmd.AddCommand(newWorkspaceRunCmd(app))
	cmd.AddCommand(newWorkspaceSeedCmd(app))
	cmd.AddCommand(newWorkspaceShellCmd(app))
	return cmd
}

func (a *App) workspaceManager(p *project.Project) (*workspace.Manager, projectPaths, error) {
	paths, err := a.pathsFor(p)
	if err != nil {
		return nil, projectPaths{}, err
	}
	return workspace.New(p.Root, p.ID, paths.WorkspacesDir, a.Git), paths, nil
}

// bareRunTargetSentinel is the pflag NoOptDefVal for --run. Target names must
// match ^[A-Za-z0-9][A-Za-z0-9._-]*$, so an @-prefixed value can never collide
// with a real target name.
const bareRunTargetSentinel = "@run.default"

func newWorkspaceCreateCmd(app *App) *cobra.Command {
	var branch string
	var runTarget string
	var noStart bool
	var seed bool
	cmd := &cobra.Command{
		Use:   "create name",
		Short: "Create a named workspace backed by a Git worktree",
		Long: `Create a named workspace backed by a Git worktree.

Use --run without a value to run run.default, --run <target> to run an explicit
configured target, and --run= to skip running.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if runTarget == bareRunTargetSentinel && len(args) == 2 {
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name, target := workspaceCreateRunTarget(args, runTarget)
			return app.workspaceCreate(cmd.Context(), name, branch, target, seed, noStart)
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "", "branch to create or check out (default: boxt/<name>)")
	cmd.Flags().StringVar(&runTarget, "run", "", "run a configured target in the new workspace (bare --run uses run.default)")
	cmd.Flags().Lookup("run").NoOptDefVal = bareRunTargetSentinel
	cmd.Flags().BoolVar(&seed, "seed", false, "copy ignored files from the default workspace")
	cmd.Flags().BoolVar(&noStart, "no-start", false, "do not create or start the VM automatically for --run")
	// pflag renders the optional-value sentinel in flag usage; hide the internal
	// sentinel from help while keeping bare --run parsing.
	help := cmd.HelpFunc()
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		run := c.Flags().Lookup("run")
		sentinel := run.NoOptDefVal
		run.NoOptDefVal = ""
		defer func() { run.NoOptDefVal = sentinel }()
		help(c, args)
	})
	return cmd
}

// workspaceCreateRunTarget normalizes the optional --run value. pflag does not
// consume a space-separated token after an optional-value flag, so a second
// positional argument (after the workspace name) is treated as the explicit
// target for backwards compatibility with `--run <target>`.
func workspaceCreateRunTarget(args []string, runTarget string) (name, target string) {
	if runTarget == bareRunTargetSentinel && len(args) == 2 {
		return args[0], args[1]
	}
	return args[0], runTarget
}

func (a *App) workspaceCreate(ctx context.Context, name, branch, runTarget string, seed, noStart bool) error {
	if err := workspace.ValidateName(name); err != nil {
		return err
	}
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	runRequested := runTarget != ""
	runName := runTarget
	var inv invocation
	if runRequested {
		prefix := "boxt workspace create " + name + " --run"
		if runTarget == bareRunTargetSentinel {
			inv, err = resolveRun(p.Config, nil, -1, false, prefix)
			runName = p.Config.Run.Default
		} else {
			inv, err = resolveRun(p.Config, []string{runTarget}, -1, false, prefix)
		}
		if err != nil {
			return err
		}
	}
	mgr, _, err := a.workspaceManager(p)
	if err != nil {
		return err
	}
	if err := a.ensureHooksTrusted(p, config.HookWorkspaceCreate); err != nil {
		return err
	}
	ws, err := mgr.Create(ctx, name, branch)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Created workspace %s.\n", ws.Name)
	fmt.Fprintf(a.Out, "  Host:   %s\n", ws.HostPath)
	fmt.Fprintf(a.Out, "  Guest:  %s\n", ws.GuestPath)
	fmt.Fprintf(a.Out, "  Branch: %s\n", ws.Branch)
	if seed {
		result, err := mgr.SeedWithRules(ctx, name, p.Config.SeedRules())
		if err != nil {
			return err
		}
		a.printSeedResult(result)
	}
	allowStart := !noStart && p.Config.VM.AutoStartEnabled()
	if err := a.runHookPhase(ctx, p, config.HookWorkspaceCreate, "pre", ws, allowStart, false); err != nil {
		rollbackErr := mgr.Rollback(ctx, ws)
		if rollbackErr != nil {
			return fmt.Errorf("workspace create aborted by pre-hook: %w (rollback failed: %v)", err, rollbackErr)
		}
		return fmt.Errorf("workspace create aborted by pre-hook: %w", err)
	}
	if runRequested {
		fmt.Fprintf(a.Out, "Running target %s in workspace %s...\n", runName, ws.Name)
		if err := a.execute(ctx, p, ws.Name, ws.GuestPath, inv, allowStart); err != nil {
			return err
		}
	}
	if err := a.runHookPhase(ctx, p, config.HookWorkspaceCreate, "post", ws, allowStart, false); err != nil {
		return reportPostError(err)
	}
	return nil
}

func newWorkspaceListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List default and named workspaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.workspaceList(cmd.Context())
		},
	}
}

func (a *App) workspaceList(ctx context.Context) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	mgr, _, err := a.workspaceManager(p)
	if err != nil {
		return err
	}
	isGit, err := mgr.IsGit(ctx)
	if err != nil {
		return err
	}
	named, err := mgr.List(ctx)
	if err != nil {
		return err
	}
	all := append([]workspace.Workspace{mgr.Default(ctx)}, named...)

	w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPATH\tBRANCH\tHEAD\tDIRTY\tAHEAD/BEHIND\tSTATUS")
	for _, ws := range all {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			ws.Name,
			ws.HostPath,
			workspaceBranch(ws),
			workspaceHead(ws),
			workspaceDirty(ws),
			workspaceAheadBehind(ws),
			workspaceStatus(ws),
		)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if !isGit {
		fmt.Fprintln(a.Out, "\nNote: named workspaces require a Git repository.")
	}
	return nil
}

func workspaceBranch(ws workspace.Workspace) string {
	if ws.Branch == "" {
		return "-"
	}
	return ws.Branch
}

func workspaceHead(ws workspace.Workspace) string {
	if ws.Head == "" {
		return "-"
	}
	return ws.Head
}

func workspaceDirty(ws workspace.Workspace) string {
	if ws.Missing || ws.Broken {
		return "-"
	}
	if ws.Dirty {
		return "dirty"
	}
	return "clean"
}

func workspaceAheadBehind(ws workspace.Workspace) string {
	if ws.Default || !ws.Compared {
		return "-"
	}
	return fmt.Sprintf("%d/%d", ws.Ahead, ws.Behind)
}

func workspaceStatus(ws workspace.Workspace) string {
	switch {
	case ws.Missing:
		return "missing"
	case ws.Broken:
		return "broken"
	case ws.Locked:
		return "locked"
	default:
		return "ready"
	}
}

func newWorkspaceMergeCmd(app *App) *cobra.Command {
	var into string
	var remove bool
	cmd := &cobra.Command{
		Use:   "merge name [--into branch] [--remove]",
		Short: "Fast-forward the project root to a workspace branch",
		Long: `Fast-forward the branch checked out in the project root to a workspace branch.

The merge is fast-forward only: boxt never creates merge commits, rebases,
squashes or forces refs. The workspace and project root must be clean (ignored
files may remain). Use --remove to delete the worktree after a successful merge;
the Git branch is always kept.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.workspaceMerge(cmd.Context(), args[0], into, remove)
		},
	}
	cmd.Flags().StringVar(&into, "into", "", "target branch (must be checked out in the project root)")
	cmd.Flags().BoolVar(&remove, "remove", false, "remove the workspace worktree after a successful merge")
	return cmd
}

func (a *App) workspaceMerge(ctx context.Context, name, into string, remove bool) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	mgr, _, err := a.workspaceManager(p)
	if err != nil {
		return err
	}
	if err := a.ensureHooksTrusted(p, config.HookWorkspaceMerge); err != nil {
		return err
	}
	if remove {
		if err := a.ensureHooksTrusted(p, config.HookWorkspaceRemove); err != nil {
			return err
		}
	}
	ws, err := mgr.Get(ctx, name)
	if err != nil {
		return err
	}
	allowStart := p.Config.VM.AutoStartEnabled()
	if err := a.runHookPhase(ctx, p, config.HookWorkspaceMerge, "pre", ws, allowStart, false); err != nil {
		return fmt.Errorf("workspace merge aborted by pre-hook: %w", err)
	}
	result, err := mgr.Merge(ctx, name, into, false)
	if err != nil {
		return err
	}
	if result.AlreadyCurrent {
		fmt.Fprintf(a.Out, "Already up to date: %s is at %s.\n", result.TargetBranch, shortHead(result.NewHead))
	} else {
		fmt.Fprintf(a.Out, "Fast-forwarded %s to %s.\n", result.TargetBranch, result.SourceBranch)
		fmt.Fprintf(a.Out, "  HEAD: %s -> %s\n", shortHead(result.OldHead), shortHead(result.NewHead))
	}
	if err := a.runHookPhase(ctx, p, config.HookWorkspaceMerge, "post", ws, allowStart, false); err != nil {
		return reportPostError(err)
	}
	if !remove {
		return nil
	}
	if err := a.workspaceRemove(ctx, name, false); err != nil {
		var ignored *workspace.ErrIgnoredFiles
		var dirty *workspace.ErrDirty
		if errors.As(err, &ignored) || errors.As(err, &dirty) {
			fmt.Fprintf(a.Err, "Warning: merge succeeded, but workspace %s was kept: %v\n", name, err)
			return nil
		}
		return err
	}
	return nil
}

func shortHead(head string) string {
	head = strings.TrimSpace(head)
	if len(head) > 7 {
		return head[:7]
	}
	return head
}

func newWorkspaceRemoveCmd(app *App) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "remove name",
		Short: "Remove a named workspace without deleting its branch",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.workspaceRemove(cmd.Context(), args[0], force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "discard uncommitted changes (the Git branch is still kept)")
	return cmd
}

func (a *App) workspaceRemove(ctx context.Context, name string, force bool) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	mgr, _, err := a.workspaceManager(p)
	if err != nil {
		return err
	}
	if err := a.ensureHooksTrusted(p, config.HookWorkspaceRemove); err != nil {
		return err
	}
	set := p.Config.HooksFor(config.HookWorkspaceRemove)
	hasHooks := len(set.Pre) > 0 || len(set.Post) > 0
	var ws workspace.Workspace
	if hasHooks {
		ws, err = mgr.Get(ctx, name)
		if err != nil {
			return err
		}
		allowStart := p.Config.VM.AutoStartEnabled()
		if err := a.runHookPhase(ctx, p, config.HookWorkspaceRemove, "pre", ws, allowStart, false); err != nil {
			return fmt.Errorf("workspace remove aborted by pre-hook: %w", err)
		}
	}
	removed, err := mgr.RemoveWithOptions(ctx, name, workspace.RemoveOptions{
		Force: force,
		Seed:  p.Config.SeedRules(),
	})
	if err != nil {
		return err
	}
	if removed.Missing {
		fmt.Fprintf(a.Out, "Pruned stale workspace metadata for %s.\n", removed.Name)
		return nil
	}
	fmt.Fprintf(a.Out, "Removed workspace %s.\n", removed.Name)
	if removed.Branch != "" {
		fmt.Fprintf(a.Out, "Git branch %s was kept.\n", removed.Branch)
	}
	if hasHooks {
		allowStart := p.Config.VM.AutoStartEnabled()
		if err := a.runHookPhase(ctx, p, config.HookWorkspaceRemove, "post", removed, allowStart, true); err != nil {
			return reportPostError(err)
		}
	}
	return nil
}

func newWorkspacePathCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "path name",
		Short: "Print the host path of a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.workspacePath(cmd.Context(), args[0])
		},
	}
}

func (a *App) workspacePath(ctx context.Context, name string) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	mgr, _, err := a.workspaceManager(p)
	if err != nil {
		return err
	}
	ws, err := mgr.Get(ctx, name)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Out, ws.HostPath)
	return nil
}

func newWorkspaceSeedCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "seed name",
		Short: "Copy ignored files from the default workspace into a workspace",
		Long: `Copy ignored files (for example .env, node_modules or caches) from the default
workspace into a named workspace. Tracked files are never copied, existing target
paths are never overwritten, and symlinks or special files are skipped.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.workspaceSeed(cmd.Context(), args[0])
		},
	}
}

func (a *App) workspaceSeed(ctx context.Context, name string) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	mgr, _, err := a.workspaceManager(p)
	if err != nil {
		return err
	}
	result, err := mgr.SeedWithRules(ctx, name, p.Config.SeedRules())
	if err != nil {
		return err
	}
	a.printSeedResult(result)
	return nil
}

func (a *App) printSeedResult(result workspace.SeedResult) {
	fmt.Fprintf(a.Out, "Seeded %d ignored file(s).\n", result.Copied)
	if len(result.Conflicts) > 0 {
		fmt.Fprintf(a.Err, "Skipped %d existing path(s): %s\n", len(result.Conflicts), strings.Join(result.Conflicts, ", "))
	}
	for _, warning := range result.Skipped {
		fmt.Fprintf(a.Err, "Warning: %s\n", warning)
	}
}

func newWorkspaceRunCmd(app *App) *cobra.Command {
	var noStart bool
	cmd := &cobra.Command{
		Use:   "run name [target] [-- args...] [-- command [args...]]",
		Short: "Run a target or arbitrary command in a workspace",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.workspaceRun(cmd.Context(), args, cmd.ArgsLenAtDash(), noStart)
		},
	}
	cmd.Flags().BoolVar(&noStart, "no-start", false, "do not create or start the VM automatically")
	return cmd
}

func (a *App) workspaceRun(ctx context.Context, args []string, dash int, noStart bool) error {
	name, rest, restDash, err := workspaceRunArgs(args, dash)
	if err != nil {
		return err
	}
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	mgr, _, err := a.workspaceManager(p)
	if err != nil {
		return err
	}
	ws, err := mgr.Get(ctx, name)
	if err != nil {
		return err
	}
	inv, err := resolveRun(p.Config, rest, restDash, a.interactiveTerminal(), "boxt workspace run "+name)
	if err != nil {
		return err
	}
	return a.execute(ctx, p, ws.Name, ws.GuestPath, inv, !noStart && p.Config.VM.AutoStartEnabled())
}

func workspaceRunArgs(args []string, dash int) (name string, rest []string, restDash int, err error) {
	if len(args) == 0 {
		return "", nil, -1, errors.New("workspace name is required")
	}
	name = args[0]
	rest = args[1:]
	restDash = -1
	if dash >= 0 {
		if dash == 0 {
			return "", nil, -1, errors.New("workspace name is required before `--`")
		}
		restDash = dash - 1
	}
	return name, rest, restDash, nil
}

func newWorkspaceShellCmd(app *App) *cobra.Command {
	var noStart bool
	cmd := &cobra.Command{
		Use:   "shell name",
		Short: "Open an interactive shell in a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.workspaceShell(cmd.Context(), args[0], noStart)
		},
	}
	cmd.Flags().BoolVar(&noStart, "no-start", false, "do not create or start the VM automatically")
	return cmd
}

func (a *App) workspaceShell(ctx context.Context, name string, noStart bool) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	mgr, _, err := a.workspaceManager(p)
	if err != nil {
		return err
	}
	ws, err := mgr.Get(ctx, name)
	if err != nil {
		return err
	}
	return a.execute(ctx, p, ws.Name, ws.GuestPath, a.shellInvocation(p), !noStart && p.Config.VM.AutoStartEnabled())
}
