# AGENTS.md

Engineering guide for coding agents working on **boxt**.

boxt is a local-first development environment manager: one persistent Ubuntu
24.04 VM per project, a shared mise-managed toolchain, and multiple independent
Git worktrees ("workspaces") mounted into that VM. Agent sandboxing is the
headline use case; the generic run-target/workspace model is the foundation.
The user-facing documentation is in `README.md`; this file records invariants
and workflows that must not be broken accidentally.

## Quick commands

```bash
make build                 # ./bin/boxt
make check                 # gofmt, go vet, go test ./...
go test ./...
go test -race ./...
make integration           # gated real-VM tests; see below
```

The release build is validated with:

```bash
goreleaser release --snapshot --clean
```

## Repository map

```text
cmd/boxt/                 process entry point
internal/command/         subprocess seam, exit codes, cancellation/process groups
internal/config/          schema, validation, atomic/locked YAML edits
internal/project/          discovery, canonical identity, worktree metadata
internal/preset/           static composable starter configurations for init
internal/platform/         host OS/arch, data dirs, prerequisites
internal/lima/            limactl adapter, generated Lima YAML, lifecycle, ports
internal/executor/        single execution engine (run/shell/workspaces)
internal/workspace/       Git worktree create/list/remove, names/paths
internal/toolchain/       mise installation, reconciliation, tool status
internal/cli/             Cobra commands and wiring; presentation only
examples/node-app/        complete runnable example
```

## Non-negotiable invariants

### Identity and storage

- Canonical project root: absolute, symlink-resolved.
- Project ID: first 16 hex characters of SHA-256(canonical project root).
- Lima instance name: `boxt-<project-id>`.
- Host workspace root: `<data-dir>/workspaces/<project-id>`.
- Guest workspace root: `/workspaces/<project-id>`.
- Default workspace: project root, mounted at `/workspace`.
- Data dir: macOS `~/Library/Application Support/boxt`; Linux
  `${XDG_DATA_HOME:-~/.local/share}/boxt`.
- Never mount the host home directory, an ancestor of home, `/`, or the Docker
  socket. The workspace data directory must be outside the project root.
- Only the project root and its project-scoped workspace root are mounted.
  Mounts are writable direct host mounts, not file synchronization.

### Configuration

- `.boxt/config.yaml` is the only source of truth for boxt-managed settings.
- Config version is `1`; unknown fields and unsupported versions are rejected.
- Tool/config edits must be atomic, locked across processes, and preserve
  comments, unrelated YAML and symlinked config files.
- `vm.port_forwarding` supports:
  - `auto` (default): Lima dynamic forwarding of guest loopback services to host
    loopback. This is deliberate, requested for Vite/agent workflows.
  - `explicit`: forward only `vm.ports`; all other ports are ignored.
- Explicit host ports are checked for conflicts. Guest port 22 is reserved.
- VM-level settings (CPU, memory, ports, forwarding mode, mounts/data dir) are
  applied at creation. boxt stores a fingerprint and refuses to run against a VM
  whose configuration differs; instruct the user to `boxt destroy` then
  `boxt up`. Do not silently update the fingerprint.
- `vm.auto_start` controls whether run/shell commands may create/start/provision
  the VM automatically (default true). `--no-start` overrides it for one command.
  It is an execution policy, not part of the VM creation fingerprint.
- Tool/run-target changes do not require VM recreation.
- Presets are static one-shot starter fragments for `boxt init`. They write
  ordinary config and are never stored as metadata. Composition is
  order-independent: tools/targets merge by name, conflicts are errors, and
  distinct default-target suggestions leave `run.default` unset unless
  `--default-target` is given. Do not turn presets into a runtime concept or a
  remote/plugin catalog.

### Lima

- Use supported `limactl` interfaces only; do not depend on internal Lima files or
  undocumented behavior.
- Generated config pins Ubuntu 24.04 images with digests, `minimumLimaVersion:
  2.0.0`, guest user `boxt` with passwordless sudo, containerd disabled and
  `propagateProxyEnv: false`.
- Validate generated YAML with `limactl validate` when changing config generation.
- `boxt up` is idempotent, starts/creates the VM, reconciles the toolchain, then
  reports ready. Provisioning failures must not claim readiness; leaving the VM
  running for troubleshooting is intentional.
- Auto-start before run/shell creates a missing VM and starts/provisions a stopped
  one. `--no-start` disables it for one command; `vm.auto_start: false` disables
  it for the project. `boxt up` always performs explicit setup.
- Do not assume a guest IP address.
- WSL2 is experimental host support: run the Linux boxt/limactl binaries inside
  WSL, keep projects/data on ext4 (not `/mnt/c`), and use nested virtualization
  for KVM. `platform.IsWSL` gates WSL-specific hints and path warnings.

### Execution

- All run/shell/workspace execution goes through `internal/executor`.
- `boxt run <target> -- <args...>` and
  `boxt workspace run <ws> <target> -- <args...>` append args to the configured
  target command as separate argv entries. `boxt run -- <command...>` remains the
  arbitrary-command form. More than one positional argument before `--` is an
  error, and the configured command slice must never be mutated.
- Pass argv separately; never build a shell string from user input and never use
  `cd ... && ...` as the execution mechanism. Use
  `limactl shell --workdir <guest-path>`.
- Interactive commands inherit terminal streams so `limactl`/SSH allocates the
  PTY, forwards window size and propagates child exit status. Non-interactive
  commands are piped to suppress an unwanted PTY.
- Non-interactive children run in a separate process group and are killed as a
  group on SIGINT/SIGTERM. Interactive terminal SIGINT is left to SSH.
- Preserve guest exit codes through the error chain so `boxt` exits with the same
  status. Do not leak full internal `limactl` argv into normal errors.
- `interactive: true` needs a host terminal for a guest PTY; warn when stdout is
  redirected.
- Per-workspace ports: `port_base` plus a deterministic offset (default workspace
  0, named workspaces 1–999 from project ID + name) is injected as `port_env`
  (default `PORT`) through `/usr/bin/env`, never by rewriting argv or using a
  shell. Reject offset collisions at execution time. `auto` forwarding exposes
  the same host port; `explicit` requires matching `vm.ports` entries.

### Toolchain

- mise is an internal implementation detail. Never require or merge `mise.toml`,
  `.mise.toml` or user mise configuration.
- Run mise with `MISE_NO_CONFIG=1` and `MISE_EXEC_AUTO_INSTALL=0`.
- Install the pinned mise release from the official `https://mise.run` endpoint
  into a private `mktemp` file. Accept an existing newer mise binary.
- Use apt only for system dependencies (curl, CA certificates, Git, tar, xz,
  unzip); use sudo only for provisioning/system administration.
- `tool add`: update config first, install immediately when running, defer when
  stopped/missing, keep the declaration on failure and point at `boxt provision`.
  `--target` adds a run target named after the tool; `--interactive` requires
  `--target`. Never silently overwrite a different target definition.
- `tool remove`: remove only the declaration; never uninstall shared software.
  Warn if a run target still references the tool.
- Reconciliation must skip satisfied tools and be safe to run repeatedly.
  Floating aliases (`latest`, `lts`, `stable`) are re-checked on provision.
  OpenCode's current mise backend is npm-based and requires Node; the preset
  intentionally includes Node LTS.

### Workspaces

- Use real Git worktrees, never manual copies or `rm -rf`.
- Worktrees live outside the project checkout under the project-scoped data dir.
- Default branch: `boxt/<workspace>`; `--branch` may override. Recreating a
  workspace reuses an existing branch if present.
- `boxt workspace create <name> --run <target>` resolves and validates the target
  before creating anything, then creates the worktree and runs the target through
  the shared executor in the new guest path. Preserve the guest exit status, leave
  the workspace on failure, and honor `--no-start`/`vm.auto_start`.
- `boxt workspace merge` is fast-forward only. Require clean source and project
  root, reject detached HEAD and diverged histories before changing refs, and
  never force/squash/create merge commits. `--remove` uses the safe worktree
  removal path and never deletes the branch.
- `workspace list`/`status` summaries are best-effort: report dirty state,
  short HEAD and ahead/behind vs the default branch, but never fail the whole
  list for one missing, detached or broken worktree.
- Ignored-file seeding copies only `git ls-files --others --ignored
  --exclude-standard` paths from the default workspace, never tracked files,
  never overwrites existing target paths, and skips symlinks/special files.
  `create --seed` happens after worktree creation and before `--run`/hooks.
- Store the owning project root in the worktree's Git admin directory as
  `boxt-project`, so commands run from inside a workspace resolve the same VM.
- Reject workspace names that are absolute, traversal-based, reserved (`default`)
  or otherwise unsafe. Reject duplicates.
- Creation is atomic: verify the checkout and write metadata; roll back the
  worktree (and only a newly-created branch) if post-add setup fails.
- Removal:
  - refuse uncommitted changes (`--force` to discard);
  - refuse ignored files such as `.env`/caches (`--force` to discard);
  - refuse locked worktrees (unlock with Git);
  - never prune unrelated worktrees;
  - handle stale metadata by targeted removal of that worktree only;
  - never delete the branch.
- Only boxt-created worktrees are managed. Do not treat unrelated manual Git
  worktrees as boxt workspaces.

### Hooks

- Lifecycle hooks are project-controlled code and are disabled until explicitly
  trusted. Store a SHA-256 of the normalized hook config in the data dir keyed by
  project ID; any hook change invalidates approval. Untrusted operations fail
  before mutation and list the commands. `boxt hooks status|trust|clear` manage
  approval. Hooks never run during `init` or `doctor`.
- Events are workspace create/merge/remove, each with pre/post. Commands are argv
  arrays; never build a shell string. Guest hooks use the shared executor and
  toolchain env in the workspace guest path; host hooks use `command.Runner` with
  the workspace host path as cwd (remove post runs in project root). Expose
  `BOXT_PROJECT_ROOT`, `BOXT_WORKSPACE`, `BOXT_WORKSPACE_PATH`, `BOXT_GUEST_PATH`.
- Pre-hook failure aborts the operation (create rolls back the new worktree and
  only a newly-created branch). Post-hook failure reports failure but does not
  undo a completed create/merge/remove. Apply a fixed generous timeout per hook.
- `create --seed` runs after worktree creation and before hooks and `--run`.

### Project discovery

- Find the nearest ancestor `.boxt/config.yaml`.
- From a boxt worktree, resolve the owning project via `boxt-project` metadata.
- A nested initialized project inside a workspace wins over the enclosing
  workspace's metadata.
- Refuse unmanaged linked worktrees that contain a copied `.boxt` config, and
  refuse home/root project roots.
- Never initialize a project implicitly; produce a helpful `boxt init` hint.

### Safety and UX

- `boxt doctor` diagnoses host prerequisites and project configuration. It may
  create the project-scoped data directory, but it never installs host
  dependencies, edits configuration, or creates/starts the VM. Exit 0 for
  OK/WARN, exit 1 when any check fails.
- `boxt destroy` deletes only the VM. Project files, config and worktrees remain.
  Confirmation is interactive (`yes`) or `--yes`.
- Do not silently expose services beyond loopback. `auto` forwarding is loopback
  only; `host_ip: 0.0.0.0` is an explicit LAN exposure.
- Provide actionable errors with hints. Keep normal output quiet; `-v` enables
  command debug logs. Remember that command arguments may contain secrets.
- Do not add Docker/Kubernetes, Nix, remote VMs, non-Git workspaces, active
  workspace selection, a TUI, database service management, credential sync,
  editor integration, or a plugin framework.

## Testing

Unit tests use fakes for external processes. Git tests use real temporary
repositories, and config tests exercise locking, symlinks and atomic writes.

Integration tests are gated and compile only with `-tags=integration`:

```bash
make build
BOXT_INTEGRATION=1 \
BOXT_INTEGRATION_MEMORY=512MiB \
BOXT_INTEGRATION_LIMA_ROOT=/var/tmp \
BOXT_LIMACTL=$(command -v limactl) \
BOXT_BIN=$(pwd)/bin/boxt \
go test -p 1 -tags=integration ./...
```

- `BOXT_INTEGRATION_MEMORY` defaults to `2GiB`; use a smaller guest on low-memory
  hosts. Without KVM, QEMU TCG is slow and larger guests can be OOM-killed.
- `BOXT_INTEGRATION_LIMA_ROOT` defaults to `/tmp`; point it at a filesystem with
  room for Lima images/disks.
- Integration coverage: VM lifecycle/persistence, project and workspace mounts,
  guest user, PTY shell and exit status, tool install/persistence, dynamic port
  forwarding, workspace host/guest visibility, destroy preservation.
- Real validation has passed on Linux/aarch64 with Lima 2.3.0 and QEMU TCG.
  macOS, Fedora and WSL2 VM runs remain host-specific; WSL2 is experimental and
  should be checked on a real Windows 11 host.
- Validate generated Lima YAML after config changes:

```bash
BOXT_LIMACTL=$(command -v limactl) go test ./internal/lima -run LimactlValidate
```

## Change checklist

1. Keep CLI thin; external tools go through `internal/command`.
2. Add or update unit tests for every behavior change.
3. Run `make check`, `go test -race ./...`, and cross-build
   `GOOS=darwin GOARCH=arm64` / `GOOS=linux GOARCH=amd64`.
4. Run gated integration tests for VM, executor, toolchain or workspace changes.
5. Update `README.md` for user-visible behavior and `AGENTS.md` for invariants.
6. Check that destroy/removal paths still never delete project/user data.
7. Verify config edits remain atomic, locked and comment/symlink preserving.

The original implementation specification was removed after the MVP was
completed. It remains available in Git history if historical requirements are
needed.
