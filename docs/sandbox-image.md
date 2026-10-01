# Workspace images

## Download a built image

Run on the host with Incus running:

```sh
# Download, verify, provision/upgrade and configure SSH
python3 scripts/sandbox-provision.py rollout
# Or pin a published version
# python3 scripts/sandbox-provision.py rollout --release workspace-vX.Y.Z
```

Defaults to `latest`; macOS uses `colima-collab-ai`, Linux uses `local`.
No GitHub CLI, login or manual download. The script selects the server architecture,
resolves one release tag, verifies its checksum, and imports the container archive.
[First setup](sandbox.md) · [Upgrade and retained data](sandbox-storage.md#replace-dev-retain-data).

<details>
<summary>Download only or use a local build</summary>

`sandbox-download.py --release latest` downloads without provisioning; explicit tags
also work. Verified images stay under `dist/images/`; `current` selects the latest
download without overwriting older images. Keep the cache for future provisioning.

For a local image, use `sandbox-provision.py rollout --image-dir dist/workspace-dev`.
The download helper also accepts extracted CI assets (`--from-dir`) or an existing
artifact ZIP (`--from-zip`). CI artifacts expire after seven days and require GitHub
sign-in; public releases do not. `--output NEW_DIRECTORY` selects an explicit cache location.

</details>

## What is installed

| Component | Installation |
| --- | --- |
| `broker`, `collab-codex`, `collab-mcp`, `collab` | Compiled from the selected app source inside the image; Go tests run first |
| Codex 0.159.3, Claude Code 2.1.283 | Official native distributions, verified against committed SHA256 checksums |
| Go 1.25.14 | Official archive, checksum verified; available for workspace development |
| RTK 0.50.0 | Checksum-pinned binary; Codex/Claude hooks and persistent usage history |
| Starship 1.26.0 | Checksum-pinned native release; enabled in interactive agent Bash shells |
| Git, ripgrep, tmux, OpenSSH, Python | Ubuntu 24.04 packages |
| Claude collaboration config | `/etc/collab-ai/claude-mcp.json` |
| Interactive shell aliases | `/etc/profile.d/collab-agent-aliases.sh`; `codex`, `claude` and `dashboard` |
| Collaboration skill and build manifests | `/usr/local/share/collab-ai/` |

No host files, credentials or private keys enter the image. SSH host keys are
generated per workspace; Claude auto-updates are disabled. The broker starts at
boot, in dev or [control](secured-runtime.md). [Persistent storage](sandbox-storage.md).

## Development builds

Build locally only when you need a custom source or tool version:

```sh
# macOS; Linux: --remote local. Defaults use tools.lock.json.
python3 scripts/sandbox-image.py --remote colima-collab-ai \
  --collab-ref HEAD --output dist/workspace-dev
```

| Control | Selection |
| --- | --- |
| `--collab-ref TAG_OR_SHA` | Build a locally available commit/tag; fetch it first if needed |
| `--collab-ref working-tree` | Default: tracked local edits, excluding untracked files |
| `--codex-version`, `--claude-version`, `--go-version` | Exact versions; defaults come from the lock |
| `--base-image SHA256` | Ubuntu 24.04 fingerprint for the build server's architecture |
| `--lock-file FILE` | A custom complete lock file, including checksums |
| `--output DIRECTORY` | New directory for the build; existing directories are never overwritten |

Overrides resolve publisher checksums; unsupported packages and moving versions
such as `latest` are rejected. The manifest records source commit, tracked edits,
recipe file hashes, tool versions and checksums. The recipe comes from your current
checkout, even when building another app commit. `--network` / `--storage-pool`
select non-default Incus resources.

Build on the target architecture. The builder uses an unprivileged temporary
`collab-build-*` project (4 CPUs, 4 GiB RAM, 12 GiB disk), an allowlist of source files,
no host mounts, and no model calls. It verifies Go dependencies and cleans up its
own project on failure. After a forced kill, inspect the printed project before
removing it; Incus 6.0 LTS requires deleting its instances/images before the project.

Output: `workspace.tar.gz`, manifest, `image.tfvars.json` and release files.
Pass its output directory with `--image-dir` for [initial setup](sandbox.md#2-install-or-upgrade)
or [upgrade](sandbox-storage.md#replace-dev-retain-data). Keep the exported image;
upstream may prune pinned base images. Rebuilds are not bit-identical: OS packages
resolve at build time and metadata includes timestamps. OS versions are recorded.

## CI and releases

[CI](../.github/workflows/ci.yml) runs Go/Python/OpenTofu checks and native ARM64/AMD64
image tests: provisioning, SSH, installed tools, broker, persistence, replacement,
network opt-out and control boundaries. Tests use no credentials or model requests.

Successful pushes to `main` publish both architectures under a unique
`workspace-v0.0.0-build.RUN.ATTEMPT` tag and make that release `latest`.
Explicit `workspace-vX.Y.Z` tags publish versioned releases too. Publication happens
only after all checks and both image builds pass; incomplete uploads stay in draft.
Published assets are never overwritten. `latest` becomes available after the first
successful main release build.

**Run workflow** accepts development source/tool overrides without publishing.
