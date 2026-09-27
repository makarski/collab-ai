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
- Native Codex also runs through `collab-codex --restricted-operator` with a
  named budget. The proof exercises denied operator requests, native tool
  routing, persisted usage, and exhaustion with operator stdin still open.
- Native Claude uses `--restricted --tools "" --strict-mcp-config` and one
  approved MCP tool in dev. A model response requesting local Bash is rejected.

The native-client tests use deterministic loopback fake providers. The fake Claude
key is a fixture string, not a credential; any displayed token costs are computed
from synthetic responses. No provider requests or subscription spending occur.
The tested versions are those pinned in `infra/image/tools.lock.json`.
The probes use `codex exec`, the Codex App Server and `claude -p`; interactive
terminals and resumed conversations are not exercised here.

The restricted Codex probe combines native tool routing and budget supervision.
These tests **do not** establish a complete protected subscription workflow, shared Claude/Codex
accounting, all hook/plugin/resume paths, or zero overshoot. The ordinary launcher
still runs under its caller's permissions; adding a status socket does not
automatically move it into a protected container.

## Experimental restricted operator

`--restricted-operator` limits the **stdio protocol**, not the native client's
filesystem or network access. It requires an existing `--budget NAME` selected by
the administrator. The executable, environment, working directory, configuration,
budget directory and broker endpoint must remain under trusted control outside dev.
No credentials or network access are installed by this flag.

Allowed requests:

| Method | Accepted parameters |
| --- | --- |
| `initialize` | Optional client metadata and `experimentalApi`; replaced with proxy-owned metadata/capabilities |
| `initialized` | Empty notification |
| `thread/start` | Empty object; native host defaults and proxy-owned tools apply; approvals are set to `never` |
| `turn/start` | Managed `threadId` and 1–16 literal text items |
| `turn/steer` | Same text input plus `expectedTurnId` |
| `turn/interrupt` | Managed `threadId` and `turnId` |

Parameters are bounded to 64 KiB and reconstructed before forwarding. Unknown
methods/fields, caller-supplied tool results, local images/skills, configuration
overrides, usage notifications and responses to server requests are rejected.
The frontend cannot opt out of accounting notifications. A second thread is
rejected; resume/fork, terminal mode and forwarded CLI arguments are unsupported.
Unexpected host approval/input requests stop the session and persist a budget
failure rather than waiting for an unsupported approval flow.

This is an offline integration building block, not a production launch recipe.
Use the proof above to exercise it. Trusted native configuration and executor
provisioning, interactive/resume binding, supervisor-death containment and native
subscription authentication still need integration under [#34](https://github.com/makarski/collab-ai/issues/34).

Source references: [Codex's pinned environment configuration](https://github.com/openai/codex/blob/rust-v0.156.1/codex-rs/exec-server/src/environment_toml.rs)
and [Claude's tool restrictions](https://code.claude.com/docs/en/cli-reference).
