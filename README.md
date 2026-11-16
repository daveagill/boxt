# boxt

**boxt** runs coding agents and development commands in a persistent, project-
scoped Linux sandbox. Each project gets one Ubuntu VM, a shared toolchain, and
multiple independent Git workspaces.

Run agents without giving them your host home directory, host SSH keys or Docker
socket. Project files stay on the host where your editor works; agents can edit
the project directly or work in an independent Git worktree.

```bash
mkdir myapp
cd myapp
git init

boxt doctor
boxt init --preset opencode
boxt run opencode
```

`boxt doctor` checks that your host has the required tools and configuration; it
never installs anything or starts a VM. The first run creates, starts and
provisions the VM automatically; `boxt up` does the same explicitly. The project
root is the default workspace; no extra workspace setup is required. To run
another agent in parallel in its own checkout:

```bash
boxt workspace create agent-2
boxt workspace run agent-2 opencode
```

Or create the workspace and start its agent in one command:

```bash
boxt workspace create agent-2 --run opencode
```

See [Getting started](#getting-started), [Presets](#presets),
[Workspaces](#workspaces), [Running commands](#running-commands) and the
complete [example](#complete-example).

## Supported hosts

| Host | Architecture |
|---|---|
| macOS | Apple Silicon (arm64) |
| Ubuntu | x86_64, aarch64 |
| Fedora | x86_64, aarch64 |
| Windows 11 + WSL2 (Ubuntu) | x86_64, aarch64 (experimental) |

The guest is always Ubuntu 24.04 LTS. [Lima](https://lima-vm.io/) provides
virtualization, Git worktrees provide workspaces, and mise is used internally for
tools.

Native Windows and macOS Intel are not supported. WSL2 is supported
experimentally: use WSL2 (not WSL1), keep projects and boxt data on the WSL ext4
filesystem, and enable nested virtualization for KVM acceleration. Without KVM,
Lima falls back to slow QEMU software emulation. Linux aarch64 support depends on
the installed QEMU and virtualization stack. The automated test suite
cross-compiles for all release targets and exercises host-path logic; real VM
runs must be performed on the supported host platforms.

## Getting started

`boxt doctor` reports `OK`, `WARN` or `FAIL` for each prerequisite and prints the
exact command or setting to fix failures. It exits non-zero when anything fails.
It never installs host software, edits project configuration, or starts the VM.

```text
OK    host: macos/arm64
OK    git: 2.55.0
OK    limactl: 2.3.0
OK    virtualization: Apple Virtualization (vz) is available
OK    data: /Users/you/Library/Application Support/boxt
```

### Install prerequisites

Install Lima 2.0 or newer and Git 2.42 or newer. boxt does not bundle Lima or
QEMU and does not install host dependencies automatically.

**macOS Apple Silicon**

```bash
brew install lima git
limactl --version
```

The default Lima `vz` driver uses Apple Virtualization, so QEMU is not required.

**Ubuntu/Debian**

Install QEMU for your CPU architecture and `qemu-img`, then install Lima from the
[Lima installation guide](https://lima-vm.io/docs/installation/):

```bash
# x86_64
sudo apt-get update
sudo apt-get install qemu-system-x86 qemu-utils git
# aarch64
sudo apt-get update
sudo apt-get install qemu-system-arm qemu-utils git
```

Verify `/dev/kvm` exists and is usable by your user. Add yourself to the `kvm`
group, then log out and back in (or start a new login session):

```bash
ls -l /dev/kvm
sudo usermod -aG kvm "$USER"
```

Without KVM Lima uses slow software emulation (TCG), so `boxt doctor` reports a
warning rather than failing.

**Fedora**

```bash
# x86_64
sudo dnf install qemu-system-x86 qemu-img git
# aarch64
sudo dnf install qemu-system-arm qemu-img git
```

Install Lima from the [Lima installation guide](https://lima-vm.io/docs/installation/)
and verify `/dev/kvm` and your user's `kvm` group membership as above.

**Windows 11 + WSL2 (experimental)**

Run the Linux steps inside WSL. Install the **Linux** `limactl` release and the
QEMU packages inside the WSL distribution, not on Windows. Enable nested
virtualization in `%UserProfile%\.wslconfig`:

```ini
[wsl2]
nestedVirtualization=true
memory=12GB
```

Restart WSL, verify `/dev/kvm` exists inside WSL, and keep the project under
`~/projects/...` on the WSL ext4 filesystem, not `/mnt/c/...`. `boxt doctor`
warns when a project is on a Windows-mounted path.

### Install boxt

Release archives are published as
`boxt_<version>_<os>_<arch>.tar.gz` together with `checksums.txt`:

```bash
version=0.1.0
curl -LO "https://github.com/daveagill/boxt/releases/download/v${version}/boxt_${version}_linux_arm64.tar.gz"
curl -LO "https://github.com/daveagill/boxt/releases/download/v${version}/checksums.txt"
sha256sum -c checksums.txt --ignore-missing   # macOS: shasum -a 256 -c
tar xzf "boxt_${version}_linux_arm64.tar.gz"
install -m 0755 boxt ~/.local/bin/boxt
```

From source (Go 1.24+):

```bash
git clone https://github.com/daveagill/boxt
cd boxt
make build
./bin/boxt version
```

`make install PREFIX=~/.local` installs to `$(PREFIX)/bin/boxt`.

### Create the first project

```bash
mkdir myapp
cd myapp
git init

boxt doctor
boxt init --preset opencode
boxt run opencode
```

This creates `.boxt/config.yaml` and never overwrites existing configuration.
`boxt init [path]` works for another directory. Commands work from any directory
inside the project, including from inside a named workspace.

Use presets to scaffold common environments:

```bash
boxt init --preset node
boxt init --preset opencode
boxt init --preset node --preset opencode
boxt init --preset node --preset opencode --default-target opencode
```

Presets are starter configuration only. See [Presets](#presets).

## VM lifecycle

```bash
boxt up        # create/start the VM and reconcile tools; idempotent
boxt status    # project, VM, mounts, tools and workspaces
boxt down      # stop the VM, preserving all guest state
boxt destroy   # delete the VM and guest state (confirms unless --yes)
```

`boxt up` creates the VM on first use, waits until it is running, ensures system
packages and mise, and installs declared tools before reporting the environment
ready. If provisioning fails the VM stays running for troubleshooting.

`boxt down` keeps installed tools, caches and credentials inside the guest.
`boxt destroy` deletes only the VM and its guest state; project files,
configuration and Git worktrees are never deleted. It requires an interactive
terminal and the literal confirmation `yes`, or `--yes` for scripts:

```bash
boxt destroy --yes
```

A stopped or missing VM is created/started automatically and the toolchain is
reconciled before `run`, `shell`, `workspace run` and `workspace shell`. Disable
this per command with `--no-start`, or for the project with:

```yaml
vm:
  auto_start: false
```

`boxt up` remains the explicit lifecycle/setup command and always creates,
starts and provisions. boxt never implicitly initializes an uninitialized
project directory.

The VM name is derived from a canonical project path hash, so symlinks and
different `cd` paths map to the same VM. Changing `vm.cpus`, `vm.memory`,
`vm.ports`, `vm.port_forwarding` or the boxt data directory on an existing VM
requires `boxt destroy` followed by `boxt up`; Lima stores VM resources,
forwarding and mounts at creation time. boxt refuses to run against a VM whose
recorded configuration differs and tells you to recreate it.

## Host and guest filesystem layout

```text
Host

~/projects/myapp/                     project root (default workspace)
├── .git/
├── .boxt/config.yaml
├── src/
└── package.json

~/.local/share/boxt/workspaces/<project-id>/
├── feature-a/                        named workspace worktrees
└── feature-b/
```

```text
Guest

/workspace/                           project root, writable
/workspaces/<project-id>/             project-scoped named workspaces
├── feature-a/
└── feature-b/
```

`<data-dir>` is `~/Library/Application Support/boxt` on macOS and
`${XDG_DATA_HOME:-~/.local/share}/boxt` on Linux. Named worktrees live outside the
main checkout so the parent repository never reports them as untracked
directories. Only the project and its project-scoped workspace root are mounted.
The host home directory and Docker socket are never mounted.

## Configuration

`.boxt/config.yaml` is the source of truth for boxt-managed configuration.

```yaml
version: 1

vm:
  image: ubuntu-24.04
  cpus: 4
  memory: 8GiB
  port_forwarding: auto
  auto_start: true

tools:
  node: "lts"
  python: "latest"

run:
  default: dev
  targets:
    dev:
      command: ["npm", "run", "dev"]
      port_base: 3000
      port_env: PORT       # default when port_base is set
    test:
      command: ["npm", "test"]
    codex:
      command: ["codex"]
      interactive: true
```

Configuration is validated with field paths. Unsupported versions and unknown
fields are rejected. Tool declarations are edited atomically while preserving
comments and unrelated configuration.

## Presets

Presets are static starter fragments. They write ordinary tools and run targets;
after `boxt init`, `.boxt/config.yaml` is the only source of truth and boxt does
not remember which preset was used.

| Preset | Contents |
|---|---|
| `minimal` | VM defaults only |
| `node` | Node LTS; `dev` and `test` targets; default `dev` |
| `python` | Latest Python; interactive `python` target |
| `go` | Latest Go; `build` and `test` targets |
| `rust` | Rust stable; `build` and `test` targets |
| `opencode` | OpenCode; interactive target; default `opencode` |
| `codex` | Codex CLI; interactive target; default `codex` |
| `claude` | Claude Code; interactive target; default `claude` |

Node uses mise's `lts` alias. Python and Go do not have an LTS channel, so those
presets use `latest`; Rust uses `stable`.

Presets compose:

```bash
boxt init --preset node --preset opencode
```

Rules:

- Repeated `--preset` values are merged in an order-independent way.
- Tools merge by name; conflicting versions are an error.
- Targets merge by name; conflicting definitions are an error.
- Each preset may suggest a default target. Zero or one distinct suggestion sets
  `run.default`; multiple distinct suggestions leave it unset.
- When suggestions conflict, `init` prints the choices and leaves no default.
  Use `--default-target <name>` to choose one.
- The final configuration is validated before anything is written.
- Existing configuration is never overwritten.

Examples:

```bash
boxt init --preset opencode
# run.default: opencode

boxt init --preset node --preset opencode
# no default; dev and opencode are both available

boxt init --preset node --preset opencode --default-target opencode
# run.default: opencode
```

Use `boxt init --help` to list presets. Shell completion for `--preset` is
available. Presets use floating runtime versions (`node: lts`, `python: latest`,
`go: latest`, `rust: stable`) so `boxt provision` refreshes them; pin exact
versions in `.boxt/config.yaml` when reproducibility matters.

## Tools

Tools are installed into the persistent VM with mise (an internal detail).
Installed tools and caches live in the guest home and survive `boxt down` /
`boxt up`.

```bash
boxt tool add node@lts
boxt tool add python@latest
boxt tool add opencode@latest --target --interactive
boxt tool list
boxt tool remove node
boxt provision
```

`boxt tool add` updates the configuration atomically. If the VM is running, the
tool is installed immediately; if it is stopped or not created, installation is
deferred until the next `boxt up` (or the next auto-starting run/shell command).
A failed installation keeps the declaration and tells you to retry with
`boxt provision`.

Adding a tool does not automatically create a run target. Use `--target` to add a
target named after the tool, and `--interactive` to give it a PTY:

```bash
boxt tool add opencode@latest --target --interactive
```

That writes both the tool declaration and:

```yaml
run:
  targets:
    opencode:
      command: ["opencode"]
      interactive: true
```

`--interactive` requires `--target`. Existing target definitions are never
silently overwritten.

`boxt tool remove` removes only the declaration. Installed software stays in the
VM because other workspaces or run targets may still use it. boxt warns if a run
target still references the removed tool.

`boxt provision` is the idempotent repair command: it starts the VM if needed,
ensures mise and system packages, installs missing declared tools, and corrects
versions. Run targets and shells use mise's execution environment directly, so
tools are on `PATH` without relying on `.bashrc` or `.profile`.

Unsupported mise tool identifiers fail during provisioning with mise's error. The
pinned mise installer is downloaded from the official `https://mise.run` endpoint
into a private temporary file and installed as the guest user; an existing newer
mise installation is accepted.

## Running commands

```bash
boxt                    # run the default target (same as `boxt run`)
boxt run                # run the default target
boxt run test           # run a named target
boxt run test -- --watch  # append arguments to a named target
boxt run -- npm test    # run an arbitrary command
boxt shell              # interactive shell in the default workspace
boxt run opencode       # run a configured interactive agent
boxt run claude -- -p "review this change"
boxt workspace run agent-1 opencode
boxt workspace run agent-1 test -- --watch
```

Arguments after `--` are appended to the target command as separate argv entries;
boxt never assembles a shell string. `boxt run -- <command>` remains the arbitrary
command form, and more than one positional argument before `--` is an error.
Targets are explicit; boxt never treats an installed executable as a run target.
An unknown target produces guidance:

```text
Error: unknown run target: codex

Define it under run.targets in .boxt/config.yaml,
or use `boxt run -- codex` to execute the command directly.
```

`interactive: true` requests a PTY when boxt is attached to a terminal, which is
required for coding agents and REPLs. Arbitrary commands after `--` use a PTY when
boxt is attached to a terminal. If stdout is redirected, boxt warns that the guest
will not receive a PTY. Guest exit statuses become boxt's exit status. Commands
are passed as argument arrays, never assembled from user input into a shell
string.

Coding agents are ordinary interactive run targets. Presets such as `opencode`,
`codex` and `claude` only scaffold their tools and target; there is no separate
agent execution engine or implicit PATH lookup.

## Workspaces

Named workspaces are independent Git worktrees. They share a VM, installed tools
and caches, but have their own checkout, branch, HEAD and index.

```bash
boxt workspace create feature-a
boxt workspace create feature-b --branch experiment/b
boxt workspace create agent-1 --run opencode
boxt workspace create agent-1 --seed
boxt workspace list
boxt workspace seed agent-1
boxt workspace path feature-a
boxt workspace run feature-a test
boxt workspace run feature-a -- npm test
boxt workspace shell feature-a
boxt workspace merge feature-a
boxt workspace merge feature-a --remove
boxt workspace remove feature-a
```

`boxt workspace list` shows each workspace's branch, short HEAD, dirty state and
ahead/behind counts relative to the default workspace's current branch. Missing,
broken and locked worktrees are reported without failing the whole list.

`--seed` (or `boxt workspace seed <name>`) copies ignored files from the default
workspace into a named workspace after the worktree is created and before
`--run`. Only ignored files are copied (`git ls-files --others --ignored
--exclude-standard`), never tracked source. Existing target paths are left alone
and reported, and symlinks or special files are skipped. This is useful for
local `.env` files, dependency caches and build output.

The default branch name is `boxt/<workspace>`. Pass `--branch` to create or check
out a different branch. `--run <target>` resolves the target before the worktree
is created, then creates the workspace and runs the target in it through the
shared executor. A non-zero target exit status is propagated and the workspace is
left in place for inspection; `--no-start` prevents VM auto-start. The default
workspace is the project root, always exists, and cannot be created or removed. It
can be addressed as `default` in `workspace path|run|shell`.

`boxt workspace merge <name>` fast-forwards the branch currently checked out in
the project root to the workspace branch. It refuses diverged histories rather
than creating a merge commit, and it refuses a dirty workspace or project root.
`--into <branch>` must name the branch already checked out in the project root.
`--remove` removes the worktree only after a successful merge; the Git branch is
kept. boxt never rebases, squashes or forces a merge.

`boxt workspace remove` refuses to discard uncommitted changes or ignored local
files such as `.env` and caches; `--force` deliberately discards them. Git
branches are always retained. Locked worktrees must be unlocked with Git first.
Stale metadata is pruned safely and boxt never uses a manual `rm -rf` to delete a
worktree. Only worktrees created by boxt are listed and managed; manual Git
worktrees are left alone.

Named workspaces require the project root to be the Git repository root. An empty
repository with no commits is supported using an orphan branch (Git 2.42+).

## Lifecycle hooks

Projects can run pre/post hooks for workspace create, merge and remove:

```yaml
hooks:
  workspace_create:
    pre:
      - command: ["npm", "install"]
    post:
      - command: ["git", "status", "--short"]
        where: host
  workspace_merge:
    pre: []
    post: []
  workspace_remove:
    pre: []
    post: []
```

`where` is `guest` (default) or `host`. Guest hooks run through the same executor
as run targets, with the shared toolchain environment and the workspace guest path
as the working directory; they may auto-up/provision the VM like any execution.
Host hooks run as your host user via the normal command runner. Commands are argv
arrays and are never passed through a shell. Hooks receive `BOXT_PROJECT_ROOT`,
`BOXT_WORKSPACE`, `BOXT_WORKSPACE_PATH` and `BOXT_GUEST_PATH`. Each hook has a
generous fixed timeout.

A pre-hook failure aborts the operation. A post-hook failure is reported without
undoing a completed create, merge or remove. Remove post-hooks run in the project
root because the workspace path is gone. `workspace create --seed` seeds ignored
files before hooks and `--run`.

Hooks are arbitrary project-controlled code, so boxt does not run them until you
approve the current configuration:

```bash
boxt hooks status   # show commands and trust state
boxt hooks trust    # approve the current hook hash
boxt hooks clear    # remove approval
```

The approval is a SHA-256 of the normalized hook configuration, stored in the
boxt data directory per project. Changing any hook command or location
invalidates it. Untrusted operations fail before creating, merging or removing
anything and list the commands that need review. Hooks never run during
`boxt init` or `boxt doctor`. Guest hooks run as the `boxt` user; host hooks run
with your host user's permissions.

## Port forwarding

`vm.port_forwarding` controls exposure:

- `auto` (default) uses Lima dynamic forwarding: a service started inside the
  guest on a loopback port is reachable on the host loopback at the same port.
  This makes Vite and other dev servers work without per-port configuration.
- `explicit` forwards only the ports listed under `vm.ports` and ignores all
  others.

Explicit mappings work in both modes and can change the host port or bind address:

```yaml
vm:
  port_forwarding: explicit
  ports:
    - guest: 3000
      host: 8080
      host_ip: 127.0.0.1   # default
```

boxt checks explicitly requested host ports before starting the VM and reports a
conflict instead of silently choosing another port. Dynamic forwarding collisions
are handled by Lima. Binding `host_ip: 0.0.0.0` exposes a service on your network;
the default loopback binding does not.

The guest has normal outbound internet access. Inbound access from the host is
limited to explicit mappings and (in `auto` mode) dynamic loopback forwards;
boxt does not expose guest services on the LAN by default. Port-forwarding
configuration is applied when the VM is created, so changing `vm.ports` or
`vm.port_forwarding` requires `boxt destroy` followed by `boxt up`.

On WSL2, forwards bind inside the WSL VM; reaching them from Windows also depends
on WSL `localhostForwarding`, firewall and VPN configuration.

### Per-workspace ports

Targets that start a server can declare `port_base`. boxt adds a deterministic
per-workspace offset and injects the resulting guest port through an environment
variable, so parallel workspaces do not collide:

```yaml
run:
  targets:
    dev:
      command: ["npm", "run", "dev"]
      port_base: 3000
      port_env: PORT   # optional; defaults to PORT
```

The default workspace uses offset 0, so `dev` receives `PORT=3000`. Named
workspaces use a stable 1–999 offset derived from the project ID and workspace
name. A collision between current workspace offsets is an error; rename a
workspace or choose a different `port_base`. boxt only sets the environment
variable; the service must honor it (Vite reads `PORT`, Node servers usually read
`process.env.PORT`, Python apps can read `os.environ["PORT"]`). boxt does not
rewrite arbitrary command arguments.

In `auto` forwarding mode the guest port is reachable on the same host port. In
`explicit` mode, add a matching `vm.ports` mapping for every possible
`port_base + offset` you need; otherwise the service remains guest-only. Port
offsets are an execution-time convention, not extra VM configuration, so changing
`port_base` does not require recreating the VM.

## Security limitations

- Lima networking is convenient, not a hardened security boundary equivalent to a
  remote machine. Guest processes can reach host services that are reachable from
  the Lima network.
- The project and workspace directories are writable host mounts. Code running in
  the guest can modify project files, which is intentional.
- In `auto` forwarding mode, guest loopback services are exposed on host loopback.
  Use `explicit` when you want a deny-by-default policy.
- Host SSH keys, credentials, the home directory and the Docker socket are never
  copied or mounted automatically. Credentials you enter inside the guest persist
  in its home directory.
- Ordinary development commands run as the non-root `boxt` user. Passwordless
  sudo is available only for provisioning and system administration.
- Lifecycle hooks are project-controlled code. They require explicit approval via
  `boxt hooks trust`, and changing the hook configuration invalidates approval.
  Guest hooks run as `boxt`; host hooks run as your host user.

## Troubleshooting

Start with `boxt doctor`. It reports missing tools, unsupported hosts, KVM/QEMU
problems, data-directory permissions and project configuration errors with
platform-specific commands. It does not install anything.

Use `-v` for debug logs, including external commands (arguments only, no
environment variables):

```bash
boxt -v up
boxt -v run test
```

Command arguments can appear in debug logs and error messages. Avoid passing
secrets as command-line arguments; use files or guest environment variables set
inside the VM instead.

**Lima not found**

Install Lima from <https://lima-vm.io/docs/installation/>. On macOS:
`brew install lima`. On Linux, install Lima and the QEMU packages for your
architecture; verify `/dev/kvm` exists for acceleration.

**WSL2: no `/dev/kvm` / VM is extremely slow**

Enable nested virtualization and restart WSL:

```ini
# %UserProfile%\.wslconfig
[wsl2]
nestedVirtualization=true
memory=12GB
```

Check `/dev/kvm` inside WSL. Without it boxt uses QEMU software emulation (TCG),
which works but is slow. Also ensure the guest memory fits within the WSL memory
limit.

**WSL2: project permissions or Git worktrees behave strangely**

The project is probably on `/mnt/c` (DrvFs). Move it to the WSL ext4 filesystem,
for example `~/projects/myapp`, and set `XDG_DATA_HOME` there as well.

**WSL2: ports work inside WSL but not from Windows**

First verify from inside WSL:

```bash
curl 127.0.0.1:3000
```

If that works, check WSL `localhostForwarding`, Windows Firewall/VPN rules, and
that the forward is bound to `127.0.0.1` inside WSL. LAN exposure across the
WSL/Windows boundary is more restrictive than on a native Linux host.

**Project not initialized**

Commands must run inside a directory tree containing `.boxt/config.yaml`. Run
`boxt init` at the project root. boxt deliberately does not initialize projects
implicitly.

**No default run target**

Set `run.default` in `.boxt/config.yaml`, or invoke a configured target with
`boxt run <target>`. For a workspace, use
`boxt workspace run <workspace> <target>`.

**Interactive command has no TTY**

boxt warns when an interactive target runs with stdout redirected. Run the command
directly in a terminal, or remove the pipe/redirection, to give the guest a PTY.

**VM exists but is broken**

`boxt status` reports broken/uninitialized VMs. If `boxt up` cannot recover it:

```bash
boxt destroy --yes
boxt up
```

Project files and worktrees are preserved.

**Port conflict**

Stop the process using the port, or change `vm.ports`. boxt never silently picks
another host port for explicit mappings.

**Provisioning failed**

The declaration is kept. Fix the reported guest/network/tool error and run
`boxt provision`. The VM stays running for inspection.

**Named workspace operations fail**

Named workspaces require a Git repository whose root is the project root. Run
`git status` from the project root and check that `.boxt/config.yaml` is in the
repository root. For stale worktree metadata, run
`boxt workspace remove <name>`; boxt prunes it without deleting unrelated files.

**Uncommitted changes prevent removal**

Commit or stash the changes, or use `boxt workspace remove <name> --force` to
discard them deliberately. The branch is kept either way.

## Complete example

See [`examples/node-app`](examples/node-app) for a complete project with a Vite-
style dev server, a test command and an interactive agent target.

```bash
cd examples/node-app
boxt up
boxt run dev       # serves on guest :3000, reachable on host :3000 in auto mode
boxt run test
boxt workspace create feature-a
boxt workspace run feature-a test
```

## Development

Engineering invariants and the agent workflow are documented in
[`AGENTS.md`](AGENTS.md).

```bash
make check          # formatting, vet and unit tests
make test           # unit tests
make integration    # requires Lima + virtualization; opt-in
make build          # build ./bin/boxt
make snapshot       # local release archives (goreleaser)
```

Unit tests use fakes for external processes. Git worktree tests use temporary real
repositories. VM integration tests are gated by `BOXT_INTEGRATION=1` and skip
cleanly when Lima is unavailable; set `BOXT_LIMACTL` to a specific `limactl`
binary if it is not on `PATH`, and `BOXT_BIN` to a built boxt binary.

```bash
make integration
# small/low-memory host:
BOXT_INTEGRATION_MEMORY=512MiB make integration
```

The suite was executed on Linux/aarch64 with Lima, QEMU and TCG (no KVM) using
512 MiB guests. It covers VM lifecycle and persistence, project/workspace mounts,
the guest user, PTY shell and exit status, tool installation and persistence,
workspace host/guest visibility, and dynamic port forwarding. macOS Apple Silicon,
Fedora/x86_64 and WSL2 VM runs must be performed on those hosts; WSL2 support is
experimental.

Generated Lima configuration is validated against `limactl validate` when
`BOXT_LIMACTL` is set:

```bash
BOXT_LIMACTL=$(command -v limactl) go test ./internal/lima -run LimactlValidate
```

The test suite cross-compiles for macOS arm64 and Linux amd64/arm64 and covers
platform path logic, but host-specific virtualization behavior cannot be
exercised in a Linux container without KVM.

## Out of scope

boxt deliberately does not implement Docker/Kubernetes orchestration, remote
VMs, non-Git workspaces, active workspace selection, a TUI, service management
for databases, automatic host credential sync, editor integration, or existing
mise configuration compatibility.
