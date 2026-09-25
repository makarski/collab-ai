# collab-ai

**A local foundation for sandboxed AI collaboration.**

Connect Codex and Claude Code with durable inboxes and automatic message delivery.
Provision an isolated Linux workspace with Incus. [MIT licensed](LICENSE).

[Agent setup](#run) · [Sandbox setup](#set-up-a-sandbox) ·
[Stop the sandbox](docs/sandbox.md#stop-or-remove) · [Architecture](#how-it-fits-together)

## Set up a sandbox

![Sandbox containment on macOS: a Colima Linux VM contains an Incus project and an offline workspace. Agents currently run outside the workspace. Linux hosts do not need the Colima VM.](docs/assets/sandbox.svg)

[Diagram source (PlantUML)](docs/assets/sandbox.puml)

**Today:** the sandbox is an empty, offline container. Agents run on your host;
agent installation inside the sandbox and hard token caps are not implemented.

On **macOS**, install the tools and start the dedicated host from this repository:

```sh
brew install colima incus opentofu python
python3 scripts/sandbox-host.py apply
```

Incus is the Mac client; Colima runs its Linux server. On **Linux**, install Incus
directly; Colima is unnecessary. **Starting the host does not create the workspace.**
Follow the [sandbox guide](docs/sandbox.md) to pin an image and provision it.

Stop the dedicated Mac VM, keeping its data:

```sh
colima stop collab-ai
```

[Status and shell](docs/sandbox.md#4-inspect-and-use-the-workspace) ·
[Stop, restart, or remove](docs/sandbox.md#stop-or-remove) ·
[Incus web UI](docs/sandbox.md#incus-web-ui)

## Run

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

### Claude Code

Create the [dedicated channel config](docs/quickstart.md#3-start-claude-code),
then launch from your project directory:

```sh
claude --strict-mcp-config --mcp-config /absolute/path/to/mcp.json \
  --dangerously-load-development-channels server:collab
```

Automatic delivery requires Claude's channel opt-in and organization policy
support. [Manual MCP setup](docs/mcp.md) is available for either agent.

## Broker status

```sh
./collab dashboard --socket /tmp/collab-ai.sock
./collab status --socket /tmp/collab-ai.sock
```

See connected agents and pending acknowledgments. [Dashboard controls](docs/dashboard.md).

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
