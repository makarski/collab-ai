# Workspace image

The [sandbox guide](sandbox.md) covers building, provisioning and SSH. This page
describes the artifact and how to rebuild it.

## What is installed

| Component | Installation |
| --- | --- |
| `broker`, `collab-codex`, `collab-mcp`, `collab` | Compiled from this checkout inside the image; Go tests run first |
| Codex 0.156.1, Claude Code 2.1.283 | Official native distributions, verified against committed SHA256 checksums |
| Go 1.25.14 | Official archive, checksum verified; available for workspace development |
| Git, ripgrep, tmux, OpenSSH, Python | Ubuntu 24.04 packages |
| Claude collaboration config | `/etc/collab-ai/claude-mcp.json` |
| Collaboration skill and build manifests | `/usr/local/share/collab-ai/` |

The `agent` account owns `/workspace` and the broker's persistent database at
`/var/lib/collab-ai/inboxes.db`. The broker starts at boot and uses
`/tmp/collab-ai.sock`. SSH host keys are generated per workspace during SSH setup,
never baked into the image. The SSH server runs only through Incus, per connection.
Agent credentials, host files and private SSH keys are never copied into the build.
Claude updates are disabled so its installed version stays fixed.

## Build or rebuild

```sh
# macOS; use --remote local on Linux.
python3 scripts/sandbox-image.py --remote colima-collab-ai --output dist/workspace-v2
```

Use `--network NAME` or `--storage-pool NAME` for a non-default Incus host.
The builder uses a temporary project named `collab-build-*`, an unprivileged
container, 4 CPUs, 4 GiB RAM and a 12 GiB disk limit. It copies an allowlist of source
files, with no host bind mounts. Go dependencies are verified against `go.sum`.
No model requests are made. Cleanup removes only the project it created, including
on a build failure. If the process is forcibly killed, inspect and delete its
printed temporary project explicitly with `incus project delete REMOTE:PROJECT --force`.

[tools.lock.json](../infra/image/tools.lock.json) pins Ubuntu image fingerprints
and tool archives for ARM64 and x86-64. Build on a host of the target architecture;
cross-compilation is not used. Upstream may prune old base images, so retain your
exported workspace artifact. Updating the lock is an explicit reviewed change.

The output contains `workspace.tar.gz`, its source/tool manifest, and
`image.tfvars.json`. Copy that variables file to `infra/incus/image.auto.tfvars.json`
and review a new Terraform/OpenTofu plan. **Replacing an existing workspace can
delete its disk.** Export work you need before applying a replacement.

Provisioning is pinned to the finished image checksum. Rebuilding is not promised
to be bit-for-bit identical: Ubuntu packages resolve at build time and image
metadata includes timestamps. Exact OS package versions are recorded inside the
image. The source manifest records file hashes, including tracked local edits.

## Distribution

This implementation produces a native Incus image; it does not publish packages.
To transfer it to another machine, copy the archive and manifest, check its SHA256,
and set `image_file` to its new absolute path while retaining `image_fingerprint`.
The provisioner imports it into the managed project.

GitHub Container Registry supports OCI images. A native Incus export needs either
an explicit download/import step or a separate OCI packaging path; uploading loose
binaries alone would not provision this workspace. The existing artifact gives us
a tested unit to distribute. [GHCR formats](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry),
[Incus image import](https://linuxcontainers.org/incus/docs/main/howto/images_copy/).
