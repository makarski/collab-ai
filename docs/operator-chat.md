# Operator chat (draft)

**Status: design agreed by Claude and Codex, 2026-10-12.** Not implemented.
Extends #68 (operator control plane; part 1 merged in #75) with an operator
inbox, messages to the operator, and an interactive chat. The chat is the first
form of the **forum** from `docs/team-presets.md`. On its own it is a
discussion space: it doesn't enforce task ownership or approval workflows.

## Goal

The human runs one interactive chat in `secured` instead of single
`collab operator send` commands:

- see connected agents (later duos and teams) and their status
- write multi-line messages with normal editing, and choose recipients
- see replies and delivery progress (accepted, received, acknowledged)
- keep a durable history that is logged like every other message

## Contract

**1. Messages to the operator**
- Agents may send **replies and proactive messages** (questions, status). The
  forum needs both; otherwise an escalation would need a fake parent.
- They are durable only, with sender and session stamped by the broker.
- **Quotas:** a per-sender limit on pending messages and bytes, plus an
  aggregate limit on the operator inbox's messages and bytes.
  - Every message counts, including replies; `in_reply_to` doesn't bypass the
    limits.
  - The per-sender quota follows the logical sender across reconnects.
  - The aggregate limit matters because a new agent ID can evade per-sender
    limits; agent IDs aren't authenticated principals today.
  - Over quota, the send is rejected atomically with `inbox_full` and never
    dropped silently.
- **Replies:** for a reply to an operator message, the broker verifies that
  the sender was among that message's frozen recipients. An unknown or
  unavailable parent is rejected explicitly. A valid reply doesn't mean the
  task is done.

**2. Acknowledgment**
- `adapter_received` stays automatic, once the chat has admitted the message
  locally within its bounds.
- **"Acknowledged by operator"** is an explicit human action, such as a key
  press. It means the human marked the message as received. It never means
  understanding, approval or task completion.
- Rendering, focus or scrolling never acknowledges.
- The acknowledgment and its audit row commit together. A repeated
  acknowledgment is idempotent. If the audit fails, the receipt stays
  unchanged.

**3. Sessions**
- One live **writable** session owns the operator inbox. A second claimant
  fails and is told who the owner is; there's no silent takeover.
- The inbox survives the chat exiting.
- **Read-only viewers** come later through #19. They never claim replay,
  receive or acknowledge.
- Viewing and writing are separate privileges, even though UID 0 holds both
  today.

**4. Replay, flow control and resync**
- **Bounded replay pages** with a cursor and credit, never dumping the inbox
  into the 64-frame writer queue. Pages are bounded by count and payload bytes.
- **Delivery cursor and human acknowledgment are separate**, so the chat can
  page through more than 64 unacknowledged messages without looping on page 1.
- **Slow UI:** a slow chat is disconnected safely. On reconnect, unacknowledged
  IDs replay and the UI deduplicates.
- **Resync:** receipt state resyncs from a persisted snapshot.
  - Presence can be coalesced, and is labeled as a current observation, not
    durable history.
  - Snapshot plus subscription carry a watermark (broker epoch and event
    revision), with explicit gap and reset semantics, so a reconnect or dropped
    events can't leave stale state unnoticed.
  - The session heartbeat doesn't prove a human is watching.

**5. Relation to #19 (inspection)**
- History and status reads stay non-consuming: they don't take inbox
  ownership or allocate sequence numbers.
- The chat, as a participant, claims, reads and acknowledges **its own** inbox
  through separate operations. This doesn't redefine inspection.
- The chat shows the bodies of messages addressed to the operator. Broader
  history defaults to metadata, with details on explicit request.
- Delivery stages reconcile per frozen recipient.

**6. Composing**
- Recipients come from a dedicated addressing field or an explicit leading
  command. Pasted `@text` in the body never widens the recipients.
- Before sending, the chat shows the chosen scope and a preview of the current
  recipients. The authoritative frozen recipients and exclusions exist only
  once the broker accepts, and are shown after sending (especially for `@all`).
- Enter inserts a newline; sending uses a separate explicit key.
- Sender and origin are shown outside the peer text. Control characters render
  as inert text.

**7. Audit**
- Operator sends and acknowledgments are recorded in `operator_audit`, as in
  part 1.
- Messages to the operator are ordinary messages with `origin: agent` and
  `to: operator`.

## PRs

1. **Operator inbox:** versioned protocol, messages and replies to the
   operator with quotas, operator acknowledgment with audit, and CLI
   `receive`/`ack`. Tests for restart and capacity.
2. **Session and synchronization:** bounded #19 inspection reads, plus the
   operator session's stream semantics (paging, credit, watermark, gap and
   reset). Tests with a scripted client.
3. **`collab operator chat`:** the Bubble Tea UI, plus a manual end-to-end
   demonstration.

**Later:** read-only viewers, forum groups by team or duo (#73), and links to
library entries.

## Out of scope

`attach` to agent seats (#69), team presets (#73), web UI (#36).

Related: #19, #21, #68, #73, #74, `docs/runtime-boundaries-v2.md`.

## PR1 contract: operator inbox (agreed by Claude and Codex, 2026-10-12)

**Scope:** messages to the operator, one-shot receive/received/ack operations,
the CLI, and a status count. The live session, stream and single-owner
guarantee are PR2.

**Known PR1 limitation:** the CLI is one-shot and has no exclusive owner. Two
concurrent receives can return the same messages. Mutations are serialized
through the hub and store transactions. The internal placeholder session
`operator-inbox` is storage bookkeeping only. It is never shown as presence or
counted as a connected session.

### Capability

- `protocol.Version = 5` (`OperatorInboxVersion = 5`).
- An agent may send to `operator` only if its negotiated version is ≥ 5;
  otherwise the send is rejected with "requires protocol_version 5".
- A v5 broker answers unknown operator requests with `unsupported_operation`.
  For a `malformed_frame` from an older broker, the CLI shows the error plus an
  upgrade hint, without claiming the broker lacks support.

### Agent → operator

- `to: "operator"` requires `durable` and `ack_requested`. The message is
  stored with `origin: agent` in the durable inbox of agent ID `operator`.
- Exact durable retries resolve before reply validation and quota, and consume
  nothing.
- **Replies:** if `in_reply_to` is set, it must name an existing message with
  origin `operator` whose frozen recipients include the sender's logical ID
  (independent of session). Otherwise the send is rejected with
  `invalid_reply`. A database error is `internal`, never `invalid_reply`.
  Without `in_reply_to`, the message is proactive.
- **Quotas,** checked atomically in the persist transaction and measured with
  `DurableWireBytes`; violations return `inbox_full`:
  - per logical sender: at most 16 pending messages and 512 KiB
  - the whole operator inbox: at most 256 messages and 8 MiB
  - the global pending and retained caps still apply
  - the agent inbox limit (32 messages, 2 MiB) is unchanged and doesn't apply
    to `operator`
  - a single message over 512 KiB is rejected
  - pending ends only with the operator's acknowledgment
- `operator` never registers and never goes through `ClaimPendingFor`.
- Agent ack frames can't touch operator-inbox rows; that returns
  `invalid_ack`.

### Wire (operator socket, UID-checked, one request per connection)

```go
type PageRequest struct {
    AfterSeq uint64 `json:"after_seq"`
    UpperSeq uint64 `json:"upper_seq"`
    Limit    int    `json:"limit,omitempty"` // omitted or 0 → 16; 1..32 allowed
}
type OperatorPage struct {
    Messages     []Message `json:"messages"`
    NextAfterSeq uint64    `json:"next_after_seq"`
    UpperSeq     uint64    `json:"upper_seq"`
    More         bool      `json:"more"`
}
// New Message fields:
//   PageRequest *PageRequest  `json:"page_request,omitempty"`
//   Page        *OperatorPage `json:"page,omitempty"`
//   MessageIDs  []string      `json:"message_ids,omitempty"`
```

**`operator_receive {page_request}` returns `{type: "operator_page", page}`.**
- `upper_seq = 0` starts a new scan. The broker captures `MAX(messages.seq)`
  from committed history (never the in-memory sequence, which includes welcome
  frames). It reads the bound, the rows and `more` in one read transaction.
- It returns unacknowledged operator-inbox messages with
  `after_seq < seq ≤ upper_seq`, in sequence order. The page is a live pending
  view: acknowledgments can remove rows. It isn't a historical snapshot.
- `next_after_seq` is the sequence of the last returned row, and stays
  unchanged for an empty page. `more` means pending rows remain in
  `(next_after_seq, upper_seq]`.
- An empty history (`upper_seq = 0`) gives an empty page with `more = false`;
  the next receive starts a new scan.
- **Byte budget:** the whole serialized frame stays at or below
  `MaxFrameBytes`, with an envelope reserve. When eligible pending rows exist,
  each page holds at least one message, since messages are at most 512 KiB.
  Payloads are never truncated, and empty pages are valid.
- **Cursors** are sequence-based and survive restarts of the same database.
  After a database replacement or restore, start a new scan; that case isn't
  reliably detectable.
- `after_seq > upper_seq`, `upper_seq` beyond the current committed maximum,
  or `limit` outside 0..32 return `malformed_frame` ("invalid cursor" or
  "invalid limit").
- Receiving changes no receipts. It writes one audit row with the bounds, the
  count and the result, never payloads. If that audit fails, the response is
  `internal` and nothing is returned.

**`operator_received {message_ids}`** is sent after the client has adopted a
page.
- The list must be non-empty, at most 32 IDs, deduplicated, and within the
  frame byte limit.
- All IDs are validated before any write. Any ID outside the operator inbox
  returns `invalid_ack`, with no changes.
- It commits `adapter_received` idempotently in both receipt tables, together
  with an audit row, in one transaction. Already received or acknowledged
  items keep their stage and timestamps.
- Sender notification goes to the original sender session only, on the first
  transition.
- The response is `ack` with stage `adapter_received` and per-ID `receipts`.
  `ReceiptState` gains an additive `message_id` field.

**`operator_ack {message_id}`** is the human's explicit receipt.
- It requires the receipt stage. The audit row, `durable_inbox` and
  `message_receipts` commit in one transaction.
- A repeat is idempotent: timestamps are kept, no quota is released twice, and
  no new notification goes out.
- A message ID outside the operator inbox returns `invalid_ack`, with no side
  effects.
- The original sender session is notified once, on the first transition. The
  sender can recover the stage later through a retry snapshot. A failed
  notification never rolls back the acknowledgment.
- "Acknowledged by operator" means received. It never means viewed,
  understood, approved or done.

### Adapters, CLI, status

- `wait_reply` accepts `from: "operator"` through a separate sender check.
  `ValidateAgentID` stays strict for registration. The Claude and Codex
  instructions explain how to write to the operator.
- An operator `in_reply_to` is shown as a correlation claim, not as proof that
  one message caused another.
- **CLI:**
  - `collab operator receive [--socket P] [--after SEQ] [--upper SEQ] [--limit N] [--json] [--timeout D]`
    (`--upper` echoes the returned `upper_seq` to continue the same scan)
    validates the whole response, writes its output, and only then sends
    `operator_received`. If writing output fails, no receipt is sent. A failed
    receipt is reported as a distinct error, and the printed page stays
    usable and replayable. The CLI doesn't persist cursors.
  - `collab operator ack [--socket P] [--json] [--timeout D] MESSAGE_ID`.
  - `collab operator send` gains `--in-reply-to ID`.
- **Status:** an additive `StatusSnapshot.OperatorPending *int`. `nil` means an
  older broker and is shown as "unknown", not 0. `DurablePending.Total`
  includes operator-inbox rows; `OperatorPending` is a separate view of those
  same rows.
- **#19:** inspection routes never change state.
