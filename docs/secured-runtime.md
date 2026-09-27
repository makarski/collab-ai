# Separate dev and secured runtimes

Add `secured_runtime = true` to `infra/incus/sandbox.auto.tfvars`, then follow the
usual [plan and apply](sandbox.md#3-preview-and-apply). Use a workspace image built
from this revision or later. Existing deployments keep their `workspace` container.

| Runtime | Purpose | Access |
| --- | --- | --- |
| `workspace` (dev) | Repositories, builds, agent-executed commands and broker | Existing unprivileged SSH setup; selected host mounts |
| `secured` | Private settings and budget storage | Human administrator through the host's Incus socket |

Both containers are offline, unprivileged and have separate UID mappings.
Secured adds 1 CPU, 2 GiB RAM, a root disk of `disk_gib`, a 1 GiB state volume and
a 16 MiB status volume. It inherits no dev mounts and does not start the broker.
No authenticated clients or budget supervisor launch automatically.

**This provisions isolation and storage, not integrated agent-resistant sessions.**
Native authentication, interactive/resume routing and shared accounting remain
tracked in [#34](https://github.com/makarski/collab-ai/issues/34). Keep credentials
out of dev and do not enable model access as part of this setup.

## Human budget administration

Run on your host. On Linux replace `colima-collab-ai:` with `local:`:

```sh
incus --project collab-ai exec colima-collab-ai:secured -- \
  env COLLAB_BUDGET_DIR=/var/lib/collab-ai-secured/budgets \
  collab budget create my-task --tokens 100000

incus --project collab-ai exec colima-collab-ai:secured -- \
  env COLLAB_BUDGET_DIR=/var/lib/collab-ai-secured/budgets \
  collab budget status my-task --json
```

The budget directory lives on a root-only custom volume mounted only in secured.
Creation refuses to overwrite a cap or reset usage. There is no workload-facing
administration endpoint. Incus administrators remain trusted: they can change
containers and disks. Keep host control scripts, state, credentials and the Incus
socket outside dev mounts; run project code only in `workspace`.

`/mnt/collab-status` is a separate volume: secured root can publish a status socket;
dev receives a read-only mount. Its root-owned directory is not world-writable.
Only public status belongs here, never budget JSON, credentials or admin sockets.
Once a protected launcher publishes `status.sock`, a dev terminal can use:

```sh
collab budget status my-task --socket /mnt/collab-status/status.sock --json
```

Provisioning alone leaves this directory empty. A missing socket is an error;
there is no fallback to a dev-local budget. Read-only mounting prevents directory
changes but does **not** prevent socket requests; the status server's read-only
API rejects mutations. Polling this endpoint is visibility, not admission control.

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
Changing an image can still replace the dev root disk. Export needed data first.
Setting `secured_runtime = false`, changing the project/pool, or applying a destroy
plan can delete the custom volumes and their budgets: review deletions carefully.
The [destroy procedure](sandbox.md#stop-or-remove) now includes both runtimes and
volumes. It retains the host VM and shared storage pool.

## Validation

CI tests both architectures with an offline fake client: single-runtime upgrade
without losing dev data, unchanged second plan,
separate mappings, denied private-state access, denied status replacement/remount,
real status reads over the shared UDS, persisted accounting after stop/start and
secured replacement, and teardown. Dev host mounts are tested in the same deployment.
No provider requests or subscription credentials are used.

To repeat with a disposable project (macOS shown):

```sh
python3 scripts/sandbox-smoke.py --remote colima-collab-ai \
  --image-dir dist/installed-workspace --secured-check
```

The storage pool/kernel must support Incus ID-mapped custom volumes
([`security.shifted`](https://linuxcontainers.org/incus/docs/main/reference/storage_zfs/#storage-volume-configuration));
ZFS requires 2.2+ for idmaps. Unsupported hosts should fail provisioning rather
than sharing the containers' identities or using world-writable IPC directories.
