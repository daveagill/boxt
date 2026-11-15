//go:build integration

package workspace

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/git"
	"github.com/daveagill/boxt/internal/lima"
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

func TestIntegrationWorkspaceVisibleOnHostAndGuest(t *testing.T) {
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
	t.Setenv("LIMA_HOME", shortLimaHome(t))
	client := &lima.Client{Bin: bin, Runner: command.ExecRunner{}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	base := t.TempDir()
	repo := filepath.Join(base, "project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "config", "user.email", "test@example.com")
	gitRun(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("host-and-guest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-q", "-m", "initial")

	hostRoot := filepath.Join(base, "workspaces", "0123456789abcdef")
	yamlData, err := lima.GenerateConfig(lima.VMOptions{
		ProjectID:     "0123456789abcdef",
		ProjectRoot:   repo,
		WorkspacesDir: hostRoot,
		CPUs:          2,
		Memory:        integrationMemory(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cfgPath, err := lima.WriteConfig(filepath.Join(base, "vm"), yamlData)
	if err != nil {
		t.Fatal(err)
	}
	name := "boxt-it-ws-0123456789abcdef"
	if err := client.StartWithConfig(ctx, name, cfgPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_ = client.Delete(cleanupCtx, name)
	})

	m := New(repo, "0123456789abcdef", hostRoot, git.New(command.ExecRunner{}))
	ws, err := m.Create(ctx, "feature-a", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if status := gitRun(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("parent repository sees external worktree: %q", status)
	}

	guestReadme := ws.GuestPath + "/README.md"
	assertGuestFile(t, ctx, client, name, "/workspace/README.md", "host-and-guest")
	assertGuestFile(t, ctx, client, name, guestReadme, "host-and-guest")

	if err := os.WriteFile(filepath.Join(ws.HostPath, "from-host.txt"), []byte("visible\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertGuestFile(t, ctx, client, name, ws.GuestPath+"/from-host.txt", "visible")
	if err := os.Remove(filepath.Join(ws.HostPath, "from-host.txt")); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Remove(ctx, "feature-a", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	var stderr bytes.Buffer
	err = client.Shell(ctx, lima.ShellRequest{
		Instance: name,
		WorkDir:  lima.GuestUserHome,
		Command:  []string{"test", "-e", ws.GuestPath},
		Stderr:   &stderr,
	})
	if err == nil {
		t.Fatalf("workspace still visible in guest after removal (stderr %s)", stderr.String())
	}
}

func assertGuestFile(t *testing.T, ctx context.Context, client *lima.Client, instance, path, want string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := client.Shell(ctx, lima.ShellRequest{
		Instance: instance,
		WorkDir:  lima.GuestUserHome,
		Command:  []string{"cat", path},
		Stdout:   &stdout,
		Stderr:   &stderr,
	}); err != nil {
		t.Fatalf("cat %s: %v (stderr %s)", path, err, stderr.String())
	}
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("cat %s = %q, want %q", path, stdout.String(), want)
	}
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	out, err := command.Output(context.Background(), command.ExecRunner{}, command.Command{Name: "git", Args: full})
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}
