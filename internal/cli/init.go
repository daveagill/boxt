package cli

import (
	"errors"
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/boxt/boxt/internal/git"
	"github.com/boxt/boxt/internal/platform"
	"github.com/boxt/boxt/internal/project"
)

func newInitCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Initialize a boxt project",
		Long:  "Create .boxt/config.yaml in the given directory (or the current directory). Existing configuration is never overwritten.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			} else {
				cwd, err := app.Getwd()
				if err != nil {
					return fmt.Errorf("determine working directory: %w", err)
				}
				path = cwd
			}
			p, err := project.Init(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(app.Out, "Initialized boxt project in %s\n", p.Root)
			app.warnWSLPath(p.Root)
			printGitNote(cmd, app, p)
			fmt.Fprintf(app.Out, "\nNext: run `boxt up` to create and start the project VM.\n")
			return nil
		},
	}
	return cmd
}

func printGitNote(cmd *cobra.Command, app *App, p *project.Project) {
	lookPath := app.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if err := platform.CheckGit("", lookPath); err != nil {
		fmt.Fprintf(app.Out, "Note: Git was not found. Named workspaces require Git.\n")
		return
	}
	top, err := app.Git.TopLevel(cmd.Context(), p.Root)
	if err != nil {
		if errors.Is(err, git.ErrNotRepository) {
			fmt.Fprintf(app.Out, "Note: this directory is not a Git repository. Named workspaces require Git.\n")
			return
		}
		app.Logger.Debug("Git detection failed", "error", err)
		fmt.Fprintf(app.Out, "Note: could not inspect Git repository (%v).\n", err)
		return
	}
	if top != p.Root {
		fmt.Fprintf(app.Out, "Note: project root is inside the Git repository %s. Named workspaces require the project root to be the repository root.\n", top)
	}
}
