package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	cmd.AddCommand(newWorkspaceRebaseCmd(app))
	cmd.AddCommand(newWorkspaceRemoveCmd(app))
	cmd.AddCommand(newWorkspacePathCmd(app))
	cmd.AddCommand(newWorkspaceRunCmd(app))
	cmd.AddCommand(newWorkspaceSeedCmd(app))
	cmd.AddCommand(newWorkspaceShellCmd(app))
	cmd.AddCommand(newWorkspaceSwitchCmd(app))
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
	switch ws.Operation {
	case "rebase":
		return "rebasing"
	case "merge", "cherry-pick":
		return ws.Operation + " in progress"
	}
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
	var rebase bool
	var noRebase bool
	cmd := &cobra.Command{
		Use:   "merge name [--into branch] [--rebase | --no-rebase] [--remove]",
		Short: "Integrate a workspace branch into the project root branch",
		Long: `Integrate a workspace branch into the branch checked out in the project root.

By default the merge is fast-forward only. With --rebase, or workspace.merge:
rebase in .boxt/config.yaml, the workspace branch is rebased onto the target
first and then fast-forwarded. boxt never creates merge commits, squashes or
forces refs. The workspace and project root must be clean (ignored files may
remain). Use --remove to delete the worktree after a successful merge; the Git
branch is always kept.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: app.completeWorkspaceNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if rebase && noRebase {
				return errors.New("--rebase and --no-rebase are mutually exclusive")
			}
			return app.workspaceMerge(cmd.Context(), args[0], into, remove, rebase, noRebase)
		},
	}
	cmd.Flags().StringVar(&into, "into", "", "target branch (must be checked out in the project root)")
	cmd.Flags().BoolVar(&rebase, "rebase", false, "rebase the workspace branch onto the target before fast-forwarding")
	cmd.Flags().BoolVar(&noRebase, "no-rebase", false, "fast-forward only, overriding workspace.merge")
	cmd.Flags().BoolVar(&remove, "remove", false, "remove the workspace worktree after a successful merge")
	return cmd
}

func (a *App) workspaceMerge(ctx context.Context, name, into string, remove, rebase, noRebase bool) error {
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
	mode := p.Config.WorkspaceMergeMode()
	switch {
	case rebase:
		mode = config.MergeModeRebase
	case noRebase:
		mode = config.MergeModeFF
	}
	ws, err := mgr.Get(ctx, name)
	if err != nil {
		return err
	}
	allowStart := p.Config.VM.AutoStartEnabled()
	if mode == config.MergeModeRebase {
		// Fail before rewriting the workspace branch if the merge could not
		// complete, for example because the project root is dirty.
		if err := mgr.RequireCleanRoot(ctx); err != nil {
			return err
		}
		result, err := mgr.Rebase(ctx, name, into)
		if err != nil {
			return err
		}
		if !result.AlreadyCurrent {
			printRebaseResult(a.Out, result)
		}
	}
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
	if err := a.workspaceRemove(ctx, name, false, false); err != nil {
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

func newWorkspaceRebaseCmd(app *App) *cobra.Command {
	var into string
	var cont bool
	var abort bool
	cmd := &cobra.Command{
		Use:   "rebase name [--into branch] [--continue | --abort]",
		Short: "Replay a workspace branch onto the project root branch",
		Long: `Replay the named workspace's commits onto the branch checked out in the
project root (git rebase). The target branch is never changed and the worktree
is not removed.

A conflict leaves the rebase open in the workspace. Resolve the files there,
then run --continue; to discard the rebase and restore the previous tip, run
--abort.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: app.completeWorkspaceNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cont && abort {
				return errors.New("--continue and --abort are mutually exclusive")
			}
			if (cont || abort) && into != "" {
				return errors.New("--into cannot be combined with --continue or --abort")
			}
			return app.workspaceRebase(cmd.Context(), args[0], into, cont, abort)
		},
	}
	cmd.Flags().StringVar(&into, "into", "", "target branch (must be checked out in the project root)")
	cmd.Flags().BoolVar(&cont, "continue", false, "continue a rebase after resolving conflicts")
	cmd.Flags().BoolVar(&abort, "abort", false, "abort an in-progress rebase and restore the previous tip")
	return cmd
}

func (a *App) workspaceRebase(ctx context.Context, name, into string, cont, abort bool) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	mgr, _, err := a.workspaceManager(p)
	if err != nil {
		return err
	}
	switch {
	case cont:
		result, err := mgr.RebaseContinue(ctx, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "Continued rebase of %s.\n", result.SourceBranch)
		fmt.Fprintf(a.Out, "  HEAD: %s -> %s\n", shortHead(result.OldHead), shortHead(result.NewHead))
		return nil
	case abort:
		result, err := mgr.RebaseAbort(ctx, name)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "Aborted rebase of %s; restored %s.\n", result.SourceBranch, shortHead(result.NewHead))
		return nil
	}
	result, err := mgr.Rebase(ctx, name, into)
	if err != nil {
		return err
	}
	if result.AlreadyCurrent {
		fmt.Fprintf(a.Out, "Already up to date: %s is based on %s at %s.\n", result.SourceBranch, result.TargetBranch, shortHead(result.NewHead))
		return nil
	}
	printRebaseResult(a.Out, result)
	return nil
}

func printRebaseResult(out io.Writer, result workspace.RebaseResult) {
	if result.FastForwarded {
		fmt.Fprintf(out, "Moved %s to %s.\n", result.SourceBranch, result.TargetBranch)
	} else {
		fmt.Fprintf(out, "Rebased %s onto %s.\n", result.SourceBranch, result.TargetBranch)
	}
	fmt.Fprintf(out, "  HEAD: %s -> %s\n", shortHead(result.OldHead), shortHead(result.NewHead))
}

func shortHead(head string) string {
	head = strings.TrimSpace(head)
	if len(head) > 7 {
		return head[:7]
	}
	return head
}

func newWorkspaceRemoveCmd(app *App) *cobra.Command {
	var force, dryRun bool
	cmd := &cobra.Command{
		Use:   "remove name",
		Short: "Remove a named workspace without deleting its branch",
		Long: `Remove a named workspace without deleting the Git branch.

With --dry-run, report what removal would do and exit without changing anything.
A dry run never runs lifecycle hooks and does not require them to be trusted.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: app.completeWorkspaceNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.workspaceRemove(cmd.Context(), args[0], force, dryRun)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "discard uncommitted changes (the Git branch is still kept)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be removed without changing anything")
	return cmd
}

func (a *App) workspaceRemove(ctx context.Context, name string, force, dryRun bool) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	mgr, _, err := a.workspaceManager(p)
	if err != nil {
		return err
	}
	opts := workspace.RemoveOptions{Force: force, Seed: p.Config.SeedRules()}
	if dryRun {
		return a.workspaceRemoveDryRun(ctx, p, mgr, name, opts)
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
	removed, err := mgr.RemoveWithOptions(ctx, name, opts)
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

func (a *App) workspaceRemoveDryRun(ctx context.Context, p *project.Project, mgr *workspace.Manager, name string, opts workspace.RemoveOptions) error {
	plan, err := mgr.PlanRemoval(ctx, name, opts)
	if err != nil {
		return err
	}
	set := p.Config.HooksFor(config.HookWorkspaceRemove)

	branch := plan.Branch
	if branch == "" {
		branch = "(detached)"
	}
	fmt.Fprintf(a.Out, "Workspace:  %s\n", plan.Workspace.Name)
	if plan.Missing {
		fmt.Fprintf(a.Out, "Branch:     %s\n", branch)
		fmt.Fprintln(a.Out, "Verdict:    would prune stale workspace metadata")
		a.printDryRunHooks(set)
		fmt.Fprintln(a.Out, "\nNothing was changed.")
		return nil
	}
	fmt.Fprintf(a.Out, "Branch:     %s (kept)\n", branch)

	blockers := plan.Blockers()
	switch {
	case plan.Locked:
		fmt.Fprintln(a.Out, "Verdict:    cannot be removed (locked by Git)")
	case plan.Broken:
		fmt.Fprintln(a.Out, "Verdict:    cannot be removed (broken worktree metadata)")
	case plan.Force:
		fmt.Fprintln(a.Out, "Verdict:    would be removed with --force (changes would be discarded)")
	case len(blockers) > 0:
		fmt.Fprintln(a.Out, "Verdict:    would be refused")
	default:
		fmt.Fprintln(a.Out, "Verdict:    would be removed")
	}

	a.printDryRunPaths("Dirty", plan.Dirty)
	a.printDryRunPaths("Untracked", plan.Untracked)
	switch {
	case plan.IgnoredTotal == 0:
		fmt.Fprintln(a.Out, "Ignored:    none")
	default:
		line := fmt.Sprintf("%s would be deleted", plural(plan.IgnoredTotal, "ignored file"))
		switch {
		case plan.Force:
			line += " (--force discards them)"
		case !plan.AllowRemove:
			line += " (seed.allow_remove is false)"
		case len(plan.IgnoredUnmatched) > 0:
			line += fmt.Sprintf(" (%d do not match seed rules)", len(plan.IgnoredUnmatched))
		}
		fmt.Fprintf(a.Out, "Ignored:    %s\n", line)
	}
	a.printDryRunHooks(set)

	if len(blockers) > 0 {
		fmt.Fprintln(a.Out, "\nBlocked by:")
		for _, b := range blockers {
			fmt.Fprintf(a.Out, "  - %s\n", b)
		}
		fmt.Fprintf(a.Out, "\nUse `boxt workspace remove %s --force` to discard these, or resolve them first.\n", name)
	}
	if len(plan.IgnoredUnmatched) > 0 {
		fmt.Fprintln(a.Out, "\nIgnored paths that do not match seed.include/seed.exclude:")
		printPathList(a.Out, plan.IgnoredUnmatched)
	}
	fmt.Fprintln(a.Out, "\nNothing was changed.")
	return nil
}

func (a *App) printDryRunHooks(set config.HookSet) {
	switch {
	case len(set.Pre) == 0 && len(set.Post) == 0:
		fmt.Fprintln(a.Out, "Hooks:      none")
	default:
		fmt.Fprintf(a.Out, "Hooks:      %d pre, %d post configured; not run in dry-run\n", len(set.Pre), len(set.Post))
	}
}

func (a *App) printDryRunPaths(label string, paths []string) {
	if len(paths) == 0 {
		fmt.Fprintf(a.Out, "%-11s none\n", label+":")
		return
	}
	fmt.Fprintf(a.Out, "%-11s %d\n", label+":", len(paths))
	printPathList(a.Out, paths)
}

func printPathList(out io.Writer, paths []string) {
	const max = 10
	for i, path := range paths {
		if i == max {
			fmt.Fprintf(out, "  ... and %d more\n", len(paths)-max)
			return
		}
		fmt.Fprintf(out, "  %s\n", path)
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func newWorkspacePathCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:               "path name",
		Short:             "Print the host path of a workspace",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: app.completeWorkspaceNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.workspacePath(cmd.Context(), args[0])
		},
	}
}

// errSwitchNeedsShell explains that `workspace switch` requires the shell
// function from `boxt shell-init`: a child process cannot change its parent
// shell's working directory.
type errSwitchNeedsShell struct{ name string }

func (e *errSwitchNeedsShell) Error() string {
	return fmt.Sprintf("boxt cannot change the working directory of your shell, so it cannot switch to %q", e.name)
}

func (e *errSwitchNeedsShell) Hint() string {
	return fmt.Sprintf(`Load the shell integration from your shell startup file:

  zsh:  eval "$(boxt shell-init zsh)"
  bash: eval "$(boxt shell-init bash)"
  fish: boxt shell-init fish | source

Then run `+"`boxt workspace switch %[1]s`"+`.

Without the integration, switch for one command with:

  cd "$(boxt workspace path %[1]s)"`, e.name)
}

func newWorkspaceSwitchCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "switch name",
		Short: "Change the current shell's directory to a workspace (needs shell-init)",
		Long: `Change the current shell's directory to a workspace.

A command cannot change its parent shell's working directory, so this only works
through the function printed by ` + "`boxt shell-init`" + `:

  zsh:  eval "$(boxt shell-init zsh)"

Without the integration, use ` + "`cd \"$(boxt workspace path <name>)\"`" + `.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: app.completeWorkspaceNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return &errSwitchNeedsShell{name: args[0]}
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
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: app.completeWorkspaceNames,
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
		Use:               "run name [target] [-- args...] [-- command [args...]]",
		Short:             "Run a target or arbitrary command in a workspace",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: app.completeWorkspaceNames,
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
		Use:               "shell name",
		Short:             "Open an interactive shell in a workspace",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: app.completeWorkspaceNames,
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

// completeWorkspaceNames suggests managed workspace names, including the
// implicit default workspace. It is host-only, never starts the VM, and stays
// silent on errors so completion never turns into a failure.
func (a *App) completeWorkspaceNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	directive := cobra.ShellCompDirectiveNoFileComp
	if len(args) > 0 {
		return nil, directive
	}
	p, err := a.loadProject()
	if err != nil {
		return nil, directive
	}
	mgr, _, err := a.workspaceManager(p)
	if err != nil {
		return nil, directive
	}
	list, err := mgr.List(cmd.Context())
	if err != nil {
		return nil, directive
	}
	names := make([]string, 0, 1+len(list))
	names = append(names, workspace.DefaultName)
	for _, ws := range list {
		names = append(names, ws.Name)
	}
	return names, directive
}
