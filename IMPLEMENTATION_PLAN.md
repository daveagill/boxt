# boxt — Implementation Plan

This plan turns `plan.md` into ordered, testable milestones. The primary agent acts as
technical lead: it defines interfaces, integrates delegated work, and verifies every
milestone with the full test suite.

## Environment constraints (verified)

- Workspace started with `plan.md` only; now contains a complete Go implementation.
- Go 1.27.1 on Linux/aarch64.
- Git 2.55.0 available.
- Lima 2.3.0 downloaded for config validation and real VM integration tests.
- QEMU 11.0.3 installed; `/dev/kvm` is unavailable, so real VMs run under TCG
  (slow but functional).
- Integration tests are gated (`BOXT_INTEGRATION=1`, `BOXT_LIMACTL`, `BOXT_BIN`).
  They passed in this environment with 512 MiB guests.
- macOS and Fedora host-specific VM runs are not available here. Unit tests cover
  platform path logic and all release targets cross-compile.

## Architecture summary

```
cmd/boxt/main.go                 process entry, exit-code mapping
internal/cli/                    Cobra commands + presentation only
internal/config/                 schema, defaults, validation, atomic YAML edits
internal/project/                discovery, canonical identity, project metadata
internal/platform/               GOOS/XDG/Application Support paths, prereqs
internal/lima/                   limactl adapter, YAML generation, lifecycle/status
internal/executor/               single execution engine (non-interactive + PTY)
internal/workspace/              Git worktree service, names/paths
internal/toolchain/              mise installation, reconciliation, tool status
```

### Core interfaces (defined before parallel work)

```go
// Process execution seam used by Lima, Git and tests.
type Runner interface {
    Run(ctx context.Context, cmd Command) error
}

// Lima guest execution seam used by the executor and toolchain.
type Guest interface {
    RunGuest(ctx context.Context, req GuestRequest) error
}

// Git seam used by workspace discovery and mutation.
type Git interface {
    Output(ctx context.Context, args ...string) (string, error)
}
```

Rules:

- CLI never calls `os/exec` directly; all external tools go through focused adapters.
- Commands are always `[]string`; no shell strings built from user input.
- Guest commands use `limactl shell --workdir` (no `cd ... && ...`).
- Interactive execution inherits terminal streams and propagates exit codes/signals.
- `.boxt/config.yaml` stays the source of truth; mise configuration is internal.

### Identity and paths (fixed design)

- Canonical project root: absolute path with symlinks resolved (`EvalSymlinks`).
- Project ID: first 16 hex chars of SHA-256 of the canonical root.
- Lima instance: `boxt-<project-id>`.
- Host workspace root: `<data-dir>/workspaces/<project-id>`.
- Guest workspace root: `/workspaces/<project-id>` (single project-scoped mount).
- Default workspace host path: project root; guest path: `/workspace`.
- Named workspace host path: `<workspace-root>/<name>`; guest path:
  `/workspaces/<project-id>/<name>`.
- Data directory: macOS `~/Library/Application Support/boxt`; Linux
  `${XDG_DATA_HOME:-~/.local/share}/boxt`.

### Configuration extensions

The initial schema in `plan.md` is implemented as-is. One optional, documented field is
added to satisfy the networking requirement without exposing services by default:

```yaml
vm:
  ports:
    - guest: 3000      # required
      host: 3000      # required
      host_ip: 127.0.0.1  # optional, default 127.0.0.1
```

Host ports are checked before starting the VM. Conflicts are reported, never silently
remapped.

## Milestones, dependencies, acceptance criteria

### M1 — CLI, configuration, project discovery

**Depends:** none.

Tasks:
- `go.mod`, `cmd/boxt`, Cobra root, global `--verbose`, error/exit-code mapping.
- `config`: schema, defaults, strict validation with field paths, version check,
  atomic writes, YAML-node editing for tool mutations (preserve comments/unknown keys).
- `project`: root discovery (`boxt init [path]`), non-overwrite init, Git detection,
  canonical ID.
- `platform`: data-dir resolution for macOS/Linux, host OS/arch, prerequisite messages.
- `boxt init`, `boxt status` shell (VM section reports "not created" until M2).
- Unit tests + README skeleton (installation/init/config).

**Acceptance:**
- `boxt init` creates `.boxt/config.yaml` once, never overwrites; invalid files fail
  with actionable field paths.
- Commands from nested directories find the nearest project root.
- Symlinked paths map to one project ID/VM.
- `go test ./...` passes; `gofmt`/`go vet` clean.

### M2 — Lima integration and lifecycle

**Depends:** M1.

Tasks:
- `lima` client over a `Runner` seam; command construction tested with fakes.
- YAML generation: Ubuntu 24.04 (arm64/amd64), user `boxt`, writable `/workspace`,
  scoped `/workspaces/<id>` mount, resources, optional port forwards, no host home /
  Docker socket.
- `up` (create/start, idempotent), `down`, `status`, `destroy` (confirmation,
  `--yes`), auto-start helper for run commands, port conflict detection.
- Integration tests gated by `BOXT_INTEGRATION=1`.

**Acceptance:**
- Generated YAML passes `limactl validate` when available; no deprecated/guessed keys.
- Lifecycle commands are exactly supported Lima CLI invocations.
- Destroy never removes project files or worktrees.
- Unit tests cover missing/stopped/running/already-running states.

### M3 — Unified execution, run targets, shell

**Depends:** M1 (config targets), M2 (guest execution).

Tasks:
- `executor` request model, guest workdir resolution, streamed execution, PTY
  interactive mode, terminal restoration, signal/exit-code propagation.
- `boxt` / `boxt run`, default and named targets, `--` arbitrary commands,
  `--no-start`; exact unknown-target guidance.
- `boxt shell` starts in default workspace.
- Unit tests for resolution, argv construction, exit codes; integration PTY test.

**Acceptance:**
- All execution paths use one executor and differ only by working directory.
- Unknown target never falls back to PATH.
- Non-zero guest exit status becomes boxt's exit status.
- No `cd &&` or user-string shell interpolation in execution.

### M4 — Toolchain (mise)

**Depends:** M2 (provisioning/execution), M3 (execute wrapper).

Research input: `mise` installation, global config/trust, `mise exec`, `mise ls --json`,
tool IDs. No user-facing mise config.

Tasks:
- Ensure base apt dependencies (idempotent, sudo only for provisioning).
- Install/verify mise once per VM under `/home/boxt`.
- Reconcile declared tools; install missing/switch versions; idempotent `provision`.
- `boxt tool add/remove/list`; add applies immediately when running, defers when
  stopped; remove only removes declaration.
- Execution environment wraps commands with mise so PATH works non-interactively;
  never rely on `.bashrc`.
- `boxt up` provisions before declaring ready.
- Unit tests with fake guest; integration tests gated.

**Acceptance:**
- Tool versions persist across `down`/`up`.
- Version mismatches are corrected by `provision`; failures are explicit.
- Removing a declaration leaves installed software intact and says so.
- `boxt run` sees declared tools without shell profile setup.

### M5 — Workspaces (Git worktrees)

**Depends:** M1 (identity/paths), M2 (workspace mount), M3 (executor).

Tasks:
- Workspace name validation (no traversal/absolute/duplicates/reserved `default`).
- Git worktree create/list/remove; external host path; branch `boxt/<name>` by
  default with `--branch` override.
- Dirty-worktree refusal with `--force`; stale metadata via `worktree prune`; never
  `rm -rf`; branch retained.
- `workspace create|list|path|remove|run|shell`.
- Host tests with real temporary Git repos; gated guest visibility integration tests.

**Acceptance:**
- Parent repo never reports external worktrees as untracked.
- Dirty worktrees are not silently deleted.
- Guest and host both see named workspaces at the deterministic paths.
- Workspace operations work from any directory inside the project.

### M6 — Polish, docs, release

**Depends:** M1–M5.

Tasks:
- Complete `status`, error messages, debug logging, troubleshooting docs.
- README complete with workflow, layout, networking/security limitations, platform
  notes, examples.
- CI workflow, Makefile, release packaging (goreleaser config and install docs).
- Cross-platform unit tests (`GOOS=darwin`, path resolution), final vet/test pass.
- Final limitations report.

**Acceptance:**
- Definition of done in `plan.md` §18 is addressed or explicitly documented as
  unverified in this environment (no virtualization here).
- `go test ./...`, `go vet ./...`, `gofmt -l` clean; integration suite documented.

## Delegation map

| Work | Owner | Why |
|---|---|---|
| Lima CLI/YAML/PTY research | subagent (parallel) | independent external facts |
| mise install/exec research | subagent (parallel) | independent external facts |
| Git worktree behavior research + experiments | subagent (parallel) | independent external facts |
| Core config/project/platform + interfaces | primary | tightly coupled foundation |
| Lima config generator + fake-runner tests | primary, optional bounded subagent review | shared schema/interface |
| Executor + PTY | primary | cross-cutting, security-sensitive |
| Git workspace package | primary (after interfaces) or one bounded subagent | independent once paths/Git seam fixed |
| Toolchain/mise | primary | depends on executor/env design |
| README/docs draft | subagent after CLI stabilizes | low-conflict, independent |
| Security/test coverage review | subagent near end of each major milestone | independent review |

No two agents edit the same files concurrently. Shared interfaces and file ownership are
fixed before delegation. Every delegated change is inspected and tested by the primary
agent before acceptance.

## Verification commands

```bash
gofmt -l .
go vet ./...
go test ./...
BOXT_INTEGRATION=1 BOXT_LIMACTL=$(command -v limactl) BOXT_BIN=$(pwd)/bin/boxt \
  go test -p 1 -tags=integration ./...
go build ./cmd/boxt
```

## Risks / open items

- The full integration suite passed on Linux/aarch64 with QEMU TCG and 512 MiB
  guests. Without KVM, VMs are slow; larger guest memory can exceed a small
  container's host RAM and cause QEMU to be OOM-killed.
- macOS Apple Silicon and Fedora/Ubuntu x86_64 VM runs remain unverified here;
  their path handling is unit-tested and all release targets cross-compile.
- Lima YAML schema changes across versions: minimum Lima 2.0 is declared in
  generated config and generation is kept conservative.
- Custom guest user `boxt` works; passwordless sudo is configured by Lima and was
  exercised by real apt provisioning.
- Port forwarding defaults to `auto` (loopback-only dynamic forwarding, requested
  for Vite/agent workflows). `explicit` gives deny-by-default. This is a
  deliberate amendment to the original default-off plan.

## Progress log

- **M1 complete** — Go module, Cobra CLI, config schema/validation/atomic node-preserving
  edits, project discovery/canonical IDs, platform data dirs, `boxt init`, unit tests.
  Commit `5325ea9`.
- **M2 complete** — `limactl` adapter over a process-runner seam, generated Lima YAML
  validated with real `limactl 2.3.0`, project/workspace mounts, guest user `boxt`,
  port-conflict checks, `up`/`down`/`status`/`destroy`, auto-start helper, gated
  integration tests. Unit tests use a fake Lima runner.
- **M3 complete** — single executor for non-interactive and PTY execution, target
  resolution with no PATH fallback, arbitrary commands after `--`, auto-start and
  `--no-start`, `boxt shell`, signal handling, exit-status propagation. Interactive
  streams are inherited by `limactl shell`, which allocates the SSH PTY and handles
  window resize; non-interactive streams are piped to avoid an unwanted PTY.
  Non-interactive SIGINT/SIGTERM cancel the child; interactive SIGTERM cancels it.
- **M4 complete** — mise installer version pinned, apt base dependencies,
  idempotent per-tool reconciliation that skips satisfied tools, `tool add/list/remove`,
  `provision`, automatic provisioning in `up`/auto-start, and non-interactive tool
  PATH via `mise exec` with configs and auto-install disabled. An existing newer
  mise install is accepted. Unit tests use a fake guest; integration test gated.
- **M5 complete** — stable project ID paths, external Git worktree create/list/remove,
  dirty/force handling, stale metadata pruning, branch retention, default workspace,
  named workspace `run`/`shell`, host and gated guest integration tests. Lima's
  implicit port-forward catch-all is now controlled by `vm.port_forwarding`
  (`auto` default, `explicit` to restrict to declared ports).
- **M6 complete** — richer `status` (actual VM resources, config drift, workspaces),
  config fingerprints with destroy/up guidance, config write locking and symlink
  preservation, ignored-file protection on workspace removal, atomic worktree
  rollback, private mise installer temp file, process-group cancellation, home/root
  mount rejection, overlapping-mount validation, project metadata discovery from
  worktrees, cross-platform builds, CI, goreleaser packaging, example project, and
  final documentation.
- **Verified with real Lima/QEMU VMs** (Linux/aarch64, TCG, 512 MiB guests):
  VM create/start/stop/delete, project and workspace mounts, guest user `boxt`,
  persistence across restart, `boxt up` provisioning (apt + mise), `tool add node@24`,
  node execution through `mise exec`, PTY shell in `/workspace`, guest exit status,
  dynamic port forwarding from guest to host, workspace create/list/run/shell,
  project discovery from inside a workspace, destroy preserving project/worktrees,
  and full gated integration suite (`internal/lima`, `internal/toolchain`,
  `internal/workspace`, `internal/cli`).
