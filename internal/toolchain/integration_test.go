//go:build integration

package toolchain

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
	"github.com/boxt/boxt/internal/config"
	"github.com/boxt/boxt/internal/lima"
	"github.com/boxt/boxt/internal/project"
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

func TestIntegrationProvisionPersistsAcrossRestart(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	base := t.TempDir()
	projectRoot := filepath.Join(base, "project")
	workspaces := filepath.Join(base, "workspaces", "0123456789abcdef")
	for _, dir := range []string{projectRoot, workspaces} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	yamlData, err := lima.GenerateConfig(lima.VMOptions{
		ProjectID:     "0123456789abcdef",
		ProjectRoot:   projectRoot,
		WorkspacesDir: workspaces,
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
	p := &project.Project{
		Root: projectRoot,
		ID:   "0123456789abcdef",
		Config: &config.Config{
			Version: 1,
			Tools:   map[string]string{"node": "24"},
		},
	}
	name := p.VMName()
	if err := client.StartWithConfig(ctx, name, cfgPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_ = client.Delete(cleanupCtx, name)
	})

	m := New(client)
	report, err := m.Provision(ctx, p)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if report.Failed() || len(report.Tools) != 1 {
		t.Fatalf("report = %#v", report)
	}
	if !strings.HasPrefix(report.Tools[0].Installed, "24.") {
		t.Fatalf("node version = %q", report.Tools[0].Installed)
	}
	assertNode := func() {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if err := client.Shell(ctx, lima.ShellRequest{
			Instance: name,
			WorkDir:  lima.GuestProjectPath,
			Command:  WrapArgv(map[string]string{"node": "24"}, []string{"node", "--version"}),
			Stdout:   &stdout,
			Stderr:   &stderr,
		}); err != nil {
			t.Fatalf("node --version: %v (stderr %s)", err, stderr.String())
		}
		if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "v24.") {
			t.Fatalf("node --version output = %q", stdout.String())
		}
	}
	assertNode()

	if err := client.Stop(ctx, name); err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx, name); err != nil {
		t.Fatal(err)
	}
	statuses, err := m.List(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].State != StateInstalled {
		t.Fatalf("tools did not persist: %#v", statuses)
	}
	assertNode()

	// A second provision must be idempotent.
	if _, err := m.Provision(ctx, p); err != nil {
		t.Fatalf("second Provision: %v", err)
	}
}
