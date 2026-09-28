# Workspace image

The [sandbox guide](sandbox.md) covers download, provisioning and SSH. This page
describes development builds and CI releases.

## What is installed

| Component | Installation |
| --- | --- |
| `broker`, `collab-codex`, `collab-mcp`, `collab` | Compiled from the selected app source inside the image; Go tests run first |
| Codex 0.156.1, Claude Code 2.1.283 | Official native distributions, verified against committed SHA256 checksums |
| Go 1.25.14 | Official archive, checksum verified; available for workspace development |
| Git, ripgrep, tmux, OpenSSH, Python | Ubuntu 24.04 packages |
| Claude collaboration config | `/etc/collab-ai/claude-mcp.json` |
| Collaboration skill and build manifests | `/usr/local/share/collab-ai/` |

In standalone mode, the `agent` account owns `/workspace` and the broker's database at
`/var/lib/collab-ai/inboxes.db`. The broker starts at boot and uses
`/tmp/collab-ai.sock`. SSH host keys are generated per workspace during SSH setup,
never baked into the image. The SSH server runs only through Incus, per connection.
Agent credentials, host files and private SSH keys are never copied into the build.
Claude updates are disabled so its installed version stays fixed.

With [control mode](secured-runtime.md) enabled, the local workspace broker is
disabled. A dedicated `broker` user in control owns the database and shared socket;
`agent` joins `collab-clients` to connect through the read-only IPC mount.

## Development builds

```sh
# macOS; use --remote local on Linux.
python3 scripts/sandbox-image.py --remote colima-collab-ai \
  --collab-ref HEAD --codex-version 0.157.0 --output dist/workspace-dev
```

| Control | Selection |
| --- | --- |
| `--collab-ref TAG_OR_SHA` | Build a locally available commit/tag; fetch it first if needed |
| `--collab-ref working-tree` | Default: tracked local edits, excluding untracked files |
| `--codex-version`, `--claude-version`, `--go-version` | Exact versions; defaults come from the lock |
| `--base-image SHA256` | Ubuntu 24.04 fingerprint for the build server's architecture |
| `--lock-file FILE` | A custom complete lock file, including checksums |
| `--output DIRECTORY` | New directory for the build; existing directories are never overwritten |

Tool overrides resolve SHA256 checksums from official publisher metadata before
installing anything. Missing checksums or unsupported native packages stop the
build. Moving tool versions such as `latest` are rejected. The manifest records
the resolved app commit, local-edit status, file hashes, tool versions and checksums.
The selected app source uses the image recipe from your current checkout.

Use `--network NAME` or `--storage-pool NAME` for a non-default Incus host.
The builder uses a temporary project named `collab-build-*`, an unprivileged
container, 4 CPUs, 4 GiB RAM and a 12 GiB disk limit. It copies an allowlist of source
files, with no host bind mounts. Go dependencies are verified against `go.sum`.
No model requests are made. Cleanup removes only the project it created, including
on a build failure. If the process is forcibly killed, inspect and delete its
printed temporary project explicitly with `incus project delete REMOTE:PROJECT --force`
on current clients. With Incus 6.0 LTS, remove its instances and images first,
then delete the empty project without `--force`.

[tools.lock.json](../infra/image/tools.lock.json) pins Ubuntu image fingerprints
and tool archives for ARM64 and x86-64. Build on a host of the target architecture;
cross-compilation is not used. Upstream may prune old base images, so retain your
exported workspace artifact. Updating the lock is an explicit reviewed change.

The output contains `workspace.tar.gz`, its source/tool manifest,
`image.tfvars.json`, and architecture-specific files under `release/`.
Copy the variables file to `infra/incus/image.auto.tfvars.json`
and review a new Terraform/OpenTofu plan. **Replacing an existing workspace can
delete its disk.** Export work you need before applying a replacement.

Provisioning is pinned to the finished image checksum. Rebuilding is not promised
to be bit-for-bit identical: Ubuntu packages resolve at build time and image
metadata includes timestamps. Exact OS package versions are recorded inside the
image. The source manifest records file hashes, including tracked local edits.

## CI and releases

[CI](../.github/workflows/ci.yml) runs Go, Python and OpenTofu checks, then builds
native images on separate AMD64 and ARM64 GitHub-hosted Ubuntu runners. Each image
is downloaded through the same installer and tested in an offline Incus workspace:
boot, unchanged plan, SSH as `agent`, tool versions, broker readiness and restart
persistence. No credentials or model requests are involved.

PRs and main builds produce Actions artifacts retained for seven days. Once this
workflow is on main, **Run workflow** also accepts an app ref and exact tool versions.
These development runs do not publish releases. Download and extract the artifact
for your architecture, then install it locally:

```sh
python3 scripts/sandbox-download.py --remote colima-collab-ai \
  --from-dir /path/to/extracted-artifact --output dist/installed-workspace
```

Maintainers publish by pushing a `workspace-vX.Y.Z` tag at the reviewed commit.
Only after checks and both image tests pass does CI create a GitHub Release with
the two native images and checksum manifests. Release builds use that commit and
its committed tool pins; manual overrides never enter this publishing path.
An existing release is not overwritten. No release is published by opening a PR.

Normal setup uses `sandbox-download.py --release TAG`, then imports through
Terraform/OpenTofu. Keep the selected artifact for repeatable provisioning.
[GitHub Actions artifacts](https://docs.github.com/en/actions/tutorials/store-and-share-data),
[Incus image import](https://linuxcontainers.org/incus/docs/main/howto/images_copy/).
