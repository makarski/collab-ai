# Broker status

Build the operator CLI alongside the broker:

```sh
go build -o collab ./cmd/collab
./collab status
./collab status --json
./collab status --socket /path/to/broker.sock --timeout 5s
```

The socket defaults to `COLLAB_SOCKET_PATH`, then `/tmp/collab-ai.sock`. The
overall timeout defaults to three seconds and must be greater than zero and at
most 30 seconds. No agent ID, model, MCP adapter, or database path is needed.
The command sends one status request through the existing Unix socket and exits.
It never registers an agent, allocates a message sequence, consumes an inbox,
claims replay ownership, or acknowledges a message.

Exit status is **0** for a complete snapshot, **1** for unavailable, unsupported,
or degraded status (or an output failure), and **2** for invalid arguments.
`status --help` exits successfully. `--json` produces one JSON document on stdout,
including for operational failures; usage and output errors go to stderr.

## Observations and limits

`reachable` means the local socket connection succeeded. `health` distinguishes
`ready` (registry and storage queried), `degraded` (live registry available but
session history/pending counts unavailable), `unsupported` (older broker or
unsupported status schema), and `unavailable` (connection, timeout, shutdown, or
invalid response). A reachable socket alone does not establish broker readiness.

`snapshot_at` is the broker's UTC observation time, and `broker_started_at` is
the start of this hub instance. Registration, routing, acknowledgments, and
snapshots are serialized by the hub, so the snapshot reflects one point in its
event processing. Socket arrivals still waiting to be processed are not included.
The response is a snapshot, not a heartbeat or a live subscription. Status
database reads have a one-second budget so a failed query cannot indefinitely
block routing. Storage failures retain live registry information and expose
unavailable counts, with an explanatory error.

`connected_sessions` is the total number of current transport owners, including
ones omitted by the display limit. Session rows use these states:

| State | Meaning |
| --- | --- |
| `transport_connected` | This session currently owns the logical inbox in this broker's registry. |
| `disconnected` | A historical session with a recorded disconnect timestamp. |
| `stale` | No current owner matches this historical session, and no disconnect timestamp was recorded, for example after a crash. |

`connected_at` and `disconnected_at` come from the session lifecycle. Live
`last_seen_at` is registration or the most recent inbound frame processed by the
hub, including adapter receipts and rejected messages. It does not track model
activity or prove the peer read anything. Historical last-seen times and
disconnect reasons are not persisted, so they are `null`/`unavailable`; a stale
session's disconnect time is not guessed. Inspection never edits stale records.

The response contains at most **100 sessions**: live owners first, sorted by
logical ID, then newest retained historical sessions. `sessions_truncated`
indicates omitted rows; `history_available` indicates whether storage could be
read. Session records have no automatic expiry and remain until operator-managed
database maintenance. The display limit is not a history-retention guarantee.
Do not infer that an omitted identity never connected or is currently offline;
consult the truncation indicator and connected total.

## Pending delivery

`durable_pending.total` counts retained **recipient deliveries** whose explicit
agent acknowledgment has not committed. A broadcast pending for three recipients
counts as three. Offline recipients are included. Adapter receipt, delegated
listening, and successful host notification do not reduce this count. Only a
committed `agent_acknowledged` does. These counts use the same
[durable inbox contract](durable-inboxes.md) as replay and idempotent retries.

`durable_pending.recipients` lists logical IDs with nonzero pending counts,
sorted by ID, with at most **100 recipients**. `recipients_truncated` marks
omissions; the total always covers all durable pending deliveries. Pending work
belongs to the logical inbox, so it is not duplicated across that agent's
historical session rows. `legacy_unacknowledged` is always `null`: this command
does not report legacy/non-durable delivery counts, and pre-upgrade untracked
history cannot establish explicit acknowledgment state. A failed durable query
returns `durable_pending: null`, never a fabricated zero.

## JSON contract

The JSON schema has `schema_version: 1`, separate from the broker messaging
`protocol_version`. New optional fields may be added; consumers should ignore
unknown fields. Unknown metrics/timestamps are `null`, available zero counts are
numeric zero, and empty lists are `[]`. On a failed connection `protocol_version`
is zero because no version was observed. A successful empty broker returns:

```json
{
  "schema_version": 1,
  "reachable": true,
  "health": "ready",
  "snapshot_at": "2026-09-22T12:00:00Z",
  "broker_started_at": "2026-09-22T11:00:00Z",
  "protocol_version": 3,
  "connected_sessions": 0,
  "sessions": [],
  "session_limit": 100,
  "sessions_truncated": false,
  "history_available": true,
  "durable_pending": {
    "total": 0,
    "recipients": [],
    "recipient_limit": 100,
    "recipients_truncated": false
  },
  "legacy_unacknowledged": null
}
```

Session objects contain `agent_id`, `session_id`, `state`, `connected_at`,
`disconnected_at`, `last_seen_at`, and `disconnect_reason`. Pending recipient
objects contain `agent_id` and `pending`. Errors add a human-readable `error`;
scripts should use `health` and exit status rather than matching that prose.
The CLI quotes identifiers in human output so control characters cannot alter
the terminal display.

## Wire and compatibility

The new broker recognizes this as the first newline-delimited frame:

```json
{"type":"status"}
```

It returns `{"type":"status","status":{...}}` and closes the connection.
Additional fields or pipelined frames cannot register an agent. Status is not
accepted as a messaging-session command after `hello`. The existing one-MiB
frame limit and socket permissions apply. There is no new port, HTTP endpoint,
access token, message-body flag, or direct database reader. Identities and
aggregate counts remain within the existing local broker access boundary.

Older brokers reject the first frame with `expected_hello`; the CLI reports
`unsupported` and leaves counts unavailable. It never falls back to registering
an inspection agent. Upgrading the broker adds status while keeping messaging
protocol v3 unchanged. Opening an existing database adds an index for bounded
recent-session queries; it does not rewrite session history or enroll legacy
messages in durable delivery.

`go test -race ./... -timeout=30s` covers absent/unresponsive/legacy brokers,
empty and multiple sessions, stale sessions after a real broker crash,
disconnects, pending/receipt/acknowledgment boundaries, storage failures,
bounded history and recipient lists, text/JSON output, cancellation, and
non-interference with ownership, replay, inbox frames, and message sequences.
