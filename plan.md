# Build boxt — MVP Implementation Specification

## 1. Project overview

Build **boxt**, a cross-platform, local-first development environment manager for macOS (Apple Silicon), Ubuntu Linux and Fedora Linux.

Boxt gives each project its own persistent Linux virtual machine, a consistent development environment, and multiple independent working directories called workspaces.

The primary goal is to let developers and coding agents work inside reproducible, persistent Linux environments without requiring developers to manage virtual machines manually.

Boxt is a practical CLI tool, not a cloud service, container orchestration system, or general-purpose package manager.

### Core principles

1. One persistent VM per project.
2. Linux development environment regardless of host OS.
3. Project files remain accessible and editable from the host.
4. Multiple independent workspaces share the same VM.
5. All workspaces share installed tools and caches.
6. Commands execute inside the VM through one unified execution engine.
7. Use established tools such as Lima, Git and mise instead of reinventing them.
8. Keep the MVP small, understandable and extensible.
9. Boxt owns its tool configuration and uses mise as an internal implementation detail.

## 2. Technology choices and supported platforms

Use:

- **Language:** Go
- **CLI framework:** Cobra
- **VM management:** Lima
- **Guest OS:** Ubuntu 24.04 LTS
- **Tool version manager:** mise
- **Workspace implementation:** Git worktrees
- **Configuration:** YAML
- **Logging:** Structured logging
- **Testing:** Go testing framework, with integration tests where practical

### Supported host platforms

| Host OS | Architectures |
|---|---|
| macOS | Apple Silicon (arm64) |
| Ubuntu Linux | x86_64, aarch64 |
| Fedora Linux | x86_64, aarch64 |

Support aarch64 Linux where the selected Lima backend and virtualization environment permit it.

Use Lima as the VM manager on all supported platforms.

The guest environment must consistently use Ubuntu 24.04 LTS, regardless of host OS.

### Host-specific requirements

- Detect host OS and architecture at runtime.
- Check Lima installation and required virtualization prerequisites.
- Provide platform-specific installation instructions.
- Use appropriate host paths and application data directories.
- Handle differences in virtualization support and networking.
- Do not assume macOS-specific paths or commands.
- Do not assume all Linux hosts have identical package managers.
- Use Homebrew, apt or dnf as appropriate when automated dependency installation is explicitly supported.
- Do not silently install host dependencies without user consent.

Do not introduce Docker, Kubernetes, containerd, Nix, or other virtualization/orchestration systems.

Lima is the VM manager.

Use Lima's supported configuration and command interfaces. Avoid depending on undocumented internal implementation details.

## 3. Product architecture

Boxt manages five primary concepts.

### Project

A project is the root directory containing the original project checkout and `.boxt/config.yaml`.

Each project owns exactly one persistent Lima VM.

The VM retains installed tools, caches, configuration and other guest state across restarts.

### Workspace

A workspace is an independent working directory inside a project.

There are two kinds.

**Default workspace**
- The original project directory.
- Always exists.
- Cannot be removed.
- Uses the original Git checkout if the project is a Git repository.

**Named workspace**
- An additional working directory.
- Initially implemented using Git worktrees.
- Has an independent checkout, branch, HEAD and Git index.
- Shares the Git object database and repository metadata.
- Uses the same project VM and installed tools.

Do not introduce active workspace selection in the MVP.

### Run target

A run target is a named command that users want to execute inside a workspace.

Examples:
- `dev`
- `test`
- `build`
- `python`
- `codex`
- `opencode`
- `claude`

Run targets are not limited to agents.

An agent is simply an executable that can be configured as an interactive run target.

### Tool

A tool is software installed into the guest environment.

Examples:
- Node.js
- Python
- Go
- Rust
- Codex CLI
- OpenCode CLI
- ripgrep

Use mise as the internal tool version manager.

A tool being installed does not automatically make it a run target.

### VM

A VM is the persistent Linux environment belonging to a project.

It provides:
- Shared installed tools.
- Shared caches.
- Persistent guest home directory.
- Project and workspace mounts.
- Command execution.
- Interactive shell access.

### Important distinction

Keep these responsibilities separate:

| Concept | Responsibility |
|---|---|
| Project | Owns VM and configuration |
| Workspace | Determines working directory |
| Run target | Defines a named command |
| Tool | Defines installed software |
| VM | Provides persistent Linux environment |

Do not create a generic component registry or plugin framework in the MVP.

## 4. CLI design

Implement the following CLI.

### Project initialization and VM lifecycle

```bash
boxt init [path]
boxt up
boxt down
boxt status
boxt destroy
```

Behavior:

- `boxt init` initializes a project in the specified directory or current directory.
- Create `.boxt/config.yaml`.
- Detect whether the project is a Git repository.
- Initialize the VM configuration.
- Do not overwrite existing project configuration.
- `boxt up` creates and starts the VM if necessary.
- `boxt down` stops the VM without deleting its state.
- `boxt status` displays project, VM and workspace information.
- `boxt destroy` removes the VM and its guest state after explicit confirmation.

Destroying a VM must not delete project files or Git worktrees.

### Running commands

```bash
boxt
boxt run
boxt run dev
boxt run test
boxt run python
boxt run codex
boxt run -- npm test
boxt run -- python script.py
```

Behavior:

- `boxt` and `boxt run` are equivalent.
- Both execute the configured default run target.
- `boxt run <target>` executes a named target.
- `boxt run -- <command> [args...]` executes an arbitrary command.
- Named targets must be explicitly configured.
- Do not automatically expose every installed executable as a run target.
- Do not introduce special `--agent` flags.
- Do not implement a separate `exec` command.

Target resolution:

1. Resolve the name in `run.targets`.
2. If found, execute its configured command.
3. Otherwise, report an unknown target and explain how to execute arbitrary commands using `--`.

Never silently fall back from an unknown target to an executable on PATH.

### Shell

```bash
boxt shell
```

Opens an interactive shell inside the guest VM.

The shell starts in the default workspace.

Use the same environment initialization and PATH setup as normal command execution.

### Workspace management

```bash
boxt workspace create feature-a
boxt workspace list
boxt workspace remove feature-a
boxt workspace path feature-a
boxt workspace run feature-a
boxt workspace run feature-a dev
boxt workspace run feature-a test
boxt workspace run feature-a python
boxt workspace run feature-a codex
boxt workspace run feature-a -- npm test
boxt workspace shell feature-a
```

Behavior:

- `create` creates a named workspace.
- `list` displays all known workspaces.
- `remove` removes a named workspace.
- `path` prints its host filesystem path.
- `run` executes a target or arbitrary command in that workspace.
- `shell` opens an interactive shell in that workspace.

The default workspace is implicit and cannot be removed.

Workspace names must be validated. Prevent path traversal, absolute paths and invalid names.

Do not allow duplicate workspace names.

Workspace operations must use Git's worktree commands, not manual copying of Git directories.

For the MVP, named workspaces require a Git repository. Non-Git workspace support can be added later.

## 5. Host and guest filesystem architecture

This is an important architectural requirement.

The default project directory is mounted into the guest at:

```text
/workspace
```

Do not mount the user's home directory.

Do not mount the host Docker socket.

Do not expose unrelated host directories to the VM.

### Default workspace

Example host layout:

```text
Host:
~/projects/myapp/
├── .git/
├── .boxt/
│   └── config.yaml
├── src/
├── package.json
└── README.md
```

Guest layout:

```text
Guest:
/workspace/
├── .git/
├── .boxt/
├── src/
├── package.json
└── README.md
```

Use a writable direct host mount.

Do not use file synchronization as the default mechanism.

### Named workspaces

Do not place named worktrees inside the main project checkout.

This would cause the parent repository to see them as untracked directories unless ignored.

Instead, store named worktrees in a separate boxt-managed directory on the host.

Use the platform's appropriate user data directory:

- macOS: `~/Library/Application Support/boxt/workspaces/`
- Linux: `${XDG_DATA_HOME:-~/.local/share}/boxt/workspaces/`

Use a stable project ID derived from the canonical project path, with a collision-resistant hash.

Do not rely only on the project directory name.

Example:

```text
Host:

~/projects/myapp/
├── .git/
├── .boxt/
├── src/
└── package.json

~/.local/share/boxt/workspaces/
└── <project-id>/
    ├── feature-a/
    └── feature-b/
```

On macOS, substitute the corresponding Application Support directory.

Create Git worktrees using commands equivalent to:

```bash
git -C <project-root> worktree add \
    <workspace-path> \
    -b <branch-name>
```

Allow a branch name to be specified separately if useful.

The workspace name and branch name should not be assumed to be identical forever.

### Guest visibility

Mount the project-specific workspace directory separately into the VM.

For example:

```text
Host:
~/.local/share/boxt/workspaces/<project-id>/

Guest:
/workspaces/<project-id>/
```

On macOS, use the equivalent Application Support path.

This mount must be scoped to the current project.

Do not mount the entire boxt data directory or the user's home directory.

A named workspace might therefore have these paths:

```text
Host:
/Users/me/.local/share/boxt/workspaces/abc123/feature-a

Guest:
/workspaces/abc123/feature-a
```

Use a single predictable mapping between host and guest paths.

Store the mapping in project metadata or derive it deterministically.

### Workspace Git behavior

Use Git's worktree functionality for all workspace operations.

On creation:
- Validate the project is a Git repository.
- Verify the requested workspace does not already exist.
- Determine the host workspace path.
- Create the worktree using Git.
- Verify the resulting checkout.
- Record workspace metadata if required.

On removal:
- Check for uncommitted changes.
- Do not silently discard changes.
- Require explicit confirmation or a deliberate force option.
- Use `git worktree remove`.
- Clean up metadata only after successful removal.

Never use `rm -rf` as the normal mechanism for deleting a worktree.

Handle stale Git worktree metadata and missing directories gracefully.

Ensure host-side Git operations continue to work correctly with the external worktree locations.

## 6. Lima VM implementation

Each project gets one persistent Lima VM.

Use a stable VM instance name derived from the project ID.

Default guest configuration:

- Ubuntu 24.04 LTS.
- Non-root user named `boxt`.
- Sudo access for provisioning.
- Persistent guest filesystem.
- Project mount at `/workspace`.
- Named workspace mount at `/workspaces/<project-id>`.
- Outbound internet access.
- No unnecessary inbound access.

### VM lifecycle

Implement:

- Create VM.
- Start VM.
- Stop VM.
- Inspect VM status.
- Destroy VM.
- Execute guest commands.
- Open interactive guest shell.

The VM should not be recreated every time `boxt run` executes.

`boxt up` should be idempotent.

If a run command is invoked while the VM is stopped, boxt should start it automatically unless an explicit option disables this behavior.

Use Lima's supported command execution and shell mechanisms.

Do not assume a particular guest IP address.

### Networking

Allow normal outbound internet access.

Support controlled guest-to-host port forwarding using Lima's supported networking functionality.

For MVP:
- Avoid exposing guest services to the host unless explicitly requested.
- Do not silently choose another host port if a requested port is unavailable.
- Report port conflicts clearly.
- Document that Lima networking does not provide complete isolation from host services.

Do not claim that the VM is a security boundary equivalent to a hardened remote machine.

## 7. Configuration format

Use YAML in:

```text
.boxt/config.yaml
```

Initial schema:

```yaml
version: 1

vm:
  image: ubuntu-24.04
  cpus: 4
  memory: 8GiB

tools:
  node: "24"
  python: "3.13"
  go: "1.24"

run:
  default: dev

  targets:
    dev:
      command: ["npm", "run", "dev"]

    test:
      command: ["npm", "test"]

    python:
      command: ["python"]
      interactive: true

    codex:
      command: ["codex"]
      interactive: true

    opencode:
      command: ["opencode"]
      interactive: true
```

This is the initial schema, not a reason to implement every possible configuration feature.

### Configuration requirements

- Validate configuration on load.
- Produce helpful validation errors with field paths.
- Reject unsupported configuration versions.
- Preserve unrelated user configuration when modifying files.
- Never silently overwrite configuration.
- Use atomic writes when modifying YAML.
- Support sensible defaults.

### Source of truth

`.boxt/config.yaml` is the source of truth for all boxt-managed configuration.

Boxt uses mise internally, but users do not need to configure or manage mise directly.

Do not require or implement compatibility with existing:
- `mise.toml`
- `.mise.toml`

Do not implement:
- Detection of existing mise configuration.
- Configuration merging.
- Version conflict resolution with user-owned mise files.
- User-facing mise backend configuration.
- Mise-specific configuration precedence.

Users may manually install other software inside the VM, but boxt does not promise to manage it.

## Toolchain management with mise

Install mise once per VM.

Use mise internally as the primary manager for language runtimes, development tools and standalone CLI applications supported by its backends.

Boxt owns the tool declarations. `.boxt/config.yaml` is the source of truth for boxt-managed tools.

Users should not need to know about or directly configure mise.

Example configuration:

```yaml
tools:
  node: "24"
  python: "3.13"
  go: "1.24"
  codex: latest
```

Do not assume every tool is available through mise. Use actual supported tool identifiers and backends. If a tool requires an unsupported installation mechanism, report this clearly.

### Commands

```bash
boxt tool list
boxt tool add node@24
boxt tool add python@3.13
boxt tool remove node
boxt provision
```

### `boxt tool add`

Adding a tool must automatically apply the change whenever possible.

Behavior:

1. Parse and validate the tool name and version.
2. Update `.boxt/config.yaml` atomically.
3. If the VM is running, immediately install the requested tool using mise.
4. If the VM is stopped, save the declaration and defer installation until the next `boxt up`.
5. Report whether installation succeeded, failed or was deferred.

If immediate installation fails:
- Keep the configuration declaration.
- Clearly report that the declaration was saved but installation failed.
- Explain that `boxt provision` can retry the operation.
- Do not silently roll back the configuration.

### `boxt tool remove`

Removing a tool removes its declaration from `.boxt/config.yaml`.

Do not automatically uninstall the software from the VM.

Installed software may be shared by multiple workspaces or referenced by run targets. Removing a declaration must not unexpectedly break other workspaces.

Report that the declaration was removed but the installed software remains in the VM.

### `boxt tool list`

Display:
- Tool name.
- Declared version.
- Installed version, when available.
- Installation status.
- Whether the installed version matches the declaration.

### `boxt provision`

This is an explicit reconciliation and repair command.

It must reconcile the VM's installed toolchain against `.boxt/config.yaml`.

Responsibilities:
- Install missing declared tools.
- Install or switch to declared versions.
- Correct version mismatches.
- Ensure mise is installed and operational.
- Ensure required system dependencies are available.
- Detect and report unsupported tools.
- Report provisioning failures clearly.

Provisioning must be safe to run repeatedly and should be idempotent.

If the VM is stopped, `boxt provision` should start it, perform provisioning and leave it running.

### Automatic provisioning during VM startup

`boxt up` must automatically reconcile declared tools before reporting the environment ready.

Startup behavior:

1. Create or start the VM.
2. Ensure the guest user and required base dependencies exist.
3. Install or verify mise.
4. Reconcile declared tools.
5. Verify that the execution environment can access the installed tools.
6. Report that the environment is ready.

If provisioning fails, report the failure and do not claim the environment is fully ready.

The VM may remain running to allow troubleshooting.

### Persistence and execution environment

- Install mise once in the VM.
- Store mise-managed installations and caches in the persistent guest filesystem.
- Use the non-root `boxt` user for normal tool installation.
- Ensure tools are available in both interactive and non-interactive execution.
- Do not rely on `.bashrc` or `.profile` to initialize PATH.
- Use mise's documented environment and execution interfaces.
- Avoid shell commands constructed through string interpolation.

### Existing mise configurations

Existing `mise.toml` and `.mise.toml` files are outside the MVP scope.

Do not implement configuration detection, merging, precedence or conflict resolution.

Boxt uses its own configuration format and manages its own tool declarations.

### System packages

Use the guest OS package manager for system-level dependencies when required.

For the Ubuntu guest, use apt.

Do not attempt to turn mise into a general system package manager.

Full service installation and lifecycle management are outside the initial scope.

## 9. Unified execution engine

All execution must go through one shared internal executor.

The following commands should use the same execution implementation:

```bash
boxt run dev
boxt run codex
boxt run -- python script.py

boxt workspace run feature-a dev
boxt workspace run feature-a codex
boxt workspace run feature-a -- python script.py

boxt shell
boxt workspace shell feature-a
```

The only important difference is the selected working directory.

### Execution request

Use a request model conceptually similar to:

```go
type ExecutionRequest struct {
    ProjectID string
    Workspace string
    WorkDir   string

    Command []string

    Interactive bool
}
```

The executor is responsible for:

1. Ensuring the VM is running.
2. Resolving the guest working directory.
3. Preparing the guest environment.
4. Resolving the executable and arguments.
5. Launching the command.
6. Connecting terminal streams when interactive.
7. Forwarding terminal dimensions and signals.
8. Returning the command exit code.

Do not construct shell strings from user input.

Pass command and arguments separately.

Do not use `cd ... && command` as the execution mechanism.

Use Lima's supported guest execution interface with an explicit working directory.

### Interactive terminal support

This is essential because boxt must support coding agents and REPLs.

Implement proper PTY handling:

- stdin forwarding
- stdout and stderr forwarding
- terminal dimensions
- resize events
- SIGINT
- SIGTERM
- terminal restoration on exit
- correct process exit status

The interactive process must behave like a normal foreground terminal application.

Do not implement interactive commands by capturing their output and printing it after completion.

### Run target semantics

A run target defines an executable and arguments:

```yaml
run:
  targets:
    test:
      command: ["npm", "test"]
```

An optional interactive setting may be used:

```yaml
interactive: true
```

Use PTY execution automatically where appropriate for interactive terminal applications.

Do not introduce agent-specific target types in the MVP.

Agents are ordinary executables that can be configured as interactive targets.

Do not add implicit target registration based on installed tools.

## 10. Security and filesystem safety

The VM should run ordinary development commands as the non-root `boxt` user.

Use sudo only for provisioning and system administration.

Requirements:

- Never mount the host home directory.
- Never mount the Docker socket.
- Never copy host SSH keys or credentials into the guest automatically.
- Do not silently expose host services.
- Validate workspace names and filesystem paths.
- Avoid shell interpolation.
- Use safe subprocess argument handling.
- Protect configuration writes.
- Do not delete user data without explicit confirmation.
- Clearly document the security limitations of the VM networking model.

The guest's persistent home directory can store tools, caches and credentials entered by the user inside the VM.

## 11. Project discovery

Commands should work when invoked from any directory inside an initialized project.

Find the nearest ancestor containing:

```text
.boxt/config.yaml
```

Use that directory as the project root.

Allow `boxt init [path]` to initialize a project at an explicit path.

Do not automatically initialize a project when a configuration file is missing.

Provide a helpful error explaining how to initialize one.

Use canonicalized project paths when deriving project identity.

Handle symbolic links consistently so that the same project does not accidentally receive multiple VMs.

## 12. Internal architecture

Organize the Go code into clear packages.

Suggested structure:

```text
cmd/
  boxt/

internal/
  cli/
    root.go
    init.go
    lifecycle.go
    run.go
    shell.go
    workspace.go
    tool.go

  config/
    config.go
    schema.go
    validate.go

  project/
    discovery.go
    metadata.go

  vm/
    lima.go
    lifecycle.go
    execute.go
    config.go

  workspace/
    workspace.go
    git.go
    paths.go

  executor/
    executor.go
    request.go
    pty.go

  toolchain/
    mise.go
    provision.go

  platform/
    platform.go
    paths.go
```

Keep package boundaries clear.

CLI commands should not directly execute Lima or Git commands throughout the codebase.

Wrap external tools behind focused interfaces so they can be tested with mocks.

Avoid premature abstraction.

Do not build a generic plugin architecture, component registry or agent framework.

## 13. Error handling and UX

Boxt should provide clear, actionable errors.

Examples:

```text
Project not initialized.

Run `boxt init` to initialize this directory.
```

```text
Unknown run target: python

Define it under run.targets in .boxt/config.yaml,
or use `boxt run -- python` to execute the command directly.
```

```text
Workspace already exists: feature-a
```

```text
Cannot remove workspace feature-a:
it contains uncommitted changes.

Commit or stash your changes before removing it.
```

```text
Host port 5432 is already in use.

Choose another port or stop the process using that port.
```

Use sensible logging levels.

Do not print excessive Lima output during normal operation.

Provide a verbose/debug option for troubleshooting.

## 14. Testing requirements

Implement unit tests for:

- Configuration parsing and validation.
- Project discovery.
- Project ID generation.
- Workspace name validation.
- Workspace path resolution.
- Git worktree operations.
- Run target resolution.
- Execution request construction.
- CLI argument parsing.
- Error handling.
- Platform-specific data directory resolution.

Implement integration tests where practical for:

- Lima VM creation and lifecycle.
- VM persistence across stop/start.
- Project mount.
- Named workspace mount.
- Tool installation.
- Command execution.
- Interactive shell execution.
- Interactive PTY behavior.
- Git worktree creation and removal.

Tests must verify that named workspaces are visible on both the host and guest.

Verify that creating a named worktree does not make the parent repository report it as an untracked directory.

Use mocks for external processes in unit tests.

Provide a clear way to run integration tests separately.

### Platform testing

Test on:

- macOS Apple Silicon.
- Ubuntu x86_64.
- Fedora x86_64.

Add aarch64 Linux support where the available Lima backend and virtualization environment permit it.

Document platform-specific limitations.

## 15. Documentation

Write a README covering:

- What boxt is.
- Supported host platforms.
- Prerequisites.
- Installation.
- Project initialization.
- Starting and stopping the VM.
- Tool installation.
- Configuring run targets.
- Running commands.
- Creating and managing workspaces.
- Running coding agents.
- Host and guest filesystem layout.
- Networking behavior.
- Security limitations.
- Troubleshooting.

Include a complete example project.

Example workflow:

```bash
mkdir myapp
cd myapp

git init

boxt init
boxt up

boxt tool add node@24

boxt workspace create feature-a

boxt run dev
boxt workspace run feature-a codex

boxt workspace list
boxt workspace path feature-a

boxt down
```

## 16. Explicitly out of scope for the MVP

Do not implement:

- Docker or container orchestration.
- Kubernetes.
- Nix.
- Remote/cloud VMs.
- Multi-VM projects.
- Workspace synchronization.
- Non-Git workspace implementations.
- Active workspace selection.
- Command palette or interactive TUI.
- General-purpose component catalog.
- Agent-specific installation framework.
- Agent-specific execution framework.
- Service management for PostgreSQL, Redis and other databases.
- Automatic host credential synchronization.
- Automatic host editor integration.
- Shell integration for `boxt workspace cd`.
- General-purpose port management beyond basic Lima forwarding.
- Multi-user VM access control.
- Existing mise configuration compatibility.
- Automatic merging of mise configuration.
- User-facing mise configuration.

The architecture should leave room for these capabilities without implementing them prematurely.

## 17. Implementation milestones

Implement in the following order.

### Milestone 1 — CLI and configuration

- Go project setup.
- Cobra CLI.
- Project discovery.
- `boxt init`.
- Configuration schema and validation.
- Platform-specific application data directories.
- Unit tests.

### Milestone 2 — Lima integration

- Generate Lima configuration.
- Create and start VM.
- Stop and destroy VM.
- Project mount.
- Guest user setup.
- VM status.
- Additional workspace mount.
- Integration tests.

### Milestone 3 — Unified execution

- Guest command execution.
- `boxt run`.
- Default target.
- Named targets.
- Arbitrary commands after `--`.
- Interactive PTY.
- `boxt shell`.

### Milestone 4 — Toolchain

- Install mise.
- Tool declarations.
- `boxt tool list`.
- `boxt tool add`.
- `boxt tool remove`.
- `boxt provision`.
- Persistent tool installations.
- Non-interactive PATH initialization.

### Milestone 5 — Workspaces

- Stable project ID.
- Host workspace directory.
- Additional Lima mount.
- Git worktree creation.
- Git worktree listing.
- Git worktree removal.
- Workspace path resolution.
- `boxt workspace run`.
- `boxt workspace shell`.
- Host/guest integration tests.

### Milestone 6 — Polish

- Status output.
- Better error messages.
- Debug logging.
- Documentation.
- Cross-platform testing.
- Installation and release packaging.

## 18. Definition of done

The MVP is complete when a developer can:

1. Install boxt on macOS Apple Silicon, Ubuntu Linux or Fedora Linux.
2. Initialize an existing Git project.
3. Start a persistent Ubuntu VM.
4. Access the project directory from the VM.
5. Install development tools into the persistent VM.
6. Define and execute named run targets.
7. Execute arbitrary commands inside the VM.
8. Launch interactive coding agents inside the VM.
9. Create multiple Git workspaces.
10. Access those workspaces from both the host and guest.
11. Execute commands in any workspace without changing a global active-workspace setting.
12. Stop and restart the VM without losing installed tools or guest state.
13. Destroy the VM without deleting project files or Git worktrees.

The implementation must be reliable, tested and documented.

## 19. Development instructions

Before implementing:

1. Inspect the repository and existing code.
2. Identify existing conventions and dependencies.
3. Check the current Lima configuration and execution APIs.
4. Check the current mise installation and configuration interfaces.
5. Confirm Git worktree behavior for the selected directory layout.
6. Identify platform-specific differences between macOS, Ubuntu and Fedora.
7. Verify that the selected Lima mount configuration supports both project and external workspace directories.

Do not guess at external tool behavior or invent unsupported flags.

Prefer small, working increments over a large untested implementation.

Keep the code idiomatic Go.

Do not overengineer the MVP.

When a design decision is unclear, prefer the simplest implementation that preserves the architecture and public CLI described here.

At the end of each milestone:
- Run tests.
- Fix regressions.
- Update documentation.
- Summarize implemented functionality.
- Identify any incomplete work or known limitations.

**The final result should be a usable, persistent local development environment manager — not merely a CLI prototype that wraps a few Lima commands.**
