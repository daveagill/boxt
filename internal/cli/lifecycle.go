package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/lima"
	"github.com/daveagill/boxt/internal/platform"
	"github.com/daveagill/boxt/internal/project"
	"github.com/daveagill/boxt/internal/workspace"
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
	paths := projectPaths{
		DataDir:       dataDir,
		WorkspacesDir: platform.WorkspacesDir(dataDir, p.ID),
		VMDir:         vmDir,
		LimaConfig:    filepath.Join(vmDir, lima.ConfigFileName),
	}
	a.warnWSLPath(paths.WorkspacesDir)
	return paths, nil
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
		fmt.Fprintln(a.Out, "Provisioning toolchain...")
		report, err := a.Provision(ctx, p)
		if report != nil {
			printProvisionReport(a, report)
		}
		if err != nil {
			return fmt.Errorf("VM %s is running, but provisioning failed: %w", p.VMName(), err)
		}
	}
	fmt.Fprintf(a.Out, "Environment ready: %s (%s)\n", p.VMName(), p.Root)
	return nil
}

// startVM ensures the workspace root exists, writes the generated Lima config,
// and creates or starts the VM. It is idempotent and leaves a running VM alone.
func (a *App) startVM(ctx context.Context, p *project.Project, paths projectPaths) (bool, error) {
	name := p.VMName()
	status, err := a.Lima.Inspect(ctx, name)
	if err != nil {
		return false, err
	}
	if !status.Running {
		// A running VM owns its forwarded ports; checking them would report a
		// false conflict with Lima's own forwarder.
		if err := lima.CheckPorts(p.Config.VM.Ports); err != nil {
			return false, err
		}
	}
	if err := platform.EnsureDir(paths.WorkspacesDir, 0o755); err != nil {
		return false, err
	}
	opts := a.vmOptions(p, paths)
	generated, err := lima.GenerateConfig(opts)
	if err != nil {
		return false, err
	}
	if _, err := lima.WriteConfig(paths.VMDir, generated); err != nil {
		return false, err
	}
	stored, hasStored, err := readFingerprint(paths)
	if err != nil {
		return false, err
	}
	if status.Exists && hasStored && stored != lima.Fingerprint(opts) {
		return false, &configDriftError{VM: name}
	}
	if err := lima.WriteFingerprint(paths.VMDir, lima.Fingerprint(opts)); err != nil {
		return false, err
	}
	if status.Exists {
		warnResourceDrift(a, p, status.Instance)
	}
	if !status.Running {
		if warning := platform.VirtualizationWarning(runtime.GOOS); warning != "" {
			fmt.Fprintf(a.Err, "Warning: %s\n", warning)
		}
	}
	started := false
	switch {
	case !status.Exists:
		fmt.Fprintf(a.Out, "Creating VM %s...\n", name)
		if err := a.Lima.StartWithConfig(ctx, name, paths.LimaConfig); err != nil {
			return false, err
		}
		started = true
	case status.Running:
		fmt.Fprintf(a.Out, "VM %s is already running.\n", name)
	default:
		fmt.Fprintf(a.Out, "Starting VM %s...\n", name)
		if err := a.Lima.Start(ctx, name); err != nil {
			return false, err
		}
		started = true
	}
	return started, nil
}

func (a *App) vmOptions(p *project.Project, paths projectPaths) lima.VMOptions {
	return lima.VMOptions{
		ProjectID:      p.ID,
		ProjectRoot:    p.Root,
		WorkspacesDir:  paths.WorkspacesDir,
		CPUs:           p.Config.VM.CPUs,
		Memory:         p.Config.VM.Memory,
		PortForwarding: p.Config.VM.PortForwarding,
		Ports:          p.Config.VM.Ports,
	}
}

func readFingerprint(paths projectPaths) (string, bool, error) {
	data, err := os.ReadFile(filepath.Join(paths.VMDir, lima.FingerprintFileName))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read VM configuration fingerprint: %w", err)
	}
	fp := strings.TrimSpace(string(data))
	return fp, fp != "", nil
}

func (a *App) checkConfigDrift(p *project.Project, paths projectPaths) error {
	stored, ok, err := readFingerprint(paths)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if stored != lima.Fingerprint(a.vmOptions(p, paths)) {
		return &configDriftError{VM: p.VMName()}
	}
	return nil
}

type configDriftError struct{ VM string }

func (e *configDriftError) Error() string {
	return fmt.Sprintf("VM %s was created with a different configuration", e.VM)
}
func (e *configDriftError) Hint() string {
	return "VM settings such as cpu, memory, ports and forwarding mode are applied only at creation.\nRun `boxt destroy --yes` and `boxt up` to apply the change."
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

func warnResourceDrift(a *App, p *project.Project, inst lima.Instance) {
	var changes []string
	if inst.CPUs > 0 && inst.CPUs != p.Config.VM.CPUs {
		changes = append(changes, fmt.Sprintf("cpus %d -> %d", inst.CPUs, p.Config.VM.CPUs))
	}
	if inst.Memory > 0 {
		if want, err := config.MemoryBytes(p.Config.VM.Memory); err == nil && want != inst.Memory {
			changes = append(changes, fmt.Sprintf("memory %s -> %s", formatBytes(inst.Memory), p.Config.VM.Memory))
		}
	}
	if len(changes) == 0 {
		return
	}
	fmt.Fprintf(a.Err, "Warning: VM %s was created with different resources (%s).\nRun `boxt destroy` and `boxt up` to apply the change.\n", p.VMName(), strings.Join(changes, ", "))
}

func formatBytes(n int64) string {
	const gib = 1024 * 1024 * 1024
	if n%gib == 0 {
		return fmt.Sprintf("%dGiB", n/gib)
	}
	const mib = 1024 * 1024
	if n%mib == 0 {
		return fmt.Sprintf("%dMiB", n/mib)
	}
	return fmt.Sprintf("%dB", n)
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
	if err := platform.CheckSupported(); err != nil {
		return err
	}
	paths, err := a.pathsFor(p)
	if err != nil {
		return err
	}
	name := p.VMName()
	vmState := "unknown - Lima not installed"
	limaAvailable := true
	lookPath := a.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if err := platform.CheckLima(runtime.GOOS, lookPath); err != nil {
		limaAvailable = false
	}
	var status lima.Status
	if limaAvailable {
		status, err = a.Lima.Inspect(ctx, name)
		if err != nil {
			return err
		}
		vmState = "not created"
		if status.Exists {
			switch status.Instance.Status {
			case "Running":
				vmState = "running"
			case "Stopped":
				vmState = "stopped"
			case "Broken", "Uninitialized", "":
				vmState = "broken"
			default:
				vmState = strings.ToLower(status.Instance.Status)
			}
			if status.Instance.VMType != "" {
				vmState += fmt.Sprintf(", %s/%s", status.Instance.VMType, status.Instance.Arch)
			}
		}
	}
	drift := false
	if status.Exists {
		if err := a.checkConfigDrift(p, paths); err != nil {
			var driftErr *configDriftError
			if !errors.As(err, &driftErr) {
				return err
			}
			drift = true
		}
	}
	resources := fmt.Sprintf("%d CPUs, %s memory", p.Config.VM.CPUs, p.Config.VM.Memory)
	if status.Exists && status.Instance.CPUs > 0 && status.Instance.Memory > 0 {
		actual := fmt.Sprintf("%d CPUs, %s memory", status.Instance.CPUs, formatBytes(status.Instance.Memory))
		if actual != resources {
			resources = fmt.Sprintf("%s (configured: %s)", actual, resources)
		}
	}
	fmt.Fprintf(a.Out, "Project:           %s\n", p.Root)
	fmt.Fprintf(a.Out, "Project ID:        %s\n", p.ID)
	fmt.Fprintf(a.Out, "Config:            %s\n", p.ConfigPath)
	fmt.Fprintf(a.Out, "VM:                %s (%s)\n", name, vmState)
	fmt.Fprintf(a.Out, "Guest image:       %s\n", p.Config.VM.Image)
	fmt.Fprintf(a.Out, "Resources:         %s\n", resources)
	fmt.Fprintf(a.Out, "Port forwarding:   %s\n", p.Config.VM.PortForwarding)
	autoStart := "disabled"
	if p.Config.VM.AutoStartEnabled() {
		autoStart = "enabled"
	}
	fmt.Fprintf(a.Out, "Auto-up:           %s\n", autoStart)
	if drift {
		fmt.Fprintf(a.Out, "Config drift:      run `boxt destroy --yes` and `boxt up` to apply changed VM settings\n")
	}
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

	mgr := workspace.New(p.Root, p.ID, paths.WorkspacesDir, a.Git)
	isGit, gitErr := mgr.IsGit(ctx)
	named, listErr := mgr.List(ctx)
	if listErr != nil {
		named = nil
	}
	fmt.Fprintln(a.Out, "Workspaces:")
	w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  NAME\tPATH\tBRANCH\tHEAD\tDIRTY\tAHEAD/BEHIND\tSTATUS")
	for _, ws := range append([]workspace.Workspace{mgr.Default(ctx)}, named...) {
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\n",
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
	if gitErr == nil && !isGit {
		fmt.Fprintln(a.Out, "  note: named workspaces require a Git repository.")
	}
	if listErr != nil {
		fmt.Fprintf(a.Out, "  note: named workspaces unavailable: %v\n", listErr)
	}
	return nil
}

// ensureVMForExecution ensures the VM is running. When allowed, a stopped or
// missing VM is created/started and provisioned first. A VM that was never
// created is not created when auto-start is disabled.
func (a *App) ensureVMForExecution(ctx context.Context, p *project.Project, allowStart bool) error {
	name := p.VMName()
	status, err := a.Lima.Inspect(ctx, name)
	if err != nil {
		return err
	}
	paths, err := a.pathsFor(p)
	if err != nil {
		return err
	}
	if status.Exists {
		if err := a.checkConfigDrift(p, paths); err != nil {
			return err
		}
	}
	if status.Running {
		return nil
	}
	if !allowStart {
		if !status.Exists {
			return &vmNotCreatedError{Name: name}
		}
		return &vmStoppedError{Name: name}
	}
	if _, err := a.startVM(ctx, p, paths); err != nil {
		return err
	}
	if a.Provision != nil {
		fmt.Fprintln(a.Err, "Provisioning toolchain...")
		report, err := a.Provision(ctx, p)
		if report != nil {
			printProvisionReport(a, report)
		}
		if err != nil {
			return fmt.Errorf("VM %s was started, but provisioning failed: %w", name, err)
		}
	}
	return nil
}

type vmNotCreatedError struct{ Name string }

func (e *vmNotCreatedError) Error() string { return fmt.Sprintf("VM %s does not exist", e.Name) }
func (e *vmNotCreatedError) Hint() string {
	return "Run `boxt up` to create and start the project VM, or omit --no-start (and ensure vm.auto_start is enabled)."
}

type vmStoppedError struct{ Name string }

func (e *vmStoppedError) Error() string { return fmt.Sprintf("VM %s is stopped", e.Name) }
func (e *vmStoppedError) Hint() string {
	return "Run `boxt up` to start it, or omit --no-start (and ensure vm.auto_start is enabled)."
}
