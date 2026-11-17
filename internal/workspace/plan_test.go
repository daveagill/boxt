package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daveagill/boxt/internal/config"
)

func TestPlanRemovalClean(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "clean", "")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := m.PlanRemoval(context.Background(), "clean", RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.WouldRemove() || len(plan.Blockers()) != 0 {
		t.Fatalf("clean plan blocked: %#v", plan.Blockers())
	}
	if plan.Branch != "boxt/clean" {
		t.Fatalf("branch = %q", plan.Branch)
	}
	if _, err := os.Stat(ws.HostPath); err != nil {
		t.Fatal("plan mutated the workspace")
	}
}

func TestPlanRemovalReportsDirtyAndUntracked(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)
	ws, err := m.Create(context.Background(), "dirty", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.HostPath, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := m.PlanRemoval(context.Background(), "dirty", RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Dirty) != 1 || plan.Dirty[0] != "README.md" {
		t.Fatalf("dirty = %v", plan.Dirty)
	}
	if len(plan.Untracked) != 1 || plan.Untracked[0] != "new.txt" {
		t.Fatalf("untracked = %v", plan.Untracked)
	}
	if plan.WouldRemove() {
		t.Fatal("dirty plan reports removable")
	}
	blockers := strings.Join(plan.Blockers(), "\n")
	if !strings.Contains(blockers, "uncommitted changes") || !strings.Contains(blockers, "not ignored") {
		t.Fatalf("blockers = %q", blockers)
	}
}

func TestPlanRemovalIgnoredPolicy(t *testing.T) {
	patterns := []string{".env", "node_modules/"}
	setups := []struct {
		name      string
		rules     config.SeedConfig
		wantBlock bool
	}{
		{name: "allow_remove false", rules: config.SeedConfig{}, wantBlock: true},
		{name: "matching rules", rules: config.SeedConfig{AllowRemove: true, Include: []string{".env", "node_modules/"}}},
		{name: "unmatched rules", rules: config.SeedConfig{AllowRemove: true, Include: []string{".env"}}, wantBlock: true},
	}
	for _, tc := range setups {
		t.Run(tc.name, func(t *testing.T) {
			m, wsPath := setupIgnoredWorkspace(t, "ignored", patterns...)
			if err := os.MkdirAll(filepath.Join(wsPath, "node_modules", "pkg"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wsPath, "node_modules", "pkg", "index.js"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			plan, err := m.PlanRemoval(context.Background(), "ignored", RemoveOptions{Seed: tc.rules})
			if err != nil {
				t.Fatal(err)
			}
			if plan.IgnoredTotal == 0 {
				t.Fatal("expected ignored files")
			}
			if got, want := plan.WouldRemove(), !tc.wantBlock; got != want {
				t.Fatalf("WouldRemove = %v, blockers = %v", got, plan.Blockers())
			}
		})
	}
}

func TestPlanRemovalLockedAndMissing(t *testing.T) {
	repo := initRepo(t)
	m := newManager(t, repo)

	locked, err := m.Create(context.Background(), "locked", "")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "worktree", "lock", "--reason", "testing", locked.HostPath)
	plan, err := m.PlanRemoval(context.Background(), "locked", RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Locked || plan.LockReason != "testing" {
		t.Fatalf("locked plan = %#v", plan)
	}
	if len(plan.Blockers()) == 0 {
		t.Fatal("locked plan has no blockers")
	}

	missing, err := m.Create(context.Background(), "missing", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(missing.HostPath); err != nil {
		t.Fatal(err)
	}
	plan, err = m.PlanRemoval(context.Background(), "missing", RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Missing {
		t.Fatalf("missing plan = %#v", plan)
	}
	if out := runGit(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(out, missing.HostPath) {
		t.Fatal("dry-run pruned stale worktree metadata")
	}
}

func TestParseStatusEntries(t *testing.T) {
	// Modified, untracked, and a rename, whose original path is a separate
	// NUL-terminated field.
	out := " M src/a.go\x00?? new.txt\x00R  src/b.go\x00src/old.go\x00"
	dirty, untracked := parseStatusEntries(out)
	if strings.Join(dirty, ",") != "src/a.go,src/b.go" {
		t.Fatalf("dirty = %v", dirty)
	}
	if strings.Join(untracked, ",") != "new.txt" {
		t.Fatalf("untracked = %v", untracked)
	}
}
