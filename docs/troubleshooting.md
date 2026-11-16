# Troubleshooting

Start with `boxt doctor`. It reports `OK`, `WARN` or `FAIL` for each prerequisite
and prints the exact command or setting to fix failures, exiting non-zero when
anything fails. It never installs host software, edits project configuration or
starts the VM.

```text
OK    host: macos/arm64
OK    git: 2.55.0
OK    limactl: 2.3.0
OK    virtualization: Apple Virtualization (vz) is available
```

Use `boxt -v up` or `boxt -v run test` for debug logs of external commands
(arguments only, no environment variables). Command arguments can appear in logs
and error messages, so avoid passing secrets as arguments; use files or guest
environment variables instead.

**Lima, QEMU or KVM missing**

See [Platform setup](../README.md#platform-setup) for per-OS packages and KVM
notes.

**WSL2: VM is extremely slow**

Check that `/dev/kvm` exists inside WSL and that guest memory fits the WSL limit;
without KVM boxt falls back to slow software emulation (TCG). See
[Platform setup](../README.md#platform-setup).

**WSL2: project permissions or Git worktrees behave strangely**

The project is probably on `/mnt/c` (DrvFs). Move it to the WSL ext4 filesystem,
for example `~/projects/myapp`, and set `XDG_DATA_HOME` there as well.

**WSL2: ports work inside WSL but not from Windows**

Verify `curl 127.0.0.1:3000` inside WSL, then check `localhostForwarding`, Windows
Firewall/VPN rules, and that the forward binds `127.0.0.1`.

**Project not initialized**

Run `boxt init` at the project root. Commands need a directory tree containing
`.boxt/config.yaml`; boxt never initializes implicitly.

**VM exists but is broken**

`boxt status` reports broken or uninitialized VMs. If `boxt up` cannot recover it,
run `boxt destroy --yes` then `boxt up`. Project files and worktrees are preserved.

**Port conflict**

Stop the process using the port, or change `vm.ports`. boxt never silently picks
another host port for explicit mappings.

**Provisioning failed**

The declaration is kept. Fix the reported error and run `boxt provision`; the VM
stays running for inspection.

**Named workspace operations fail**

Named workspaces need the Git repository root to be the project root; `git status`
from the project root and the warning from `boxt init` will show a mismatch. To
remove a workspace that still has uncommitted or ignored files, use
`boxt workspace remove <name> --force`; the branch is kept either way. Stale
worktree metadata is pruned by `boxt workspace remove <name>`.

[Back to the README](../README.md)
