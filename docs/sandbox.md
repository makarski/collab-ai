# Set up an Incus sandbox

**Host → download image → provision → SSH → sign in.** Run host commands from
this repository. Already provisioned? Use the [daily quick start](../README.md#set-up-a-sandbox).

The image includes collab-ai, Codex, Claude Code, Go, Git, tmux, Starship and RTK.
Defaults: 2 CPUs, 4 GiB RAM, 10 GiB root disk, 10 GiB projects, 2 GiB agent home.
Projects and agent home persist; tools live on the replaceable root disk.

## 1. Install the host tools

**macOS** — Homebrew required:

```sh
brew install colima incus python git
python3 scripts/sandbox-host.py plan
python3 scripts/sandbox-host.py apply
```

This starts the dedicated `collab-ai` Colima VM running Incus. Existing Docker
profiles stay separate. Starting the VM does **not** create the workspace.

**Linux** — [install Incus](https://linuxcontainers.org/incus/docs/main/installing/),
Python 3.9+, Git and OpenSSH. Use an existing
quota-capable pool (ZFS/Btrfs), a managed network bridge and Incus access.
Skip Colima; replace `colima-collab-ai:` with `local:` throughout.
Do not reinitialize an existing server. Mac requires Colima 0.10.3+.

## 2. Download the workspace image

Follow [Download a built image](sandbox-image.md#download-a-built-image) on the host.
Download the ZIP in your browser, then run the Python helper. GitHub CLI is optional.
The verified image is available at `dist/images/current`.
For an existing deployment, use [upgrade](sandbox-storage.md#replace-dev-retain-data).

## 3. Preview and apply

From the repository root on your host:

```sh
python3 scripts/sandbox-provision.py plan --image-dir dist/images/current
# Review the printed plan, then:
python3 scripts/sandbox-provision.py apply
```

No host Terraform/OpenTofu installation is needed. The helper runs pinned OpenTofu
in a disposable operator container. **New deployments default to dev plus offline
control.** Use `--storage-pool` / `--network` for a non-default Incus pool/bridge.

State, backup, settings and saved plans stay on the host, normally under
`~/.local/state/collab-ai/operator/collab-ai/`. The command prints the actual path
and remembers it. Existing repository/worktree state is discovered; multiple matches
require one explicit `--state-dir` selection. It never adopts an existing Incus
project using empty state. Host mounts that expose operator state are rejected.

Edit the printed directory's `operator.auto.tfvars.json` for settings such as
`dev_network_enabled`, `running`, or `secured_runtime`, then plan/apply again.
Existing deployments keep their existing variable files. Both containers must be
stopped before control-mode updates. [Provisioning and recovery](sandbox-operator.md).

## 4. SSH into the workspace

On the host:

```sh
python3 scripts/sandbox-ssh.py --remote colima-collab-ai
ssh -F infra/incus/ssh/config workspace
```

You enter as `agent`, without sudo, in `/workspace`. The helper creates a dedicated
host login key and copies **only its public key**. It never forwards your SSH agent.
SSH uses `incus exec`, works offline and opens no network SSH listener.

[Sign in below](#sign-in-and-network-access), then use the
[README commands](../README.md#set-up-a-sandbox) to clone, start/resume and reconnect.

| Inside an interactive SSH/tmux shell | Behavior |
| --- | --- |
| `codex` | Managed terminal, ID `codex-1` |
| `claude` | Preconfigured channels, ID `claude-1` |
| `dashboard` | Same broker's read-only terminal dashboard |
| `command codex` / `command claude` | Native CLI for login/setup |

Use one session per ID; extra sessions need separate identities. Scripts use native
binaries, not these aliases. Ask both agents to read `/usr/local/share/collab-ai/SKILL.md`;
it is not automatically discovered. Copy only `SKILL.md` and `references/` when
installing that skill elsewhere.

Starship settings go in persistent `~/.config/starship.toml`. Older containers need
an [image update](sandbox-storage.md#replace-dev-retain-data).

**RTK reduces shell output** through Codex/Claude hooks, configured on first boot.
Approve the RTK hook if Codex prompts. `rtk gain` shows estimated output savings,
not provider billing or a token cap. History stays in `~/.local/share/rtk/` across
container replacement; the `codex` alias grants that directory write access.
Existing settings are preserved; telemetry is not enabled. To opt out, run
`rtk init -g --codex --uninstall` and `rtk init -g --uninstall --auto-patch`, then restart agents.

## Sign in and network access

Inside the container:

```sh
command codex login --device-auth
command codex login status
command claude auth login
```

Follow the CLI instructions in your host browser. Enable Codex device login in
your account/workspace if required. `localhost:1455` in your browser points to the
host, not the container; use [device login](https://learn.chatgpt.com/docs/auth#login-on-headless-devices).
Claude channels also require consent and account/organization support.
Credentials saved in `/home/agent` persist; host credentials are not imported.

**Dev is online by default; control has no NIC.** To disable dev networking, set
`dev_network_enabled = false` in the existing deployment variables and repeat
plan/apply. This interrupts network connections; SSH and the broker still work.
Set `true` and apply to restore access. For another project on the same server,
select a different bridge: instances named `workspace` cannot share its managed DNS.

## Status and recovery

Run on the host (Linux: `local:`):

```sh
incus --project collab-ai list colima-collab-ai:
```

Expect `workspace` (and optional `secured`) to be `RUNNING`; allow a few seconds
for the broker. **Keep the colon.** The remote is `colima-collab-ai:`, not `collab-ai:`.

| Problem | Action |
| --- | --- |
| Remote missing | Check `colima list`; rerun `sandbox-host.py apply` |
| Empty list / missing project | Check `incus project list colima-collab-ai:`; complete steps 2–3 |
| SSH host key changed | Follow the [replacement procedure](sandbox-storage.md#replace-dev-retain-data) |
| Need a recovery shell | `incus --project collab-ai exec colima-collab-ai:workspace -- /bin/sh` (root) |

## Stop or remove

On the host; these retain data:

```sh
incus --project collab-ai stop colima-collab-ai:workspace
incus --project collab-ai stop colima-collab-ai:secured  # only in control mode
colima stop collab-ai                                 # macOS only
```

Use the [daily block](../README.md#set-up-a-sandbox) to restart. Containers do not
autostart with the VM. Set `running = false` in deployment variables to keep them
stopped across applies; otherwise an apply may start them.
For deletion, follow [back up and remove](sandbox-storage.md#deliberate-removal).

## Incus web UI

On the host, with Incus running:

```sh
incus webui colima-collab-ai:  # Linux: incus webui local:
```

Open the printed URL, select project `collab-ai`, and keep the command running.
The URL contains a temporary login token; keep it private. Ctrl+C closes the UI
proxy without stopping containers. Use the provisioning helper for managed changes to avoid drift.

If `webui` is unknown, update the Incus client (`brew upgrade incus` on Mac).
Missing UI assets belong on the **Incus server**, not in the workspace.
On Debian/Ubuntu with the [Zabbly repository](https://github.com/zabbly/incus#other-packages):

```sh
# Mac: install in the dedicated VM only if missing
colima ssh --profile collab-ai -- sudo apt-get update
colima ssh --profile collab-ai -- sudo apt-get install incus-ui-canonical
# Linux: run apt-get update/install incus-ui-canonical on the Incus server
```

For other distributions, use their Incus UI package instructions.
[Agent dashboard](dashboard.md) is a separate terminal UI.

## Security: can agents execute code on my host?

Commands run inside the container, including scripts from host mounts. No host
shell, sudo or SSH-agent forwarding is provided. Writable mounts allow changes
that **you or host automation may later execute**; review those changes.

Dev has general network access, including reachable host/LAN services—not a provider
allowlist. No port forwards are added; NAT is not an ingress firewall. Keep host
credentials and administrative sockets out. Containers share the Linux server's
kernel (Colima's VM on Mac), so isolation is not absolute.
[Incus security](https://linuxcontainers.org/incus/docs/main/explanation/security/).

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

Maintainer checks (CI supplies OpenTofu), after provider initialization:

```sh
python3 -m unittest discover -s scripts/tests -v
tofu -chdir=infra/incus fmt -check
tofu -chdir=infra/incus validate
tofu -chdir=infra/incus test
```

Live-tested on Apple Silicon with Colima 0.10.3 and Incus server 7.1/client 7.4:
image build/export, provisioning, unchanged second plan, unprivileged SSH,
host-key replacement detection, dev network enable/disable with offline control,
dedicated Linux sharing IDs, read-only denial, and restart persistence.
Mock tests pass with OpenTofu 1.12.6 and Terraform 1.15.4. The image workflow also
exercises provisioning and SSH on AMD64 and ARM64 Ubuntu runners.

</details>
