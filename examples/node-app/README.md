# Example: Node.js app

A small dependency-free Node.js project used to demonstrate boxt.

```bash
cd examples/node-app
boxt up
boxt run dev
```

The server listens on port 3000 inside the VM. With the default
`port_forwarding: auto`, open <http://127.0.0.1:3000> on the host.

```bash
boxt run test
boxt workspace create feature-a
boxt workspace run feature-a test
boxt workspace run feature-a codex
boxt down
```

`codex` is configured as an interactive target. Add `opencode` or another agent
the same way if it is installed in the VM.
