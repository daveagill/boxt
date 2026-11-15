package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/boxt/boxt/internal/config"
	"github.com/boxt/boxt/internal/platform"
	"github.com/boxt/boxt/internal/project"
	"github.com/boxt/boxt/internal/toolchain"
)

// ToolManager is the toolchain seam used by CLI commands and tests.
type ToolManager interface {
	Provision(ctx context.Context, p *project.Project) (*toolchain.Report, error)
	Install(ctx context.Context, p *project.Project, name, version string) (string, error)
	List(ctx context.Context, p *project.Project) ([]toolchain.ToolStatus, error)
}

func newToolCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool",
		Short: "Manage tools installed in the project VM",
		Long:  "Tools are declared in .boxt/config.yaml and installed into the persistent project VM with mise.",
	}
	cmd.AddCommand(newToolListCmd(app))
	cmd.AddCommand(newToolAddCmd(app))
	cmd.AddCommand(newToolRemoveCmd(app))
	return cmd
}

func newToolListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List declared and installed tools",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.toolList(cmd.Context())
		},
	}
}

func (a *App) toolList(ctx context.Context) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	names := p.Config.ToolNames()
	if len(names) == 0 {
		fmt.Fprintln(a.Out, "No tools declared.")
		return nil
	}
	statuses := make([]toolchain.ToolStatus, 0, len(names))
	for _, name := range names {
		statuses = append(statuses, toolchain.ToolStatus{
			Name:     name,
			Declared: p.Config.Tools[name],
			State:    toolchain.StateNotProvisioned,
		})
	}

	limaAvailable := true
	vmRunning := false
	lookPath := a.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if err := platform.CheckLima(runtime.GOOS, lookPath); err != nil {
		limaAvailable = false
	} else {
		status, err := a.Lima.Inspect(ctx, p.VMName())
		if err != nil {
			return err
		}
		vmRunning = status.Exists && status.Running
	}
	if limaAvailable && vmRunning && a.Toolchain != nil {
		statuses, err = a.Toolchain.List(ctx, p)
		if err != nil {
			return fmt.Errorf("inspect installed tools: %w (run `boxt provision` to repair the toolchain)", err)
		}
	}

	w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TOOL\tDECLARED\tINSTALLED\tSTATUS")
	for _, s := range statuses {
		installed := s.Installed
		if installed == "" {
			installed = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.Declared, installed, toolStateLabel(s, limaAvailable, vmRunning))
	}
	return w.Flush()
}

func toolStateLabel(s toolchain.ToolStatus, limaAvailable, vmRunning bool) string {
	if !limaAvailable {
		return "Lima unavailable"
	}
	if !vmRunning {
		return "VM stopped"
	}
	return string(s.State)
}

func newToolAddCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "add name@version",
		Short: "Declare a tool and install it when the VM is running",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.toolAdd(cmd.Context(), args[0])
		},
	}
}

func (a *App) toolAdd(ctx context.Context, spec string) error {
	name, version, err := config.ParseToolSpec(spec)
	if err != nil {
		return err
	}
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	previous, existed := p.Config.Tools[name]
	if err := config.AddTool(p.ConfigPath, name, version); err != nil {
		return err
	}
	// Reload so subsequent operations use the persisted declaration.
	p, err = project.Open(p.Root)
	if err != nil {
		return fmt.Errorf("declaration was saved, but reloading configuration failed: %w", err)
	}
	if existed && previous != version {
		fmt.Fprintf(a.Out, "Updated %s: %s -> %s.\n", name, previous, version)
	} else if existed {
		fmt.Fprintf(a.Out, "Tool %s@%s is already declared.\n", name, version)
	} else {
		fmt.Fprintf(a.Out, "Declared %s@%s.\n", name, version)
	}

	lookPath := a.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if err := platform.CheckLima(runtime.GOOS, lookPath); err != nil {
		fmt.Fprintln(a.Out, "Installation deferred until the VM is available.")
		return nil
	}
	status, err := a.Lima.Inspect(ctx, p.VMName())
	if err != nil {
		return &toolInstallError{Name: name, Version: version, Err: fmt.Errorf("inspect VM: %w", err)}
	}
	if !status.Exists {
		fmt.Fprintln(a.Out, "VM does not exist yet; installation deferred until `boxt up`.")
		return nil
	}
	if !status.Running {
		fmt.Fprintln(a.Out, "VM is stopped; installation deferred until `boxt up`.")
		return nil
	}
	if a.Toolchain == nil {
		return &toolInstallError{Name: name, Version: version, Err: errors.New("toolchain is unavailable")}
	}

	fmt.Fprintf(a.Out, "Installing %s@%s...\n", name, version)
	installed, err := a.Toolchain.Install(ctx, p, name, version)
	if err != nil {
		return &toolInstallError{Name: name, Version: version, Err: err}
	}
	if installed != "" && installed != version {
		fmt.Fprintf(a.Out, "Installed %s@%s (resolved %s).\n", name, version, installed)
	} else {
		fmt.Fprintf(a.Out, "Installed %s@%s.\n", name, version)
	}
	return nil
}

type toolInstallError struct {
	Name    string
	Version string
	Err     error
}

func (e *toolInstallError) Error() string {
	return fmt.Sprintf("tool %s@%s was declared, but installation failed: %v", e.Name, e.Version, e.Err)
}

func (e *toolInstallError) Unwrap() error { return e.Err }

func (e *toolInstallError) Hint() string {
	return "The declaration is saved. Run `boxt provision` to retry installation."
}

func newToolRemoveCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove name",
		Short: "Remove a tool declaration (installed software is kept)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.toolRemove(args[0])
		},
	}
}

func (a *App) toolRemove(name string) error {
	if !config.ValidToolName(name) {
		return fmt.Errorf("invalid tool name %q", name)
	}
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if err := config.RemoveTool(p.ConfigPath, name); err != nil {
		if errors.Is(err, config.ErrToolNotFound) {
			return fmt.Errorf("tool not declared: %s", name)
		}
		return err
	}
	fmt.Fprintf(a.Out, "Removed declaration for %s. Installed software remains in the VM.\n", name)
	return nil
}
