# Mount a host project

## Where agents can edit

- **Sandbox copy:** agents can code; changes return through Git or a patch, not automatic host sync.
- **Host mount:** read-only by default. Linux permits writes with a dedicated sharing account; macOS does not.

For coding, clone or copy into an unmounted directory such as `/workspace/project-work`.

## Choose directories

Create `mounts.json` outside the repository:

```json
[
  {
    "project_name": "project",
    "host_path": "/absolute/path/to/your/project",
    "container_mount_path": "/workspace/project",
    "container_readonly": true
  }
]
```

`container_readonly` defaults to `true`: agents cannot edit or write build output
into the mount. Your host access stays unchanged.

Use existing, non-overlapping host directories and unique container paths directly
under `/workspace`. Project names must be unique: 1–30 lowercase letters, digits
or hyphens, starting with a letter. Host permissions still apply.

```sh
python3 scripts/sandbox-host.py mounts-plan --mounts-file /path/to/mounts.json
```

## Linux writable sharing

Set `"container_readonly": false` and create a dedicated account (Ubuntu/Debian):

```sh
sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin collab-share
```

Use a unique UID, private same-name group, no supplementary groups, login shell
or sudo. Grant access only to the chosen directory:

```sh
share_dir=/absolute/path/to/your/project
sudo setfacl -R -m u:collab-share:rwX "$share_dir"
# Inherit access for the sharing account and your operator on new files/directories.
sudo find "$share_dir" -type d -exec setfacl -m "d:u:collab-share:rwx,d:u:$(id -u):rwx" {} +

# Allow Incus to map only this dedicated UID/GID (skip IDs already allocated).
share_uid=$(id -u collab-share)
share_gid=$(id -g collab-share)
sudo usermod --add-subuids "$share_uid-$share_uid" --add-subgids "$share_gid-$share_gid" root

python3 scripts/sandbox-host.py mounts-plan --mounts-file /path/to/mounts.json --share-user collab-share
```

Install `acl` if needed. Private parent directories may need traverse permission
for `collab-share`. Preserve existing UID/GID mappings and host file ownership.

Mounts do **not** prevent execution. Review host changes before running them.
[Security limits](sandbox.md#security-can-agents-execute-code-on-my-host).

## Apply

On **macOS**, stop the dedicated VM before changing its read-only mounts:

```sh
colima stop collab-ai
python3 scripts/sandbox-host.py mounts-apply --mounts-file /path/to/mounts.json
python3 scripts/sandbox-host.py apply
```

On **Linux**, generate the Incus variables directly:

```sh
python3 scripts/sandbox-host.py mounts-apply --mounts-file /path/to/mounts.json

# Add --share-user collab-share for writable mounts or access via that account.
```

On **both platforms**, apply the generated `infra/incus/mounts.auto.tfvars.json`
from the same directory as your existing deployment state:

```sh
tofu -chdir=infra/incus plan -out=sandbox.tfplan
tofu -chdir=infra/incus apply sandbox.tfplan
```

For state stored elsewhere, set `--output /path/to/deployment/mounts.auto.tfvars.json`
and adjust `-chdir`. Restart dev after UID mapping changes.

## Change or remove a mount

Stop dev and detach the changed mount first (Linux: use `local:`):

```sh
incus --project collab-ai stop colima-collab-ai:workspace
incus --project collab-ai profile device remove colima-collab-ai:offline host-project
```

When removing the last mount, also clear its identity mapping. For an identity
change, detach all host mounts before clearing it:

```sh
incus --project collab-ai profile unset colima-collab-ai:offline raw.idmap
```

Edit the list and repeat **Apply**. Use `[]` to remove all mounts; keep the empty
generated variables file. Host data and separately granted ACLs remain unchanged.
