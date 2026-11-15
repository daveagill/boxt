package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/boxt/boxt/internal/config"
	"github.com/boxt/boxt/internal/project"
	"github.com/boxt/boxt/internal/toolchain"
)

type fakeToolchain struct {
	report           *toolchain.Report
	provisionErr     error
	installedVersion string
	installErr       error
	statuses         []toolchain.ToolStatus
	listErr          error
	provisionCalls   int
	installCalls     int
	listCalls        int
	installName      string
	installVersion   string
}

func (f *fakeToolchain) Provision(ctx context.Context, p *project.Project) (*toolchain.Report, error) {
	f.provisionCalls++
	return f.report, f.provisionErr
}

func (f *fakeToolchain) Install(ctx context.Context, p *project.Project, name, version string) (string, error) {
	f.installCalls++
	f.installName = name
	f.installVersion = version
	return f.installedVersion, f.installErr
}

func (f *fakeToolchain) List(ctx context.Context, p *project.Project) ([]toolchain.ToolStatus, error) {
	f.listCalls++
	return f.statuses, f.listErr
}

func loadConfig(t *testing.T, f *lifecycleFixture) *config.Config {
	t.Helper()
	cfg, err := config.Load(f.project.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestToolAddWhileRunning(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	ft := &fakeToolchain{installedVersion: "24.22.3"}
	f.app.Toolchain = ft
	if code := f.run(t, "tool", "add", "node@24"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if ft.installCalls != 1 || ft.installName != "node" || ft.installVersion != "24" {
		t.Fatalf("install calls = %#v", ft)
	}
	if got := loadConfig(t, f).Tools["node"]; got != "24" {
		t.Fatalf("declaration = %q", got)
	}
	for _, want := range []string{"Declared node@24", "Installing node@24", "Installed node@24"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, f.out.String())
		}
	}
}

func TestToolAddWhenStoppedDefers(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Stopped"
	ft := &fakeToolchain{}
	f.app.Toolchain = ft
	if code := f.run(t, "tool", "add", "python@3.13"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if ft.installCalls != 0 {
		t.Fatal("installed while VM was stopped")
	}
	if !strings.Contains(f.out.String(), "deferred") {
		t.Fatalf("missing deferred message: %s", f.out.String())
	}
	if got := loadConfig(t, f).Tools["python"]; got != "3.13" {
		t.Fatalf("declaration = %q", got)
	}
}

func TestToolAddWhenVMMissingDefers(t *testing.T) {
	f := newLifecycleFixture(t)
	ft := &fakeToolchain{}
	f.app.Toolchain = ft
	if code := f.run(t, "tool", "add", "go@1.24"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if ft.installCalls != 0 {
		t.Fatal("installed without a VM")
	}
	if !strings.Contains(f.out.String(), "does not exist") || !strings.Contains(f.out.String(), "deferred") {
		t.Fatalf("unexpected output: %s", f.out.String())
	}
}

func TestToolAddLimaMissing(t *testing.T) {
	f := newLifecycleFixture(t)
	f.app.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	ft := &fakeToolchain{}
	f.app.Toolchain = ft
	if code := f.run(t, "tool", "add", "node@24"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if ft.installCalls != 0 {
		t.Fatal("installed without Lima")
	}
	if !strings.Contains(f.out.String(), "deferred") {
		t.Fatalf("unexpected output: %s", f.out.String())
	}
}

func TestToolAddInstallFailureKeepsDeclaration(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	ft := &fakeToolchain{installErr: errors.New("network down")}
	f.app.Toolchain = ft
	if code := f.run(t, "tool", "add", "node@24"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if got := loadConfig(t, f).Tools["node"]; got != "24" {
		t.Fatalf("declaration was rolled back: %#v", loadConfig(t, f).Tools)
	}
	if !strings.Contains(f.errOut.String(), "declaration is saved") || !strings.Contains(f.errOut.String(), "boxt provision") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestToolAddInvalidSpec(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "tool", "add", "Node"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if len(loadConfig(t, f).Tools) != 0 {
		t.Fatal("invalid spec modified configuration")
	}
}

func TestToolAddUpdatesVersion(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	if err := config.AddTool(f.project.ConfigPath, "node", "22"); err != nil {
		t.Fatal(err)
	}
	ft := &fakeToolchain{installedVersion: "24.22.3"}
	f.app.Toolchain = ft
	if code := f.run(t, "tool", "add", "node@24"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if got := loadConfig(t, f).Tools["node"]; got != "24" {
		t.Fatalf("declaration = %q", got)
	}
	if !strings.Contains(f.out.String(), "Updated node: 22 -> 24") {
		t.Fatalf("unexpected output: %s", f.out.String())
	}
}

func TestToolRemove(t *testing.T) {
	f := newLifecycleFixture(t)
	if err := config.AddTool(f.project.ConfigPath, "node", "24"); err != nil {
		t.Fatal(err)
	}
	if code := f.run(t, "tool", "remove", "node"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if _, ok := loadConfig(t, f).Tools["node"]; ok {
		t.Fatal("declaration not removed")
	}
	if !strings.Contains(f.out.String(), "Installed software remains") {
		t.Fatalf("missing retention note: %s", f.out.String())
	}
}

func TestToolRemoveMissing(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "tool", "remove", "node"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "tool not declared") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestToolListRunning(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	if err := config.AddTool(f.project.ConfigPath, "node", "24"); err != nil {
		t.Fatal(err)
	}
	if err := config.AddTool(f.project.ConfigPath, "python", "3.13"); err != nil {
		t.Fatal(err)
	}
	f.app.Toolchain = &fakeToolchain{statuses: []toolchain.ToolStatus{
		{Name: "node", Declared: "24", Installed: "24.22.3", State: toolchain.StateInstalled},
		{Name: "python", Declared: "3.13", Installed: "3.12.1", State: toolchain.StateMismatch},
	}}
	if code := f.run(t, "tool", "list"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	for _, want := range []string{"TOOL", "DECLARED", "INSTALLED", "STATUS", "node", "24.22.3", "installed", "python", "3.12.1", "mismatch"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("list missing %q:\n%s", want, f.out.String())
		}
	}
}

func TestToolListStoppedVM(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Stopped"
	if err := config.AddTool(f.project.ConfigPath, "node", "24"); err != nil {
		t.Fatal(err)
	}
	ft := &fakeToolchain{}
	f.app.Toolchain = ft
	if code := f.run(t, "tool", "list"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if ft.listCalls != 0 {
		t.Fatal("queried tools while VM was stopped")
	}
	if !strings.Contains(f.out.String(), "VM stopped") {
		t.Fatalf("unexpected output: %s", f.out.String())
	}
}

func TestToolListNoTools(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "tool", "list"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(f.out.String(), "No tools declared") {
		t.Fatalf("unexpected output: %s", f.out.String())
	}
}

func TestToolListLimaMissing(t *testing.T) {
	f := newLifecycleFixture(t)
	f.app.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if err := config.AddTool(f.project.ConfigPath, "node", "24"); err != nil {
		t.Fatal(err)
	}
	if code := f.run(t, "tool", "list"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "Lima unavailable") {
		t.Fatalf("unexpected output: %s", f.out.String())
	}
}

func TestProvisionCommand(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Stopped"
	f.app.Toolchain = &fakeToolchain{report: &toolchain.Report{
		MiseVersion: "2026.11.8",
		Tools: []toolchain.ToolResult{
			{Name: "node", Declared: "24", Installed: "24.22.3"},
		},
	}}
	if code := f.run(t, "provision"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if f.countCommands("start") != 1 {
		t.Fatalf("provision did not start VM: %#v", f.runner.commands)
	}
	if f.app.Toolchain.(*fakeToolchain).provisionCalls != 1 {
		t.Fatal("provision was not invoked")
	}
	for _, want := range []string{"node 24 -> 24.22.3", "Provisioning complete"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, f.out.String())
		}
	}
}

func TestProvisionFailure(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	f.app.Toolchain = &fakeToolchain{
		report: &toolchain.Report{MiseVersion: "2026.11.8", Tools: []toolchain.ToolResult{
			{Name: "node", Declared: "24", Err: errors.New("download failed")},
		}},
		provisionErr: errors.New("download failed"),
	}
	if code := f.run(t, "provision"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "FAILED node@24") || !strings.Contains(f.errOut.String(), "provisioning failed") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestUpRunsProvisionHook(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	called := false
	f.app.Provision = func(ctx context.Context, p *project.Project) error {
		called = true
		return nil
	}
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !called {
		t.Fatal("up did not provision")
	}
	if !strings.Contains(f.out.String(), "Provisioning toolchain") {
		t.Fatalf("unexpected output: %s", f.out.String())
	}
}

func TestUpProvisionFailureReported(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	f.app.Provision = func(ctx context.Context, p *project.Project) error {
		return errors.New("mise failed")
	}
	if code := f.run(t, "up"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "provisioning failed") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}
