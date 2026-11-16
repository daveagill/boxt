package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/daveagill/boxt/internal/doctor"
	"github.com/daveagill/boxt/internal/lima"
	"github.com/daveagill/boxt/internal/project"
)

func newDoctorCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check host prerequisites and project configuration",
		Long: `Check host prerequisites and project configuration without installing anything.

The command exits 0 when every check is OK or WARN, and 1 when any check fails.
It never starts or creates the project VM.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.doctor(cmd.Context())
		},
	}
}

func (a *App) doctor(ctx context.Context) error {
	deps := doctor.DefaultDeps()
	if a.LookPath != nil {
		deps.LookPath = a.LookPath
	}
	if a.Runner != nil {
		deps.Runner = a.Runner
	}
	if a.DataDir != nil {
		deps.DataDir = a.DataDir
	}
	getwd := a.Getwd
	if getwd == nil {
		getwd = os.Getwd
	}
	deps.LookupProject = func() (*project.Project, error) {
		dir, err := getwd()
		if err != nil {
			return nil, fmt.Errorf("determine working directory: %w", err)
		}
		return project.Lookup(dir)
	}
	deps.CheckPorts = func(ctx context.Context, p *project.Project) error {
		if a.Lima != nil {
			status, err := a.Lima.Inspect(ctx, p.VMName())
			if err != nil {
				return err
			}
			if status.Running {
				// The running VM owns its forwarded host ports.
				return nil
			}
		}
		return lima.CheckPorts(p.Config.VM.Ports)
	}

	report := doctor.Run(ctx, deps)
	if _, err := fmt.Fprint(a.Out, doctor.Format(report, a.Verbose)); err != nil {
		return err
	}
	if report.Failed() {
		return errors.New("doctor found problems; fix the FAIL items above")
	}
	return nil
}
