# Set up an Incus sandbox

**Two steps matter:** start an Incus host, then provision a workspace inside it.
The host bootstrap alone does not create a container.

This setup creates an **empty, offline Linux container** with 2 CPUs, 4 GiB memory,
10 GiB disk, and a 512-process limit. It has no NIC, host mounts, or credentials.
Agent installation and hard token caps are not implemented.

[Install](#1-install-the-host-tools) · [Provision](#2-select-and-pin-the-image) ·
[Status and shell](#4-inspect-and-use-the-workspace) · [Stop](#stop-or-remove) · [Web UI](#incus-web-ui)

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

Requirements: Colima 0.10.3+ and Python 3.9+ on Mac; OpenTofu or Terraform 1.9+
on either platform. Commands below use `tofu`; `terraform` works too.
The Incus provider is locked to 1.2.0. The host needs internet access for downloads.

## 2. Select and pin the image

```sh
# Apple Silicon / ARM64. For Intel or AMD, replace arm64 with amd64.
incus image info images:ubuntu/24.04/arm64
cp infra/incus/sandbox.tfvars.example infra/incus/sandbox.auto.tfvars
```

Edit the copied file:

| Setting | Value |
| --- | --- |
| `incus_socket` | Absolute socket path printed by the Mac bootstrap; usually `/var/lib/incus/unix.socket` on Linux |
| `image_fingerprint` | Full 64-character fingerprint from the image info above |
| `storage_pool` | Existing quota-capable pool; `default` for the Colima host |

Keep the fingerprint fixed for repeatable runs. It also pins the image architecture.

## 3. Preview and apply

```sh
tofu -chdir=infra/incus init
tofu -chdir=infra/incus validate
tofu -chdir=infra/incus plan -out=sandbox.tfplan
```

Review the plan: it creates a project, cached image, profile, and container. Then:

```sh
tofu -chdir=infra/incus apply sandbox.tfplan
```

An unchanged second plan should report no changes. Keep the ignored state and
variables files; use one state per deployment. Image changes may replace the
workspace and its disk, so review replacement/deletion actions before applying.

## 4. Inspect and use the workspace

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
| Open a shell | `incus --project collab-ai exec colima-collab-ai:workspace -- /bin/sh` | `incus --project collab-ai exec local:workspace -- /bin/sh` |

Expect `workspace` to be `RUNNING`. The shell is root inside an unprivileged
container; `exit` leaves it running. `ip -brief link` should show only loopback.

- **Remote missing on Mac:** check `colima list`, then rerun the host bootstrap.
  Colima removes its remote when the VM stops and recreates it on startup.
- **Empty container list:** check the project list first. An empty table can also
  mean the project is missing. Complete steps 2–3 to create it.
- **Workspace stopped:** start it below. Host and container power states are separate.

## Stop or remove

These commands retain the workspace's data:

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

The bootstrap does not open a UI. With the host running and UI assets installed:

| macOS | Linux |
| --- | --- |
| `incus webui colima-collab-ai:` | `incus webui local:` |

The command opens a temporary localhost URL. Keep its terminal running; Ctrl+C
closes the UI proxy without stopping your containers. Select project `collab-ai`.
No public HTTPS listener is required.

**“The server doesn't have a web UI installed”** means the Linux server needs UI
assets, such as `incus-ui-canonical` from [Zabbly](https://github.com/zabbly/incus#other-packages).
The Mac client does not install them. [Incus web UI command](https://linuxcontainers.org/incus/docs/main/reference/manpages/incus/webui/).

Use the UI to inspect; apply managed changes through Terraform/OpenTofu to avoid
drift. `collab dashboard` separately shows agent connections and inboxes.

<details>
<summary>Isolation, repeatability, and validation details</summary>

The Mac host uses [colima.json](../infra/incus/colima.json): 4 CPUs, 8 GiB RAM,
40 GiB disk, no host mounts or SSH-agent forwarding, and no default-context switch.
The script refuses foreign profiles and saved configuration drift. Inspect an
error before changing or deleting an existing profile.

The workspace uses an isolated UID mapping, blocks privileged/nested containers,
and disables the Incus guest API. It shares the Linux host kernel. Keep the host's
administrative socket outside the workspace.

The image is cached in the project. Upstream may remove older images; export the
cache for long-term rebuilds. Host packages and Colima's VM image are not pinned
OS builds. Storage backends may reject disk shrinking. Keep state local, serialize
applies, and do not use a second empty state for the same project. Project deletion
does not force-delete resources created outside this state.

Offline checks after provider initialization:

```sh
python3 -m unittest discover -s scripts/tests -v
tofu -chdir=infra/incus fmt -check
tofu -chdir=infra/incus validate
tofu -chdir=infra/incus test
```

Live-tested on Apple Silicon with Colima 0.10.3, Incus server 7.1/client 7.4,
OpenTofu 1.12.6 and Ubuntu 24.04: create, unchanged second plan, isolation and
resource settings, stop/start persistence, and teardown. Mock tests also pass
with Terraform 1.15.4. Native Linux and Intel Mac hosts have not been live-tested.

</details>
