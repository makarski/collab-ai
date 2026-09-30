# Workspace images

A downloadable image bundle (GitHub calls it an **artifact**) contains the prebuilt
Linux container (`.tar.gz`) and its version/checksum manifest (`.json`).
No local compilation is needed. CI downloads expire after seven days.

## Download a built image

Run on your **host**, from the repository root, with GitHub CLI (`gh`) installed
and signed in. Start the Incus host first. Select a successful CI run for the
commit you want; for a PR, replace `--branch main` with its branch name.

```sh
# List builds, then paste the chosen run's ID when prompted
gh run list --repo makarski/collab-ai --workflow CI --branch main --status success --limit 5
printf 'CI run ID: '
read -r run_id

# Apple Silicon / ARM64 server; use amd64 for an x86-64 server
gh run download "$run_id" --repo makarski/collab-ai \
  --name workspace-linux-arm64 --dir "dist/ci-image-$run_id"

# Verify and prepare the downloaded image; Linux: --remote local
python3 scripts/sandbox-download.py --remote colima-collab-ai \
  --from-dir "dist/ci-image-$run_id" --output "dist/workspace-$run_id"
```

Both directories are **on your host**:

- `dist/ci-image-RUN_ID`: files downloaded and extracted by `gh`; input to the verifier.
- `dist/workspace-RUN_ID`: verified image plus `image.tfvars.json`; input to provisioning.

The output directory must be new. Keep it: provisioning references the image there.
Downloading does not replace a container. Continue with
[new provisioning](sandbox.md#2-download-the-workspace-image) or [upgrade](sandbox-storage.md#replace-dev-retain-data),
using the generated `image.tfvars.json` for the selected run.

## What is installed

| Component | Installation |
| --- | --- |
| `broker`, `collab-codex`, `collab-mcp`, `collab` | Compiled from the selected app source inside the image; Go tests run first |
| Codex 0.156.1, Claude Code 2.1.283 | Official native distributions, verified against committed SHA256 checksums |
| Go 1.25.14 | Official archive, checksum verified; available for workspace development |
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
Use the variables for [initial setup](sandbox.md#2-download-the-workspace-image)
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
`sandbox-download.py --remote REMOTE --release TAG --output NEW_DIRECTORY` instead
of the CI download above. Keep the verified image for later provisioning.
