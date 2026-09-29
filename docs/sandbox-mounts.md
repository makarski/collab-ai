# Mount a host project

Host sharing is opt-in and **read-only by default**. macOS supports read-only
sharing; edit inside the persistent `/workspace` volume. On native Linux, writable
sharing requires a dedicated account with access to the selected directory.
The sandbox never maps your operator account through this helper.

## Where agents can edit

| Project location | What agents can do |
| --- | --- |
| Sandbox-owned working copy in persistent `/workspace` storage | Edit code and write build output inside the sandbox. |
| Host mount with `container_readonly: true` (the default) | Read and analyze files; cannot edit code or write build output into that mount. |
| Host mount with `container_readonly: false` | Edit host files directly, subject to host permissions. Supported on native Linux with a dedicated sharing identity. |

**For coding on macOS, use a sandbox-owned working copy.** Clone the project inside
the sandbox, or copy the needed files from a read-only mount into a separate,
unmounted directory such as `/workspace/project-work`. Changes in that copy do
not automatically sync back to the host; return them through Git or a patch.
A path under `/workspace` can still be a read-only host mount—its location alone
does not make it writable.

## Choose directories

Create `mounts.json` outside the repository: a list of projects and their host-to-container mounts.

```json
[
  {
    "project_name": "project",
    "host_path": "/absolute/path/to/your/project",
    "container_mount_path": "/workspace/project"
  },
  {
    "project_name": "reference",
    "host_path": "/absolute/path/to/reference",
    "container_mount_path": "/workspace/reference",
    "container_readonly": true
  }
]
```

`project_name` identifies the project; `host_path` is its existing directory on
your machine and `container_mount_path` is where it appears inside the sandbox.
Project names must be unique, use lowercase letters, digits and hyphens,
start with a letter, and have at most 30 characters.

`container_readonly: true` prevents writes through this mount from inside the
container; it does not make the host directory read-only for you. It defaults to
`true`. Container mount paths must be directly under `/workspace` and unique.
Sources cannot overlap; choose individual directories, not `/` or your whole home.
The helper resolves symlinks and previews the actual paths without applying changes:

```sh
python3 scripts/sandbox-host.py mounts-plan --mounts-file /path/to/mounts.json
```

Host files keep their ownership and permissions. Without an explicit sharing
identity, the sandbox needs existing read/traverse permission; private host files
stay private. Mounted data uses host storage and is outside the workspace quota.

## Linux writable sharing

For a mount you want to edit, set `"container_readonly": false`. Create a dedicated account
once (Ubuntu/Debian example):

```sh
sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin collab-share
```

It must have a unique UID, a private same-name group, no supplementary groups,
and no login shell. Do not grant it sudo or other administrative privileges.
The helper checks account/group membership; it does not audit sudoers or host services.

Grant access only to your selected directory, using ACLs instead of changing ownership:

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

Install your distribution's `acl` package if `setfacl` is unavailable. Private parent
directories may also need traverse-only permission for `collab-share`; grant it only
on the required ancestors. Keep the existing `/etc/subuid` and `/etc/subgid` ranges.
See [Incus ID mappings](https://linuxcontainers.org/incus/docs/main/userns-idmap/).
The selected account maps to dev's `agent` (1001:1001); other container IDs stay isolated.

**Mounts do not prevent execution.** Incus did not preserve `noexec` when tested
with its bind mounts, so this setup does not claim that protection. Even `noexec`
would not stop an interpreter reading a script or later execution on the host.
Writable sharing allows changes to host files, including scripts and build
configuration. Use sandbox-owned storage when you do not want that exposure.
[Execution restrictions are tracked in #51](https://github.com/makarski/collab-ai/issues/51).

## Apply

On **macOS**, stop the dedicated VM before changing its read-only mounts:

```sh
colima stop collab-ai
python3 scripts/sandbox-host.py mounts-apply --mounts-file /path/to/mounts.json
python3 scripts/sandbox-host.py apply
```

This configures Mac → Colima and Colima → Incus sharing. It only changes the
managed `collab-ai` profile. Ordinary starts preserve configured mounts.
Writable mounts and `--share-user` are rejected on macOS.

On **Linux**, generate the Incus variables directly:

```sh
# Read-only; add --share-user collab-share if access needs the dedicated identity.
python3 scripts/sandbox-host.py mounts-apply --mounts-file /path/to/mounts.json

# For a writable manifest, use the prepared dedicated account instead:
# python3 scripts/sandbox-host.py mounts-apply --mounts-file /path/to/mounts.json --share-user collab-share
```

On **both platforms**, apply the generated `infra/incus/mounts.auto.tfvars.json`
from the same directory as your existing deployment state:

```sh
tofu -chdir=infra/incus plan -out=sandbox.tfplan
tofu -chdir=infra/incus apply sandbox.tfplan
```

Use `--output /path/to/deployment/mounts.auto.tfvars.json` if your state lives
elsewhere, and adjust `-chdir`. An existing workspace needs a stop/start when its
UID mapping changes. Then [SSH into dev](sandbox.md#4-ssh-into-the-workspace)
and `cd /workspace/project`.

## Change or remove a mount

Incus validates existing profiles before restricting allowed paths. Before
removing or replacing a source, stop dev and detach that device. For `project`
above (Linux: replace `colima-collab-ai:` with `local:`):

```sh
incus --project collab-ai stop colima-collab-ai:workspace
incus --project collab-ai profile device remove colima-collab-ai:offline host-project
```

When removing the last mount or changing identity, clear the old mapping too;
for an identity change, detach all host devices first:

```sh
incus --project collab-ai profile unset colima-collab-ai:offline raw.idmap
```

Edit the manifest and repeat **Apply**. Use `[]` to remove all mounts. Keep the
empty generated variables file so the next plan explicitly selects no mounts.
Removal does not delete host data or revoke host ACLs you granted separately.

If access fails, check `id` inside dev, host permissions, and the UID/GID seen by
the Incus server. Do not use recursive `chown` on your host project.
