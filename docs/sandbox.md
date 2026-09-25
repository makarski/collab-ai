# Provision a local Incus sandbox

This provisions an **offline Linux workspace** with Terraform or OpenTofu:
one dedicated Incus project, a cached image pinned by SHA256, an isolation
profile, and a persistent container. The default limits are 2 CPUs, 4 GiB memory,
10 GiB root disk, and 512 processes.

The container has no NIC, host-directory mounts, forwarded credentials, nested
containers, or Incus guest API. It does not install or launch coding agents.
Hard token budgets are not implemented; this offline environment cannot contact
model providers. The Incus host needs internet access to download images.

## 1. Install the host tools

On **macOS**, install Homebrew first if needed, then:

```sh
brew install colima incus opentofu python
```

On macOS, the Homebrew `incus` package installs the **client**. The Incus server
needs Linux: Colima supplies the Linux VM and runs the server inside it using
its [Incus runtime](https://colima.run/docs/runtimes/#incus). Both tools are
needed for this local macOS setup. Native Linux can run the server directly
without Colima.

Use Colima 0.10.3 or later and Python 3.9+. The bootstrap uses Apple's VZ driver.
OpenTofu 1.9+ or Terraform 1.9+ is required; commands below use `tofu`, which can
be replaced with `terraform`. The Incus provider is pinned to 1.2.0 with a
checked-in dependency lock file.

From the collab-ai repository:

```sh
python3 scripts/sandbox-host.py plan
python3 scripts/sandbox-host.py apply
```

`plan` only prints the host configuration and launch command. `apply` creates a
dedicated `collab-ai` Colima profile with 4 CPUs, 8 GiB memory and 40 GiB disk,
using [infra/incus/colima.json](../infra/incus/colima.json). It disables host mounts,
SSH-agent forwarding, SSH-config generation, and default-context switching.
Colima adds its named Incus remote but leaves your default remote unchanged.
Your existing Docker/Colima profiles are not modified.

Repeat `apply` to start/reuse the same profile. An unowned profile or changed
saved configuration is rejected rather than overwritten. Startup failures leave
the owned configuration available for retry. Inspect drift before reconciling it;
do not remove a profile containing data just to clear an error.

On **Linux**, skip the Colima script. Install and initialize Incus using the
[distribution instructions](https://linuxcontainers.org/incus/docs/main/installing/).
Use a supported Incus release and an existing storage pool that enforces volume
quotas (for example ZFS or Btrfs). Do not reinitialize an existing server. The
operator needs access to its Unix socket; keep that access outside the container.

## 2. Select and pin the image

Choose a container image matching the server architecture. For Apple Silicon:

```sh
incus image info images:ubuntu/24.04/arm64
cp infra/incus/sandbox.tfvars.example infra/incus/sandbox.auto.tfvars
```

For an Intel/x86_64 host, use `images:ubuntu/24.04/amd64`. In
`sandbox.auto.tfvars`, set:

- `incus_socket`: the absolute socket printed by the bootstrap (macOS), or your
  Linux server's socket, usually `/var/lib/incus/unix.socket`.
- `image_fingerprint`: the full 64-character `Fingerprint` from the image info.
  The fingerprint also pins the architecture; choose the host-matching image above.

Aliases and partial fingerprints are rejected by the configuration. Keep the
same fingerprint for repeat runs. The first apply caches that image in the
dedicated project. Upstream may eventually remove old images: export the cached
image if long-term rebuilds matter. Do not silently replace a missing pin with
the latest image.

This makes the resource configuration and container image repeatable. Host
packages and Colima's own VM image are not a bit-for-bit locked OS build.
Record `colima version`, `incus version`, and `tofu version` with live results.

## 3. Preview and apply

```sh
tofu -chdir=infra/incus init
tofu -chdir=infra/incus validate
tofu -chdir=infra/incus plan -out=sandbox.tfplan
tofu -chdir=infra/incus apply sandbox.tfplan
```

Read the plan before applying it. An initial plan creates four resources: project,
image, profile, and instance. A subsequent plan with unchanged inputs should report
no changes. Image changes can replace the instance and its disk;
check replacement actions before applying. Changes to CPU, memory, disk or power
state go through the same plan/apply workflow. Disk shrinking may be unsupported
by your storage backend; do not assume an in-place resize is reversible.

The local `terraform.tfstate`, saved plans and `sandbox.auto.tfvars` stay ignored
by Git. Keep the state and lock file: they identify managed resources. Use one
working directory/state per deployment and serialize operations through the
tool's state lock. Do not apply a second empty state against the same project,
or automatically import someone else's resources.

## 4. Inspect and use the workspace

The names refer to different layers:

| Layer | macOS | Native Linux |
| --- | --- | --- |
| Colima profile (host VM) | `collab-ai` | Not used |
| Incus remote (server connection) | `colima-collab-ai:` | `local:` |
| Incus project (resource namespace) | `--project collab-ai` | `--project collab-ai` |
| Container (inside that project) | `workspace` | `workspace` |

A remote needs its trailing colon. `collab-ai:` is not the macOS remote name;
`colima-collab-ai:` is. The project name does not create or select a remote.
Use `incus remote list` to see the server connections known to your client.
If you changed `project_name`, substitute that value below.

### macOS

```sh
# Start/reuse the dedicated VM and register its Incus remote (from the repo)
python3 scripts/sandbox-host.py apply

# Check the VM, registered server connections, and available projects
colima list
incus remote list
incus project list colima-collab-ai:

# After the Terraform/OpenTofu apply in step 3
incus --project collab-ai list colima-collab-ai:
incus --project collab-ai config show colima-collab-ai:workspace --expanded
incus --project collab-ai exec colima-collab-ai:workspace -- /bin/sh
```

`colima list` should show the host VM as `Running`; the Incus list should show
`workspace` as `RUNNING`. These are separate power states. Colima removes its
Incus remote when the profile stops and adds it again at startup, so a missing
`colima-collab-ai` remote can simply mean the VM is stopped.

### Native Linux

Run these as a user with access to the initialized local Incus server:

```sh
# Check the server connection and available projects
incus remote list
incus info local:
incus project list local:

# After the Terraform/OpenTofu apply in step 3
incus --project collab-ai list local:
incus --project collab-ai config show local:workspace --expanded
incus --project collab-ai exec local:workspace -- /bin/sh
```

On Linux, `local:` connects directly to the Linux server's Unix socket. There
is no Colima VM to start. On macOS, the default `local:` remote cannot run a
server natively; use the explicit `colima-collab-ai:` remote instead. Neither
set of commands requires changing the client's default remote.

If the **project is missing**, complete steps 2–3: starting the host alone does
not create `collab-ai` or `workspace`. If the **container is stopped**, set
`running = true` in your variables and plan/apply again. Type `exit` to leave
the container shell without stopping it.

Expect only a managed root disk and loopback networking. In the container shell,
`ip -brief link` shows its interfaces. The shell runs as root inside an
unprivileged container; this is not yet an agent-user installation. Container
isolation shares the Linux host kernel, rather than giving each agent its own VM.

### Incus web UI

The bootstrap does **not launch a web UI**. Incus supports an optional web
interface; when the server has its UI assets installed, open it with:

```sh
# macOS: start the collab-ai Colima profile first
incus webui colima-collab-ai:

# Native Linux
incus webui local:
```

Run only the command for your platform. `incus webui` prints a temporary
localhost URL and attempts to open your browser. Keep that terminal running;
Ctrl+C stops the local UI proxy, not the Incus server or containers. Select the
`collab-ai` project in the UI after provisioning it. This works through the
existing Unix-socket remote without enabling a public HTTPS listener.

If it reports **"The server doesn't have a web UI installed"**, the UI assets
are missing on the **Linux Incus server**. Install the appropriate UI package
there (for example `incus-ui-canonical` with the
[Zabbly packages](https://github.com/zabbly/incus#other-packages)); installing the
macOS client does not install server-side UI assets. UI availability depends on
the server image/distribution. See the
[web UI command](https://linuxcontainers.org/incus/docs/main/reference/manpages/incus/webui/)
and its [local proxy implementation](https://github.com/lxc/incus/blob/main/cmd/incus/webui_unix.go).

Use the UI to inspect the environment. Make managed configuration changes through
Terraform/OpenTofu so the next apply does not undo manual edits. The Incus UI
manages infrastructure; `collab dashboard` shows agent connections and inboxes.

## Stop or remove

To stop and retain the workspace, set `running = false` in your variables file,
then plan/apply. Set it back to `true` to start it. Incus boot autostart is disabled.
On macOS, `colima stop collab-ai` stops the dedicated host VM and retains its data.

To deliberately delete the managed workspace and its data:

```sh
tofu -chdir=infra/incus plan -destroy -out=destroy.tfplan
# Review the deletion plan first. Applying it removes the workspace root disk.
tofu -chdir=infra/incus apply destroy.tfplan
```

This does not delete the host VM or shared storage pool. Project deletion does
not force-delete resources added outside this state. Keep snapshots/exports
outside the resources being destroyed if their contents must survive teardown.

## Validation

Offline checks, with no VM or provider requests:

```sh
python3 -m unittest discover -s scripts/tests -v
tofu -chdir=infra/incus fmt -check
tofu -chdir=infra/incus validate
tofu -chdir=infra/incus test
```

The provider tests use mocks. They check the planned isolation and input
validation, not successful Incus execution. A live acceptance check must apply,
inspect the expanded config and network, and run a second plan to verify no
changes. Also verify persistence through stop/start and inspect a destroy plan
before using disposable data for teardown.

Live-tested on Apple Silicon with Colima 0.10.3, Incus server 7.1/client 7.4,
OpenTofu 1.12.6 and an Ubuntu 24.04 container: creation, an unchanged second plan,
loopback-only networking, isolated UID mapping, memory/process limits, disabled
guest API, retained data through stop/start, and teardown. Mock tests also pass
with Terraform 1.15.4. Native Linux and Intel Mac hosts have not been live-tested.

References: [Incus provider](https://github.com/lxc/terraform-provider-incus),
[project restrictions](https://linuxcontainers.org/incus/docs/main/reference/projects/),
[Colima Incus runtime](https://colima.run/docs/runtimes/#incus).
