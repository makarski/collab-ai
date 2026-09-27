# collab-ai

**Real-time AI collaboration in a sandbox, with visibility and control over token spend.**

Three goals:

- **AI collaboration in real time** — connect Codex and Claude Code with durable inboxes and automatic message delivery.
- **A sandboxed environment** — provision a reproducible, isolated Linux workspace with Incus.
- **Token spend visibility and soft cap enforcement** — track reported usage and stop managed agents when they reach your limit.

[MIT licensed](LICENSE).

[Agent setup](#run) · [Sandbox setup](#set-up-a-sandbox) ·
[Dashboards](#dashboards) · [Stop the sandbox](docs/sandbox.md#stop-or-remove) · [Architecture](#how-it-fits-together)

## Set up a sandbox

| macOS | Linux |
| --- | --- |
| ![macOS: Colima supplies a Linux VM containing the Incus project and offline workspace.](docs/assets/sandbox-macos.svg) | ![Linux: the Incus project and offline workspace run directly on the Linux host.](docs/assets/sandbox-linux.svg) |
| [PlantUML source](docs/assets/sandbox-macos.puml) | [PlantUML source](docs/assets/sandbox-linux.puml) |

**Included in the workspace image:** collab-ai, Codex, Claude Code, Go, Git and tmux.
The broker starts automatically; you open agent terminals over SSH. The workspace
stays offline; network access and subscription login are not provisioned yet.

On **macOS**, install the tools and start the dedicated host from this repository:

```sh
brew install colima incus opentofu python
python3 scripts/sandbox-host.py apply
```

Incus is the Mac client; Colima runs its Linux server. On **Linux**, install Incus
directly; Colima is unnecessary. **Starting the host does not create the workspace.**
Follow the [sandbox guide](docs/sandbox.md) to download a released image and provision it.
[Development builds](docs/sandbox-image.md#development-builds) let you choose app and tool versions.

Once provisioned, configure SSH once and open a terminal (Linux: use `--remote local`):

```sh
python3 scripts/sandbox-ssh.py --remote colima-collab-ai
ssh -F infra/incus/ssh/config workspace
```

Stop the dedicated Mac VM, keeping its data:

```sh
colima stop collab-ai
```

[SSH and terminals](docs/sandbox.md#4-ssh-into-the-workspace) ·
[Mount host directories](docs/sandbox-mounts.md) ·
[Stop, restart, or remove](docs/sandbox.md#stop-or-remove) ·
[Incus web UI](docs/sandbox.md#incus-web-ui)

## Run

For agents running directly on your host:

You need **Go 1.25+**, **macOS or Linux**, and your agent CLIs.
Codex integration is tested with **0.156.1**.
For config files and a first-message check, follow [first-time setup](docs/quickstart.md).

```sh
git clone https://github.com/makarski/collab-ai.git
cd collab-ai
go build -o broker ./cmd/broker
go build -o collab-codex ./cmd/codex
go build -o collab-mcp ./cmd/mcp
go build -o collab ./cmd/collab

# Start the broker and leave it running
COLLAB_SOCKET_PATH=/tmp/collab-ai.sock COLLAB_DB_PATH="$PWD/collab-ai.db" ./broker
```

In another terminal, from the same repository directory:

```sh
./collab-codex --agent-id codex-1 --socket /tmp/collab-ai.sock --terminal -- -C /path/to/your/project
```

Keep the session open for automatic listening. Give each agent a unique ID;
pass Codex options after `--`.

### Resume or fork

Resume or fork managed or ordinary Codex conversations. Exit the previous session first:

```sh
./collab-codex --agent-id codex-1 --terminal -- resume SESSION_ID
./collab-codex --agent-id codex-1 --terminal -- fork SESSION_ID
```

[More options and compatibility](docs/host-integration.md#codex-terminal).

### Set a Codex soft cap

```sh
mkdir -p "$HOME/.local/state/collab-ai"
./collab-codex --agent-id codex-1 --terminal \
  --token-cap 100000 --budget-file "$HOME/.local/state/collab-ai/task-budget.json" -- \
  -C /path/to/your/project
```

Reuse the file on resume to retain accounting. At the limit, the supervisor
interrupts Codex and shuts it down. Usage reports can arrive late, so overshoot
is possible. This currently covers the managed Codex process; Claude and shared
budget controls are not included. [Accounting and limits](docs/host-integration.md#codex-soft-cap).

### Claude Code

Create the [dedicated channel config](docs/quickstart.md#3-start-claude-code),
then launch from your project directory:

```sh
claude --strict-mcp-config --mcp-config /absolute/path/to/mcp.json \
  --dangerously-load-development-channels server:collab
```

Automatic delivery requires Claude's channel opt-in and organization policy
support. [Manual MCP setup](docs/mcp.md) is available for either agent.

## Dashboards

**Incus web UI** — inspect containers, resources and logs from your host:

```sh
incus webui colima-collab-ai:  # Linux: incus webui local:
```

Open the printed URL and select project `collab-ai`. Keep the command running.
[UI setup and troubleshooting](docs/sandbox.md#incus-web-ui).

**Collaboration dashboard** — view both agents and pending inboxes in Bubble Tea.
After the [SSH setup](docs/sandbox.md#4-ssh-into-the-workspace), open it from your host:

```sh
ssh -t -F infra/incus/ssh/config workspace collab dashboard
```

For a broker running directly on your host:

```sh
./collab dashboard --socket /tmp/collab-ai.sock
./collab status --socket /tmp/collab-ai.sock
```

The dashboard is read-only; token usage and cap controls are not implemented.
Codex prompts and approvals stay in its terminal. [Dashboard controls](docs/dashboard.md).

## How it fits together

![Architecture: human interaction in each agent mode, a shared broker UDS file, and durable SQLite inboxes.](docs/assets/architecture.svg)

[Diagram source (PlantUML)](docs/assets/architecture.puml)

Dotted arrows show startup; solid arrows show communication. The shared UDS
connects agents to the broker, with a separate inbox for each agent.

## Documentation

- [First-time setup](docs/quickstart.md) · [Sandbox guide](docs/sandbox.md) · [Troubleshooting](docs/troubleshooting.md)
- [Agent collaboration skill](docs/skills/collab-ai/SKILL.md) · [Host integrations](docs/host-integration.md) · [Manual MCP](docs/mcp.md)
- [Protocol and storage](docs/protocol.md) · [Durable inboxes](docs/durable-inboxes.md) · [Implementation status](docs/status.md)

## Contributing

Try it on a real task and [tell us what happened](https://github.com/makarski/collab-ai/issues).
Bug reports, docs improvements, and focused pull requests are welcome.

```sh
go test -race ./... -timeout=30s
```
