package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/platform"
	"github.com/daveagill/boxt/internal/project"
)

func newMountCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mount",
		Short: "Manage additional host paths mounted into the VM",
		Long:  "Additional mounts are declared in .boxt/config.yaml under vm.mounts and applied when the VM is created.",
	}
	cmd.AddCommand(newMountListCmd(app))
	cmd.AddCommand(newMountAddCmd(app))
	cmd.AddCommand(newMountRemoveCmd(app))
	return cmd
}

func newMountListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List additional host mounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.mountList()
		},
	}
}

func (a *App) mountList() error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if len(p.Config.VM.Mounts) == 0 {
		fmt.Fprintln(a.Out, "No additional mounts configured.")
		return nil
	}
	w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "GUEST\tHOST\tMODE")
	for _, m := range p.Config.VM.Mounts {
		mode := "rw"
		if !m.WritableEnabled() {
			mode = "ro"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", m.GuestPath(), m.Host, mode)
	}
	return w.Flush()
}

func newMountAddCmd(app *App) *cobra.Command {
	var guest string
	var readOnly bool
	cmd := &cobra.Command{
		Use:   "add <host>",
		Short: "Declare an additional host mount",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.mountAdd(cmd.Context(), args[0], guest, readOnly)
		},
	}
	cmd.Flags().StringVar(&guest, "guest", "", "guest mount point (default /mnt/<basename>)")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "mount read-only instead of the default read-write")
	return cmd
}

func (a *App) mountAdd(ctx context.Context, hostArg, guest string, readOnly bool) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	host, err := mountHostPath(a, hostArg)
	if err != nil {
		return err
	}
	if guest == "" {
		guest = path.Join("/mnt", filepath.Base(host))
	} else {
		guest = path.Clean(guest)
	}
	writable := !readOnly
	mount := config.Mount{Host: host, Guest: guest, Writable: &writable}
	if err := config.ValidateMountSources([]config.Mount{mount}); err != nil {
		return err
	}
	if err := config.AddMount(p.ConfigPath, mount); err != nil {
		return err
	}
	mode := "read-write"
	if readOnly {
		mode = "read-only"
	}
	fmt.Fprintf(a.Out, "Added mount %s (host %s, %s).\n", mount.GuestPath(), mount.Host, mode)
	a.warnMountRecreate(ctx, p)
	return nil
}

func newMountRemoveCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <guest-path-or-host-path>",
		Short: "Remove an additional host mount declaration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.mountRemove(cmd.Context(), args[0])
		},
	}
}

func (a *App) mountRemove(ctx context.Context, target string) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if strings.HasPrefix(target, "/") {
		target = path.Clean(target)
	} else if host, hostErr := mountHostPath(a, target); hostErr == nil {
		target = host
	}
	removed, err := config.RemoveMount(p.ConfigPath, target)
	if err != nil {
		if errors.Is(err, config.ErrMountNotFound) {
			return fmt.Errorf("mount not declared: %s", target)
		}
		return err
	}
	fmt.Fprintf(a.Out, "Removed mount %s (host %s).\n", removed.GuestPath(), removed.Host)
	a.warnMountRecreate(ctx, p)
	return nil
}

func mountHostPath(a *App, host string) (string, error) {
	expanded, err := config.ExpandMountHost(host)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(expanded) {
		cwd, err := a.Getwd()
		if err != nil {
			return "", fmt.Errorf("determine working directory: %w", err)
		}
		expanded = filepath.Join(cwd, expanded)
	}
	return filepath.Clean(expanded), nil
}

func (a *App) warnMountRecreate(ctx context.Context, p *project.Project) {
	lookPath := a.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if err := platform.CheckLima(runtime.GOOS, lookPath); err != nil {
		return
	}
	status, err := a.Lima.Inspect(ctx, p.VMName())
	if err != nil || !status.Exists {
		return
	}
	fmt.Fprintln(a.Out, "Mounts are applied when the VM is created; run `boxt recreate` to apply the change.")
}
