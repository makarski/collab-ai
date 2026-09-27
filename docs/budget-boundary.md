# Offline budget boundary proof

This is an **experimental test setup**, not the installed workspace configuration.
It creates two disposable Incus containers with no network interfaces:

| Container | Runs | Shared access |
| --- | --- | --- |
| `secured` | Budget owner, supervisor, native clients, fake model server | Publishes a read-only budget API; connects to dev executors |
| `dev` | Unprivileged commands and MCP executor | Reads the status socket; cannot see secured state or its processes |

The human's Incus client provisions and destroys the containers. Neither
container receives the host's Incus socket, credentials, or project files.

## Run

Use an image built from this branch; the script verifies its manifest checksum.
On Linux, run against a disposable initialized local Incus server:

```sh
sudo python3 scripts/sandbox-boundary.py --remote local --image-dir dist/installed
```

On macOS, with the dedicated Colima host already running:

```sh
python3 scripts/sandbox-boundary.py --remote colima-collab-ai --image-dir dist/installed
```

For local Go changes, `--bin-dir /absolute/path/to/bins` overlays Linux `collab`
and `collab-codex` binaries matching the server architecture. The script creates
a unique project and temporary IPC directories, then removes them on exit. It
does not restart Colima or alter its mount configuration. CI runs this proof on
both native Linux architectures.

## What it tests

- The dev user reads a cap but cannot change it through API requests, a spoofed
  admin identity, a local budget with the same name, or filesystem access.
- The dev mount cannot replace the status socket. Incus administration is absent.
- The protected launcher stops a fake client at 107 reported tokens against a
  cap of 100, even while operator stdin remains open. Exhaustion survives a
  container restart; relaunch is refused. Losing the status endpoint returns an
  error rather than a local or unlimited allowance.
- Native Codex uses `environments.toml` with `include_local = false` and a stdio
  executor in dev. A requested `local` execution environment is rejected.
- Native Claude uses `--restricted --tools "" --strict-mcp-config` and one
  approved MCP tool in dev. A model response requesting local Bash is rejected.

The native-client tests use deterministic loopback fake providers. The fake Claude
key is a fixture string, not a credential; any displayed token costs are computed
from synthetic responses. No provider requests or subscription spending occur.
The tested versions are those pinned in `infra/image/tools.lock.json`.

These are separate tests of tool routing and budget supervision. They **do not**
establish a complete protected subscription workflow, shared Claude/Codex
accounting, all hook/plugin/resume paths, or zero overshoot. The ordinary launcher
still runs under its caller's permissions; adding a status socket does not
automatically move it into a protected container. Draft PR #43 remains experimental.

Source references: [Codex's pinned environment configuration](https://github.com/openai/codex/blob/rust-v0.156.1/codex-rs/exec-server/src/environment_toml.rs)
and [Claude's tool restrictions](https://code.claude.com/docs/en/cli-reference).
