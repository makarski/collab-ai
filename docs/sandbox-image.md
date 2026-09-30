# Workspace image

The [sandbox guide](sandbox.md) covers download, provisioning and SSH. This page
describes development builds and CI releases.

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

In standalone mode, the `agent` account owns `/workspace` and the broker's database at
`/var/lib/collab-ai/inboxes.db`. The broker starts at boot and uses
`/tmp/collab-ai.sock`. SSH host keys are generated per workspace during SSH setup,
never baked into the image. The SSH server runs only through Incus, per connection.
Agent credentials, host files and private SSH keys are never copied into the build.
Claude updates are disabled so its installed version stays fixed.
`/workspace` and `/home/agent` use separate persistent volumes. First boot sets
agent ownership and seeds missing shell defaults without overwriting stored files.

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
For a new deployment, copy the variables file to `infra/incus/image.auto.tfvars.json`.
For an existing container, follow [upgrade or reprovision](sandbox-storage.md#replace-dev-retain-data)
to keep the deployment state and persistent data.

Provisioning is pinned to the finished image checksum. Rebuilding is not promised
to be bit-for-bit identical: Ubuntu packages resolve at build time and image
metadata includes timestamps. Exact OS package versions are recorded inside the
image. The source manifest records file hashes, including tracked local edits.

## CI and releases

[CI](../.github/workflows/ci.yml) runs Go, Python and OpenTofu checks, then builds
native images on separate AMD64 and ARM64 GitHub-hosted Ubuntu runners. Each image
is downloaded through the same installer and tested in an offline Incus workspace:
boot, unchanged plan, SSH as `agent`, tool versions, broker readiness and restart
persistence, dev replacement with retained data, SSH re-pinning and deletion protection. No credentials or model requests are involved.
The smoke test also enables dev networking, checks DHCP and access to the bridge's
DNS service, then removes the NIC with the offline opt-out. Control stays offline.

PRs and main builds produce downloadable image bundles (GitHub calls these
**artifacts**), retained for seven days. Each contains a prebuilt Linux container
image (`.tar.gz`) and its version/checksum manifest (`.json`). No compilation is needed.

### Download a built image

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
[new provisioning](sandbox.md#3-preview-and-apply) or [upgrade](sandbox-storage.md#replace-dev-retain-data),
using the generated `image.tfvars.json` for the selected run.

### Publish a release

**Run workflow** accepts an app ref and exact tool versions; these development runs
do not publish releases.

Maintainers publish by pushing a `workspace-vX.Y.Z` tag at the reviewed commit.
Only after checks and both image tests pass does CI create a GitHub Release with
the two native images and checksum manifests. Release builds use that commit and
its committed tool pins; manual overrides never enter this publishing path.
An existing release is not overwritten. No release is published by opening a PR.

Normal setup uses `sandbox-download.py --release TAG`, then imports through
Terraform/OpenTofu. Keep the selected artifact for repeatable provisioning.
[GitHub Actions artifacts](https://docs.github.com/en/actions/tutorials/store-and-share-data),
[Incus image import](https://linuxcontainers.org/incus/docs/main/howto/images_copy/).
