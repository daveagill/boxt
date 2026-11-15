package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/boxt/boxt/internal/lima"
	"github.com/boxt/boxt/internal/platform"
	"github.com/boxt/boxt/internal/project"
)

type projectPaths struct {
	DataDir       string
	WorkspacesDir string
	VMDir         string
	LimaConfig    string
}

func (a *App) pathsFor(p *project.Project) (projectPaths, error) {
	dataDir, err := a.DataDir()
	if err != nil {
		return projectPaths{}, err
	}
	vmDir := platform.VMsDir(dataDir, p.ID)
	return projectPaths{
		DataDir:       dataDir,
		WorkspacesDir: platform.WorkspacesDir(dataDir, p.ID),
		VMDir:         vmDir,
		LimaConfig:    filepath.Join(vmDir, lima.ConfigFileName),
	}, nil
}

func (a *App) requireLima() error {
	if err := platform.CheckSupported(); err != nil {
		return err
	}
	return platform.CheckLima(runtime.GOOS, a.LookPath)
}

func newUpCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Create and start the project VM",
		Long:  "Create the project VM if needed, start it, reconcile declared tools, and report when the environment is ready.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.up(cmd.Context())
		},
	}
}

func (a *App) up(ctx context.Context) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if err := a.requireLima(); err != nil {
		return err
	}
	paths, err := a.pathsFor(p)
	if err != nil {
		return err
	}
	if _, err := a.startVM(ctx, p, paths); err != nil {
		return err
	}
	if a.Provision != nil {
		if err := a.Provision(ctx, p); err != nil {
			return fmt.Errorf("VM %s is running, but provisioning failed: %w", p.VMName(), err)
		}
	}
	fmt.Fprintf(a.Out, "Environment ready: %s (%s)\n", p.VMName(), p.Root)
	return nil
}

// startVM ensures the workspace root exists, writes the generated Lima config,
// and creates or starts the VM. It is idempotent and leaves a running VM alone.
func (a *App) startVM(ctx context.Context, p *project.Project, paths projectPaths) (bool, error) {
	if err := lima.CheckPorts(p.Config.VM.Ports); err != nil {
		return false, err
	}
	if err := platform.EnsureDir(paths.WorkspacesDir, 0o755); err != nil {
		return false, err
	}
	generated, err := lima.GenerateConfig(lima.VMOptions{
		ProjectID:     p.ID,
		ProjectRoot:   p.Root,
		WorkspacesDir: paths.WorkspacesDir,
		CPUs:          p.Config.VM.CPUs,
		Memory:        p.Config.VM.Memory,
		Ports:         p.Config.VM.Ports,
	})
	if err != nil {
		return false, err
	}
	if _, err := lima.WriteConfig(paths.VMDir, generated); err != nil {
		return false, err
	}

	name := p.VMName()
	status, err := a.Lima.Inspect(ctx, name)
	if err != nil {
		return false, err
	}
	switch {
	case !status.Exists:
		fmt.Fprintf(a.Out, "Creating VM %s...\n", name)
		if err := a.Lima.StartWithConfig(ctx, name, paths.LimaConfig); err != nil {
			return false, err
		}
		return true, nil
	case status.Running:
		fmt.Fprintf(a.Out, "VM %s is already running.\n", name)
		return false, nil
	default:
		fmt.Fprintf(a.Out, "Starting VM %s...\n", name)
		if err := a.Lima.Start(ctx, name); err != nil {
			return false, err
		}
		return true, nil
	}
}

func newDownCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "Stop the project VM without deleting its state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.down(cmd.Context())
		},
	}
}

func (a *App) down(ctx context.Context) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if err := a.requireLima(); err != nil {
		return err
	}
	name := p.VMName()
	status, err := a.Lima.Inspect(ctx, name)
	if err != nil {
		return err
	}
	if !status.Exists {
		fmt.Fprintf(a.Out, "VM %s does not exist.\n", name)
		return nil
	}
	if !status.Running {
		fmt.Fprintf(a.Out, "VM %s is already stopped.\n", name)
		return nil
	}
	if err := a.Lima.Stop(ctx, name); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "VM %s stopped. Guest state is preserved.\n", name)
	return nil
}

func newDestroyCmd(app *App) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "Delete the project VM and its guest state",
		Long:  "Delete the project VM and all guest state. Project files, configuration and Git worktrees are never deleted.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.destroy(cmd.Context(), yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "confirm destruction without an interactive prompt")
	return cmd
}

func (a *App) destroy(ctx context.Context, yes bool) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if err := a.requireLima(); err != nil {
		return err
	}
	name := p.VMName()
	status, err := a.Lima.Inspect(ctx, name)
	if err != nil {
		return err
	}
	if !status.Exists {
		fmt.Fprintf(a.Out, "VM %s does not exist.\n", name)
		return nil
	}
	if !yes {
		prompt := fmt.Sprintf(
			"Destroy VM %s and all of its guest state?\nProject files and Git workspaces will be kept.\nType 'yes' to confirm: ",
			name,
		)
		if a.Confirm == nil {
			return errors.New("confirmation is unavailable; re-run with --yes")
		}
		ok, err := a.Confirm(prompt)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("destroy cancelled; nothing was deleted")
		}
	}
	if err := a.Lima.Delete(ctx, name); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Destroyed VM %s. Project files and Git workspaces were not deleted.\n", name)
	return nil
}

func newStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show project, VM and workspace information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.status(cmd.Context())
		},
	}
}

func (a *App) status(ctx context.Context) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if err := a.requireLima(); err != nil {
		return err
	}
	paths, err := a.pathsFor(p)
	if err != nil {
		return err
	}
	name := p.VMName()
	status, err := a.Lima.Inspect(ctx, name)
	if err != nil {
		return err
	}

	vmState := "not created"
	if status.Exists {
		vmState = strings.ToLower(status.Instance.Status)
		if status.Instance.VMType != "" {
			vmState += fmt.Sprintf(" (%s/%s)", status.Instance.VMType, status.Instance.Arch)
		}
	}
	fmt.Fprintf(a.Out, "Project:           %s\n", p.Root)
	fmt.Fprintf(a.Out, "Project ID:        %s\n", p.ID)
	fmt.Fprintf(a.Out, "Config:            %s\n", p.ConfigPath)
	fmt.Fprintf(a.Out, "VM:                %s (%s)\n", name, vmState)
	fmt.Fprintf(a.Out, "Guest image:       %s\n", p.Config.VM.Image)
	fmt.Fprintf(a.Out, "Resources:         %d CPUs, %s memory\n", p.Config.VM.CPUs, p.Config.VM.Memory)
	fmt.Fprintf(a.Out, "Project mount:     %s (host %s)\n", lima.GuestProjectPath, p.Root)
	fmt.Fprintf(a.Out, "Workspaces mount:  %s (host %s)\n", lima.WorkspacesGuestDir(p.ID), paths.WorkspacesDir)

	tools := "none declared"
	if names := p.Config.ToolNames(); len(names) > 0 {
		parts := make([]string, 0, len(names))
		for _, n := range names {
			parts = append(parts, n+" "+p.Config.Tools[n])
		}
		tools = strings.Join(parts, ", ")
	}
	fmt.Fprintf(a.Out, "Tools:             %s\n", tools)
	return nil
}

// ensureVMForExecution starts a stopped VM for a run command. A VM that was
// never created is an error; boxt does not implicitly initialize projects.
func (a *App) ensureVMForExecution(ctx context.Context, p *project.Project, allowStart bool) error {
	name := p.VMName()
	status, err := a.Lima.Inspect(ctx, name)
	if err != nil {
		return err
	}
	if !status.Exists {
		return &vmNotCreatedError{Name: name}
	}
	if status.Running {
		return nil
	}
	if !allowStart {
		return &vmStoppedError{Name: name}
	}
	fmt.Fprintf(a.Err, "Starting VM %s...\n", name)
	return a.Lima.Start(ctx, name)
}

type vmNotCreatedError struct{ Name string }

func (e *vmNotCreatedError) Error() string { return fmt.Sprintf("VM %s does not exist", e.Name) }
func (e *vmNotCreatedError) Hint() string {
	return "Run `boxt up` to create and start the project VM."
}

type vmStoppedError struct{ Name string }

func (e *vmStoppedError) Error() string { return fmt.Sprintf("VM %s is stopped", e.Name) }
func (e *vmStoppedError) Hint() string {
	return "Run `boxt up` to start it, or omit --no-start."
}
