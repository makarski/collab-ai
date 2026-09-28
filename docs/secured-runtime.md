# Control runtime setup (offline)

Use a workspace image built from this revision or later. For an existing deployment,
stop workspace and any existing secured container first (Linux: use `local:`):

```sh
incus --project collab-ai stop colima-collab-ai:workspace
# If secured already exists:
incus --project collab-ai stop colima-collab-ai:secured
```

Then add `secured_runtime = true` to `infra/incus/sandbox.auto.tfvars` and follow
[plan and apply](sandbox.md#3-preview-and-apply). The apply starts both containers
when `running = true`. Fresh installations can enable this option directly.
Enabling the layout preserves dev volumes. Images may replace the dev root disk;
`/workspace` and `/home/agent` survive. This storage layout supports fresh deployments only.

An apply-time host preflight rejects enabling/upgrading the layout while either container is running.
Incus hot-added read-only mounts can be remounted writable by container root;
the broker, status and executor mounts must be present at boot. Keep both containers
stopped after a failed apply, then retry. Do not hot-add these mounts.

| Runtime | Purpose | Access |
| --- | --- | --- |
| `workspace` (dev) | Codex/Claude adapters, repositories and builds running as `agent` | Unprivileged SSH; selected host mounts |
| `secured` (control) | One collaboration broker and SQLite database; operator dashboard | Human administration through host Incus |

Both containers are offline, unprivileged and have separate UID mappings.
Secured adds 1 CPU, 2 GiB RAM, a root disk of `disk_gib`, 1 GiB private state,
and three 16 MiB IPC volumes. Existing instance names are retained to avoid a
destructive rename. With this mode enabled, the workspace's standalone broker
does not start. Both dev adapters and the control dashboard use the same broker.

**The sandbox remains offline.** No credentials or external model access are
provisioned. The existing experimental protected Codex launcher is retained below;
ordinary dev agents do not gain protected accounting by sharing its broker.

## Shared broker and dashboard

After [SSH setup](sandbox.md#4-ssh-into-the-workspace), inspect the broker from dev:

```sh
ssh -F infra/incus/ssh/config workspace collab status
```

Open the operator dashboard in control (Linux: replace `colima-collab-ai:` with `local:`):

```sh
incus --project collab-ai exec colima-collab-ai:secured -t -- collab dashboard
```

The shared socket is `/mnt/collab-ipc/broker.sock`. Existing `/tmp/collab-ai.sock`
defaults are root-owned aliases, so `collab-codex`, `collab-mcp`, and the installed
Claude MCP configuration connect without overrides. `/run/collab-ai/broker.sock`
also remains an alias in control for the experimental launcher.

Control mounts the IPC volume read-write; dev mounts it read-only at boot.
Directory `0750` and socket `0660` are owned by `broker:collab-clients`; `agent`
belongs to the client group. Incus shifts the volume identities into each isolated
container's mapping. Clients can send protocol requests, but cannot replace the
socket or access the database. This does not authenticate agent IDs or isolate
clients from other processes running as the same user.

The only active collaboration database is
`/var/lib/collab-ai-secured/broker/broker.db`. On upgrade, an existing control
`broker.db` is copied consistently into that directory once; the original is retained.
The old workspace database is left untouched and is **not merged** into control.
Export any needed workspace history before replacing its root disk. The control
database survives broker crashes and control-container replacement.

## Experimental protected Codex launcher

This separate path still takes JSON protocol input, not terminal prompts.
Subscription login, protected terminal/resume support and Claude/shared accounting
remain under [#34](https://github.com/makarski/collab-ai/issues/34). Its managed
provider points to an unused loopback fixture port (`18080`).

Run on your host. On Linux replace `colima-collab-ai:` with `local:`:

```sh
incus --project collab-ai exec colima-collab-ai:secured -- \
  env COLLAB_BUDGET_DIR=/var/lib/collab-ai-secured/budgets \
  collab budget create my-task --tokens 100000

incus --project collab-ai exec colima-collab-ai:secured -- \
  env COLLAB_BUDGET_DIR=/var/lib/collab-ai-secured/budgets \
  collab budget status my-task --json
```

Launch from the host, connecting stdin/stdout to a restricted App Server client:

```sh
incus --project collab-ai exec colima-collab-ai:secured -T -- \
  collab-supervised-codex my-task
```

This takes [JSON-line protocol messages](budget-boundary.md#experimental-restricted-operator),
not terminal prompts. Terminal and resume support are not available on this path.
The budget is fixed by the administrator; operator messages cannot select another.
To stop it from another host terminal:

```sh
incus --project collab-ai exec colima-collab-ai:secured -- \
  systemctl stop collab-codex-my-task.service
```

The budget directory remains root-only on the private volume mounted only in secured.
Creation refuses to overwrite a cap or reset usage. There is no workload-facing
administration endpoint. Incus administrators remain trusted: they can change
containers and disks. Keep host control scripts, state, credentials and the Incus
socket outside dev mounts; run project code only in `workspace`.

`/mnt/collab-status` is a separate volume: secured root can publish a status socket;
dev receives a read-only mount. Its root-owned directory is not world-writable.
Only public status belongs here, never budget JSON, credentials or admin sockets.
While `my-task` is running, a dev terminal can read its published status:

```sh
collab budget status my-task --socket /mnt/collab-status/my-task.sock --json
```

Provisioning alone leaves this directory empty. A missing socket is an error;
there is no fallback to a dev-local budget. Read-only mounting prevents directory
changes but does **not** prevent socket requests; the status server's read-only
API rejects mutations. Polling this endpoint is visibility, not admission control.

## Installed services and trust

- `collab-secured-setup` installs managed configuration on every secured boot,
  preserving budgets and usage. `/workspace` there is root-owned and contains no dev repository.
- `collab-secured-broker` runs as `broker` and owns the shared IPC socket and private
  database directory. The broker group can traverse the private volume root; budget
  and native configuration directories stay root-only. Its startup helper removes
  only a stale socket it owns, and refuses live endpoints, files or symlinks.
- `collab-client-setup` connects default client paths to the shared socket. The dev
  broker is disabled by its mount condition; a missing control broker never causes
  fallback to a dev-local database. Without `secured_runtime`, the standalone
  workspace broker still runs as before.
- `collab-dev-executor.socket` owns a root-only socket on `/mnt/collab-executor`.
  Each accepted connection starts a native executor as `agent`, with no added privileges.
  Secured mounts the endpoint read-only. Agent commands cannot connect, unlink it,
  substitute a symlink, or modify installed executables.

Container/host administrators remain trusted. Dev root can replace its executor;
the protection here is against unprivileged agent commands. Missing or unsafe
endpoints prevent launch through a bounded connection check; there is no local-execution
fallback. This startup check does not monitor later executor disconnects, which
remain native tool errors under the session's soft cap. The supervisor
[stops native clients on failure](budget-boundary.md#supervisor-failure).

## Status, stop and restart

```sh
# macOS; Linux: use local: instead.
incus --project collab-ai list colima-collab-ai:
incus --project collab-ai stop colima-collab-ai:workspace colima-collab-ai:secured
incus --project collab-ai start colima-collab-ai:secured colima-collab-ai:workspace
```

Set `running = false` and apply to keep **both** containers stopped across applies.
Set it back to `true` and apply to restart. Boot autostart is disabled. The Incus
web UI shows both containers under project `collab-ai`.

Private state survives restarts and replacement of the secured root disk.
Changing an image replaces the dev root disk but retains its workspace and agent-home volumes.
Setting `secured_runtime = false`, changing the project/pool, or applying a destroy
plan can delete the custom volumes and their budgets: review deletions carefully.
The [destroy procedure](sandbox.md#stop-or-remove) now includes both runtimes and
volumes. Persistent dev volumes block destruction until their deletion guard is explicitly disabled. The procedure retains the host VM and shared storage pool.

## Validation

CI tests both architectures with an offline fake client: rejection of unsafe hot upgrades,
stopped-workspace upgrade without data loss, unchanged second plan,
separate mappings, denied private-state access, denied status replacement/remount,
real status reads over the shared UDS, persisted accounting after stop/start and
secured replacement, and teardown. Dev host mounts are tested in the same deployment.
It also exercises the installed native configuration, broker and executor, endpoint
replacement denials, restart and missing-executor refusal. No provider requests or
subscription credentials are used.
The shared-broker proof exchanges a correlated reply between the real dev MCP
adapter and managed Codex proxy with a fake App Server, verifies both containers
see the same broker, and checks client-group permissions and SIGKILL recovery.

To repeat on the Linux Incus host (including inside the dedicated Colima VM):

```sh
sudo python3 scripts/sandbox-smoke.py --remote local \
  --image-dir dist/installed-workspace --secured-check
```

Use a maintained Incus release (CI uses [Zabbly's 6.0 LTS packages](https://github.com/zabbly/incus#60-lts-repository)).
Ubuntu 24.04's original Incus 6.0.0 package can reject these mounts on newer
kernels because of an [upstream detection bug](https://github.com/lxc/incus/issues/882).
The storage pool/kernel must support Incus ID-mapped custom volumes
([`security.shifted`](https://linuxcontainers.org/incus/docs/main/reference/storage_zfs/#storage-volume-configuration));
ZFS requires 2.2+ for idmaps. Unsupported hosts should fail provisioning rather
than sharing the containers' identities or using world-writable IPC directories.
