# Workspace images

A downloadable image bundle (GitHub calls it an **artifact**) contains the prebuilt
Linux container (`.tar.gz`) and its version/checksum manifest (`.json`).
No local compilation is needed. CI downloads expire after seven days.

## Download a built image

**Needs:** Python 3.9+, a running Incus host, and a GitHub account signed in in your
browser. **GitHub CLI (`gh`) is optional.** Run commands from this repository on your host.

1. Open [successful main builds](https://github.com/makarski/collab-ai/actions/workflows/ci.yml?query=branch%3Amain+is%3Asuccess) and select the newest completed build.
2. Under **Artifacts**, download **workspace-linux-arm64** for Apple Silicon/ARM64,
   or **workspace-linux-amd64** for Intel/AMD Linux. Keep the ZIP as downloaded.
3. Prepare it (use the actual filename if your browser added a suffix):

```sh
python3 scripts/sandbox-download.py --from-zip ~/Downloads/workspace-linux-arm64.zip
```

Mac selects `colima-collab-ai`; Linux selects `local`. The helper checks the server's
architecture and image checksum. It keeps each image separately under `dist/images/`
and updates `dist/images/current` only after verification. Keep that directory.
Repeating the command is safe; previously selected image files are not overwritten.

Continue with [first setup](sandbox.md#3-preview-and-apply) or
[upgrade](sandbox-storage.md#replace-dev-retain-data). Both use the same path:
`--image-dir dist/images/current`. No build ID or shell variable is needed.

CI downloads expire after seven days and require [GitHub sign-in](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/download-workflow-artifacts).
If there is no downloadable image, select another successful build or use a development build below.

<details>
<summary>Other download methods</summary>

- Already extracted a bundle? Use `--from-dir /path/to/extracted-bundle` instead of `--from-zip`.
- Published version available? Use `--release workspace-vX.Y.Z`; public releases need no GitHub CLI or sign-in.
- Prefer `gh`? [Install GitHub CLI](https://cli.github.com/) and run `gh auth login` first. Its download command extracts the bundle; then use `--from-dir`.
- `--remote NAME` overrides the Incus remote. `--output NEW_DIRECTORY` bypasses the default image cache.

</details>

## What is installed

| Component | Installation |
| --- | --- |
| `broker`, `collab-codex`, `collab-mcp`, `collab` | Compiled from the selected app source inside the image; Go tests run first |
| Codex 0.156.1, Claude Code 2.1.283 | Official native distributions, verified against committed SHA256 checksums |
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
Pass its output directory with `--image-dir` for [initial setup](sandbox.md#2-download-the-workspace-image)
or [upgrade](sandbox-storage.md#replace-dev-retain-data). Keep the exported image;
upstream may prune pinned base images. Rebuilds are not bit-identical: OS packages
resolve at build time and metadata includes timestamps. OS versions are recorded.

## CI and releases

[CI](../.github/workflows/ci.yml) runs Go/Python/OpenTofu checks and native ARM64/AMD64
image tests: provisioning, SSH, installed tools, broker, persistence, replacement,
network opt-out and control boundaries. Tests use no credentials or model requests.

**Run workflow** accepts source/tool overrides without publishing a release.
Maintainers publish by tagging a reviewed commit `workspace-vX.Y.Z`; checks and both
image tests must pass. Releases use committed pins, contain both architectures,
and never overwrite an existing release.

For a [published release](https://github.com/makarski/collab-ai/releases), use
`sandbox-download.py --release TAG` instead of the browser ZIP above. Keep the verified image for later provisioning.
