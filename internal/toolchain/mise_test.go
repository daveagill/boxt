package toolchain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/command"
	"github.com/daveagill/boxt/internal/config"
	"github.com/daveagill/boxt/internal/lima"
	"github.com/daveagill/boxt/internal/project"
)

type fakeTool struct {
	Requested string
	Version   string
}

type fakeGuest struct {
	depsReady     bool
	miseInstalled bool
	tools         map[string]fakeTool
	installFail   map[string]bool
	aptFail       bool
	downloadFail  bool
	miseFail      bool
	requests      []lima.ShellRequest
}

func newFakeGuest() *fakeGuest {
	return &fakeGuest{
		tools:       map[string]fakeTool{},
		installFail: map[string]bool{},
	}
}

func (f *fakeGuest) Shell(ctx context.Context, req lima.ShellRequest) error {
	f.requests = append(f.requests, req)
	joined := strings.Join(req.Command, " ")
	fail := func(msg string) error {
		if req.Stderr != nil {
			_, _ = io.WriteString(req.Stderr, msg)
		}
		return &command.Error{Name: "limactl", ExitCode: 1, Stderr: msg}
	}
	switch {
	case strings.Contains(joined, "dpkg-query"):
		if !f.depsReady {
			return fail("missing packages")
		}
		return nil
	case strings.Contains(joined, "apt-get update"):
		if f.aptFail {
			return fail("apt update failed")
		}
		return nil
	case strings.Contains(joined, "apt-get install"):
		if f.aptFail {
			return fail("apt install failed")
		}
		f.depsReady = true
		return nil
	case joined == lima.GuestMiseBin+" --version":
		if !f.miseInstalled || f.miseFail {
			return fail("mise: not found")
		}
		_, _ = io.WriteString(req.Stdout, "mise 2026.11.8 linux-arm64 (2026-11-13)\n")
		return nil
	case strings.Contains(joined, "mktemp "):
		_, _ = io.WriteString(req.Stdout, "/tmp/boxt-mise-install.test\n")
		return nil
	case strings.Contains(joined, "curl -fsSL -o"):
		if f.downloadFail {
			return fail("download failed")
		}
		return nil
	case strings.Contains(joined, "sh /tmp/boxt-mise-install"):
		f.miseInstalled = true
		return nil
	case strings.Contains(joined, "mise install "):
		spec := joined[strings.Index(joined, "mise install ")+len("mise install "):]
		spec = strings.TrimSpace(spec)
		if i := strings.IndexByte(spec, ' '); i >= 0 {
			spec = spec[:i]
		}
		if f.installFail[spec] {
			return fail("failed to install " + spec)
		}
		i := strings.LastIndex(spec, "@")
		if i < 0 {
			return fail("bad spec " + spec)
		}
		name, version := spec[:i], spec[i+1:]
		resolved := version
		switch version {
		case "24":
			resolved = "24.22.3"
		case "3.13":
			resolved = "3.13.18"
		default:
			resolved = version + ".1"
		}
		f.tools[name] = fakeTool{Requested: version, Version: resolved}
		return nil
	case strings.Contains(joined, "mise ls --json"):
		data := map[string][]map[string]any{}
		for name, tool := range f.tools {
			data[name] = []map[string]any{{
				"version":           tool.Version,
				"requested_version": tool.Requested,
				"install_path":      "/home/boxt/.local/share/mise/installs/" + name + "/" + tool.Version,
				"installed":         true,
				"active":            true,
			}}
		}
		encoded, _ := json.Marshal(data)
		_, _ = io.WriteString(req.Stdout, string(encoded))
		return nil
	case strings.Contains(joined, "mise exec"):
		return nil
	}
	return nil
}

func testProject(tools map[string]string) *project.Project {
	return &project.Project{
		Root: "/tmp/project",
		ID:   "abc",
		Config: &config.Config{
			Version: 1,
			Tools:   tools,
		},
	}
}

func TestWrapArgv(t *testing.T) {
	if got := WrapArgv(nil, []string{"true"}); len(got) != 1 || got[0] != "true" {
		t.Fatalf("empty tools changed argv: %#v", got)
	}
	tools := map[string]string{"python": "3.13", "node": "24"}
	got := WrapArgv(tools, []string{"npm", "test"})
	want := []string{
		"/usr/bin/env", "MISE_NO_CONFIG=1", "MISE_EXEC_AUTO_INSTALL=0",
		lima.GuestMiseBin, "exec", "node@24", "python@3.13", "--", "npm", "test",
	}
	if !equal(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if tools["node"] != "24" {
		t.Fatal("tools map mutated")
	}
}

func TestParseMiseVersion(t *testing.T) {
	tests := map[string]string{
		"mise 2026.11.8 linux-arm64 (2026-11-13)\n": "2026.11.8",
		"mise v2024.1.0\n":                          "v2024.1.0",
		"2026.11.8 linux-arm64 (2026-11-13)\n":      "2026.11.8",
		"weird\n":                                   "",
	}
	for in, want := range tests {
		if got := parseMiseVersion(in); got != want {
			t.Errorf("parseMiseVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVersionMatches(t *testing.T) {
	tests := []struct {
		declared, candidate string
		want                bool
	}{
		{"24", "24", true},
		{"24", "24.22.3", true},
		{"3.13", "3.13.18", true},
		{"24", "22.1.0", false},
		{"2", "24.1.0", false},
		{"latest", "1.2.3", true},
		{"1.2.3", "latest", true},
		{"", "1.2.3", false},
	}
	for _, tt := range tests {
		if got := versionMatches(tt.declared, tt.candidate); got != tt.want {
			t.Errorf("versionMatches(%q,%q)=%v want %v", tt.declared, tt.candidate, got, tt.want)
		}
	}
}

func TestStatusFor(t *testing.T) {
	tests := []struct {
		name    string
		status  ToolStatus
		entries []miseEntry
		want    State
		version string
	}{
		{"missing", ToolStatus{Name: "node", Declared: "24"}, nil, StateMissing, ""},
		{"installed via requested", ToolStatus{Name: "node", Declared: "24"}, []miseEntry{{Version: "24.22.3", RequestedVersion: "24", Installed: true}}, StateInstalled, "24.22.3"},
		{"installed via version", ToolStatus{Name: "node", Declared: "24"}, []miseEntry{{Version: "24.22.3", Installed: true}}, StateInstalled, "24.22.3"},
		{"mismatch", ToolStatus{Name: "python", Declared: "3.13"}, []miseEntry{{Version: "3.12.1", RequestedVersion: "3.12", Installed: true}}, StateMismatch, "3.12.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statusFor(tt.status, tt.entries)
			if got.State != tt.want || got.Installed != tt.version {
				t.Fatalf("got %#v, want state %s version %s", got, tt.want, tt.version)
			}
		})
	}
}

func TestParseMiseLS(t *testing.T) {
	data, err := parseMiseLS(`{"node":[{"version":"24.1.0","installed":true}]}`)
	if err != nil || data["node"][0].Version != "24.1.0" {
		t.Fatalf("parse = %#v, %v", data, err)
	}
	data, err = parseMiseLS("")
	if err != nil || len(data) != 0 {
		t.Fatalf("empty parse = %#v, %v", data, err)
	}
	if _, err := parseMiseLS("not json"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestProvisionFreshVM(t *testing.T) {
	g := newFakeGuest()
	m := New(g)
	p := testProject(map[string]string{"node": "24", "python": "3.13"})
	report, err := m.Provision(context.Background(), p)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if report.MiseVersion != "2026.11.8" {
		t.Fatalf("mise version = %q", report.MiseVersion)
	}
	if len(report.Tools) != 2 || report.Failed() {
		t.Fatalf("report = %#v", report)
	}
	if report.Tools[0].Installed != "24.22.3" || report.Tools[1].Installed != "3.13.18" {
		t.Fatalf("installed versions = %#v", report.Tools)
	}
	if !g.depsReady || !g.miseInstalled {
		t.Fatalf("provisioning incomplete: deps=%v mise=%v", g.depsReady, g.miseInstalled)
	}
	joined := allCommands(g)
	for _, want := range []string{"apt-get update", "apt-get install", "mktemp ", "curl -fsSL -o", "sh /tmp/boxt-mise-install", "mise install node@24", "mise install python@3.13", "mise exec"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("commands missing %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, "instance=boxt-abc") {
		t.Fatalf("wrong instance:\n%s", joined)
	}
}

func TestProvisionReportsFailuresAndContinues(t *testing.T) {
	g := newFakeGuest()
	g.depsReady = true
	g.miseInstalled = true
	g.installFail["node@24"] = true
	p := testProject(map[string]string{"node": "24", "python": "3.13"})
	m := New(g)
	report, err := m.Provision(context.Background(), p)
	if err == nil {
		t.Fatal("expected provisioning error")
	}
	if !strings.Contains(err.Error(), "node@24") {
		t.Fatalf("error does not name tool: %v", err)
	}
	if !report.Failed() {
		t.Fatal("report did not record failure")
	}
	if _, ok := g.tools["python"]; !ok {
		t.Fatal("did not continue provisioning other tools")
	}
}

func TestProvisionNoTools(t *testing.T) {
	g := newFakeGuest()
	m := New(g)
	p := testProject(nil)
	report, err := m.Provision(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Tools) != 0 {
		t.Fatalf("report = %#v", report)
	}
	joined := allCommands(g)
	if strings.Contains(joined, "mise install") || strings.Contains(joined, "mise exec") {
		t.Fatalf("unexpected tool commands:\n%s", joined)
	}
}

func TestProvisionIdempotent(t *testing.T) {
	g := newFakeGuest()
	g.depsReady = true
	g.miseInstalled = true
	p := testProject(map[string]string{"node": "24"})
	m := New(g)
	if _, err := m.Provision(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	g.requests = nil
	if _, err := m.Provision(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	joined := allCommands(g)
	if strings.Contains(joined, "apt-get") || strings.Contains(joined, "boxt-mise-install") {
		t.Fatalf("second provision repeated setup:\n%s", joined)
	}
	if strings.Contains(joined, "mise install node@24") {
		t.Fatalf("second provision reinstalled a satisfied tool:\n%s", joined)
	}
}

func TestProvisionSkipsSatisfiedTools(t *testing.T) {
	g := newFakeGuest()
	g.depsReady = true
	g.miseInstalled = true
	g.tools["node"] = fakeTool{Requested: "24", Version: "24.22.3"}
	p := testProject(map[string]string{"node": "24"})
	report, err := New(g).Provision(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Tools) != 1 || report.Tools[0].Installed != "24.22.3" {
		t.Fatalf("report = %#v", report)
	}
	if joined := allCommands(g); strings.Contains(joined, "mise install") {
		t.Fatalf("satisfied tool was reinstalled:\n%s", joined)
	}
}

func TestInstallSkipsSatisfiedVersion(t *testing.T) {
	g := newFakeGuest()
	g.depsReady = true
	g.miseInstalled = true
	g.tools["node"] = fakeTool{Requested: "24", Version: "24.22.3"}
	version, err := New(g).Install(context.Background(), testProject(nil), "node", "24")
	if err != nil {
		t.Fatal(err)
	}
	if version != "24.22.3" {
		t.Fatalf("version = %q", version)
	}
	if joined := allCommands(g); strings.Contains(joined, "mise install") {
		t.Fatalf("satisfied tool was reinstalled:\n%s", joined)
	}
}

func TestEnsureDepsFailure(t *testing.T) {
	g := newFakeGuest()
	g.aptFail = true
	if err := New(g).EnsureDeps(context.Background(), testProject(nil)); err == nil {
		t.Fatal("expected error")
	}
}

func TestEnsureMiseDownloadFailure(t *testing.T) {
	g := newFakeGuest()
	g.depsReady = true
	g.downloadFail = true
	if _, err := New(g).EnsureMise(context.Background(), testProject(nil)); err == nil {
		t.Fatal("expected error")
	}
}

func TestEnsureMiseBrokenInstall(t *testing.T) {
	g := newFakeGuest()
	g.depsReady = true
	g.miseFail = true
	if _, err := New(g).EnsureMise(context.Background(), testProject(nil)); err == nil {
		t.Fatal("expected error")
	}
}

func TestListNotProvisioned(t *testing.T) {
	g := newFakeGuest()
	statuses, err := New(g).List(context.Background(), testProject(map[string]string{"node": "24"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].State != StateNotProvisioned {
		t.Fatalf("statuses = %#v", statuses)
	}
}

func TestListInstalledAndMismatch(t *testing.T) {
	g := newFakeGuest()
	g.depsReady = true
	g.miseInstalled = true
	g.tools["node"] = fakeTool{Requested: "24", Version: "24.22.3"}
	g.tools["python"] = fakeTool{Requested: "3.12", Version: "3.12.1"}
	statuses, err := New(g).List(context.Background(), testProject(map[string]string{
		"node":   "24",
		"python": "3.13",
		"go":     "1.24",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 3 {
		t.Fatalf("statuses = %#v", statuses)
	}
	byName := map[string]ToolStatus{}
	for _, s := range statuses {
		byName[s.Name] = s
	}
	if byName["node"].State != StateInstalled || byName["node"].Installed != "24.22.3" {
		t.Fatalf("node = %#v", byName["node"])
	}
	if byName["python"].State != StateMismatch {
		t.Fatalf("python = %#v", byName["python"])
	}
	if byName["go"].State != StateMissing {
		t.Fatalf("go = %#v", byName["go"])
	}
}

func TestInstallReturnsResolvedVersion(t *testing.T) {
	g := newFakeGuest()
	g.depsReady = true
	g.miseInstalled = true
	version, err := New(g).Install(context.Background(), testProject(nil), "node", "24")
	if err != nil {
		t.Fatal(err)
	}
	if version != "24.22.3" {
		t.Fatalf("version = %q", version)
	}
}

func TestInstallFailure(t *testing.T) {
	g := newFakeGuest()
	g.depsReady = true
	g.miseInstalled = true
	g.installFail["node@24"] = true
	if _, err := New(g).Install(context.Background(), testProject(nil), "node", "24"); err == nil {
		t.Fatal("expected error")
	}
}

func TestManagerNoGuest(t *testing.T) {
	m := &Manager{}
	if err := m.EnsureDeps(context.Background(), testProject(nil)); err == nil {
		t.Fatal("expected error")
	}
}

func allCommands(g *fakeGuest) string {
	var b strings.Builder
	for _, r := range g.requests {
		fmt.Fprintf(&b, "instance=%s workdir=%s cmd=%s\n", r.Instance, r.WorkDir, strings.Join(r.Command, " "))
	}
	return b.String()
}

func equal(a, b []string) bool {
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
