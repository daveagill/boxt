package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/workspace"
)

func newPortCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "port",
		Short: "Inspect and edit explicit VM port forwarding",
		Long: `Inspect and edit vm.ports.

Port forwarding is applied when the VM is created, so changing vm.ports requires
recreating the VM. Port mode is a separate setting: boxt config set vm.port_forwarding auto|explicit.`,
	}
	cmd.AddCommand(newPortListCmd(app))
	cmd.AddCommand(newPortAddCmd(app))
	cmd.AddCommand(newPortRemoveCmd(app))
	return cmd
}

func newPortListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List declared and effective forwarded ports",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.portList(cmd.Context())
		},
	}
}

func (a *App) portList(ctx context.Context) error {
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Mode:       %s\n", p.Config.VM.PortForwarding)

	w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Declared:")
	if len(p.Config.VM.Ports) == 0 {
		fmt.Fprintln(w, "  none")
	} else {
		fmt.Fprintln(w, "  GUEST\tHOST\tHOST_IP")
		for _, port := range p.Config.VM.Ports {
			fmt.Fprintf(w, "  %d\t%d\t%s\n", port.Guest, port.Host, port.HostIP)
		}
	}

	var targets []string
	for _, name := range p.Config.TargetNames() {
		if p.Config.Run.Targets[name].PortBase > 0 {
			targets = append(targets, name)
		}
	}
	if len(targets) == 0 {
		fmt.Fprintln(w, "\nPer-workspace ports: none (no target declares port_base)")
		return w.Flush()
	}

	names := []string{workspace.DefaultName}
	if a.DataDir != nil && a.Git != nil {
		if mgr, _, mgrErr := a.workspaceManager(p); mgrErr == nil {
			if list, listErr := mgr.List(ctx); listErr == nil {
				for _, ws := range list {
					names = append(names, ws.Name)
				}
			}
		}
	}

	seenOffsets := map[int]string{}
	var collisions []string
	for _, name := range names {
		offset := workspace.PortOffset(p.ID, name)
		if other, ok := seenOffsets[offset]; ok && other != name {
			collisions = append(collisions, fmt.Sprintf("%s and %s both use offset %d", other, name, offset))
		} else {
			seenOffsets[offset] = name
		}
	}

	fmt.Fprintln(w, "\nPer-workspace ports:")
	fmt.Fprintln(w, "  TARGET\tWORKSPACE\tGUEST\tHOST\tREACHABLE")
	for _, targetName := range targets {
		base := p.Config.Run.Targets[targetName].PortBase
		for _, name := range names {
			guest := base + workspace.PortOffset(p.ID, name)
			host, reachable := portReachability(p.Config, guest)
			fmt.Fprintf(w, "  %s\t%s\t%d\t%s\t%s\n", targetName, name, guest, host, reachable)
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, collision := range collisions {
		fmt.Fprintf(a.Err, "Warning: port offset collision: %s\n", collision)
	}
	return nil
}

func portReachability(cfg *config.Config, guest int) (host, reachable string) {
	for _, port := range cfg.VM.Ports {
		if port.Guest != guest {
			continue
		}
		return net.JoinHostPort(port.HostIP, strconv.Itoa(port.Host)), "yes (mapped)"
	}
	if cfg.VM.PortForwarding == config.PortForwardingAuto {
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(guest)), "yes (auto)"
	}
	return "-", "guest-only (explicit)"
}

func newPortAddCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "add GUEST[:HOST][@HOST_IP]",
		Short: "Add an explicit port forwarding",
		Long: `Add an explicit guest-to-host port forward.

  boxt port add 3000
  boxt port add 3000:8080
  boxt port add 3000:8080@127.0.0.1

HOST defaults to GUEST and HOST_IP defaults to 127.0.0.1.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.portAdd(args[0])
		},
	}
}

func (a *App) portAdd(spec string) error {
	port, err := parsePortSpec(spec)
	if err != nil {
		return err
	}
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if err := config.AddPort(p.ConfigPath, port); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Forwarded guest %d to %s.\n", port.Guest, net.JoinHostPort(port.HostIP, strconv.Itoa(port.Host)))
	fmt.Fprintln(a.Out, "Note: this is a VM creation setting. Run `boxt recreate` to apply it.")
	return nil
}

func newPortRemoveCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove GUEST",
		Short: "Remove an explicit port forwarding",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.portRemove(args[0])
		},
	}
}

func (a *App) portRemove(guestArg string) error {
	guest, err := strconv.Atoi(guestArg)
	if err != nil {
		return fmt.Errorf("invalid guest port %q", guestArg)
	}
	p, err := a.loadProject()
	if err != nil {
		return err
	}
	if err := config.RemovePortByGuest(p.ConfigPath, guest); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Removed forwarding for guest port %d.\n", guest)
	fmt.Fprintln(a.Out, "Note: this is a VM creation setting. Run `boxt recreate` to apply it.")
	return nil
}

// parsePortSpec parses GUEST[:HOST][@HOST_IP].
func parsePortSpec(spec string) (config.PortForward, error) {
	port := config.PortForward{HostIP: "127.0.0.1"}
	rest := spec
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		port.HostIP = rest[at+1:]
		rest = rest[:at]
	}
	if rest == "" {
		return config.PortForward{}, fmt.Errorf("invalid port spec %q: expected GUEST[:HOST][@HOST_IP]", spec)
	}
	guestPart, hostPart := rest, ""
	if colon := strings.Index(rest, ":"); colon >= 0 {
		guestPart, hostPart = rest[:colon], rest[colon+1:]
		if hostPart == "" {
			return config.PortForward{}, fmt.Errorf("invalid host port in %q", spec)
		}
	}
	guest, err := strconv.Atoi(guestPart)
	if err != nil {
		return config.PortForward{}, fmt.Errorf("invalid guest port %q", guestPart)
	}
	host := guest
	if hostPart != "" {
		host, err = strconv.Atoi(hostPart)
		if err != nil {
			return config.PortForward{}, fmt.Errorf("invalid host port %q", hostPart)
		}
	}
	port.Guest = guest
	port.Host = host
	if net.ParseIP(port.HostIP) == nil {
		return config.PortForward{}, errors.New("invalid host IP " + strconv.Quote(port.HostIP))
	}
	return port, nil
}
