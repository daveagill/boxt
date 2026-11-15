package lima

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/daveagill/boxt/internal/command"
)

// Client wraps the supported limactl subcommands.
type Client struct {
	Bin    string
	Runner command.Runner
}

// New returns a Lima client using the provided process runner.
func New(r command.Runner) *Client {
	return &Client{Bin: "limactl", Runner: r}
}

// Instance is the subset of `limactl list --format json` boxt relies on.
type Instance struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	Dir          string `json:"dir"`
	VMType       string `json:"vmType"`
	Arch         string `json:"arch"`
	CPUs         int    `json:"cpus"`
	Memory       int64  `json:"memory"`
	Disk         int64  `json:"disk"`
	SSHLocalPort int    `json:"sshLocalPort"`
	LimaVersion  string `json:"limaVersion"`
}

// Running reports whether the instance is running.
func (i Instance) Running() bool {
	return strings.EqualFold(i.Status, "Running")
}

// Status describes whether a project VM exists and whether it is running.
type Status struct {
	Exists   bool
	Running  bool
	Instance Instance
}

// List returns all Lima instances known to the current LIMA_HOME.
func (c *Client) List(ctx context.Context) ([]Instance, error) {
	out, err := command.Output(ctx, c.Runner, command.Command{
		Name: c.bin(),
		Args: []string{"list", "--format", "json"},
	})
	if err != nil {
		return nil, fmt.Errorf("list Lima instances: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var instances []Instance
	for {
		var inst Instance
		if err := dec.Decode(&inst); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("parse limactl list output: %w", err)
		}
		instances = append(instances, inst)
	}
	return instances, nil
}

// Inspect returns the status of a named instance.
func (c *Client) Inspect(ctx context.Context, name string) (Status, error) {
	instances, err := c.List(ctx)
	if err != nil {
		return Status{}, err
	}
	for _, inst := range instances {
		if inst.Name == name {
			return Status{Exists: true, Running: inst.Running(), Instance: inst}, nil
		}
	}
	return Status{}, nil
}

// StartWithConfig creates and starts a new instance from a YAML file.
func (c *Client) StartWithConfig(ctx context.Context, name, configPath string) error {
	_, err := command.Output(ctx, c.Runner, command.Command{
		Name: c.bin(),
		Args: []string{"start", "--tty=false", "--name", name, configPath},
	})
	if err != nil {
		return fmt.Errorf("create and start VM %s: %w", name, err)
	}
	return nil
}

// Start starts an existing stopped instance.
func (c *Client) Start(ctx context.Context, name string) error {
	_, err := command.Output(ctx, c.Runner, command.Command{
		Name: c.bin(),
		Args: []string{"start", "--tty=false", name},
	})
	if err != nil {
		return fmt.Errorf("start VM %s: %w", name, err)
	}
	return nil
}

// Stop stops a running instance without deleting its state.
func (c *Client) Stop(ctx context.Context, name string) error {
	_, err := command.Output(ctx, c.Runner, command.Command{
		Name: c.bin(),
		Args: []string{"stop", "--tty=false", name},
	})
	if err != nil {
		return fmt.Errorf("stop VM %s: %w", name, err)
	}
	return nil
}

// Delete removes an instance and its guest state.
func (c *Client) Delete(ctx context.Context, name string) error {
	_, err := command.Output(ctx, c.Runner, command.Command{
		Name: c.bin(),
		Args: []string{"delete", "--tty=false", "--force", name},
	})
	if err != nil {
		return fmt.Errorf("delete VM %s: %w", name, err)
	}
	return nil
}

// ShellRequest describes one guest shell invocation.
type ShellRequest struct {
	Instance        string
	WorkDir         string
	Command         []string
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	NewProcessGroup bool
}

// Shell executes a command (or an interactive shell when Command is empty) in
// the guest using an explicit working directory. limactl propagates the child
// exit status and allocates a PTY when stdout is a terminal.
func (c *Client) Shell(ctx context.Context, req ShellRequest) error {
	if req.Instance == "" {
		return errors.New("Lima instance name is required")
	}
	args := []string{"shell", "--start=false"}
	if req.WorkDir != "" {
		args = append(args, "--workdir", req.WorkDir)
	}
	args = append(args, req.Instance)
	if len(req.Command) > 0 {
		args = append(args, "--")
		args = append(args, req.Command...)
	}
	err := c.Runner.Run(ctx, command.Command{
		Name:            c.bin(),
		Args:            args,
		Stdin:           req.Stdin,
		Stdout:          req.Stdout,
		Stderr:          req.Stderr,
		NewProcessGroup: req.NewProcessGroup,
	})
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "limactl"
}
