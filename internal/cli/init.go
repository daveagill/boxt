package cli

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/daveagill/boxt/internal/git"
	"github.com/daveagill/boxt/internal/platform"
	"github.com/daveagill/boxt/internal/preset"
	"github.com/daveagill/boxt/internal/project"
)

func newInitCmd(app *App) *cobra.Command {
	var presets []string
	var defaultTarget string
	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Initialize a boxt project",
		Long: "Create .boxt/config.yaml in the given directory (or the current directory). Existing configuration is never overwritten.\n\n" +
			preset.HelpText() + "\n\nExamples:\n" +
			"  boxt init --preset node\n" +
			"  boxt init --preset opencode\n" +
			"  boxt init --preset node --preset opencode --default-target opencode",
		Args: cobra.MaximumNArgs(1),
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

			resolved, err := preset.Resolve(presets, defaultTarget)
			if err != nil {
				return err
			}
			var p *project.Project
			if len(presets) == 0 && defaultTarget == "" {
				p, err = project.Init(path)
			} else {
				header := "# boxt project configuration.\n" +
					"# Generated with presets: " + strings.Join(resolved.Presets, ", ") + ".\n" +
					"# This file is the source of truth for this project's VM, tools and run targets."
				p, err = project.InitWithConfig(path, resolved.Config, header)
			}
			if err != nil {
				return err
			}

			fmt.Fprintf(app.Out, "Initialized boxt project in %s\n", p.Root)
			if len(resolved.Presets) > 0 {
				fmt.Fprintf(app.Out, "Presets: %s\n", strings.Join(resolved.Presets, ", "))
			}
			if resolved.DefaultTarget != "" {
				fmt.Fprintf(app.Out, "Default target: %s\n", resolved.DefaultTarget)
			}
			if len(resolved.Suggestions) > 1 && resolved.DefaultTarget == "" {
				parts := make([]string, 0, len(resolved.Suggestions))
				for _, s := range resolved.Suggestions {
					parts = append(parts, fmt.Sprintf("%s (%s)", s.Target, s.Preset))
				}
				fmt.Fprintf(app.Out, "Note: presets suggest different default targets: %s.\n", strings.Join(parts, ", "))
				fmt.Fprintln(app.Out, "No default run target was set. Use `boxt run <target>` or set run.default in .boxt/config.yaml.")
			}
			app.warnWSLPath(p.Root)
			printGitNote(cmd, app, p)
			fmt.Fprintf(app.Out, "\nNext: run `boxt up` to create and start the project VM.\n")
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&presets, "preset", nil, "starter preset to compose (repeatable)")
	cmd.Flags().StringVar(&defaultTarget, "default-target", "", "set run.default to this target")
	_ = cmd.RegisterFlagCompletionFunc("preset", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return preset.Names(), cobra.ShellCompDirectiveNoFileComp
	})
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
