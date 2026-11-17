package workspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/daveagill/boxt/internal/config"
)

// RemovalPlan is a read-only description of what RemoveWithOptions would do. It
// drives `boxt workspace remove --dry-run` and never changes refs, worktrees or
// files.
type RemovalPlan struct {
	Workspace Workspace
	Branch    string

	Missing    bool
	Locked     bool
	LockReason string
	Broken     bool
	Operation  string

	Dirty     []string
	Untracked []string

	IgnoredTotal     int
	IgnoredMatched   []string
	IgnoredUnmatched []string

	AllowRemove bool
	Force       bool
}

// Blockers lists the reasons a non-forced removal would be refused, in the same
// order RemoveWithOptions applies them.
func (p RemovalPlan) Blockers() []string {
	if p.Force {
		return nil
	}
	var blockers []string
	if p.Locked {
		blockers = append(blockers, "the worktree is locked by Git")
	}
	if p.Broken {
		blockers = append(blockers, "the worktree metadata is broken")
	}
	if p.Missing {
		return blockers
	}
	if p.Operation != "" {
		blockers = append(blockers, fmt.Sprintf("a %s is in progress", p.Operation))
	}
	if len(p.Dirty) > 0 {
		blockers = append(blockers, fmt.Sprintf("%s %s uncommitted changes", plural(len(p.Dirty), "tracked file"), hasHave(len(p.Dirty))))
	}
	if len(p.Untracked) > 0 {
		blockers = append(blockers, fmt.Sprintf("%s %s not ignored", plural(len(p.Untracked), "untracked file"), isAre(len(p.Untracked))))
	}
	switch {
	case p.IgnoredTotal == 0:
	case !p.AllowRemove:
		blockers = append(blockers, fmt.Sprintf("%s would be deleted and seed.allow_remove is false", plural(p.IgnoredTotal, "ignored file")))
	case len(p.IgnoredUnmatched) > 0:
		blockers = append(blockers, fmt.Sprintf("%s do not match seed.include/seed.exclude", plural(len(p.IgnoredUnmatched), "ignored path")))
	}
	return blockers
}

// WouldRemove reports whether a non-forced removal would succeed.
func (p RemovalPlan) WouldRemove() bool {
	return len(p.Blockers()) == 0 && !p.Missing
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func hasHave(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}

// PlanRemoval inspects a workspace and reports what RemoveWithOptions would do
// without mutating anything. It deliberately does not prune stale metadata.
func (m *Manager) PlanRemoval(ctx context.Context, name string, opts RemoveOptions) (RemovalPlan, error) {
	if name == DefaultName {
		return RemovalPlan{}, &ErrDefaultWorkspace{}
	}
	if err := ValidateName(name); err != nil {
		return RemovalPlan{}, err
	}
	if _, err := m.repoRoot(ctx); err != nil {
		return RemovalPlan{}, err
	}
	ws, err := m.find(ctx, name)
	if err != nil {
		return RemovalPlan{}, err
	}
	plan := RemovalPlan{
		Workspace:   ws,
		Branch:      ws.Branch,
		AllowRemove: opts.Seed.AllowRemove,
		Force:       opts.Force,
	}
	if ws.Locked {
		plan.Locked = true
		plan.LockReason = ws.LockReason
		return plan, nil
	}
	if ws.Broken {
		plan.Broken = true
		return plan, nil
	}
	if ws.Missing {
		plan.Missing = true
		return plan, nil
	}

	op, err := m.pendingOperation(ctx, ws.HostPath)
	if err != nil {
		return RemovalPlan{}, fmt.Errorf("inspect workspace %s: %w", name, err)
	}
	plan.Operation = op

	inspection, err := m.inspectRemoval(ctx, ws.HostPath, opts.Seed)
	if err != nil {
		return RemovalPlan{}, err
	}
	plan.Dirty = inspection.Dirty
	plan.Untracked = inspection.Untracked
	plan.IgnoredTotal = len(inspection.Ignored)
	plan.IgnoredMatched = inspection.Matched
	plan.IgnoredUnmatched = inspection.Unmatched
	return plan, nil
}

// removalInspection is the shared, read-only policy input for both PlanRemoval
// and checkRemovable.
type removalInspection struct {
	Dirty     []string
	Untracked []string
	Ignored   []string
	Matched   []string
	Unmatched []string
}

func (m *Manager) inspectRemoval(ctx context.Context, dir string, rules config.SeedConfig) (removalInspection, error) {
	status, err := m.Git.Output(ctx, dir, "status", "--porcelain", "-z", "--ignore-submodules=none")
	if err != nil {
		return removalInspection{}, fmt.Errorf("check workspace status: %w", err)
	}
	dirty, untracked := parseStatusEntries(status)
	ignored, err := m.ignoredPaths(ctx, dir)
	if err != nil {
		return removalInspection{}, err
	}
	inspection := removalInspection{Dirty: dirty, Untracked: untracked, Ignored: ignored}
	if len(ignored) > 0 {
		matcher, err := newSeedMatcher(rules)
		if err != nil {
			return removalInspection{}, err
		}
		for _, rel := range ignored {
			if matcher.Match(rel) {
				inspection.Matched = append(inspection.Matched, rel)
			} else {
				inspection.Unmatched = append(inspection.Unmatched, rel)
			}
		}
	}
	return inspection, nil
}

// parseStatusEntries splits `git status --porcelain -z` output into tracked
// changes and non-ignored untracked paths. Rename and copy entries carry a
// second NUL-separated original path, which is skipped.
func parseStatusEntries(out string) (dirty, untracked []string) {
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 3 {
			continue
		}
		code := entry[:2]
		path := entry[3:]
		if code == "??" {
			untracked = append(untracked, path)
			continue
		}
		if code == "!!" {
			continue
		}
		dirty = append(dirty, path)
		if strings.ContainsAny(code, "RC") {
			i++
		}
	}
	return dirty, untracked
}
