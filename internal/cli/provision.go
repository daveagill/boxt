package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/boxt/boxt/internal/toolchain"
)

func newProvisionCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "provision",
		Short: "Reconcile the VM toolchain with .boxt/config.yaml",
		Long:  "Start the VM if needed, ensure mise and system dependencies, then install or correct every declared tool. Safe to run repeatedly.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.provision(cmd.Context())
		},
	}
}

func (a *App) provision(ctx context.Context) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if err := a.requireLima(); err != nil {
		return err
	}
	if a.Toolchain == nil {
		return errors.New("toolchain is unavailable")
	}
	paths, err := a.pathsFor(p)
	if err != nil {
		return err
	}
	if _, err := a.startVM(ctx, p, paths); err != nil {
		return err
	}

	fmt.Fprintln(a.Out, "Provisioning toolchain...")
	report, err := a.Toolchain.Provision(ctx, p)
	if report != nil {
		printProvisionReport(a, report)
	}
	if err != nil {
		return fmt.Errorf("provisioning failed: %w", err)
	}
	fmt.Fprintf(a.Out, "Provisioning complete (mise %s).\n", report.MiseVersion)
	return nil
}

func printProvisionReport(a *App, report *toolchain.Report) {
	for _, t := range report.Tools {
		switch {
		case t.Err != nil:
			fmt.Fprintf(a.Err, "  FAILED %s@%s: %v\n", t.Name, t.Declared, t.Err)
		case t.Installed != "" && t.Installed != t.Declared:
			fmt.Fprintf(a.Out, "  %s %s -> %s\n", t.Name, t.Declared, t.Installed)
		default:
			fmt.Fprintf(a.Out, "  %s %s\n", t.Name, t.Declared)
		}
	}
}
