# Set up an Incus sandbox

**Host → image → workspace → SSH.** Run these commands from the repository root.

This setup creates an **offline Linux workspace** with 2 CPUs, 4 GiB memory,
10 GiB disk, and a 512-process limit. By default it has no NIC, host mounts, or credentials.
It includes collab-ai, Codex, Claude Code, Go, Git, ripgrep and tmux. The broker
starts automatically. Model access remains disabled by the absence of networking
and credentials; provisioning does not yet configure subscription logins or model access.
To work on a local checkout, opt into [selected host directory mounts](sandbox-mounts.md).

[Install](#1-install-the-host-tools) · [Image](#2-download-the-workspace-image) ·
[SSH](#4-ssh-into-the-workspace) · [Agent dashboard](dashboard.md#open-the-dashboard) ·
[Stop](#stop-or-remove) · [Web UI](#incus-web-ui)

## 1. Install the host tools

**macOS:** install Homebrew, then run from the cloned collab-ai repository:

```sh
brew install colima incus opentofu python
python3 scripts/sandbox-host.py plan
python3 scripts/sandbox-host.py apply
```

`plan` previews; `apply` starts a dedicated `collab-ai` Colima VM. Incus on the Mac
is the client; its server runs inside that Linux VM. Repeat `apply` to restart it.
Existing Docker profiles remain separate.

**Linux:** [install and initialize Incus](https://linuxcontainers.org/incus/docs/main/installing/)
directly; skip Colima and the bootstrap script. Use an existing quota-capable
storage pool, such as ZFS or Btrfs, and a user with access to the Incus socket.
Do not reinitialize an existing server.

Requirements: Colima 0.10.3+ on Mac; Python 3.9+ and OpenTofu or Terraform 1.9+
on either platform. Commands below use `tofu`; `terraform` works too.
The Incus provider is locked to 1.2.0. The host needs internet access for downloads.

## 2. Download the workspace image

Choose a published tag from [workspace releases](https://github.com/makarski/collab-ai/releases).
Replace the example tag below with that version. Until the first release is published,
use a [development build or CI artifact](sandbox-image.md).

```sh
# macOS; on Linux replace colima-collab-ai with local.
python3 scripts/sandbox-download.py --remote colima-collab-ai --release workspace-v0.1.0
cp infra/incus/sandbox.tfvars.example infra/incus/sandbox.auto.tfvars
cp dist/installed-workspace/image.tfvars.json infra/incus/image.auto.tfvars.json
```

The downloader selects your Incus server's architecture and verifies the image's
SHA256. Nothing compiles locally. CI has already built and tested the image;
the finished workspace has no NIC. No Docker or host Go installation is needed.

Edit `sandbox.auto.tfvars`:

| Setting | Value |
| --- | --- |
| `incus_socket` | Absolute socket path printed by the Mac bootstrap; usually `/var/lib/incus/unix.socket` on Linux |
| `storage_pool` | Existing quota-capable pool; `default` for the Colima host |

The generated image variables pin the artifact's absolute path and SHA256.
Keep that artifact: provisioning verifies its checksum. For another version, use
a new `--output` directory. [Image contents and build details](sandbox-image.md).

## 3. Preview and apply

For separate dev and secured containers, add `secured_runtime = true` to
`sandbox.auto.tfvars`. Stop workspace and any existing secured container before
enabling or upgrading the layout; the apply restarts them. See [secured runtime setup](secured-runtime.md) for private
state, host-only budget commands and lifecycle instructions. It remains offline.

```sh
tofu -chdir=infra/incus init
tofu -chdir=infra/incus validate
tofu -chdir=infra/incus plan -out=sandbox.tfplan
```

Review the plan: it creates a project, cached image, profile, and container.
With `secured_runtime = true`, it also adds a secured container/profile and four
managed volumes. Then:

```sh
tofu -chdir=infra/incus apply sandbox.tfplan
```

An unchanged second plan should report no changes. Keep the ignored state and
variables files; use one state per deployment. Keep these files and this operator
checkout outside writable dev mounts. Image changes may replace the
workspace and its disk, so review replacement/deletion actions before applying.

## 4. SSH into the workspace

Configure access once, then use ordinary SSH in as many terminals as you need:

```sh
# macOS; on Linux use --remote local.
python3 scripts/sandbox-ssh.py --remote colima-collab-ai
ssh -F infra/incus/ssh/config workspace
```

You log in as **`agent`**, without sudo. Interactive shells start in `/workspace`.
SSH travels through `incus exec`; it needs no IP address, NIC or listening port.
Your host must already have Incus access and OpenSSH. The helper creates a dedicated
local key in the ignored `infra/incus/ssh/` directory, copies only its public key,
and pins the guest host key through Incus. It never forwards your SSH agent.

Inside the workspace:

```sh
collab status
collab dashboard
codex --version
claude --version
tmux new -A -s work
```

Both agent CLIs are installed, but model sessions cannot work offline. Once model
access is separately provided, their launch commands are:

```sh
collab-codex --agent-id codex-1 --terminal -- -C /workspace
claude --strict-mcp-config --mcp-config /etc/collab-ai/claude-mcp.json \
  --dangerously-load-development-channels server:collab
```

Open each in its own SSH terminal or tmux pane. `exit` closes a shell; tmux keeps
sessions alive across disconnects. No agents launch automatically. The collaboration
skill is available at `/usr/local/share/collab-ai/SKILL.md`.

If a replaced workspace has a new host key, the helper refuses it: verify the
replacement, remove `infra/incus/ssh/known_hosts`, then rerun the helper.
Setup replaces the single operator public key; multiple terminals share it.

## Status and recovery

| Name | macOS | Linux |
| --- | --- | --- |
| Host VM | `collab-ai` | Not needed |
| Incus remote (server) | `colima-collab-ai:` | `local:` |
| Incus project | `collab-ai` | `collab-ai` |
| Container | `workspace` | `workspace` |

**Keep the colon on the remote.** `collab-ai:` is not the Mac remote name.
`incus remote list` shows registered connections. No default-remote switch is needed.

Run the command for your platform:

| Action | macOS | Linux |
| --- | --- | --- |
| List projects | `incus project list colima-collab-ai:` | `incus project list local:` |
| List containers | `incus --project collab-ai list colima-collab-ai:` | `incus --project collab-ai list local:` |
| Recovery shell (root) | `incus --project collab-ai exec colima-collab-ai:workspace -- /bin/sh` | `incus --project collab-ai exec local:workspace -- /bin/sh` |

Expect `workspace` to be `RUNNING`. `ip -brief link` inside it should show only loopback.
After starting it, allow a few seconds for the broker to become ready.

- **Remote missing on Mac:** check `colima list`, then rerun the host bootstrap.
  Colima removes its remote when the VM stops and recreates it on startup.
- **Empty container list:** check the project list first. An empty table can also
  mean the project is missing. Complete steps 2–3 to create it.
- **Workspace stopped:** start it below. Host and container power states are separate.

## Stop or remove

These commands retain the workspace's data. With secured mode enabled,
[stop or start both runtimes](secured-runtime.md#status-stop-and-restart).

| Action | macOS | Linux |
| --- | --- | --- |
| Stop workspace | `incus --project collab-ai stop colima-collab-ai:workspace` | `incus --project collab-ai stop local:workspace` |
| Start workspace | `incus --project collab-ai start colima-collab-ai:workspace` | `incus --project collab-ai start local:workspace` |
| Stop dedicated host | `colima stop collab-ai` | Stop the workspace only |

On Mac, restart the host with `python3 scripts/sandbox-host.py apply`, then start
the workspace. Incus boot autostart is disabled.

**To keep the workspace stopped across applies**, set `running = false` in
`sandbox.auto.tfvars`, then repeat step 3. An apply with `running = true` starts
it again, even if you stopped it manually.

**To delete the workspace and its data**, review a destroy plan before applying:

```sh
tofu -chdir=infra/incus plan -destroy -out=destroy.tfplan
# Review first: the following command deletes the workspace root disk.
tofu -chdir=infra/incus apply destroy.tfplan
```

This retains the host VM and shared storage pool. Export any data you need first.

## Incus web UI

Run this **on your host**, with the Incus server running. On Mac, start it with
`python3 scripts/sandbox-host.py apply` if needed. The bootstrap does not open a browser.

| macOS | Linux |
| --- | --- |
| `incus webui colima-collab-ai:` | `incus webui local:` |

Open the exact localhost URL printed by the command if your browser does not
open automatically. Its port and login token are temporary; keep the URL private.
Keep the terminal running; Ctrl+C closes the UI proxy without stopping containers.
Select project **`collab-ai`**, then instance **`workspace`** to inspect its state,
resources, logs and console. If the project or instance is missing, complete
steps 2–3; starting Colima alone does not provision a workspace.

The proxy uses your existing Incus access. No public HTTPS listener, workspace NIC
or browser certificate setup is required for this local command.
[Incus web UI command](https://linuxcontainers.org/incus/docs/main/reference/manpages/incus/webui/).

**UI assets missing?** They belong on the Linux **server**, outside the workspace.
The tested Colima host already includes `incus-ui-canonical`; the Mac client alone
does not supply it. On Debian/Ubuntu servers using the
[Zabbly package repository](https://github.com/zabbly/incus#other-packages), install it with:

```sh
# macOS: install inside the dedicated Colima VM, only if missing.
colima ssh --profile collab-ai -- sudo apt-get update
colima ssh --profile collab-ai -- sudo apt-get install incus-ui-canonical

# Linux: run on the Incus server, only if missing.
sudo apt-get update
sudo apt-get install incus-ui-canonical
```

If the package cannot be found, follow your Incus distributor's UI installation
instructions; these commands assume its package repository is already configured.
If `webui` is an unknown command, update the host's Incus client to a release that
provides it. On Mac: `brew upgrade incus`.

Use the UI to inspect; apply managed changes through Terraform/OpenTofu to avoid
drift. For Codex and Claude connections, use the separate
[collaboration dashboard over SSH](dashboard.md#open-the-dashboard). Incus resource
limits do not enforce token caps.

<details>
<summary>Isolation, repeatability, and validation details</summary>

The Mac host uses [colima.json](../infra/incus/colima.json): 4 CPUs, 8 GiB RAM,
40 GiB disk, no host mounts by default, no SSH-agent forwarding, and no default-context switch.
The script refuses foreign profiles and saved configuration drift. Inspect an
error before changing or deleting an existing profile.

The workspace uses an isolated UID mapping, blocks privileged/nested containers,
and disables the Incus guest API. It shares the Linux host kernel. Keep the host's
administrative socket outside the workspace.

The image is cached in the project. Keep its exported artifact for reprovisioning.
Host packages and Colima's VM image are not pinned OS builds.
Storage backends may reject disk shrinking. Keep state local, serialize
applies, and do not use a second empty state for the same project. Project deletion
does not force-delete resources created outside this state.

Offline checks after provider initialization:

```sh
python3 -m unittest discover -s scripts/tests -v
tofu -chdir=infra/incus fmt -check
tofu -chdir=infra/incus validate
tofu -chdir=infra/incus test
```

Live-tested on Apple Silicon with Colima 0.10.3 and Incus server 7.1/client 7.4:
image build/export, provisioning, unchanged second plan, unprivileged SSH,
host-key replacement detection, offline networking, and restart persistence.
Mock tests pass with OpenTofu 1.12.6 and Terraform 1.15.4. The image workflow also
exercises provisioning and SSH on AMD64 and ARM64 Ubuntu runners.

</details>
