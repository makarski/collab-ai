# collab-ai broker

A Unix Domain Socket message broker for AI agents running on one machine.
Agents connect, broadcast, and direct-message each other; all conversation
state is persisted to a local SQLite database. Structured logs go to stdout.

## Run

```sh
go build -o broker ./cmd/broker
go build -o collab-mcp ./cmd/mcp
COLLAB_SOCKET_PATH=/tmp/collab-ai.sock COLLAB_DB_PATH=./collab-ai.db ./broker
```

Config (env vars, both optional):

| Var                  | Default                  | Purpose        |
|----------------------|--------------------------|----------------|
| `COLLAB_SOCKET_PATH` | `/tmp/collab-ai.sock` | UDS listen path |
| `COLLAB_DB_PATH`     | `./collab-ai.db`      | SQLite file    |

Startup refuses any existing socket path, including a live socket, stale socket,
regular file, or symlink. After a crash, verify the broker is no longer running
before removing its stale socket. Normal shutdown removes only the socket created
by that broker instance and waits for connections and session records to close.

## MCP for Codex and Claude

Start one broker, then configure each agent to launch its own `collab-mcp` process.
Each process opens one persistent broker connection on its first messaging tool
call and then reads incoming frames in the background. MCP initialization and
tool discovery do not connect or register an agent, so a short-lived inventory
probe cannot displace an active session with the same configured ID.

Call `receive` once to register before another agent sends to you. Until that
first `send`, `receive`, or `wait`, the broker considers the agent offline.
Use a distinct agent ID for every simultaneous messaging session; reusing an ID
in an actual messaging call still disconnects the previous owner.

Replace `/absolute/path/to/collab-ai/collab-mcp` below with the absolute path to
your built MCP executable. The socket path must match the broker's
`COLLAB_SOCKET_PATH`; these examples use `/tmp/collab-ai.sock` as in the Run section.

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

| Tool | Arguments | Behavior |
|------|-----------|----------|
| `send` | `to`, `text` | Writes a direct message, or broadcasts with `to: "*"`. |
| `receive` | optional `limit` (default 20, maximum 100) | Consumes queued messages and broker errors immediately. |
| `wait` | optional `timeout_seconds` (default 30, maximum 30), `limit` | Consumes queued frames or waits for the next arrival. |

Example collaboration: Codex first calls `receive({})` to register.
Claude then calls `send({"to":"codex-1","text":"Please review my changes"})`;
Codex calls `wait({"timeout_seconds":30})` and receives the message. `receive`
and `wait` return `messages`, `connected`, and an `error` if disconnected. An empty
wait that reaches its deadline also returns `timed_out: true`. These tools consume
frames, so repeated calls do not return the same message. Concurrent consumers
share one inbox; each frame goes to only one call.

Check `receive` between work steps and use `wait` while awaiting a peer. Waiting
blocks on socket arrivals rather than querying SQLite repeatedly. This adapter
does not push input into a model's conversation or wake an idle agent after its
turn ends; unattended collaboration still needs session orchestration.

`send` returns `status: "written"` and `delivery_confirmed: false`: the broker
protocol has no acknowledgement. Routing errors arrive asynchronously through
`receive` or `wait`. Messages are persisted, but offline delivery, replay, and
deduplication are not implemented. A connection loss is reported without automatic
reconnection; restart the MCP server to reconnect. Buffered frames remain available
until that restart. The inbox is bounded to 256 frames and 4 MiB of encoded frame
data; overflow closes the connection and reports that messages may be missing.

## Protocol

Newline-delimited JSON, one object per line. First frame must be a hello:

```json
{"type":"hello","agent_id":"claude-1","harness":"claude-code 1.5","model":"claude-sonnet-4"}
```

Broker replies with a welcome:

```json
{"type":"welcome","seq":1,"agent_id":"claude-1"}
```

Send a broadcast (`to: "*"`) or a direct message (`to: "<agent_id>"`):

```json
{"type":"msg","to":"*","payload":{"text":"hello everyone"}}
{"type":"msg","to":"gpt-1","payload":{"text":"hi"}}
```

Delivered messages carry a broker-assigned global monotonic `seq`:

```json
{"type":"msg","seq":2,"from":"claude-1","to":"gpt-1","payload":{...},"ts":"..."}
```

Routing and protocol errors go back to the sender:

```json
{"type":"error","code":"unknown_recipient","detail":"no connected agent with id gpt-1"}
```

Test interactively with `nc -U /tmp/collab-ai.sock`.

Frames are limited to 1 MiB including the newline. A reader whose 64-frame outgoing
queue fills is disconnected so it cannot block other agents. Direct sends that
encounter a full recipient queue report `recipient_unavailable`; broadcasts continue
to healthy recipients. Socket writes time out after five seconds. Disconnecting a
slow reader can discard pending frames; persistence is a log, not a delivery queue.

## State

SQLite file with two tables: `agents` (connect/disconnect events incl.
harness + model) and `messages` (seq, ts, sender, recipient, payload).
Message sequence allocation resumes across broker restarts via `MAX(messages.seq)`.
Welcome frames also consume sequence numbers, but are not persisted; a trailing
welcome sequence can therefore be reused after a restart. Do not use welcome
sequences as durable replay cursors.

## Test

```sh
go test -race ./... -timeout=30s
```

The suite covers routing, replacement connections, slow readers, bounded framing,
socket ownership, shutdown, discovery without a broker, same-ID inventory probes,
concurrent lazy registration, and MCP message exchange through a broker.
