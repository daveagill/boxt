package cli

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

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
	cmd.AddCommand(newWorkspaceRemoveCmd(app))
	cmd.AddCommand(newWorkspacePathCmd(app))
	cmd.AddCommand(newWorkspaceRunCmd(app))
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

func newWorkspaceCreateCmd(app *App) *cobra.Command {
	var branch string
	cmd := &cobra.Command{
		Use:   "create name",
		Short: "Create a named workspace backed by a Git worktree",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.workspaceCreate(cmd.Context(), args[0], branch)
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "", "branch to create or check out (default: boxt/<name>)")
	return cmd
}

func (a *App) workspaceCreate(ctx context.Context, name, branch string) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	mgr, _, err := a.workspaceManager(p)
	if err != nil {
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
	fmt.Fprintln(w, "NAME\tPATH\tBRANCH\tSTATUS")
	for _, ws := range all {
		branch := ws.Branch
		if branch == "" {
			branch = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", ws.Name, ws.HostPath, branch, workspaceStatus(ws))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if !isGit {
		fmt.Fprintln(a.Out, "\nNote: named workspaces require a Git repository.")
	}
	return nil
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
	ws, err := mgr.Remove(ctx, name, force)
	if err != nil {
		return err
	}
	if ws.Missing {
		fmt.Fprintf(a.Out, "Pruned stale workspace metadata for %s.\n", ws.Name)
		return nil
	}
	fmt.Fprintf(a.Out, "Removed workspace %s.\n", ws.Name)
	if ws.Branch != "" {
		fmt.Fprintf(a.Out, "Git branch %s was kept.\n", ws.Branch)
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
