# Host integration

The default MCP adapter remains a manual inbox. Two opt-in integrations can
submit peer context to a running host: a Claude Code channel and an App Server
stdio proxy for a single Codex thread. The broker still uses UDS. Both modes use [durable inbox recovery](durable-inboxes.md) for accepted v3
messages. Neither adds a scheduler or listens after its process stops.

## Claude Code

Build `go build -o collab-mcp ./cmd/mcp`. Add `--claude-channel` to the adapter's
arguments in an explicitly selected MCP configuration:

```json
{
  "mcpServers": {
    "collab": {
      "command": "/absolute/path/to/collab-mcp",
      "args": ["--agent-id", "claude-1", "--socket", "/tmp/collab-ai.sock", "--claude-channel"]
    }
  }
}
```

For this locally developed server, start an **interactive** Claude session:

```sh
claude --strict-mcp-config --mcp-config /absolute/path/to/mcp.json \
  --dangerously-load-development-channels server:collab
```

Read and accept Claude's local-development consent dialog, then ask Claude to
call `collab`'s `listen` tool. Initialization, discovery, and `listener_status`
alone do not register a broker session. Keep that same Claude process running.
The adapter advertises `experimental["claude/channel"]` and emits
`notifications/claude/channel`; it does **not** advertise permission relay.

Channels are a research preview with host version, authentication/provider,
organization policy, and per-session opt-in requirements. Current Claude docs
permit claude.ai or Console API-key authentication, with additional managed
organization restrictions. The development flag does not override organization
policy. On the tested Claude Code 2.1.272, the custom development flag is ignored
in `-p` mode: MCP tools work but the channel does not deliver. Use the interactive
development flow above. Approved production plugins have a separate `--channels`
flow; this repository is not an approved plugin.

Claude can silently ignore a successfully written notification. Therefore
`listening_delivery_unconfirmed` and `submitted` are submission observations,
never claims that Claude read a message. Explicit `acknowledge` tool calls,
confirmed by the broker, establish the acknowledgment boundary. A reply can use
`in_reply_to` for correlation, but does not implicitly acknowledge.

## Codex App Server

Build `go build -o collab-codex ./cmd/codex`. An App Server client can launch:

```sh
./collab-codex --agent-id codex-1 --socket /tmp/collab-ai.sock
```

The executable launches `codex app-server --listen stdio://` and speaks the App
Server protocol on stdin/stdout. It is a proxy for an operator-owned client, not
a terminal chat UI. Use `--codex /absolute/path/to/codex` to choose the executable.

The client initializes with `capabilities.experimentalApi: true`, sends
`initialized`, and creates one new thread with `thread/start`. The proxy adds
`collab_*` dynamic tools and messaging instructions to that thread. The caller's
existing dynamic tools, sandbox, approval policy, and other thread parameters are
preserved. `collab_` names are reserved. A failed start can be retried; a second
thread, resume, or fork needs a new proxy process. Existing desktop conversations
are not attached or controlled by this integration.

Operator request IDs must not start with `collab-`; that namespace is reserved
for injected requests. Replies arriving after an injected request times out are
consumed internally. Operator responses to host approval requests pass through
regardless of their ID.

Start normal operator work using `turn/start`. Ask the agent to call
`collab_listen` before peers send. Incoming frames enter the managed thread with:

```json
{
  "method": "turn/start",
  "params": {
    "threadId": "<managed-thread-id>",
    "input": [],
    "toolOutput": {"name": "collab_receive", "output": "<encoded peer frame>"}
  }
}
```

The host contract queues external tool output during active work and starts a
turn when idle. The proxy never promotes peer content to user instructions and
never turns an API response into an agent acknowledgment. App Server approval
requests pass through to the operator's client unchanged; that client must handle
them. Collaboration peers cannot answer them through the broker.

## Status, fallback, and shutdown

One listener owns the broker connection and consumes its frames. Messaging tools
share that owner. `receive`/`wait` drain a separate bounded copy, so they cannot
steal a frame from host submission. Only message and error frames are submitted;
acknowledgment events remain in the fallback inbox to avoid endless wake cycles.
Use `receive` between work steps to inspect receipts and drain the copy. This is
also the recovery path when Claude ignores a notification.

`listener_status` returns `inactive`, `listening_delivery_unconfirmed`,
`disconnected`, or `stopped`, the broker session ID, submission count, last
submitted message ID, and any error. `manual_check_required` stays true: neither
host's write response proves conversation exposure. Verify each tracked message
through its correlated `accepted`, `adapter_received`, and explicit
`agent_acknowledged` events. Acknowledgment means the agent considered the context,
not that it completed a task.

The broker client's inbox and listener copy each allow 256 frames / 4 MiB.
Receipts count toward the copy limit. Overflow, a failed host submission, or an
established broker disconnect stops the listener and reports a possible gap.
Unconsumed copied frames remain readable until process exit. There is no silent
reconnect. Restart obtains a new broker session and loses in-memory frames;
accepted durable messages without agent acknowledgment replay to the same
logical ID on v3. Other history and ephemeral frames are not replayed. Initial
connection failures can be retried. Deduplicate stable IDs before repeating work.

Host submissions have a five-second deadline. Waiting tools use the existing
30-second maximum. Cancellation closes a potentially partial host write; EOF or
shutdown closes the managed process and releases inbox ownership. The production
listener waits on events, without polling the database.

## Validation and host references

`go test -race ./... -timeout=30s` covers the wire contract, concurrency, explicit
acknowledgments, retained fallback, inactive discovery, foreign-thread tool calls,
approval pass-through, unavailable hosts, overflow, and cancellation. Fake-host
tests cannot establish whether a particular installed host actually wakes.

The dynamic-tool response shape is checked against Codex 0.154.0's generated
`DynamicToolCallResponse` schema: `success` and `contentItems`, with text entries
using `type: "inputText"` and `text`. Tests exercise the full tool handler path,
validation failures, RPC errors, and late replies after cancellation.

The [recorded live handoff](host-live-demo.md) demonstrates all four cases on
Claude Code 2.1.272 and Codex CLI 0.154.0, with message, conversation, broker
session, and explicit acknowledgment identities.

Host contracts: [Claude channels](https://code.claude.com/docs/en/channels),
[channel reference](https://code.claude.com/docs/en/channels-reference), and
[Codex App Server](https://learn.chatgpt.com/docs/app-server#start-a-turn).
