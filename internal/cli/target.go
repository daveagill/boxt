package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/daveagill/boxt/internal/config"
)

func newTargetCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "target",
		Short: "Manage named run targets",
		Long:  "Manage the named run targets in .boxt/config.yaml. This command edits configuration only; use boxt run to execute a target.",
	}
	cmd.AddCommand(newTargetListCmd(app))
	cmd.AddCommand(newTargetAddCmd(app))
	cmd.AddCommand(newTargetRemoveCmd(app))
	cmd.AddCommand(newTargetSetDefaultCmd(app))
	cmd.AddCommand(newTargetUnsetDefaultCmd(app))
	return cmd
}

func newTargetListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured run targets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.targetList()
		},
	}
}

func (a *App) targetList() error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	names := p.Config.TargetNames()
	if len(names) == 0 {
		fmt.Fprintln(a.Out, "No run targets configured. Add one with `boxt target add <name> -- <command>`.")
		return nil
	}
	w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tDEFAULT\tINTERACTIVE\tPORT\tCOMMAND")
	for _, name := range names {
		target := p.Config.Run.Targets[name]
		marker := ""
		if p.Config.Run.Default == name {
			marker = "*"
		}
		port := "-"
		if target.PortBase > 0 {
			env := target.PortEnv
			if env == "" {
				env = "PORT"
			}
			port = fmt.Sprintf("%d (%s)", target.PortBase, env)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			name, marker, strconv.FormatBool(target.Interactive), port, strings.Join(target.Command, " "))
	}
	return w.Flush()
}

func newTargetAddCmd(app *App) *cobra.Command {
	var interactive, setDefault bool
	var portBase int
	var portEnv string
	cmd := &cobra.Command{
		Use:   "add NAME -- command [args...]",
		Short: "Add a run target",
		Long: `Add a run target.

  boxt target add dev -- npm run dev
  boxt target add opencode --interactive -- opencode

Existing targets with a different definition are never overwritten.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			if dash != 1 || len(args) < 2 {
				return errors.New("expected: boxt target add NAME -- command [args...]")
			}
			return app.targetAdd(args[0], config.RunTarget{
				Command:     append([]string(nil), args[dash:]...),
				Interactive: interactive,
				PortBase:    portBase,
				PortEnv:     portEnv,
			}, setDefault)
		},
		ValidArgsFunction: app.completeTargetNames,
	}
	cmd.Flags().BoolVar(&interactive, "interactive", false, "request a PTY when boxt has a terminal")
	cmd.Flags().IntVar(&portBase, "port-base", 0, "inject a deterministic per-workspace port based on this base")
	cmd.Flags().StringVar(&portEnv, "port-env", "", "environment variable for the per-workspace port (default PORT)")
	cmd.Flags().BoolVar(&setDefault, "set-default", false, "also make this the default target")
	return cmd
}

func (a *App) targetAdd(name string, target config.RunTarget, setDefault bool) error {
	if !config.ValidTargetName(name) {
		return fmt.Errorf("invalid target name %q", name)
	}
	if target.PortEnv != "" && target.PortBase == 0 {
		return errors.New("--port-env requires --port-base")
	}
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	_, existed := p.Config.Run.Targets[name]
	if err := config.AddTarget(p.ConfigPath, name, target, setDefault); err != nil {
		return err
	}
	if existed {
		fmt.Fprintf(a.Out, "Run target %s is already present.\n", name)
	} else {
		fmt.Fprintf(a.Out, "Added run target %s.\n", name)
	}
	if setDefault {
		fmt.Fprintf(a.Out, "Set run.default = %s.\n", name)
	}
	return nil
}

func newTargetRemoveCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:               "remove NAME",
		Short:             "Remove a run target",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: app.completeTargetNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.targetRemove(args[0])
		},
	}
}

func (a *App) targetRemove(name string) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	wasDefault, err := config.RemoveTarget(p.ConfigPath, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Removed run target %s.\n", name)
	if wasDefault {
		fmt.Fprintln(a.Out, "Cleared run.default.")
	}
	return nil
}

func newTargetSetDefaultCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:               "set-default NAME",
		Short:             "Make a run target the default",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: app.completeTargetNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.targetSetDefault(args[0])
		},
	}
}

func (a *App) targetSetDefault(name string) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if err := config.SetDefaultTarget(p.ConfigPath, name); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Set run.default = %s.\n", name)
	return nil
}

func newTargetUnsetDefaultCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "unset-default",
		Short: "Clear the default run target",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.targetUnsetDefault()
		},
	}
}

func (a *App) targetUnsetDefault() error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if err := config.UnsetDefaultTarget(p.ConfigPath); err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "Cleared run.default.")
	return nil
}

func (a *App) completeTargetNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	p, err := a.loadProject()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return p.Config.TargetNames(), cobra.ShellCompDirectiveNoFileComp
}
