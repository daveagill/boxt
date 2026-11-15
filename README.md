# boxt

**boxt** is a local-first development environment manager. Each project gets one
persistent Ubuntu Linux VM, a shared toolchain, and multiple independent Git
workspaces.

Project files stay on the host where your editor works. Builds, tests, servers and
coding agents run in a consistent Linux environment that survives restarts.

```bash
cd examples/node-app
boxt up
boxt run dev
boxt workspace create feature-a
boxt workspace run feature-a test
```

Define your own targets and tools in `.boxt/config.yaml` — see
[Configuration](#configuration) and the complete [example](#complete-example).

## Supported hosts

| Host | Architecture |
|---|---|
| macOS | Apple Silicon (arm64) |
| Ubuntu | x86_64, aarch64 |
| Fedora | x86_64, aarch64 |

The guest is always Ubuntu 24.04 LTS. [Lima](https://lima-vm.io/) provides
virtualization, Git worktrees provide workspaces, and mise is used internally for
tools.

macOS Intel, Windows and WSL are not supported. Linux aarch64 support depends on
the installed QEMU and virtualization stack. This repository's automated tests
cross-compile for all targets and exercise host-path logic, but real VM runs must
be performed on the supported host platforms.

## Prerequisites

- [Lima](https://lima-vm.io/docs/installation/) 2.0 or newer with working
  virtualization (Apple Virtualization on macOS, QEMU/KVM on Linux)
- Git 2.42+ (orphan worktrees are used for repositories without commits)
- Go 1.24+ only when building from source

boxt checks for Lima and prints platform-specific installation guidance. It does
not install host dependencies without consent. Linux hosts need the QEMU packages
for their architecture and, for practical performance, `/dev/kvm` access:

- Ubuntu/Debian: the `qemu-system-*` package for your architecture plus
  `qemu-utils` (for `qemu-img`)
- Fedora: the `qemu-system-*` package for your architecture plus `qemu-img`

## Installation

Release archives are published as
`boxt_<version>_<os>_<arch>.tar.gz` together with `checksums.txt`:

```bash
version=0.1.0
curl -LO "https://github.com/boxt/boxt/releases/download/v${version}/boxt_${version}_linux_arm64.tar.gz"
curl -LO "https://github.com/boxt/boxt/releases/download/v${version}/checksums.txt"
sha256sum -c checksums.txt --ignore-missing   # macOS: shasum -a 256 -c
tar xzf "boxt_${version}_linux_arm64.tar.gz"
install -m 0755 boxt ~/.local/bin/boxt
```

From source:

```bash
git clone https://github.com/boxt/boxt
cd boxt
make build
./bin/boxt version
```

`make install PREFIX=~/.local` installs to `$(PREFIX)/bin/boxt`.

## Initialize a project

```bash
mkdir myapp
cd myapp
git init

boxt init
```

This creates `.boxt/config.yaml` and never overwrites existing configuration.
`boxt init [path]` works for another directory. Commands work from any directory
inside the project, including from inside a named workspace.

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

A stopped VM is started automatically and the toolchain is reconciled before
`run`, `shell`, `workspace run` and `workspace shell`. Pass `--no-start` to
disable automatic startup. A VM that was never created is an error; boxt does not
implicitly initialize projects.

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

tools:
  node: "24"
  python: "3.13"

run:
  default: dev
  targets:
    dev:
      command: ["npm", "run", "dev"]
    test:
      command: ["npm", "test"]
    codex:
      command: ["codex"]
      interactive: true
```

Configuration is validated with field paths. Unsupported versions and unknown
fields are rejected. Tool declarations are edited atomically while preserving
comments and unrelated configuration.

## Tools

Tools are installed into the persistent VM with mise (an internal detail).
Installed tools and caches live in the guest home and survive `boxt down` /
`boxt up`.

```bash
boxt tool add node@24
boxt tool add python@3.13
boxt tool list
boxt tool remove node
boxt provision
```

`boxt tool add` updates the configuration atomically. If the VM is running, the
tool is installed immediately; if it is stopped or not created, installation is
deferred until the next `boxt up` (or the next auto-starting run/shell command).
A failed installation keeps the declaration and tells you to retry with
`boxt provision`.

`boxt tool remove` removes only the declaration. Installed software stays in the
VM because other workspaces or run targets may still use it.

`boxt provision` is the idempotent repair command: it starts the VM if needed,
ensures mise and system packages, installs missing declared tools, and corrects
versions. Run targets and shells use mise's execution environment directly, so
tools are on `PATH` without relying on `.bashrc` or `.profile`.

Some tools, such as npm-backed CLIs, also need Node.js declared. Unsupported mise
tool identifiers fail during provisioning with mise's error. The pinned mise
installer is downloaded from the official `https://mise.run` endpoint into a
private temporary file and installed as the guest user; an existing newer mise
installation is accepted.

## Running commands

```bash
boxt                    # run the default target (same as `boxt run`)
boxt run                # run the default target
boxt run test           # run a named target
boxt run -- npm test    # run an arbitrary command
boxt shell              # interactive shell in the default workspace
boxt run codex          # run a configured interactive agent
```

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

Coding agents are ordinary interactive run targets. Nothing agent-specific is
built into boxt.

## Workspaces

Named workspaces are independent Git worktrees. They share a VM, installed tools
and caches, but have their own checkout, branch, HEAD and index.

```bash
boxt workspace create feature-a
boxt workspace create feature-b --branch experiment/b
boxt workspace list
boxt workspace path feature-a
boxt workspace run feature-a test
boxt workspace run feature-a -- npm test
boxt workspace shell feature-a
boxt workspace remove feature-a
```

The default branch name is `boxt/<workspace>`. Pass `--branch` to create or check
out a different branch. The default workspace is the project root, always exists,
and cannot be created or removed. It can be addressed as `default` in
`workspace path|run|shell`.

`boxt workspace remove` refuses to discard uncommitted changes or ignored local
files such as `.env` and caches; `--force` deliberately discards them. Git
branches are always retained. Locked worktrees must be unlocked with Git first.
Stale metadata is pruned safely and boxt never uses a manual `rm -rf` to delete a
worktree. Only worktrees created by boxt are listed and managed; manual Git
worktrees are left alone.

Named workspaces require the project root to be the Git repository root. An empty
repository with no commits is supported using an orphan branch (Git 2.42+).

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

## Troubleshooting

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
workspace host/guest visibility, and dynamic port forwarding. macOS Apple Silicon
and Fedora/x86_64 VM runs must be performed on those hosts.

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
mise configuration compatibility. See `plan.md` for the full specification.
