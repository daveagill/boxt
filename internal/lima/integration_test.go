//go:build integration

package lima

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boxt/boxt/internal/command"
)

func integrationClient(t *testing.T) (*Client, string) {
	t.Helper()
	if os.Getenv("BOXT_INTEGRATION") != "1" {
		t.Skip("set BOXT_INTEGRATION=1 to run Lima integration tests")
	}
	bin := os.Getenv("BOXT_LIMACTL")
	if bin == "" {
		var err error
		bin, err = exec.LookPath("limactl")
		if err != nil {
			t.Skip("limactl not installed")
		}
	}
	t.Setenv("LIMA_HOME", t.TempDir())
	return &Client{Bin: bin, Runner: command.ExecRunner{}}, bin
}

func TestIntegrationVMlifecycleAndMounts(t *testing.T) {
	client, _ := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	base := t.TempDir()
	project := filepath.Join(base, "project")
	workspaces := filepath.Join(base, "workspaces")
	wsPath := filepath.Join(workspaces, "0123456789abcdef", "feature-a")
	for _, dir := range []string{project, wsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, "hello.txt"), []byte("project-visible\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsPath, "ws.txt"), []byte("workspace-visible\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	name := "boxt-it-0123456789abcdef"
	opts := VMOptions{
		ProjectID:     "0123456789abcdef",
		ProjectRoot:   project,
		WorkspacesDir: workspaces,
		CPUs:          2,
		Memory:        "2GiB",
	}
	data, err := GenerateConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	vmDir := filepath.Join(base, "vm")
	cfgPath, err := WriteConfig(vmDir, data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_ = client.Delete(cleanupCtx, name)
	})

	if err := client.StartWithConfig(ctx, name, cfgPath); err != nil {
		t.Fatalf("StartWithConfig: %v", err)
	}
	status, err := client.Inspect(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Exists || !status.Running {
		t.Fatalf("VM not running: %#v", status)
	}

	assertGuest(t, ctx, client, name, GuestProjectPath, "project-visible", []string{"cat", "hello.txt"})
	assertGuest(t, ctx, client, name, WorkspacesGuestDir(opts.ProjectID)+"/feature-a", "workspace-visible", []string{"cat", "ws.txt"})
	assertGuest(t, ctx, client, name, GuestUserHome, "boxt", []string{"id", "-un"})
	assertGuest(t, ctx, client, name, GuestUserHome, "boxt", []string{"whoami"})

	// Guest home is persistent across stop/start.
	if err := client.Shell(ctx, ShellRequest{
		Instance: name,
		WorkDir:  GuestUserHome,
		Command:  []string{"bash", "-c", "printf persisted > \"$HOME/boxt-persist-test\""},
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
	}); err != nil {
		t.Fatalf("write guest home: %v", err)
	}
	if err := client.Stop(ctx, name); err != nil {
		t.Fatal(err)
	}
	status, err = client.Inspect(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Exists || status.Running {
		t.Fatalf("VM did not stop: %#v", status)
	}
	if err := client.Start(ctx, name); err != nil {
		t.Fatal(err)
	}
	assertGuest(t, ctx, client, name, GuestUserHome, "persisted", []string{"cat", "boxt-persist-test"})

	if err := client.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	status, err = client.Inspect(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if status.Exists {
		t.Fatalf("VM still exists after delete: %#v", status)
	}
	if _, err := os.Stat(project); err != nil {
		t.Fatal("destroy removed project files")
	}
	if _, err := os.Stat(wsPath); err != nil {
		t.Fatal("destroy removed workspace files")
	}
}

func TestIntegrationValidateGeneratedConfig(t *testing.T) {
	client, _ := integrationClient(t)
	base := t.TempDir()
	opts := VMOptions{
		ProjectID:     "0123456789abcdef",
		ProjectRoot:   filepath.Join(base, "project"),
		WorkspacesDir: filepath.Join(base, "workspaces"),
		CPUs:          2,
		Memory:        "2GiB",
	}
	for _, dir := range []string{opts.ProjectRoot, opts.WorkspacesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	data, err := GenerateConfig(opts)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath, err := WriteConfig(t.TempDir(), data)
	if err != nil {
		t.Fatal(err)
	}
	out, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{
		Name: client.Bin,
		Args: []string{"validate", cfgPath},
	})
	if err != nil {
		t.Fatalf("validate: %v\n%s\n%s", err, out, data)
	}
}

func assertGuest(t *testing.T, ctx context.Context, client *Client, instance, workdir, want string, argv []string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := client.Shell(ctx, ShellRequest{
		Instance: instance,
		WorkDir:  workdir,
		Command:  argv,
		Stdout:   &stdout,
		Stderr:   &stderr,
	})
	if err != nil {
		t.Fatalf("guest %v: %v (stderr %s)", argv, err, stderr.String())
	}
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("guest %v output = %q, want %q", argv, stdout.String(), want)
	}
}
