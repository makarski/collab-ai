# Manual MCP setup and messaging reference

[Back to the quick start](../README.md#run). For automatic delivery, use
[managed Codex or Claude channels](host-integration.md).

Start one broker, then configure each agent to launch its own `collab-mcp` process.
Each process opens one persistent broker connection on its first messaging tool
call and then reads incoming frames in the background. MCP initialization and
tool discovery do not connect or register an agent, so a short-lived inventory
probe cannot displace an active session with the same configured ID.

Call `receive` once to register before another agent sends to you. Until that
first messaging call, the broker considers the agent offline.

For automatic delivery in terminal sessions, use `collab-codex --terminal` for
Codex and a dedicated Claude channel configuration with `--auto-listen`.
These modes activate at host startup and maintain the fallback queue after
explicit acknowledgments. See [host setup](host-integration.md) for launch
commands, opt-in requirements, and failure/recovery limits. Ordinary MCP tools
alone do not wake an idle conversation.

How two agents actually work a project over the channel — session start,
listening, handoffs, review verdicts, merge policy, split work — is written up
as a copyable skill in [docs/skills/collab-ai/SKILL.md](skills/collab-ai/SKILL.md).
An `agent_id` is a logical inbox name (1–128 bytes, other than `*`). Each accepted
connection receives a unique `session_id`, also returned by `receive` and `wait`.
One session owns an inbox. A second messaging connection using the same agent ID
is rejected with `duplicate_id`, its own session ID, and `owner_session_id`; the
owner stays connected. Use a distinct agent ID for a separate simultaneous agent.

Default discovery-only probes remain connection-free. Explicit `--auto-listen`
channel configurations register after MCP initialization and must be reserved
for the owning session. To transfer inbox ownership, close
the owning adapter, then connect again; there is no takeover flag or observer
mode. A failed initial registration can be retried. An established connection
that disconnects remains terminal: restart that adapter to obtain a new session.
A v3 reconnect recovers unacknowledged durable messages; other buffered frames and acknowledgment events are not replayed. A separate
listener cannot share the working agent's logical inbox by claiming the same ID.
For a background subagent, use [delegated listening](delegated-listeners.md):
the parent grants temporary read access through its adapter, and the child calls
`wait_delegated` without registering a second broker connection. The parent
retains its inbox and is responsible for draining and explicitly acknowledging it.

## Register an adapter

Replace `/absolute/path/to/collab-ai/collab-mcp` below with the absolute path to
your built MCP executable. The socket path must match the broker's
`COLLAB_SOCKET_PATH`; these examples use `/tmp/collab-ai.sock` as in the [quick start](../README.md#run).

Codex:

```sh
codex mcp add collab -- /absolute/path/to/collab-ai/collab-mcp \
  --agent-id codex-1 --harness codex --socket /tmp/collab-ai.sock
```

Claude Code:

```sh
claude mcp add --transport stdio collab -- /absolute/path/to/collab-ai/collab-mcp \
  --agent-id claude-1 --harness claude-code --socket /tmp/collab-ai.sock
```

`collab` is the MCP server name. The executable after `--` becomes `command`,
and its flags become `args`. In Claude's configuration, the entry inside
`mcpServers` should look like this:

```json
{
  "collab": {
    "type": "stdio",
    "command": "/absolute/path/to/collab-ai/collab-mcp",
    "args": [
      "--agent-id", "claude-1",
      "--harness", "claude-code",
      "--socket", "/tmp/collab-ai.sock"
    ]
  }
}
```

## Troubleshooting

If the server does not connect, the error names which half is wrong:

- `ENOENT: Executable not found in $PATH: collab` — `command` holds the server
  name rather than the binary, and the binary path has been placed in `args`.
  The executable belongs in `command`.
- A messaging tool returns `connect to broker: dial unix <path>: no such file or
  directory` — no broker is listening on that path. Discovery can still succeed.
  Start the broker, or match `--socket` to its `COLLAB_SOCKET_PATH`. A socket
  file left behind by a crashed broker looks the same as a live one to `ls`;
  connecting to it is the only way to tell.

If the server appears under several project entries in `~/.claude.json`, fix
each one.

Running the adapter by hand to test it exits immediately with `server is
closing: EOF`, because the pipe closes its stdin. That is the end of an MCP
session, not a fault; hold stdin open to see it answer.

For a different socket, replace the `--socket` value in both configurations with
the broker's absolute socket path. `COLLAB_SOCKET_PATH` also sets the default when
`--socket` is omitted. Optional `--model` records
the model name with the agent session. Start the broker before the first messaging
tool call. A failed initial connection can be retried with another tool call.
The adapter reserves stdout for MCP protocol frames and writes diagnostics to stderr.

These commands follow the official [Codex MCP documentation](https://learn.chatgpt.com/docs/extend/mcp?surface=cli)
and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp).

## Messaging tools

| Tool | Arguments | Behavior |
|------|-----------|----------|
| `send` | `to`, `text`, optional `message_id`, `in_reply_to`, `non_durable` | Writes a direct message or broadcast (`to: "*"`); returns its ID for tracking. |
| `acknowledge` | `message_id` | Writes an explicit agent acknowledgment; its persisted confirmation arrives through `receive`/`wait`. |
| `receive` | optional `limit` (default 20, maximum 100) | Consumes queued messages and broker errors immediately. |
| `wait` | optional `timeout_seconds` (default 30, maximum 30), `limit` | Consumes queued frames or waits for the next arrival. |
| `wait_reply` | `message_id`, `from`, optional `timeout_seconds` (default 30, maximum 30) | Consumes one correlated reply or broker error; leaves unrelated frames and receipts queued. |
| `delegate_listener` | none | Manual adapter: grants one listener read access for 15 minutes; replaces any previous grant. |
| `wait_delegated` | `socket_path`, `token`, optional `after_cursor`, `timeout_seconds` (default 25), `limit` | Reads retained parent frames without consuming or acknowledging them; never registers the child's adapter. |
| `revoke_listener` | none | Revokes this adapter's grant and cancels its outstanding wait; keeps the parent connected. |

Example collaboration: Codex first calls `receive({})` to register.
Claude then calls `send({"to":"codex-1","text":"Please review my changes"})`;
Codex calls `wait({"timeout_seconds":30})` and receives the message. After
considering it in the active conversation, Codex calls
`acknowledge({"message_id":"<request ID>"})` and can reply using
`send({"to":"claude-1","text":"Review findings…","in_reply_to":"<request ID>"})`.
Claude can call `wait_reply({"message_id":"<request ID>","from":"codex-1"})`
for that specific answer, then explicitly acknowledge the returned reply's own
message ID after considering it. A timeout does not mean the request failed;
waiting again does not resend it. Continue `receive` checks for receipts and other
conversations. See [correlated replies](correlated-replies.md) for outcomes,
concurrent readers, replay, and host-mode behavior. `receive`
and `wait` return `messages`, `connected`, and an `error` if disconnected. An empty
wait that reaches its deadline also returns `timed_out: true`. These tools consume
frames, so repeated calls do not return the same message. Concurrent consumers
share one inbox; each frame goes to only one call.

Check `receive` between work steps, use `wait_reply` for a specific request, and
use `wait` for general arrivals. Waiting
blocks on socket arrivals rather than querying SQLite repeatedly. This manual mode
does not push input into a model's conversation or wake an idle agent after its
turn ends; unattended collaboration still needs session orchestration.

## Delivery and acknowledgments

`send` returns `status: "written"`, `message_id`, and
`delivery_confirmed: false`. `receive` and `wait` include correlated `ack` and
`error` frames alongside messages:

| Stage | What the broker can confirm |
|-------|-----------------------------|
| `accepted` | SQLite committed the message, reply correlation, and recipient/session membership atomically. No recipient receipt is implied. |
| `adapter_received` | The intended recipient session reported buffering the message; the broker committed that receipt. This does not mean the model has seen it. |
| `agent_acknowledged` | The recipient explicitly called `acknowledge`, and the broker committed it. This does not assert understanding or task completion. |

The adapter automatically reports receipt after decoding and checking inbox
capacity, before exposing a message to inbox consumers. It never automatically
acknowledges on behalf of the agent. `acknowledge` itself confirms only a write;
the subsequent `agent_acknowledged` event confirms persistence to both the
recipient and the original sender session. Repeating a receipt is idempotent.
For non-durable messages, only the session selected at acceptance can acknowledge.
Durable messages can be recovered and acknowledged by the current logical inbox
owner after that owner reports its own adapter receipt.

Errors carry the original `message_id`. Unknown direct recipients are rejected
before persistence. Durable sends can target offline logical IDs previously
registered with v3; online recipients must support durability. A full outgoing queue reports `recipient_unavailable`; a
recipient disconnect before explicit acknowledgment reports
`recipient_disconnected` to the original sender session, even if adapter receipt
was already confirmed. A broker disconnect, write failure, or wait timeout leaves
missing stages **unconfirmed**. `timed_out` describes an empty wait, not message
expiry or successful delivery. Check each message ID independently.

Durable messages remain pending until explicit agent acknowledgment and replay
on v3 registration after restart. Legacy history is never enrolled for replay.
Connection loss is reported without automatic reconnection; restart the adapter
with the same logical ID to recover. See [limits and retries](durable-inboxes.md). Buffered frames remain available until that
restart. The inbox is bounded to 256 frames and 4 MiB of encoded frame data;
overflow closes the connection and reports that messages may be missing. Receipt
events count toward these limits, so manual-mode senders must also drain their
inboxes. Host listeners consume the broker inbox continuously, release fallback
copies after confirmed acknowledgment, and evict receipt history under pressure;
see [host queue maintenance](host-integration.md#status-fallback-and-shutdown).
