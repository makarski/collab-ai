# Broker wire protocol

[Back to the README](../README.md#documentation). This reference covers clients that
connect directly to the broker; agent setup is in the [quick start](../README.md#run).

Newline-delimited JSON, one object per line. Messaging connections start with a
hello; the separate one-shot [`status` request](status.md#wire-and-compatibility)
does not register an agent:

```json
{"type":"hello","protocol_version":3,"agent_id":"claude-1","harness":"claude-code","model":"optional-model-name"}
```

Broker replies with a welcome. Its `protocol_version` is the negotiated version
(the lower of the client and broker versions); legacy clients that omit it
receive a welcome without that field:

```json
{"type":"welcome","protocol_version":3,"seq":1,"agent_id":"claude-1","session_id":"<unique-session-ID>"}
```

Send a broadcast (`to: "*"`) or a direct message (`to: "<agent_id>"`):

```json
{"type":"msg","message_id":"request-1","ack_requested":true,"durable":true,"to":"*","payload":{"text":"hello everyone"}}
{"type":"msg","message_id":"reply-1","in_reply_to":"request-1","ack_requested":true,"durable":true,"to":"gpt-1","payload":{"text":"hi"}}
```

Delivered messages carry a broker-assigned global monotonic `seq`:

```json
{"type":"msg","message_id":"reply-1","in_reply_to":"request-1","ack_requested":true,"seq":2,"from":"claude-1","session_id":"<sender-session-ID>","to":"gpt-1","payload":{"text":"hi"},"ts":"..."}
```

Routing and protocol errors go back to the sender:

```json
{"type":"error","message_id":"reply-1","code":"unknown_recipient","detail":"no connected agent with id gpt-1"}
```

A tracked message produces an acceptance event with a fixed recipient list:

```json
{"type":"ack","message_id":"reply-1","stage":"accepted","seq":2,"recipients":[{"agent_id":"gpt-1","session_id":"<recipient-session-ID>"}]}
```

The recipient sends receipt stages using its existing connection:

```json
{"type":"ack","message_id":"reply-1","stage":"adapter_received"}
{"type":"ack","message_id":"reply-1","stage":"agent_acknowledged"}
```

The broker validates the connection's identity, commits the stage, and reports
it with `agent_id` and `session_id`. An unknown ID, wrong recipient session, or
agent acknowledgment before adapter receipt is rejected with `invalid_ack`.
Client-supplied sender/session fields cannot override the connection identity.

Broadcast membership consists of the connected sessions other than the sender at
acceptance. Each member acknowledges independently; late arrivals are excluded.
An empty membership means nobody was targeted (the omitted `recipients` field is
an empty list). Broadcasts above 256 recipients are rejected before persistence.
Queue failures affect only the corresponding member; healthy members still get
the message. The acceptance list remains the scope even if a member disconnects.

Message and reply IDs are opaque strings of at most 128 bytes. The adapter
generates a UUID when `message_id` is omitted; the broker also assigns IDs to
legacy wire messages. Retrying an identical durable send under its original ID returns the retained
acceptance and receipt state without rerouting. Changed content or another
logical sender returns `duplicate_message`. Non-durable sends retain duplicate
rejection. Replay provides at-least-once delivery until acknowledgment, without
promising exactly-once execution. `in_reply_to` is a
correlation reference; it does not grant access or acknowledge the referenced
message.

Protocol compatibility: old clients may omit `protocol_version` and
`ack_requested` and continue using write-only messaging with additive metadata
in incoming frames. Staged events require version 2 or newer and `ack_requested: true`. Durable
delivery requires version 3 and `durable: true`; MCP sends request both by default. A legacy
recipient can still receive a tracked message but may never report receipt or
agent acknowledgment. Missing stages remain unconfirmed. An old broker is
exposed by `acknowledgments_supported: false` in the inbox response; the new
MCP `send` defaults to durable delivery and requires v3; set `non_durable: true`
explicitly for legacy delivery. `durability_supported` reports negotiation.
`acknowledge` requires v2 or newer. Tool discovery remains offline-capable.

Test interactively with `nc -U /tmp/collab-ai.sock`.

Frames are limited to 1 MiB including the newline. A reader whose 64-frame outgoing
queue fills is disconnected so it cannot block other agents. Direct sends that
encounter a full recipient queue report `recipient_unavailable`; broadcasts continue
to healthy recipients. Socket writes time out after five seconds. Disconnecting a
slow reader can discard in-memory frames; unacknowledged durable messages remain
pending for replay. Other frames are not recoverable.

## State

SQLite retains `agents` (session lifecycle and harness/model) and `messages`
(history), plus `message_metadata` (stable IDs, reply references, sender session,
and acknowledgment request) and `message_receipts` (recipient/session membership
and receipt timestamps). Opening an existing database adds the new tables and
index without rewriting history. Pre-upgrade messages have no new metadata or
receipts and cannot be acknowledged retroactively. The additive v3 tables `durable_agents`, `durable_messages`, and `durable_inbox`
retain explicitly enrolled pending work and idempotent retry state. Pending and
retained durable storage have [bounded capacity](durable-inboxes.md); no
automatic expiry or eviction is performed.
Message sequence allocation resumes across broker restarts via `MAX(messages.seq)`.
Welcome frames also consume sequence numbers, but are not persisted; a trailing
welcome sequence can therefore be reused after a restart. Do not use welcome
sequences as durable replay cursors.

