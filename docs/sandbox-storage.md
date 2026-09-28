# Persistent workspace and agent home

Fresh deployments keep dev data on two Incus volumes, mounted only in `workspace`:

| Volume | Mount | Default quota | Ownership |
| --- | --- | --- | --- |
| `workspace-data` | `/workspace` | 10 GiB (`workspace_gib`) | `agent:agent`, directory `0750` |
| `agent-home` | `/home/agent` | 2 GiB (`agent_home_gib`) | `agent:agent`, directory `0700` |

This setup targets fresh deployments.

The volumes survive container restart and replacement, including image upgrades. Incus
maps ownership into each replacement container. First boot seeds missing shell
startup files; existing settings are never overwritten. Both agents share this home.
Retaining session files does **not** yet prove native conversation resume across versions.

The root disk (`disk_gib`) is replaceable: installed tools, SSH host keys and files
outside these mounts do not survive replacement. The standalone broker database
is also on that root disk; use [control mode](secured-runtime.md) for persistent
collaboration history. In control mode, SQLite and budgets stay on their separate
private volume. No dev volume is mounted into control.

Selected host directories remain optional nested mounts under `/workspace`.
Their data lives on the host, not in `workspace-data`. Host-agent homes are never
mounted automatically. Dedicated host-sharing identities and selected host-state
import are later work.

## Back up and restore

Exit agents, editors and other writers first; keep the container running for these
file-level exports. Unshare optional host mounts using the [mount guide](sandbox-mounts.md)
first, so the archives contain sandbox data only. Keep backups private and outside
a writable sandbox mount; a home may contain credentials once login is configured.

Run on the operator's host. These commands use macOS names; on Linux replace
`colima-collab-ai:` with `local:`:

```sh
umask 077
incus --project collab-ai exec colima-collab-ai:workspace -T --user 1001 --group 1001 -- \
  tar -C /workspace --one-file-system -czf - . > workspace-data.tgz
incus --project collab-ai exec colima-collab-ai:workspace -T --user 1001 --group 1001 -- \
  tar -C /home/agent --one-file-system -czf - . > agent-home.tgz
# Both exports must succeed; inspect and retain the archives before changing storage.
tar -tzf workspace-data.tgz
tar -tzf agent-home.tgz
```

Restore into a **fresh, initialized** workspace, with no agents running. Substitute
its project name below if different. Extraction runs as `agent`, not root:

```sh
incus --project collab-ai exec colima-collab-ai:workspace -T --user 1001 --group 1001 -- \
  tar -C /workspace --no-same-owner --no-overwrite-dir -xzf - < workspace-data.tgz
incus --project collab-ai exec colima-collab-ai:workspace -T --user 1001 --group 1001 -- \
  tar -C /home/agent --no-same-owner --no-overwrite-dir -xzf - < agent-home.tgz
```

Check restored files, permissions and agent settings before discarding backups. The paths remain `/workspace` and `/home/agent`; hooks and credentials
are not validated by this procedure. No model calls or host-home import are needed.
Incus also supports [volume snapshots and exports](https://linuxcontainers.org/incus/docs/main/howto/storage_backup_volume/)
for operator-managed backups. Retention is not a substitute for backup.

## Replace dev, retain data

Review the replacement plan against the existing state:

```sh
tofu -chdir=infra/incus plan -replace=incus_instance.workspace -out=replace.tfplan
tofu -chdir=infra/incus apply replace.tfplan
```

The plan should replace only the dev container, not either data volume. After the
verified replacement, renew SSH trust explicitly: remove the old
`infra/incus/ssh/known_hosts`, then rerun `sandbox-ssh.py`. The private operator key
stays on the host; the helper installs its public key into the new root disk.
Running processes and tmux sessions do not survive container replacement.

## Deliberate removal

`prevent_destroy = true` in [storage.tf](../infra/incus/storage.tf) blocks plans
that would delete or replace the dev volumes, including a full destroy. Changing
the project or storage pool must not silently replace them.

After backing up and deciding to delete the data, change that literal to `false`
in the configuration used by this deployment. Then review and apply a destroy plan:

```sh
tofu -chdir=infra/incus plan -destroy -out=destroy.tfplan
# This deletes the containers and their managed volumes, including dev data.
tofu -chdir=infra/incus apply destroy.tfplan
```

Restore the guard to `true` before creating another deployment. The host VM and
shared storage pool remain. This is an operator safeguard, not an authorization
boundary: removing resource blocks/state or using Incus directly can bypass it.
[OpenTofu lifecycle behavior](https://opentofu.org/docs/language/meta-arguments/lifecycle/).
