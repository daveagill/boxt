# boxt

[![CI](https://github.com/daveagill/boxt/actions/workflows/ci.yml/badge.svg)](https://github.com/daveagill/boxt/actions/workflows/ci.yml)
[![Go version](https://img.shields.io/github/go-mod/go-version/daveagill/boxt)](go.mod)

**boxt** runs coding agents in local VM sandboxes, with dev environment
provisioning and Git worktree management built in.

The safe way to run your coding agents:

```bash
boxt run opencode
boxt run claude
boxt run codex

# sandbox anything
boxt run python
```

## Contents

- [Usage guide](#usage-guide)
- [Install](#install)
- [Command overview](#command-overview)
- [Running commands](#running-commands)
- [Workspaces](#workspaces)
  - [Seeding ignored files](#seeding-ignored-files)
  - [Merging and rebasing](#merging-and-rebasing)
- [Presets](#presets)
- [Tools](#tools)
- [VM lifecycle](#vm-lifecycle)
- [Configuration](#configuration)
  - [Changing VM settings](#changing-vm-settings)
  - [Additional mounts](#additional-mounts)
  - [Seed rules](#seed-rules)
- [Lifecycle hooks](#lifecycle-hooks)
- [Networking and port forwarding](#networking-and-port-forwarding)
  - [Per-workspace ports](#per-workspace-ports)
- [Security](#security)
  - [Credentials and agent logins](#credentials-and-agent-logins)
- [Troubleshooting](docs/troubleshooting.md)
- [Filesystem layout](#filesystem-layout)
- [Development](#development)

## Usage guide

1. [Install boxt](#install)
2. Navigate to an existing Git repository or create a new one:

   ```bash
   mkdir myapp && cd myapp && git init
   ```

3. Initialize boxt:

   ```bash
   boxt init --preset opencode
   ```

   `boxt init` writes `.boxt/config.yaml`, which you should commit to Git.

   Combine [Presets](#presets) to scaffold common environments, such as Node.js
   and Claude Code:

   ```bash
   boxt init --preset node --preset claude --default-target claude
   ```

4. Run your favourite coding agent in a sandbox VM:

   ```bash
   boxt run opencode
   ```

   `boxt run` creates, starts and provisions the VM, then launches the agent
   process. The project directory becomes the default workspace.

   Once the VM exists, later `boxt run` commands reconnect to it. See
   [VM lifecycle](#vm-lifecycle) for more on managing the VM.

5. Create additional [Workspaces](#workspaces) to run agents in parallel on the
   same VM:

   ```bash
   boxt workspace create feature-a --run opencode
   ```

   Each workspace is a Git worktree, so agents get their own copy of the code.
   Once a feature is complete, merge the workspace back into the default
   workspace:

   ```bash
   boxt workspace merge feature-a
   ```

6. Install [Tools](#tools):

   ```bash
   boxt tool add node@lts
   ```

   Tools are recorded in `.boxt/config.yaml` and provisioned into the VM, so dev
   environments are reproducible and shareable.

See [`examples/node-app`](examples/node-app) for a complete runnable project with a
dev server, tests and an agent target.

## Install

boxt runs on macOS, Linux and Windows 11 via WSL2.

**Install with Homebrew** (recommended)

Homebrew installs boxt along with its dependencies (Lima and QEMU):

```bash
brew install daveagill/tap/boxt
```

**Install manually**

a) Download a boxt release archive:
`boxt_<version>_<os>_<arch>.tar.gz` together with `checksums.txt`:

```bash
version=0.1.0     # replace with the latest release tag
os=linux          # darwin on macOS
arch=arm64        # amd64 on x86_64

curl -LO "https://github.com/daveagill/boxt/releases/download/v${version}/boxt_${version}_${os}_${arch}.tar.gz"
curl -LO "https://github.com/daveagill/boxt/releases/download/v${version}/checksums.txt"
sha256sum -c checksums.txt --ignore-missing   # macOS: shasum -a 256 -c
tar xzf "boxt_${version}_${os}_${arch}.tar.gz"
install -m 0755 boxt ~/.local/bin/boxt
```

b) Follow [platform setup](docs/platform-setup.md) to install dependencies (Lima and QEMU/KVM packages).

c) Run `boxt doctor` to confirm that all dependency prequisites are satisfied.

**Build and install from source** (Requires Go 1.24+):

```bash
git clone https://github.com/daveagill/boxt
cd boxt
make build
./bin/boxt version
```

`make install PREFIX=~/.local` installs to `$(PREFIX)/bin/boxt`.

## Command overview

| Command | Purpose |
|---|---|
| `boxt init [path]` | write `.boxt/config.yaml`, optionally from presets. See [Usage guide](#usage-guide). |
| `boxt up` | create/start the VM and reconcile the toolchain. See [VM lifecycle](#vm-lifecycle). |
| `boxt status` | show the project, VM, mounts, tools and workspaces. See [VM lifecycle](#vm-lifecycle). |
| `boxt down` | stop the VM, preserving all guest state. See [VM lifecycle](#vm-lifecycle). |
| `boxt destroy` | delete the VM and its guest state. See [VM lifecycle](#vm-lifecycle). |
| `boxt recreate` | destroy and recreate the VM from the current configuration. See [VM lifecycle](#vm-lifecycle). |
| `boxt` | run the default target (same as `boxt run`). See [Running commands](#running-commands). |
| `boxt run [target]` | run the default or a named target. See [Running commands](#running-commands). |
| `boxt run -- <command>` | run an arbitrary command. See [Running commands](#running-commands). |
| `boxt shell` | open an interactive shell in the current workspace. See [Running commands](#running-commands). |
| `boxt workspace create\|list\|path\|run\|shell\|switch` | create and use Git worktrees. See [Workspaces](#workspaces). |
| `boxt shell-init <shell>` | print the shell integration that powers `workspace switch`. See [Entering a workspace](#entering-a-workspace). |
| `boxt workspace merge\|rebase\|remove` | land, replay or discard a workspace. See [Workspaces](#workspaces). |
| `boxt config show\|get\|set\|unset\|validate` | inspect and edit scalar configuration. See [Editing configuration](#editing-configuration). |
| `boxt target add\|remove\|list\|set-default` | manage named run targets. See [Managing run targets](#managing-run-targets). |
| `boxt port add\|remove\|list` | inspect and edit explicit port forwarding. See [Networking and port forwarding](#networking-and-port-forwarding). |
| `boxt tool add\|remove\|list` | declare tools; `boxt provision` reconciles them. See [Tools](#tools). |
| `boxt mount add\|remove\|list` | declare additional host paths mounted into the VM. See [Additional mounts](#additional-mounts). |
| `boxt hooks status\|trust\|clear` | inspect and approve lifecycle hooks. See [Lifecycle hooks](#lifecycle-hooks). |
| `boxt doctor` | diagnose host prerequisites and project configuration. See [Troubleshooting](docs/troubleshooting.md). |

`boxt --help` and `boxt <command> --help` are authoritative. Shell completion is
available through `boxt completion <shell>`.

## Running commands

```bash
boxt                      # run the default target (same as `boxt run`)
boxt run                  # run the default target
boxt run test             # run a named target
boxt run test -- --watch  # append arguments to a named target
boxt run -- npm test      # run an arbitrary command
boxt shell                # interactive shell in the current workspace
boxt run opencode         # run a configured interactive agent
boxt run claude -- -p "review this change"
boxt workspace run feature-a opencode
boxt workspace run feature-a test -- --watch
```

`boxt`, `boxt run` and `boxt shell` execute in the workspace whose checkout
contains the current directory, falling back to the project root. See
[Entering a workspace](#entering-a-workspace).

Arguments after `--` are appended to the target as separate argv entries, not a
shell string. Targets are explicit: an installed executable is not a run target,
and more than one positional argument before `--` is an error. An unknown target
produces guidance:

```text
Error: unknown run target: codex

Add it with `boxt target add codex -- <command>` or edit run.targets in .boxt/config.yaml,
or use `boxt run -- codex` to execute the command directly.
```

`interactive: true` requests a PTY when boxt is attached to a terminal, which
coding agents and REPLs need; arbitrary commands after `--` get a PTY under the
same condition. If stdout is redirected, boxt warns that the guest will not
receive one. Guest exit statuses become boxt's exit status.

Coding agents are ordinary interactive run targets; presets such as `opencode`,
`codex` and `claude` only scaffold the tool and target.

### Managing run targets

`boxt target` edits `run.targets` and `run.default` without touching the VM:

```bash
boxt target add dev -- npm run dev
boxt target add agent --interactive -- opencode
boxt target add web --port-base 3000 --port-env PORT -- npm run dev
boxt target add dev --set-default -- npm run dev
boxt target list
boxt target set-default dev
boxt target unset-default
boxt target remove test
```

Adding an identical target is a no-op; adding a different definition for an
existing name is refused, so remove it first. Removing the default target also
clears `run.default`. `target list` shows the default marker, the interactive
flag, the configured port base and the command.

## Workspaces

Named workspaces are independent Git worktrees. They share a VM, installed tools
and caches, but have their own checkout, branch, HEAD and index. Name one after
the change it contains (`feature-a`), and use `--run` to start an agent in it:

```bash
boxt workspace create feature-a
boxt workspace create feature-b --branch experiment/b
boxt workspace create feature-a --run opencode
boxt workspace create feature-a --seed
boxt workspace list
boxt workspace path feature-a
boxt workspace switch feature-a
boxt workspace run feature-a test
boxt workspace shell feature-a
boxt workspace merge feature-a
boxt workspace rebase feature-a
boxt workspace remove feature-a
```

`boxt workspace list` shows each workspace's branch, short HEAD, dirty state and
ahead/behind counts, including missing, broken or locked worktrees and in-progress
Git operations such as `rebasing`.

Notes:

- The default branch is `boxt/<workspace>`; `--branch` overrides it.
- `--run <target>` runs a target in the new workspace; bare `--run` uses
  `run.default` and `--run=` skips it. Exit status is propagated and the workspace
  is kept; `--no-start` prevents VM auto-start.
- The default workspace is the project root and can be addressed as `default`.
- Named workspaces require the project root to be the Git repository root; an
  empty repository is supported using an orphan branch (Git 2.42+).

### Entering a workspace

`boxt`, `boxt run` and `boxt shell` execute in the workspace whose checkout
contains the current directory, falling back to the project root. Named
workspace checkouts live outside the project checkout, so `cd`-ing into one
keeps your shell and the VM in sync.

`boxt workspace switch <name>` changes the current shell's directory, which no
command can do for its parent shell. It needs the small integration printed by
`boxt shell-init`, added once to your startup file:

```zsh
# ~/.zshrc
eval "$(boxt shell-init zsh)"
```
```bash
# ~/.bashrc
eval "$(boxt shell-init bash)"
```
```fish
# ~/.config/fish/config.fish
boxt shell-init fish | source
```

`sh`, `bash`, `zsh` and `fish` are supported. boxt only prints the integration;
it never edits your startup files. Workspace-name completion comes from
`boxt completion <shell>`, so `boxt workspace switch <TAB>` lists the project's
workspaces once both are loaded.

Without the integration, use the path directly:

```bash
cd "$(boxt workspace path feature-a)"   # named workspace
cd "$(boxt workspace path default)"     # project root
```

`boxt workspace run` and `boxt workspace shell` always take an explicit name and
therefore override the current directory. From inside a worktree that is how you
work in the project root:

```bash
boxt workspace run default dev            # default target in the project root
boxt workspace run default -- npm test    # arbitrary command in the project root
boxt workspace shell default              # shell at the project root
```

`switch` is host-only: it never starts the VM and never runs lifecycle hooks.

### Seeding ignored files

`--seed` (or `boxt workspace seed <name>`) copies ignored files from the default
workspace into a named workspace before hooks and `--run`. Only git-ignored files
are copied; see [Seed rules](#seed-rules) to filter them.

### Merging and rebasing

`boxt workspace merge <name>` merges the branch checked out onto the main project:
* `--into <branch>` selects the target, if not specified merges to the main project.
* `--remove` deletes the worktree after success, keeping the branch.
* `--rebase` (or
`workspace.merge: rebase`) replays the workspace onto the target first.

Merges are performed as git fast-forward merges and require a clean workspace and target, unless `--rebase` (or
`workspace.merge: rebase`) are specified to replay the work first.

`boxt workspace rebase <name>` does that
replay alone without merging to the target.

If rebasing causing conflcits the rebase stays open: Resolve the files and rerun with `--continue`,
or run with `--abort` to abort the rebase and restore the original state.

### Removing a workspace

`boxt workspace remove` refuses uncommitted changes and ignored files such as
`.env`; `--force` discards them. With
`seed.allow_remove: true`, ignored files matching the [seed rules](#seed-rules)
are not a blocker that requires force.

Use `--dry-run` to preview the decision without changing anything:

```bash
boxt workspace remove feature-a --dry-run
boxt workspace remove feature-a --dry-run --force
```

The preview reports tracked changes, non-ignored untracked files, which ignored
files would be deleted, which ignored paths do not match the seed rules, and
whether Git has locked the worktree or left an operation in progress. It never
removes the worktree, prunes stale metadata, runs lifecycle hooks, or requires
hook approval, and it exits 0 even when removal would be refused.

## Presets

Presets are static starter fragments: they write ordinary tools and run targets,
and after `boxt init`, `.boxt/config.yaml` is the only source of truth.

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

Node uses mise's `lts` alias; Python and Go use `latest` (neither has an LTS
channel) and Rust uses `stable`. `boxt init --help` lists presets.

Presets use floating versions, so `boxt provision` refreshes them; pin exact
versions in `.boxt/config.yaml` when reproducibility matters.

## Tools

Tools are installed into the persistent VM with mise (an internal detail) and
survive `boxt down`/`boxt up`.

```bash
boxt tool add node@lts
boxt tool add python@latest
boxt tool add opencode@latest --target --interactive
boxt tool list
boxt tool remove node
boxt provision
```

`boxt tool add` updates the configuration atomically. A running VM installs the
tool immediately; otherwise installation is deferred until the next `boxt up` or
auto-starting `run`/`shell` command.

`--target` also adds a run target named after the tool, and `--interactive` gives
it a PTY (it requires `--target`).

`boxt tool remove` removes only the declaration; installed software stays because
other workspaces or targets may still use it, and boxt warns if a run target still
references it.

`boxt provision` is the idempotent repair command: it starts the VM
if needed, ensures mise and system packages, installs missing declared tools and
corrects versions. Run targets and shells get mise's environment directly, so
tools are on `PATH` without `.bashrc` or `.profile`.

## VM lifecycle

```bash
boxt up        # create/start the VM and reconcile tools; idempotent
boxt status    # project, VM, mounts, tools and workspaces
boxt down      # stop the VM, preserving all guest state
boxt destroy   # delete the VM and guest state (confirms unless --yes)
boxt recreate  # destroy and create a fresh VM from current config
```

`boxt up` creates the VM on first use, starts it, ensures system packages and
mise, installs declared tools, then reports ready. If provisioning fails the VM
stays running for troubleshooting.

`boxt status` shows what boxt resolved for the project (paths elided for
brevity):

```text
Project:           /Users/you/projects/myapp
Project ID:        1a2b3c4d5e6f7a8b
VM:                boxt-1a2b3c4d5e6f7a8b (running, vz/aarch64)
Guest image:       ubuntu-24.04
Resources:         4 CPUs, 8GiB memory
Port forwarding:   auto
Auto-up:           enabled
Project mount:     /workspace (host /Users/you/projects/myapp)
Workspaces mount:  /workspaces/1a2b3c4d5e6f7a8b (host <data-dir>/workspaces/1a2b3c4d5e6f7a8b)
Tools:             node lts, opencode latest
Workspaces:
  NAME       PATH                        BRANCH          HEAD     DIRTY  AHEAD/BEHIND  STATUS
  default    /Users/you/projects/myapp   main            a1b2c3d  clean  -             ready
  feature-a  <workspaces>/feature-a      boxt/feature-a  9f8e7d6  dirty  2/0           ready
```

`boxt down` stops the VM and keeps tools, caches and credentials in the guest.
`boxt destroy` deletes the VM and its guest state; project files, configuration and
worktrees are kept. It needs a terminal and the literal confirmation `yes`, or
`--yes` for scripts.

`boxt recreate` is `destroy` followed by `up`: it applies changed creation-time
settings and provisions a fresh VM. All guest state is lost, so it uses the same
confirmation rules as `destroy` (`--yes` for scripts).

A stopped or missing VM is created or started automatically before `run`, `shell`,
`workspace run` and `workspace shell`. Disable that per command with `--no-start`
or for the project with `vm.auto_start: false`. `boxt up` performs setup explicitly.

The VM name is a hash of the canonical project path, so symlinks and different
`cd` paths share one VM. Changing resources or forwarding requires recreating it;
see [Changing VM settings](#changing-vm-settings).

## Configuration

`.boxt/config.yaml` is the source of truth for boxt-managed configuration.

```yaml
version: 1

vm:
  image: ubuntu-24.04
  # guest CPUs and memory; applied when the VM is created, so change them with
  # `boxt recreate`
  cpus: 4
  memory: 8GiB
  port_forwarding: auto   # auto: forward guest loopback; explicit: only `ports`
  auto_start: true
  mounts:                 # additional host paths, applied at VM creation
    - host: /Users/you/shared/data
      guest: /mnt/data
      writable: true

workspace:
  # ff: merge only when the target branch can fast-forward (default)
  # rebase: replay the workspace branch onto the target, then fast-forward
  merge: ff

seed:
  include:
    - ".env"
    - "node_modules/**"
  exclude:
    - "node_modules/.cache/**"
  allow_remove: false

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

Configuration is validated with field paths: unsupported versions and unknown
fields are rejected. Edits are atomic, locked across processes, and preserve
comments, unrelated YAML and symlinked config files.

### Editing configuration

`boxt config` owns scalar settings; structured sections have typed commands:

```bash
boxt config show                    # print .boxt/config.yaml as stored
boxt config get vm.memory           # effective value after defaults
boxt config set vm.memory 16GiB
boxt config set vm.auto_start false
boxt config set workspace.merge rebase
boxt config set seed.include .env --append
boxt config unset seed.include .env
boxt config validate
```

| Key | Command |
|---|---|
| `tools` | `boxt tool` |
| `run.targets`, `run.default` | `boxt target` |
| `vm.ports` | `boxt port` |
| `vm.mounts` | `boxt mount` |
| `hooks` | hand-edited; `boxt hooks status` shows the result |
| `version`, `vm.image` | read-only in this MVP |

`boxt config set` never updates the VM creation fingerprint. Changes to
`vm.cpus`, `vm.memory`, `vm.port_forwarding`, `vm.ports` or `vm.mounts` print a
reminder to run `boxt recreate`.

### Changing VM settings

`vm.cpus`, `vm.memory`, `vm.ports`, `vm.port_forwarding`, `vm.mounts`, the
project root and the data directory are fixed at VM creation; if they change,
boxt reports the mismatch and asks for `boxt recreate`. Tools, run targets,
`vm.auto_start` and `workspace.merge` need no recreation, and upgrading boxt does
not invalidate an existing VM.

### Additional mounts

`vm.mounts` mounts extra host paths into every workspace of the project. Mounts
are applied when the VM is created:

```yaml
vm:
  mounts:
    - host: /Users/you/shared/data
      guest: /mnt/data
      writable: true
    - host: /Users/you/shared/cache
      guest: /mnt/cache
      writable: false
```

Every mount must set `writable` to `true` or `false`. `boxt mount add` defaults
to read-write and accepts `--read-only` when declaring a mount.
Configured host paths must be absolute (the `boxt mount add` command expands `~`
and relative paths) and are never the home directory, an ancestor of it, the
filesystem root or a Docker socket. Guest paths must be absolute and may not
shadow `/workspace`, `/workspaces`, the guest home or system directories, and
may not overlap another mount. The `boxt mount` commands edit the configuration
atomically:

```bash
boxt mount add ~/shared/data --guest /mnt/data
boxt mount add ~/shared/cache --guest /mnt/cache --read-only
boxt mount list
boxt mount remove /mnt/cache        # or remove by host path
```

Adding or removing a mount on an existing VM requires `boxt recreate`. `boxt
doctor` reports a missing mount source before you try to start the VM.

### Seed rules

The optional `seed` section selects which Git-ignored files
`workspace create --seed` and `workspace seed` copy. Patterns are root-relative
Gitignore-style globs: `**` spans segments, `*`, `?` and character classes match
within one, and a trailing `/` or directory name covers descendants. Excludes win,
and an empty `include` copies every ignored file.

With `allow_remove`, removing without `--force` only proceeds when every ignored
path matches those rules; unmatched paths are listed and the workspace is kept.

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

`where` is `guest` (default) or `host`. Guest hooks use the shared executor and
toolchain environment with the workspace guest path as cwd, and may auto-start or
provision the VM; host hooks run as your host user. Commands are argv arrays and
receive `BOXT_PROJECT_ROOT`, `BOXT_WORKSPACE`, `BOXT_WORKSPACE_PATH` and
`BOXT_GUEST_PATH`.

A pre-hook failure aborts the operation; a post-hook failure is reported without
undoing completed work. Remove post-hooks run in the project root.

Hooks are project-controlled code, so boxt will not run them until you approve
the current configuration:

```bash
boxt hooks status   # show commands and trust state
boxt hooks trust    # approve the current hook hash
boxt hooks clear    # remove approval
```

Any change to a hook command or location invalidates approval; untrusted
operations list the commands that need review. Hooks do not run during `boxt init`
or `boxt doctor`.

## Networking and port forwarding

`vm.port_forwarding` controls exposure:

- `auto` (default) uses Lima dynamic forwarding: a guest loopback service is
  reachable on the host loopback at the same port, so dev servers need no
  per-port configuration.
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

`boxt port` edits those mappings, and `boxt port list` shows both the declared
mappings and the effective guest port for every target that declares
`port_base`, including whether each port is reachable in the current mode:

```bash
boxt port add 3000                 # guest 3000 -> host 3000 on 127.0.0.1
boxt port add 3000:8080
boxt port add 3000:8080@127.0.0.1
boxt port list
boxt port remove 3000
```

`boxt config set vm.port_forwarding auto|explicit` changes the mode. Port changes
are applied at VM creation, so run `boxt recreate` afterwards. `boxt port list`
is read-only and works even when the VM does not exist.

boxt checks explicitly requested host ports before starting the VM and reports a
conflict rather than choosing another port. Binding `host_ip: 0.0.0.0` exposes a
service on your network; the default loopback binding does not.

The guest has normal outbound access. Inbound access is limited to explicit
mappings and, in `auto` mode, dynamic loopback forwards, so services are not
exposed on the LAN by default. Forwarding settings are applied at VM creation;
see [Changing VM settings](#changing-vm-settings). On WSL2, forwards bind inside
the WSL VM, so reaching them from Windows also depends on `localhostForwarding`,
firewall and VPN configuration.

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

The default workspace uses offset 0, so `dev` receives `PORT=3000`; named
workspaces use a stable 1–999 offset derived from the project ID and name. Offset
collisions are an error, so rename a workspace or change `port_base`. The service
must honor the variable (Vite reads `PORT`, for example). Because `boxt run` and
`boxt shell` resolve the workspace from the current directory, running them
inside a named workspace uses that workspace's offset automatically.

In `auto` mode the guest port is reachable on the same host port; in `explicit`
mode add a `vm.ports` mapping for each `port_base + offset` you need. `boxt port
list` shows the effective guest port for every workspace and marks unmapped ports
as `guest-only (explicit)`. Port offsets are an execution-time convention and need
no VM recreation; `vm.ports` itself is applied at creation.

## Security

- Lima networking is convenient, not a hardened security boundary equivalent to a
  remote machine. Guest processes can reach host services that are reachable from
  the Lima network.
- The project and workspace directories are writable host mounts, so guest code
  can modify project files.
- Additional `vm.mounts` paths are opt-in and must set `writable`. Prefer
  `writable: false` for host data that guest code or agents should not modify.
- In `auto` forwarding mode, guest loopback services are exposed on host loopback.
  Use `explicit` when you want a deny-by-default policy.
- Host SSH keys, credentials, the home directory and the Docker socket are never
  copied or mounted automatically. boxt rejects `vm.mounts` entries for the home
  directory, its ancestors, the filesystem root and Docker sockets.
- Ordinary development commands run as the non-root `boxt` user. Passwordless
  sudo is available only for provisioning and system administration.
- Lifecycle hooks require explicit approval via `boxt hooks trust`, and changing
  the hook configuration invalidates approval. Guest hooks run as `boxt`; host
  hooks run as your host user.

### Credentials and agent logins

Agents and CLIs that need credentials authenticate inside the guest, for example
by logging in through `boxt run opencode` or by running `gh auth login` in
`boxt shell`. Tokens and tool configuration are written to the guest home and
survive `boxt down`/`boxt up`; they are removed with `boxt destroy`. Nothing from
your host keychain, `~/.ssh`, `~/.gitconfig` or cloud CLI configuration is copied
or mounted.

To reset a credential, log out inside the guest or destroy the VM. Never pass
secrets as command arguments: arguments can appear in `-v` debug logs and error
messages.

## Troubleshooting

If something fails, `boxt doctor` reports missing prerequisites, unsupported
hosts, KVM problems, data-directory permissions and project configuration errors
with platform-specific fixes. For WSL2, VM, port and workspace problems, see
[docs/troubleshooting.md](docs/troubleshooting.md).

## Filesystem layout

The project root and its project-scoped workspace root are always mounted into
the guest. Additional host paths are mounted only when listed under `vm.mounts`.

```text
Host

~/projects/myapp/                     project root (default workspace)
├── .git/
├── .boxt/config.yaml
├── src/
└── package.json

<data-dir>/
├── vms/<project-id>/
│   ├── lima.yaml                     generated Lima configuration
│   └── config.sha256                 VM creation fingerprint
├── workspaces/<project-id>/
│   ├── feature-a/                    named workspace worktrees
│   └── feature-b/
└── hooks/<project-id>.sha256          lifecycle hook approval
```

```text
Guest

/workspace/                           project root, writable
/workspaces/<project-id>/             project-scoped named workspaces
├── feature-a/
└── feature-b/
/mnt/data/                            optional vm.mounts entry
```

`<data-dir>` is `~/Library/Application Support/boxt` on macOS and
`${XDG_DATA_HOME:-~/.local/share}/boxt` on Linux. boxt generates the Lima
configuration and fingerprint from `.boxt/config.yaml`; Lima keeps VM disks and
images in `~/.lima` by default. Named worktrees live outside the main checkout,
and the project ID is derived from the canonical path, so commands inside the
project or a workspace resolve to the same VM.

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

Releases publish archives, checksums and the `daveagill/homebrew-tap` formula.
Set `HOMEBREW_TAP_GITHUB_TOKEN` to a token with write access to the tap
repository before running `make release`; the formula depends on `lima` and
`git`, so Homebrew installs them automatically.

Unit tests use fakes for external processes. Git worktree tests use temporary real
repositories. VM integration tests are gated by `BOXT_INTEGRATION=1` and skip
when Lima is unavailable; set `BOXT_LIMACTL` to a specific `limactl` binary if it
is not on `PATH`, and `BOXT_BIN` to a built boxt binary.

```bash
make integration
# small/low-memory host:
BOXT_INTEGRATION_MEMORY=512MiB make integration
```

Generated Lima configuration is validated against `limactl validate` when
`BOXT_LIMACTL` is set:

```bash
BOXT_LIMACTL=$(command -v limactl) go test ./internal/lima -run LimactlValidate
```

The suite runs on Linux and macOS in CI and cross-compiles every release target.
VM integration tests run on Linux/aarch64 with Lima, QEMU and TCG (no KVM) and
cover lifecycle, mounts, PTY and exit status, tool provisioning and ports; WSL2
runs must be performed on a Windows 11 host.
