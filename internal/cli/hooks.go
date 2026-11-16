package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/executor"
	"github.com/daveagill/boxt/internal/hooks"
	"github.com/daveagill/boxt/internal/lima"
	"github.com/daveagill/boxt/internal/project"
	"github.com/daveagill/boxt/internal/workspace"
)

func newHooksCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Inspect and approve project lifecycle hooks",
		Long:  "Lifecycle hooks are project-controlled code. They never run until the current hook configuration is explicitly trusted.",
	}
	cmd.AddCommand(newHooksStatusCmd(app))
	cmd.AddCommand(newHooksTrustCmd(app))
	cmd.AddCommand(newHooksClearCmd(app))
	return cmd
}

func newHooksStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether hooks are configured and trusted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.hooksStatus()
		},
	}
}

func newHooksTrustCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "trust",
		Short: "Approve the current hook configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.hooksTrust()
		},
	}
}

func newHooksClearCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove the stored hook approval",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.hooksClear()
		},
	}
}

func (a *App) hooksStatus() error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	paths, err := a.pathsFor(p)
	if err != nil {
		return err
	}
	current, err := hooks.Hash(p.Config)
	if err != nil {
		return err
	}
	trusted, err := hooks.ReadTrusted(paths.DataDir, p.ID)
	if err != nil {
		return err
	}
	configured := p.Config.HasHooks()
	state := "not configured"
	switch {
	case configured && trusted == current:
		state = "trusted"
	case configured:
		state = "untrusted"
	}
	fmt.Fprintf(a.Out, "Hooks:    %s\n", state)
	fmt.Fprintf(a.Out, "Current:  %s\n", current)
	if trusted == "" {
		fmt.Fprintln(a.Out, "Trusted:  not approved")
	} else {
		fmt.Fprintf(a.Out, "Trusted:  %s\n", trusted)
	}
	for _, event := range []string{config.HookWorkspaceCreate, config.HookWorkspaceMerge, config.HookWorkspaceRemove} {
		set := p.Config.HooksFor(event)
		if len(set.Pre) == 0 && len(set.Post) == 0 {
			continue
		}
		fmt.Fprintf(a.Out, "%s:\n", event)
		printHookList(a.Out, "pre", set.Pre)
		printHookList(a.Out, "post", set.Post)
	}
	if configured && state == "untrusted" {
		fmt.Fprintln(a.Out, "\nRun `boxt hooks trust` after reviewing these commands.")
	}
	return nil
}

func printHookList(out io.Writer, phase string, list []config.Hook) {
	for _, hook := range list {
		fmt.Fprintf(out, "  %s: %s (%s)\n", phase, joinCommand(hook.Command), hook.Where)
	}
}

func joinCommand(argv []string) string {
	out := ""
	for i, arg := range argv {
		if i > 0 {
			out += " "
		}
		out += arg
	}
	return out
}

func (a *App) hooksTrust() error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if !p.Config.HasHooks() {
		fmt.Fprintln(a.Out, "No lifecycle hooks are configured; nothing to trust.")
		return nil
	}
	paths, err := a.pathsFor(p)
	if err != nil {
		return err
	}
	current, err := hooks.Hash(p.Config)
	if err != nil {
		return err
	}
	if err := hooks.Trust(paths.DataDir, p.ID, current); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Trusted lifecycle hooks for project %s.\n", p.ID)
	fmt.Fprintf(a.Out, "Approval hash: %s\n", current)
	fmt.Fprintln(a.Out, "Guest hooks run as the boxt user; host hooks run as your host user. Changing hooks invalidates this approval.")
	return nil
}

func (a *App) hooksClear() error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	paths, err := a.pathsFor(p)
	if err != nil {
		return err
	}
	if err := hooks.Clear(paths.DataDir, p.ID); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Cleared hook approval for project %s.\n", p.ID)
	return nil
}

func (a *App) ensureHooksTrusted(p *project.Project, event string) error {
	paths, err := a.pathsFor(p)
	if err != nil {
		return err
	}
	return hooks.EnsureTrusted(paths.DataDir, p.ID, p.Config, event)
}

func (a *App) runHookPhase(ctx context.Context, p *project.Project, event, phase string, ws workspace.Workspace, allowStart, workspaceGone bool) error {
	set := p.Config.HooksFor(event)
	list := set.Pre
	if phase == "post" {
		list = set.Post
	}
	if len(list) == 0 {
		return nil
	}
	hostDir := ws.HostPath
	guestDir := ws.GuestPath
	if workspaceGone {
		hostDir = p.Root
		guestDir = lima.GuestProjectPath
	}
	opts := hooks.Options{
		Env: hooks.Env{
			ProjectRoot:   p.Root,
			Workspace:     ws.Name,
			WorkspacePath: ws.HostPath,
			GuestPath:     ws.GuestPath,
		},
		HostDir:    hostDir,
		GuestDir:   guestDir,
		HostRunner: a.Runner,
		Out:        a.Out,
		Err:        a.Err,
		GuestRunner: func(ctx context.Context, workDir string, argv []string, env []string) error {
			inv := invocation{Command: executor.WithEnv(argv, env...)}
			return a.execute(ctx, p, ws.Name, workDir, inv, allowStart)
		},
	}
	return hooks.Run(ctx, list, opts)
}

// reportPostError keeps the completed operation intact while returning a
// failure for the hook.
func reportPostError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("operation completed, but a post-hook failed: %w", err)
}
