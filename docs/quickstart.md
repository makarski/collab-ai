# Your first agent exchange

Run one broker, one Codex session, and one Claude Code session on the same
machine. Leave their terminals open while you work. A fourth terminal lets you
check connections without interrupting either agent.

## Before you start

You need macOS or Linux, Git, Go 1.25+, and installed, signed-in `codex` and
`claude` CLIs. Check `go version`, `codex --version`, and `claude --version`.
Complete each CLI's login and project-trust prompts before testing collaboration.
The Codex integration was tested with 0.156.1.

Claude's automatic delivery requires its development-channel opt-in and an
account/organization that permits channels. If unavailable, use the
[manual MCP setup](mcp.md#register-an-adapter); ordinary MCP tools require inbox
checks and do not wake an idle agent.

Use `/tmp/collab-ai.sock` throughout this walkthrough. If you already have a
broker there, reuse it or choose a different path in **every** command and config.
Replace the example repository and project paths with your own absolute paths.

## 1. Build and start the broker

**Terminal 1:**

```sh
git clone https://github.com/makarski/collab-ai.git
cd collab-ai
go build -o broker ./cmd/broker
go build -o collab-codex ./cmd/codex
go build -o collab-mcp ./cmd/mcp
go build -o collab ./cmd/collab
pwd

COLLAB_SOCKET_PATH=/tmp/collab-ai.sock COLLAB_DB_PATH="$PWD/collab-ai.db" ./broker
```

Save the directory printed by `pwd`: that is your `/absolute/path/to/collab-ai`
below. The broker stays in the foreground. Its SQLite database retains inbox
state across restarts; keep using the same database path.

**Terminal 4**, from that repository directory, check it is serving requests:

```sh
./collab status --socket /tmp/collab-ai.sock
```

Expect `Broker: ready (socket reachable: true)`. A fresh broker has zero connected
sessions. Resolve errors here before launching agents.

## 2. Start Codex

**Terminal 2**, from the collab-ai repository directory:

```sh
./collab-codex --agent-id codex-1 --socket /tmp/collab-ai.sock --terminal -- \
  -C /absolute/path/to/your/project
```

Your normal Codex UI opens. Complete any trust prompt and start the conversation.
Its listener starts automatically when the conversation is created. Options for
Codex go after `--`; options for collab-ai go before it.

Use the session's `collab_runtime` tools. If you previously configured a manual
collab MCP adapter, do not activate it with the same `codex-1` ID. Each inbox has
one owner. [Resume and fork commands](../README.md#resume-or-fork) work for saved
managed and ordinary Codex conversations.

## 3. Start Claude Code

Save this as `mcp.json` **outside your project repository**, replacing `command`
with the absolute path to the `collab-mcp` binary you just built:

```json
{
  "mcpServers": {
    "collab": {
      "command": "/absolute/path/to/collab-ai/collab-mcp",
      "args": [
        "--agent-id", "claude-1",
        "--socket", "/tmp/collab-ai.sock",
        "--claude-channel", "--auto-listen"
      ]
    }
  }
}
```

**Terminal 3:**

```sh
cd /absolute/path/to/your/project
claude --strict-mcp-config --mcp-config /absolute/path/to/mcp.json \
  --dangerously-load-development-channels server:collab
```

Accept Claude's local-development channel consent. `--strict-mcp-config` uses
only this MCP configuration for the session. Keep this config dedicated to this
Claude session; a second session needs its own agent ID.

If tools appear but Claude ignores incoming messages, check
[channel requirements](host-integration.md#claude-code). Registration alone does
not prove that Claude received a notification.

## 4. Check both directions

In **Terminal 4**, run status again. Both `codex-1` and `claude-1` should be
connected. Then ask Claude:

> Use collab to send codex-1 "Reply with pong".

The send tool requests acknowledgment automatically. Codex should receive the
message without an inbox reminder. Ask it to acknowledge the
message and reply with `in_reply_to` set to the incoming message ID. Claude should
receive the reply without polling. Ask Claude to acknowledge the reply if it
requests acknowledgment. Do not have either agent reply to the pong again.

This checks delivery into both conversations. Broker acceptance, connection
status, and acknowledgment are separate observations; a reply does not
implicitly acknowledge its request.

For ongoing work, point both agents at the
[collaboration skill](skills/collab-ai/SKILL.md) for handoff and acknowledgment
conventions. For a shared checkout, agree who edits which files; separate
worktrees can keep simultaneous changes apart.

## Watch, stop, and return

In **Terminal 4**:

```sh
./collab dashboard --socket /tmp/collab-ai.sock
```

Press `q` to leave the dashboard; agents and broker continue running.
The dashboard shows connections and pending acknowledgments, without consuming
inboxes. [Controls](dashboard.md).

To stop collaboration, exit the agent UIs, then press Ctrl+C in the broker
terminal. Listeners stop with their processes. To return, start the broker with
the same database, then relaunch agents with their previous IDs to recover
unacknowledged durable messages. Resume the host conversation separately if you
want its history too. See [troubleshooting](troubleshooting.md) for failed
connections or stale sockets.
