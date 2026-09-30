# Persistent workspace and agent home

Fresh deployments keep dev data on two Incus volumes, mounted only in `workspace`:

| Volume | Mount | Default quota | Ownership |
| --- | --- | --- | --- |
| `workspace-data` | `/workspace` | 10 GiB (`workspace_gib`) | `agent:agent`, directory `0750` |
| `agent-home` | `/home/agent` | 2 GiB (`agent_home_gib`) | `agent:agent`, directory `0700` |

These volumes survive restart and replacement. Both agents share the home;
existing settings are preserved. Stored sessions do not prove native resume across
versions. Host homes/memory are not imported automatically.

Files outside these mounts—including standalone broker history—are lost on root-disk
replacement. [Control mode](secured-runtime.md) keeps broker history and budgets on
its private volume. Optional [host mounts](sandbox-mounts.md) store data on the host,
not in `workspace-data`.

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

Check restored files, permissions and agent settings before discarding backups; hooks
and credentials are not validated. [Incus volume snapshots/exports](https://linuxcontainers.org/incus/docs/main/howto/storage_backup_volume/)
are another backup option. Persistence alone is not a backup.

## Replace dev, retain data

Run on the **host**, from the repository root. Exit agents and [back up](#back-up-and-restore)
first. Projects and agent home survive; processes and tmux sessions do not.
Standalone broker history is lost on replacement; control-mode history persists.

For an upgrade, [download the new image](sandbox-image.md#download-a-built-image)
first. In the same host shell (Linux: use `local:`):

```sh
incus --project collab-ai stop colima-collab-ai:workspace
incus --project collab-ai stop colima-collab-ai:secured  # only in control mode

python3 scripts/sandbox-provision.py plan --image-dir "dist/workspace-$run_id" --replace
# Review the plan; persistent data volumes must be retained
python3 scripts/sandbox-provision.py apply
incus --project collab-ai list colima-collab-ai:

# Only after successful replacement: renew SSH host-key trust
mv infra/incus/ssh/known_hosts infra/incus/ssh/known_hosts.before-upgrade
python3 scripts/sandbox-ssh.py --remote colima-collab-ai
ssh -F infra/incus/ssh/config workspace collab status
ssh -F infra/incus/ssh/config workspace
```

The helper remembers the image and state directory; no `cd` or `deployment_dir`
variable is needed. Omit `--image-dir` to reuse the selected image.
[State and interrupted runs](sandbox-operator.md).

Both containers must be stopped for a control-mode image update. With `running = true`,
apply starts them again. Your private login key stays on the host. For custom SSH
state, adjust the paths and pass `--state-dir` to the helper. **Do not destroy to upgrade.**

## Deliberate removal

`prevent_destroy = true` in [storage.tf](../infra/incus/storage.tf) blocks plans
that would delete or replace the dev volumes, including a full destroy. Changing
the project or storage pool must not silently replace them.

After backing up and deciding to delete the data, change that literal to `false`
in the deployment directory printed by the helper. Then review and apply a destroy plan:

```sh
python3 scripts/sandbox-provision.py plan --destroy
# This deletes the containers and their managed volumes, including dev data.
python3 scripts/sandbox-provision.py apply
```

Restore the guard to `true` before creating another deployment. The host VM and
shared storage pool remain. This is an operator safeguard, not an authorization
boundary: removing resource blocks/state or using Incus directly can bypass it.
[OpenTofu lifecycle behavior](https://opentofu.org/docs/language/meta-arguments/lifecycle/).
