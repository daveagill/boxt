//go:build integration

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/lima"
	"github.com/daveagill/boxt/internal/platform"
	"github.com/daveagill/boxt/internal/project"
)

func integrationMemory() string {
	if v := os.Getenv("BOXT_INTEGRATION_MEMORY"); v != "" {
		return v
	}
	return "2GiB"
}

func limaTempRoot() string {
	if root := os.Getenv("BOXT_INTEGRATION_LIMA_ROOT"); root != "" {
		return root
	}
	return "/tmp"
}

func shortLimaHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(limaTempRoot(), "lh-")
	if err != nil {
		dir, err = os.MkdirTemp("", "lh-")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestIntegrationInteractivePTYAndExitStatus(t *testing.T) {
	if os.Getenv("BOXT_INTEGRATION") != "1" {
		t.Skip("set BOXT_INTEGRATION=1 to run Lima integration tests")
	}
	bin := os.Getenv("BOXT_BIN")
	if bin == "" {
		t.Skip("set BOXT_BIN to a built boxt binary")
	}
	limactl := os.Getenv("BOXT_LIMACTL")
	if limactl == "" {
		var err error
		limactl, err = exec.LookPath("limactl")
		if err != nil {
			t.Skip("limactl not installed")
		}
	}
	t.Setenv("LIMA_HOME", shortLimaHome(t))
	t.Setenv("PATH", filepath.Dir(limactl)+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	projectDir := t.TempDir()
	p, err := project.Init(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	workspaces := filepath.Join(t.TempDir(), "workspaces", p.ID)
	if err := os.MkdirAll(workspaces, 0o755); err != nil {
		t.Fatal(err)
	}
	yamlData, err := lima.GenerateConfig(lima.VMOptions{
		ProjectID:      p.ID,
		ProjectRoot:    p.Root,
		WorkspacesDir:  workspaces,
		CPUs:           2,
		Memory:         integrationMemory(),
		PortForwarding: "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfgPath, err := lima.WriteConfig(filepath.Join(t.TempDir(), "vm"), yamlData)
	if err != nil {
		t.Fatal(err)
	}
	client := &lima.Client{Bin: limactl, Runner: command.ExecRunner{}}
	if err := client.StartWithConfig(ctx, p.VMName(), cfgPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_ = client.Delete(cleanupCtx, p.VMName())
	})

	out, code := runInPTY(t, bin, projectDir, []string{
		"run", "--", "/bin/sh", "-c",
		"if [ -t 0 ] && [ -t 1 ]; then echo PTY_YES; else echo PTY_NO; fi",
	}, "")
	if code != 0 {
		t.Fatalf("exit %d; output:\n%s", code, out)
	}
	if !strings.Contains(out, "PTY_YES") {
		t.Fatalf("interactive command did not receive a PTY:\n%s", out)
	}

	out, code = runInPTY(t, bin, projectDir, []string{
		"run", "--", "/bin/sh", "-c", "exit 9",
	}, "")
	if code != 9 {
		t.Fatalf("exit = %d, want 9; output:\n%s", code, out)
	}

	out, code = runInPTY(t, bin, projectDir, []string{"shell"}, "pwd\nexit\n")
	if code != 0 {
		t.Fatalf("shell exit %d; output:\n%s", code, out)
	}
	if !strings.Contains(out, "/workspace") {
		t.Fatalf("shell did not start in /workspace:\n%s", out)
	}

	cfg := `version: 1
run:
  targets:
    port:
      command: ["/bin/sh", "-c", "printf '%s' \"$PORT\""]
      port_base: 3000
    args:
      command: ["/bin/sh", "-c", "printf '%s' \"$1\"", "--"]
`
	if err := os.WriteFile(filepath.Join(projectDir, project.ConfigRelPath), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code = runInPTY(t, bin, projectDir, []string{"run", "port"}, "")
	if code != 0 {
		t.Fatalf("port target exit %d; output:\n%s", code, out)
	}
	if !strings.Contains(out, "3000") {
		t.Fatalf("port environment not injected:\n%s", out)
	}
	out, code = runInPTY(t, bin, projectDir, []string{"run", "args", "--", "hello"}, "")
	if code != 0 {
		t.Fatalf("target args exit %d; output:\n%s", code, out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("target arguments not passed through:\n%s", out)
	}
}

func TestIntegrationWorkspaceHooks(t *testing.T) {
	if os.Getenv("BOXT_INTEGRATION") != "1" {
		t.Skip("set BOXT_INTEGRATION=1 to run Lima integration tests")
	}
	bin := os.Getenv("BOXT_BIN")
	if bin == "" {
		t.Skip("set BOXT_BIN to a built boxt binary")
	}
	limactl := os.Getenv("BOXT_LIMACTL")
	if limactl == "" {
		var err error
		limactl, err = exec.LookPath("limactl")
		if err != nil {
			t.Skip("limactl not installed")
		}
	}
	t.Setenv("LIMA_HOME", shortLimaHome(t))
	t.Setenv("PATH", filepath.Dir(limactl)+string(os.PathListSeparator)+os.Getenv("PATH"))
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	projectDir := t.TempDir()
	p, err := project.Init(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		full := append([]string{"-C", projectDir}, args...)
		if out, err := command.Output(ctx, command.ExecRunner{}, command.Command{Name: "git", Args: full}); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(projectDir, "README.md"), []byte("hooks\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := command.Output(ctx, command.ExecRunner{}, command.Command{Name: "git", Args: []string{"-C", projectDir, "add", "-A"}}); err != nil {
		t.Fatalf("git add: %v (%s)", err, out)
	}
	if out, err := command.Output(ctx, command.ExecRunner{}, command.Command{Name: "git", Args: []string{"-C", projectDir, "commit", "-q", "-m", "initial"}}); err != nil {
		t.Fatalf("git commit: %v (%s)", err, out)
	}
	cfg := `version: 1
hooks:
  workspace_create:
    pre:
      - command: ["/bin/sh", "-c", "printf guest > hook-guest.txt"]
    post:
      - command: ["/bin/sh", "-c", "printf host > hook-host.txt"]
        where: host
`
	if err := os.WriteFile(filepath.Join(projectDir, project.ConfigRelPath), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	workspaces := platform.WorkspacesDir(dataDir, p.ID)
	if err := os.MkdirAll(workspaces, 0o755); err != nil {
		t.Fatal(err)
	}
	yamlData, err := lima.GenerateConfig(lima.VMOptions{
		ProjectID:      p.ID,
		ProjectRoot:    p.Root,
		WorkspacesDir:  workspaces,
		CPUs:           2,
		Memory:         integrationMemory(),
		PortForwarding: "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfgPath, err := lima.WriteConfig(filepath.Join(t.TempDir(), "vm"), yamlData)
	if err != nil {
		t.Fatal(err)
	}
	client := &lima.Client{Bin: limactl, Runner: command.ExecRunner{}}
	if err := client.StartWithConfig(ctx, p.VMName(), cfgPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_ = client.Delete(cleanupCtx, p.VMName())
	})

	out, code := runInPTY(t, bin, projectDir, []string{"hooks", "trust"}, "")
	if code != 0 {
		t.Fatalf("hooks trust exit %d; output:\n%s", code, out)
	}
	out, code = runInPTY(t, bin, projectDir, []string{"workspace", "create", "hooky"}, "")
	if code != 0 {
		t.Fatalf("workspace create exit %d; output:\n%s", code, out)
	}
	wsPath := filepath.Join(workspaces, "hooky")
	if data, _ := os.ReadFile(filepath.Join(wsPath, "hook-guest.txt")); string(data) != "guest" {
		t.Fatalf("guest hook output = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(wsPath, "hook-host.txt")); string(data) != "host" {
		t.Fatalf("host hook output = %q", data)
	}
}

func runInPTY(t *testing.T, bin, dir string, args []string, input string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("start PTY: %v", err)
	}
	defer ptmx.Close()
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 100})
	if input != "" {
		if _, err := ptmx.Write([]byte(input)); err != nil {
			t.Fatalf("write PTY input: %v", err)
		}
	}
	var buf bytes.Buffer
	_, readErr := io.Copy(&buf, ptmx)
	err = cmd.Wait()
	if err != nil && readErr != nil && !errors.Is(readErr, io.EOF) {
		t.Logf("PTY read ended: %v", readErr)
	}
	if err == nil {
		return buf.String(), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return buf.String(), exitErr.ExitCode()
	}
	t.Fatalf("wait: %v (output %s)", err, buf.String())
	return "", -1
}
