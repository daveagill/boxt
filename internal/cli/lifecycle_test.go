package cli

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/lima"
	"github.com/daveagill/boxt/internal/project"
)

type fakeLimaRunner struct {
	vmName   string
	status   string
	cpus     int
	memory   int64
	commands []command.Command
	startErr error
	shellErr error
}

func (f *fakeLimaRunner) Run(ctx context.Context, c command.Command) error {
	f.commands = append(f.commands, c)
	if len(c.Args) == 0 {
		return nil
	}
	switch c.Args[0] {
	case "list":
		if f.status == "" {
			return nil
		}
		if c.Stdout != nil {
			cpus := f.cpus
			if cpus == 0 {
				cpus = 4
			}
			memory := f.memory
			if memory == 0 {
				memory = 8 * 1024 * 1024 * 1024
			}
			fmt.Fprintf(c.Stdout, `{"name":%q,"status":%q,"vmType":"qemu","arch":"aarch64","cpus":%d,"memory":%d}`+"\n", f.vmName, f.status, cpus, memory)
		}
	case "start":
		if f.startErr != nil {
			return f.startErr
		}
		f.status = "Running"
	case "stop":
		f.status = "Stopped"
	case "delete":
		f.status = ""
	case "shell":
		return f.shellErr
	}
	return nil
}

type lifecycleFixture struct {
	project *project.Project
	app     *App
	runner  *fakeLimaRunner
	out     *bytes.Buffer
	errOut  *bytes.Buffer
	dataDir string
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	dir := t.TempDir()
	p, err := project.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	app, outBuf, errBuf := testApp(t, dir)
	app.DataDir = func() (string, error) { return dataDir, nil }
	fr := &fakeLimaRunner{vmName: p.VMName()}
	app.Lima = &lima.Client{Bin: "limactl", Runner: fr}
	app.Confirm = func(string) (bool, error) { return true, nil }
	return &lifecycleFixture{
		project: p,
		app:     app,
		runner:  fr,
		out:     outBuf,
		errOut:  errBuf,
		dataDir: dataDir,
	}
}

func (f *lifecycleFixture) run(t *testing.T, args ...string) int {
	t.Helper()
	return Run(context.Background(), f.app, args)
}

func (f *lifecycleFixture) startCommands() int {
	n := 0
	for _, c := range f.runner.commands {
		if len(c.Args) > 0 && c.Args[0] == "start" {
			n++
		}
	}
	return n
}

func TestUpCreatesVM(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "Creating VM "+f.project.VMName()) {
		t.Fatalf("unexpected output:\n%s", f.out.String())
	}
	if !strings.Contains(f.out.String(), "Environment ready") {
		t.Fatalf("missing ready message:\n%s", f.out.String())
	}
	if f.startCommands() != 1 {
		t.Fatalf("expected one start command, got %#v", f.runner.commands)
	}
	var start command.Command
	for _, c := range f.runner.commands {
		if len(c.Args) > 0 && c.Args[0] == "start" {
			start = c
		}
	}
	wantConfig := filepath.Join(f.dataDir, "vms", f.project.ID, "lima.yaml")
	if !containsArg(start.Args, wantConfig) {
		t.Fatalf("start args %#v do not reference %q", start.Args, wantConfig)
	}
	data, err := os.ReadFile(wantConfig)
	if err != nil {
		t.Fatalf("generated config missing: %v", err)
	}
	if !strings.Contains(string(data), "mountPoint: /workspace") {
		t.Fatalf("generated config missing project mount:\n%s", data)
	}
	wsDir := filepath.Join(f.dataDir, "workspaces", f.project.ID)
	if info, err := os.Stat(wsDir); err != nil || !info.IsDir() {
		t.Fatalf("workspace dir not created: %v", err)
	}
}

func TestUpAlreadyRunning(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if f.startCommands() != 0 {
		t.Fatalf("started a running VM: %#v", f.runner.commands)
	}
	if !strings.Contains(f.out.String(), "already running") {
		t.Fatalf("unexpected output:\n%s", f.out.String())
	}
}

func TestUpAlreadyRunningWithExplicitPorts(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	cfg := fmt.Sprintf(`version: 1
vm:
  image: ubuntu-24.04
  cpus: 4
  memory: 8GiB
  ports:
    - guest: 3000
      host: %d
run:
  targets: {}
`, port)
	if err := os.WriteFile(f.project.ConfigPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if f.startCommands() != 0 {
		t.Fatalf("started a running VM: %#v", f.runner.commands)
	}
}

func TestUpStartsStoppedVM(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Stopped"
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "Starting VM") {
		t.Fatalf("unexpected output:\n%s", f.out.String())
	}
	if f.startCommands() != 1 {
		t.Fatalf("expected start, got %#v", f.runner.commands)
	}
}

func TestUpSameConfigTwice(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("first up exit %d: %s", code, f.errOut.String())
	}
	f.out.Reset()
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("second up exit %d: %s", code, f.errOut.String())
	}
	if f.startCommands() != 1 {
		t.Fatalf("expected one start, got %#v", f.runner.commands)
	}
}

func TestUpDetectsConfigDrift(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("first up exit %d: %s", code, f.errOut.String())
	}
	f.writeConfig(t, `version: 1
vm:
  image: ubuntu-24.04
  cpus: 2
  memory: 8GiB
run:
  targets: {}
`)
	f.errOut.Reset()
	if code := f.run(t, "up"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "different configuration") || !strings.Contains(f.errOut.String(), "boxt destroy") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
	if f.startCommands() != 1 {
		t.Fatalf("drifted VM was restarted: %#v", f.runner.commands)
	}
}

func TestStatusReportsConfigDrift(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("up exit %d: %s", code, f.errOut.String())
	}
	f.writeConfig(t, `version: 1
vm:
  image: ubuntu-24.04
  cpus: 2
  memory: 8GiB
run:
  targets: {}
`)
	f.out.Reset()
	if code := f.run(t, "status"); code != 0 {
		t.Fatalf("status exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "Config drift") {
		t.Fatalf("status did not report drift:\n%s", f.out.String())
	}
}

func TestUpWarnsAboutResourceDrift(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	f.runner.cpus = 2
	f.runner.memory = 4 * 1024 * 1024 * 1024
	if code := f.run(t, "up"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.errOut.String(), "different resources") || !strings.Contains(f.errOut.String(), "cpus 2 -> 4") {
		t.Fatalf("missing drift warning:\n%s", f.errOut.String())
	}
}

func TestUpStartFailure(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.startErr = &command.Error{Name: "limactl", ExitCode: 1, Stderr: "qemu missing"}
	if code := f.run(t, "up"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "qemu missing") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestUpPortConflict(t *testing.T) {
	f := newLifecycleFixture(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	cfg := fmt.Sprintf(`version: 1
vm:
  image: ubuntu-24.04
  cpus: 4
  memory: 8GiB
  ports:
    - guest: 3000
      host: %d
run:
  targets: {}
`, port)
	if err := os.WriteFile(f.project.ConfigPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := f.run(t, "up"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "already in use") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
	if f.startCommands() != 0 {
		t.Fatalf("VM was started despite port conflict: %#v", f.runner.commands)
	}
}

func TestUpRequiresLima(t *testing.T) {
	f := newLifecycleFixture(t)
	f.app.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if code := f.run(t, "up"); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(f.errOut.String(), "Lima is required") {
		t.Fatalf("unexpected error: %s", f.errOut.String())
	}
}

func TestDownStates(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		wantStop   int
		wantOutput string
	}{
		{"running", "Running", 1, "stopped"},
		{"stopped", "Stopped", 0, "already stopped"},
		{"missing", "", 0, "does not exist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.runner.status = tt.status
			if code := f.run(t, "down"); code != 0 {
				t.Fatalf("exit %d: %s", code, f.errOut.String())
			}
			stops := 0
			for _, c := range f.runner.commands {
				if len(c.Args) > 0 && c.Args[0] == "stop" {
					stops++
				}
			}
			if stops != tt.wantStop {
				t.Fatalf("stop commands = %d, want %d", stops, tt.wantStop)
			}
			if !strings.Contains(f.out.String(), tt.wantOutput) {
				t.Fatalf("output %q missing %q", f.out.String(), tt.wantOutput)
			}
		})
	}
}

func TestDestroyYes(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	if code := f.run(t, "destroy", "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "not deleted") {
		t.Fatalf("missing preservation note: %s", f.out.String())
	}
	deletes := 0
	for _, c := range f.runner.commands {
		if len(c.Args) > 0 && c.Args[0] == "delete" {
			deletes++
		}
	}
	if deletes != 1 {
		t.Fatalf("delete commands = %d", deletes)
	}
}

func TestDestroyConfirmation(t *testing.T) {
	tests := []struct {
		name     string
		confirm  bool
		wantCode int
		wantDel  int
	}{
		{"confirmed", true, 0, 1},
		{"declined", false, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.runner.status = "Running"
			var prompt string
			f.app.Confirm = func(p string) (bool, error) {
				prompt = p
				return tt.confirm, nil
			}
			code := f.run(t, "destroy")
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(prompt, "Type 'yes'") {
				t.Fatalf("unexpected prompt: %q", prompt)
			}
			deletes := 0
			for _, c := range f.runner.commands {
				if len(c.Args) > 0 && c.Args[0] == "delete" {
					deletes++
				}
			}
			if deletes != tt.wantDel {
				t.Fatalf("delete commands = %d, want %d", deletes, tt.wantDel)
			}
			if !tt.confirm && !strings.Contains(f.errOut.String(), "cancelled") {
				t.Fatalf("missing cancellation error: %s", f.errOut.String())
			}
		})
	}
}

func TestDestroyMissingVM(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "destroy", "--yes"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(f.out.String(), "does not exist") {
		t.Fatalf("unexpected output: %s", f.out.String())
	}
}

func TestStatusRunning(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	if err := config.AddTool(f.project.ConfigPath, "node", "24"); err != nil {
		t.Fatal(err)
	}
	if code := f.run(t, "status"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	for _, want := range []string{
		"Project:",
		f.project.Root,
		f.project.ID,
		f.project.VMName(),
		"running",
		"qemu/aarch64",
		"ubuntu-24.04",
		"4 CPUs",
		"8GiB",
		"Port forwarding:   auto",
		"/workspace",
		"/workspaces/" + f.project.ID,
		"node 24",
	} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("status missing %q:\n%s", want, f.out.String())
		}
	}
}

func TestStatusMissingVM(t *testing.T) {
	f := newLifecycleFixture(t)
	if code := f.run(t, "status"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "not created") {
		t.Fatalf("unexpected status:\n%s", f.out.String())
	}
	if !strings.Contains(f.out.String(), "none declared") {
		t.Fatalf("missing tools line:\n%s", f.out.String())
	}
	if !strings.Contains(f.out.String(), "Workspaces:") || !strings.Contains(f.out.String(), "default") {
		t.Fatalf("missing workspaces section:\n%s", f.out.String())
	}
}

func TestStatusWithNamedWorkspace(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.runner.status = "Running"
	if code := f.run(t, "workspace", "create", "feature-a"); code != 0 {
		t.Fatalf("create failed: %s", f.errOut.String())
	}
	f.out.Reset()
	if code := f.run(t, "status"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	for _, want := range []string{"Workspaces:", "feature-a", "boxt/feature-a", "ready"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("status missing %q:\n%s", want, f.out.String())
		}
	}
}

func TestStatusWithoutLima(t *testing.T) {
	f := newLifecycleFixture(t)
	f.app.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if code := f.run(t, "status"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	if !strings.Contains(f.out.String(), "Lima not installed") {
		t.Fatalf("unexpected status:\n%s", f.out.String())
	}
	if !strings.Contains(f.out.String(), "Project:") {
		t.Fatalf("project info missing:\n%s", f.out.String())
	}
}

func TestStatusShowsActualVMResources(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.status = "Running"
	f.runner.cpus = 2
	f.runner.memory = 4 * 1024 * 1024 * 1024
	if code := f.run(t, "status"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.errOut.String())
	}
	for _, want := range []string{"2 CPUs, 4GiB memory", "configured: 4 CPUs, 8GiB memory"} {
		if !strings.Contains(f.out.String(), want) {
			t.Fatalf("status missing %q:\n%s", want, f.out.String())
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
