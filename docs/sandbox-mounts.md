# Mount a host project

Share selected directories with the sandbox's `agent` user. Mounts are **read-only
by default**; `readonly: false` lets agents edit real host files. Nothing is shared
until you opt in. Networking and SSH-agent forwarding stay disabled.

## Choose directories

Create a local `mounts.json` outside the repository, using existing absolute paths:

```json
{
  "project": {
    "source": "/absolute/path/to/your/project",
    "path": "/workspace/project",
    "readonly": false
  },
  "reference": {
    "source": "/absolute/path/to/reference",
    "path": "/workspace/reference"
  }
}
```

Destinations must be directly under `/workspace`. Sources cannot overlap; choose
individual project directories rather than `/` or your whole home. The helper
resolves symlinks and displays the actual shared paths before writing configuration.

```sh
python3 scripts/sandbox-host.py mounts-plan --mounts-file /path/to/mounts.json
```

The generated UID/GID mapping gives `agent` (1001:1001 in our images) your access
to the selected files, without changing their ownership. Run the helper as your
normal user. Override `--uid` and `--gid` only when the directory's Incus-host
identity differs; host root cannot be mapped. Existing file permissions still apply.
Mounted files use host storage and are outside the container's root-disk quota.

## Apply

On **macOS**, stop the dedicated VM before changing its mounts:

```sh
colima stop collab-ai
python3 scripts/sandbox-host.py mounts-apply --mounts-file /path/to/mounts.json
python3 scripts/sandbox-host.py apply
```

This configures both Mac → Colima and Colima → Incus sharing. It only changes the
managed `collab-ai` profile; it does not restart a VM automatically. Keep the mount
manifest for future changes. Ordinary host starts preserve the configured mounts.

On **Linux**, generate the Incus variables directly:

```sh
python3 scripts/sandbox-host.py mounts-apply --mounts-file /path/to/mounts.json
```

On Linux hosts using `/etc/subuid` and `/etc/subgid` (including Ubuntu), an
administrator must also allow Incus to map the selected host IDs. From your normal
user's shell, grant **only your UID and GID** once, keeping the existing ranges:

```sh
sudo usermod --add-subuids "$(id -u)-$(id -u)" --add-subgids "$(id -g)-$(id -g)" root
```

If you supplied `--uid`/`--gid`, substitute those IDs instead. Check the existing
`root` allocations first and omit IDs already covered. This is separate from the
project's allowlist; without it, startup can fail with `newuidmap ... not allowed`.
See [Incus ID mappings](https://linuxcontainers.org/incus/docs/main/userns-idmap/).

On **both platforms**, apply the generated `infra/incus/mounts.auto.tfvars.json`
with your existing workspace configuration:

```sh
tofu -chdir=infra/incus plan -out=sandbox.tfplan
tofu -chdir=infra/incus apply sandbox.tfplan
```

An existing workspace may need a stop/start for a changed UID mapping. Open your
[SSH terminal](sandbox.md#4-ssh-into-the-workspace) and `cd /workspace/project`.

## Change or remove a mount

Incus checks existing profiles when restricting allowed paths. **Before removing
or replacing a source**, stop the workspace and detach that device from the
profile. For the `project` entry above (Linux: replace `colima-collab-ai:` with `local:`):

```sh
incus --project collab-ai stop colima-collab-ai:workspace
incus --project collab-ai profile device remove colima-collab-ai:offline host-project
```

When removing the last mount or changing UID/GID, also clear the old mapping;
for an identity change, detach all host devices first:

```sh
incus --project collab-ai profile unset colima-collab-ai:offline raw.idmap
```

Edit the manifest and repeat **Apply** above. Use `{}` to remove every mount.
OpenTofu refreshes the profile changes before applying the new policy. Removal
does not delete host data. Keep the empty generated variables file so a subsequent
plan explicitly restores the no-mount policy.

If reads or writes fail, check `id` inside the sandbox, source permissions, and the
UID/GID seen by the Incus server. Do not use recursive `chown` on your host project.
The project allows only the selected disk sources and mapped host IDs; the
container remains unprivileged with isolated mappings for its other IDs.
