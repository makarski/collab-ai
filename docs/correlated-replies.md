# Correlated replies

Use `wait_reply` to collect an answer to a particular request without draining
unrelated inbox frames. It is available in the manual MCP adapter, Claude channel
mode, and the managed host's dynamic tools as `collab_wait_reply`.

## Exchange

After the peer registers, send a request and retain the returned `message_id`:

```json
{"to":"claude-1","text":"Review commit abc123","message_id":"review-abc123"}
```

The peer acknowledges that request after considering it, then uses `send` with
`in_reply_to` to answer:

```json
{"to":"codex-1","text":"Review findings…","message_id":"findings-abc123","in_reply_to":"review-abc123"}
```

The requester calls `wait_reply`:

```json
{"message_id":"review-abc123","from":"claude-1","timeout_seconds":30}
```

`message_id` is the original request ID (1–128 bytes); `from` is the required
expected logical sender (1–128 bytes, other than `*`). A reply must match both
`in_reply_to` and its broker-assigned `from`. A broadcast request can be followed
by separate waits for individual peers; this tool never aggregates a broadcast.

`timeout_seconds` defaults to 30 when omitted or zero; other values must be
integers from 1 through 30. Initial registration uses the existing adapter
connection timeout; the reply-wait budget starts once the connection/listener
has been obtained. Invalid arguments are rejected before registration. Tool
discovery stays connection-free.

## Result

The result is `{ "outcome": "...", "inbox": { ... } }`. `inbox` uses the normal
inbox shape: `messages`, `connected`, `session_id`, acknowledgment/durability
capabilities, and optional `error`/`timed_out`. Original frames are returned
unchanged, including sender session, sequence, payload, timestamps, and replay
flags. The result contains zero or one frame, never a batch.

| Outcome | Meaning |
| --- | --- |
| `reply` | Consumed one `msg` matching the request and sender. |
| `broker_error` | Consumed an `error` whose `message_id` matches the request. If it names an `agent_id`, that recipient must also match `from`. |
| `timeout` | No matching frame was queued at the final deadline check and the adapter still reported connected. `messages` is `[]` and `timed_out` is true. |
| `disconnected` | No matching frame remains and the connection/listener is terminal. Inspect `inbox.error` and listener status when applicable. |

The first matching frame in arrival order wins, whether it is a reply or broker
error. A buffered matching frame is returned even after disconnection, with
`connected: false`; inspect that flag alongside the outcome. Uncorrelated broker
errors remain in the inbox for `receive`/`wait`.

`broker_error` is not automatically a rejected send. For example, a recipient
disconnect can be reported after durable acceptance; the request may still
replay. Inspect the error code and retained acceptance events. A timeout says
nothing about whether the request was accepted, the peer read it, or work is
complete. Waiting again does not resend or reconnect.

Cancellation, invalid input, and an initial connection failure return tool
errors, not the outcomes above. An already-canceled call does not consume a
frame. If cancellation races with a successful read, the queue operation may
already have consumed it; this is not an exactly-once host-exposure guarantee.

## Ownership and delivery

`wait_reply` consumes from the existing owner queue. It does not open an observer
broker connection, acknowledge anything, or allocate a message ID. All receipt
events and unrelated frames retain their queue order. Matching consumption
frees only that frame's existing count/byte allocation; the 256-frame / 4 MiB
bounds and explicit overflow behavior remain in force.

Multiple waits for different requests are supported. An arrival wakes every
selective reader, and locked matching gives each frame to at most one consuming
call. Ordinary `receive`/`wait` can consume a reply first; two waits for the same
request/peer compete. Do not run a general consuming listener alongside a
targeted wait unless that caller handles replies it takes. Delegated listeners
remain non-consuming observers of whatever the parent still retains. A child
with the parent's identity must still use only `wait_delegated`.

In host mode, `wait_reply` consumes the bounded fallback copy. The listener
remains the sole raw broker consumer; removing the fallback cannot stop host
submission. The host and tool may therefore expose the same stable message ID.
Deduplicate before acting. A returned reply does not prove host notification
success; check `listener_status` for host delivery problems.

The caller must explicitly acknowledge a reply after considering it, using the
reply's own `message_id`, if `ack_requested` is true. Neither reply correlation
nor waiting acknowledges the request or reply or establishes task completion.
Drain `receive` between work steps to handle the receipts and other messages
that targeted waits intentionally leave queued.

## Recovery and compatibility

A reply already queued before the call is immediately eligible, so a fast peer
cannot be missed merely because it answered between `send` and `wait_reply`.
After adapter/broker restart, v3 durable replies replay to the same logical
inbox until explicitly acknowledged; their original correlation remains usable.
Wait registrations themselves are not persisted. Keep the original request ID
and expected peer to wait again, and deduplicate replayed message IDs.

There is no lookup of previously consumed replies or verification that this
adapter originally sent the request. This is a selective inbox read, not a
request-history service. No wire frame, protocol-version change, database
migration, task scheduler, or transport change is introduced.
