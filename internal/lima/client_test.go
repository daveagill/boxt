package lima

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/command"
)

type fakeRunner struct {
	cmds   []command.Command
	stdout string
	err    error
}

func (f *fakeRunner) Run(ctx context.Context, c command.Command) error {
	f.cmds = append(f.cmds, c)
	if c.Stdout != nil && f.stdout != "" {
		_, _ = io.WriteString(c.Stdout, f.stdout)
	}
	return f.err
}

func TestListAndInspect(t *testing.T) {
	fr := &fakeRunner{stdout: `{"name":"boxt-a","status":"Running","arch":"aarch64","cpus":4,"memory":8589934592}
{"name":"boxt-b","status":"Stopped","vmType":"vz"}
`}
	c := &Client{Bin: "limactl", Runner: fr}
	instances, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 2 || instances[0].Name != "boxt-a" || !instances[0].Running() {
		t.Fatalf("unexpected instances: %#v", instances)
	}
	if instances[1].Running() {
		t.Fatal("stopped instance reported running")
	}
	got, err := c.Inspect(context.Background(), "boxt-b")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exists || got.Running || got.Instance.Name != "boxt-b" {
		t.Fatalf("unexpected inspect: %#v", got)
	}
	missing, err := c.Inspect(context.Background(), "nope")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Exists {
		t.Fatalf("missing instance reported existing: %#v", missing)
	}
	want := []string{"limactl", "list", "--format", "json"}
	if !equalArgs(fr.cmds[0].Args, want[1:]) {
		t.Fatalf("args = %#v", fr.cmds[0].Args)
	}
}

func TestListEmpty(t *testing.T) {
	fr := &fakeRunner{}
	c := &Client{Runner: fr}
	instances, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 0 {
		t.Fatalf("instances = %#v", instances)
	}
	if fr.cmds[0].Name != "limactl" {
		t.Fatalf("default binary = %q", fr.cmds[0].Name)
	}
}

func TestListParseError(t *testing.T) {
	c := &Client{Runner: &fakeRunner{stdout: "not json"}}
	if _, err := c.List(context.Background()); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLifecycleCommands(t *testing.T) {
	tests := []struct {
		name string
		call func(*Client) error
		want []string
	}{
		{
			name: "start with config",
			call: func(c *Client) error { return c.StartWithConfig(context.Background(), "boxt-a", "/tmp/lima.yaml") },
			want: []string{"start", "--tty=false", "--name", "boxt-a", "/tmp/lima.yaml"},
		},
		{
			name: "start",
			call: func(c *Client) error { return c.Start(context.Background(), "boxt-a") },
			want: []string{"start", "--tty=false", "boxt-a"},
		},
		{
			name: "stop",
			call: func(c *Client) error { return c.Stop(context.Background(), "boxt-a") },
			want: []string{"stop", "--tty=false", "boxt-a"},
		},
		{
			name: "delete",
			call: func(c *Client) error { return c.Delete(context.Background(), "boxt-a") },
			want: []string{"delete", "--tty=false", "--force", "boxt-a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &fakeRunner{}
			c := &Client{Bin: "limactl", Runner: fr}
			if err := tt.call(c); err != nil {
				t.Fatal(err)
			}
			if len(fr.cmds) != 1 || !equalArgs(fr.cmds[0].Args, tt.want) {
				t.Fatalf("args = %#v, want %#v", fr.cmds, tt.want)
			}
		})
	}
}

func TestLifecycleErrorWrapped(t *testing.T) {
	fr := &fakeRunner{err: &command.Error{Name: "limactl", ExitCode: 1, Stderr: "boom"}}
	c := &Client{Runner: fr}
	if err := c.Start(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "start VM x") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestShellArgs(t *testing.T) {
	tests := []struct {
		name string
		req  ShellRequest
		want []string
	}{
		{
			name: "interactive default dir",
			req:  ShellRequest{Instance: "boxt-a"},
			want: []string{"shell", "--start=false", "boxt-a"},
		},
		{
			name: "workdir and command",
			req: ShellRequest{
				Instance: "boxt-a",
				WorkDir:  "/workspace",
				Command:  []string{"npm", "test"},
			},
			want: []string{"shell", "--start=false", "--workdir", "/workspace", "boxt-a", "--", "npm", "test"},
		},
		{
			name: "command with spaces is a single argument",
			req: ShellRequest{
				Instance: "boxt-a",
				Command:  []string{"python", "a b.py"},
			},
			want: []string{"shell", "--start=false", "boxt-a", "--", "python", "a b.py"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &fakeRunner{}
			c := &Client{Bin: "limactl", Runner: fr}
			if err := c.Shell(context.Background(), tt.req); err != nil {
				t.Fatal(err)
			}
			if len(fr.cmds) != 1 || !equalArgs(fr.cmds[0].Args, tt.want) {
				t.Fatalf("args = %#v, want %#v", fr.cmds, tt.want)
			}
			if fr.cmds[0].Name != "limactl" {
				t.Fatalf("binary = %q", fr.cmds[0].Name)
			}
		})
	}
}

func TestShellRequiresInstance(t *testing.T) {
	c := &Client{Runner: &fakeRunner{}}
	if err := c.Shell(context.Background(), ShellRequest{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestShellExitStatusPreserved(t *testing.T) {
	fr := &fakeRunner{err: &command.Error{Name: "limactl", ExitCode: 42}}
	c := &Client{Runner: fr}
	err := c.Shell(context.Background(), ShellRequest{Instance: "x", Command: []string{"false"}})
	var exitCoder command.ExitCoder
	if !errors.As(err, &exitCoder) || exitCoder.ExitStatus() != 42 {
		t.Fatalf("exit status not preserved: %v", err)
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
