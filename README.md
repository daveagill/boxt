# boxt

**boxt** is a local-first development environment manager. It gives each project
its own persistent Ubuntu Linux VM, one shared toolchain, and multiple independent
Git workspaces.

> Status: under active implementation. See `IMPLEMENTATION_PLAN.md` for milestone
> status and `plan.md` for the full specification.

## Why

Keep your project files on the host where your editor works, while builds, tests,
servers and coding agents run in a consistent Linux environment that survives
restarts. One VM per project, shared by every workspace, with tools installed once.

## Supported hosts

| Host | Architecture |
|---|---|
| macOS | Apple Silicon (arm64) |
| Ubuntu | x86_64, aarch64 |
| Fedora | x86_64, aarch64 |

The guest is always Ubuntu 24.04 LTS. Lima provides virtualization; mise is used
internally for tools; named workspaces use Git worktrees.

## Prerequisites

- Go 1.24+ (building from source)
- [Lima](https://lima-vm.io/docs/installation/) and working virtualization
  (Apple Virtualization on macOS, QEMU/KVM on Linux)
- Git

boxt checks for these and prints platform-specific installation guidance. It does
not install host dependencies without consent.

## Build

```bash
make build
./bin/boxt version
```

## Initialize a project

```bash
mkdir myapp
cd myapp
git init

boxt init
```

This creates `.boxt/config.yaml` and never overwrites existing configuration.
Commands work from any directory inside an initialized project.

## Configuration

```yaml
version: 1

vm:
  image: ubuntu-24.04
  cpus: 4
  memory: 8GiB

tools:
  node: "24"

run:
  default: dev
  targets:
    dev:
      command: ["npm", "run", "dev"]
```

`.boxt/config.yaml` is the source of truth. Later milestones add VM lifecycle,
execution, tools and workspaces.

## Tools

Tools are declared in `.boxt/config.yaml` and installed into the persistent VM
with mise (an internal detail). Installed tools and caches live in the guest home
and survive `boxt down` / `boxt up`.

```bash
boxt tool add node@24
boxt tool add python@3.13
boxt tool list
boxt tool remove node
boxt provision
```

`boxt tool add` updates the configuration atomically. If the VM is running, the
tool is installed immediately; if it is stopped or not created, installation is
deferred until `boxt up`. A failed installation keeps the declaration and tells
you to retry with `boxt provision`.

`boxt tool remove` removes only the declaration. Installed software stays in the
VM, because other workspaces or run targets may still use it.

`boxt provision` is the idempotent repair command: it starts the VM if needed,
ensures mise and required system packages, installs missing declared tools,
corrects versions, and verifies the execution environment. `boxt up` performs
the same reconciliation before reporting the environment ready.

Run targets and shells use mise's execution environment directly, so tools are
on `PATH` without relying on `.bashrc` or `.profile`.

## Running commands

```bash
boxt                    # run the default target (same as `boxt run`)
boxt run                # run the default target
boxt run test           # run a named target
boxt run -- npm test    # run an arbitrary command
boxt shell              # interactive shell in the default workspace
```

Targets are explicit; boxt never treats an installed executable as a run target.
An unknown target produces guidance:

```text
Error: unknown run target: codex

Define it under run.targets in .boxt/config.yaml,
or use `boxt run -- codex` to execute the command directly.
```

`interactive: true` on a target makes boxt request a PTY (needed for agents and
REPLs). Arbitrary commands after `--` use a PTY when boxt is attached to a
terminal. Guest exit statuses become boxt's exit status. A stopped VM is started
automatically; pass `--no-start` to disable that.

```yaml
run:
  default: dev
  targets:
    dev:
      command: ["npm", "run", "dev"]
    codex:
      command: ["codex"]
      interactive: true
```

## VM lifecycle

```bash
boxt up        # create/start the VM; idempotent
boxt status    # project, VM, mounts and declared tools
boxt down      # stop, preserving guest state
boxt destroy   # delete the VM and guest state (confirms unless --yes)
```

The VM name is derived from a SHA-256 project ID, so moving the project or using a
symlink does not create a second VM unexpectedly. `boxt destroy` never deletes
project files or Git workspaces.

The host layout is:

| Host | Guest |
|---|---|
| project root | `/workspace` |
| `<data-dir>/workspaces/<project-id>` | `/workspaces/<project-id>` |

`<data-dir>` is `~/Library/Application Support/boxt` on macOS and
`${XDG_DATA_HOME:-~/.local/share}/boxt` on Linux. The host home directory and Docker
socket are never mounted.

### Port forwarding

Guest services are not exposed by default. Request a forward explicitly:

```yaml
vm:
  ports:
    - guest: 3000
      host: 3000
      host_ip: 127.0.0.1   # default
```

boxt checks the host port before starting the VM and reports a conflict instead of
silently choosing another port. Lima networking is convenient, not a hardened
security boundary; see `plan.md` for the full model.

## Development

```bash
make check          # formatting, vet and unit tests
make test           # unit tests
make integration    # requires Lima + virtualization; opt-in
```

Tests for external commands use fakes. VM integration tests are gated by
`BOXT_INTEGRATION=1` and skip cleanly when Lima is unavailable.
