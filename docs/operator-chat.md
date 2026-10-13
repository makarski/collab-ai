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

## PR2 contract: live operator session (round 2, Claude/Codex, 2026-10-13)

**Goal:** one long-lived operator connection that the chat (PR3) builds on. It
owns the operator inbox and learns about changes as they happen.

**Scope:** session, events and snapshot ship together, because resync can't
be separated from a lossy notification stream. History is a follow-up PR
(PR2b) before the chat.

### Session and ownership

- `operator_session_open {request_id, client}` is the first frame on an
  operator-socket connection (UID-checked as before). The response comes first
  on the connection: session ID, capabilities, and the epoch.
- **One writable owner.**
  - A second open while a session is live gets `owner_busy`, with the current
    owner's opened-at time and connection ID. No takeover in this PR.
  - Ownership checks, claim, release, and inbox `received`/`ack` mutations are
    all serialized in the hub goroutine.
  - Each claim gets a connection generation. A disconnect releases ownership
    only if its generation still matches, so a late callback can never release
    a newer owner.
- **Audit:**
  - If the open audit fails, no ownership is granted (`internal`).
  - Closing always releases ownership. If the close audit fails, the error is
    logged and the release still happens.
  - Open, snapshot and events never record receipts.
- **One-shot requests while a session owns the inbox:**
  - `operator_send` stays available, and its sends produce events in the live
    session.
  - `operator_receive` stays available (read-only).
  - `operator_received` and `operator_ack` are refused with `owner_busy`.
  - The CLI's `receive` reports this explicitly: "a live operator session owns
    the inbox; the messages were printed but not confirmed" (exit 1), never
    success.

### Requests and responses inside a session

- Each request needs a `request_id` of 1 to 128 bytes, unique among the
  session's in-flight requests. Each response echoes it. Unsolicited events
  carry none.
- At most 8 requests may be in flight. A violation, or an unparseable frame,
  gets an error response (with the `request_id` when it could be parsed) and
  the session is closed.
- `request_id` is correlation only, not retry deduplication. If an outcome is
  unknown, the durable `message_id` retry rules from PR1 apply.
- Allowed requests:
  - `operator_send`, `operator_receive`, `operator_received`, `operator_ack`,
    with PR1 semantics
  - `operator_snapshot`
  - `operator_message_stages` (below)

### Events (broker → session)

Events are notifications, not payloads.

| Event | When | Fields |
| --- | --- | --- |
| `conversation_changed` | a new operator-sent message (from any session or one-shot send) or a new message to the operator is committed | seq, message ID, direction (`sent`/`received`) |
| `inbox_changed` | an inbox message arrives, is received, or is acknowledged | head seq, pending count |
| `stage_changed` | a committed **first** transition of a recipient stage on an operator-sent message, or a committed reclaim that moves a pending delivery to a new session (stage `accepted`) | message ID, agent ID, session ID, stage |
| `presence_changed` | an agent connects or disconnects | agent ID, session ID, state, negotiated version |

- No events for exact retries, or for transactions rolled back after an audit
  failure.
- Operator provenance is matched by `origin = operator` and the operator
  inbox, never by a peer-supplied `from`.

### Watermark and resync

- Every event carries `{epoch, revision}`. The epoch is a random ID per broker
  start. The revision increases by exactly 1 per event within an epoch, and
  every event goes to the session. Events are **not coalesced**.
- **Cut:** events are delivered only after the session's first
  `operator_snapshot`. The hub takes the snapshot and sets the subscription
  baseline `R` in one serialized step. The snapshot response, carrying
  watermark `R`, is written before any event with a revision above `R`.
- The client discards events with revision ≤ `R`, and resyncs on any gap or
  epoch change.

### Flow control

- **Separate queues:** events (bounded, 256) and responses (bounded by the
  in-flight cap). A single writer goroutine with a write deadline. The hub
  never blocks on the socket.
- **Overflow:** event-queue overflow sets a sticky `resync_required` flag. The
  queued events are discarded, and the marker goes out through a reserved slot
  outside the full queue, so it is always delivered. The flag clears only at
  the next snapshot cut, so a later overflow can't be lost.
- **Slow reader:** a session that can't be written within the deadline is
  closed, and its ownership released. A reconnect starts with a new snapshot.

### Snapshot bounds

`operator_snapshot` returns, at watermark `R`:
- `agents`: connected agents with session ID and version, at most 100, plus a
  `truncated` flag
- `inbox`: head seq and pending count
- `recent_sent`: the newest operator-sent messages, at most 16, each with
  per-recipient stages, within the frame byte budget

If a message's receipts don't fit, it carries `receipts_truncated: true`, and
the client pages them with `operator_message_stages`. The whole response stays
at or below `MaxFrameBytes`, and payloads are never included or truncated.

`operator_message_stages {request_id, message_id, after_agent, limit}` pages
per-recipient stages of one operator-sent message, in agent-ID order:
- `limit` 1 to 256
- `next_after_agent` and `more` for the next page
- `invalid_message` for an unknown or non-operator message

### Clarifications (round 3)

**1. Snapshot as a bounded view.**
- `agents`, ordered by agent ID, has `agents_total` and `agents_truncated`.
- `recent_sent`, newest seq first, has `recent_sent_truncated`, which also
  covers omissions caused by the byte budget.
- A snapshot **replaces** the client's baseline. Entities left out are unknown
  or outside the bounded view, never "offline" or "unreceived".
- `recent_sent` entries hold metadata only: message ID, seq, timestamp, scope
  and per-recipient stages. They never include payloads.

**2. Stage pages are live; ordering is by watermark.**
- Stages are **not** monotonic. When an agent reconnects, `ClaimPendingFor`
  moves its pending messages to the new session and resets `received` to
  `accepted`. That delivery behavior stays unchanged.
- Each per-recipient state is (session ID, stage). Snapshots, stage pages and
  `stage_changed` events all carry the recipient's current session ID.
- The client tracks, per (message, agent), the watermark of the update it last
  applied:
  - Events apply in revision order.
  - A page entry read at watermark `W` applies only if `W` is newer than that
    entity's stored watermark.
  - So a later `accepted` for a new session replaces an earlier
    `adapter_received`. `agent_acknowledged` is terminal.
  - The comparison applies to every per-entity update, including an event
    that arrives after a newer page.
  - An entry the client doesn't know yet initializes at the page's `W`, even
    when `W` equals the baseline `R`.
  - A new epoch clears the baseline. Revision numbers are never compared
    across epochs.
  - Gaps still force a resync.
- **Reclaim:** when a reconnect moves pending operator-sent messages to a new
  session, the hub emits a `stage_changed` event per affected message: stage
  `accepted`, with the new session ID. These are committed transitions, so no
  event is lost silently.
- The recipient set is frozen, so the exclusive `after_agent` cursor is
  stable.
- `limit` omitted or 0 means 64; 1 to 256 is valid; anything else is
  `malformed_frame`. An unknown agent cursor is `malformed_frame` too.
- `next_after_agent` is the last returned agent, unchanged on an empty page.
  `more` is exact. The response stays within the frame bound.
- No receipt side effects.

**Audit policy:**
- audited: session open and close, and everything that returns bodies or
  changes state (`receive`, `received`, `ack`, `send`)
- not audited: the metadata reads `snapshot` and `message_stages`

**3. Barrier and queues.**
- Responses and events leave through one ordered writer, with separate
  capacity accounting.
- At each snapshot cut, including repeated snapshots, queued events are
  discarded. The snapshot response is written before any event above `R`.
- **Overflow:** deltas are suppressed until the next cut. The revision still
  advances. `resync_required` tells the client to stop applying deltas.
- Delivery of the marker is guaranteed only while the connection is writable;
  otherwise the write deadline closes the session.
- **Reserved slots:** two control slots for the resync marker and protocol
  errors, outside the 8 in-flight responses, so an error about a ninth request
  can always be written.
- An in-flight ID is released only after its response has been written, not
  when it's queued.

**4. Errors inside a session.**
- An unknown operation or invalid arguments get a correlated error response,
  and the session stays open.
- The session closes only for an envelope or framing violation (missing or
  oversized `request_id`, an unparseable frame) or an in-flight violation.
- A second `operator_session_open` on an open session gets a correlated
  `invalid_state` error.
- Requests, including mutations, are allowed before the first snapshot; no
  events are delivered until the baseline exists.

**5. Idle owners.**
- A session that never takes a snapshot, or stays idle, keeps ownership until
  its connection closes.
- There is no idle expiry and no takeover. The operator closes the chat to
  release it.
- Idle reading never times out. The write deadline applies only to pending
  writes, so an idle owner never blocks the hub or other connections.

### Wire (exact names)

New `Message` fields:

| Field | Type | Used in |
| --- | --- | --- |
| `request_id` | string | every session request, and its response |
| `client` | string | `operator_session_open` |
| `session` | `*SessionInfo` | `operator_session` response |
| `owner` | `*SessionInfo` | `owner_busy` error |
| `snapshot` | `*OperatorSnapshot` | `operator_snapshot` response |
| `stages` | `*StagePage` | `operator_message_stages` response |
| `event` | `*SessionEvent` | `type: "event"` |
| `after_agent` | string | `operator_message_stages` request |
| `limit` | int | `operator_message_stages` request |

```go
type Watermark struct {
    Epoch    string `json:"epoch"`
    Revision uint64 `json:"revision"`
}
type SessionInfo struct {
    SessionID    string    `json:"session_id"`
    Epoch        string    `json:"epoch,omitempty"`
    Capabilities []string  `json:"capabilities,omitempty"`
    OpenedAt     time.Time `json:"opened_at"`
}
type RecipientStage struct {
    AgentID   string `json:"agent_id"`
    SessionID string `json:"session_id"`
    Stage     string `json:"stage"`
}
type SentSummary struct { // metadata only, never payload
    MessageID         string           `json:"message_id"`
    Seq               uint64           `json:"seq"`
    TS                time.Time        `json:"ts"`
    To                string           `json:"to"`
    Recipients        []RecipientStage `json:"recipients"`
    ReceiptsTruncated bool             `json:"receipts_truncated"`
}
type AgentPresence struct {
    AgentID         string `json:"agent_id"`
    SessionID       string `json:"session_id"`
    ProtocolVersion int    `json:"protocol_version"`
}
type OperatorSnapshot struct {
    Watermark           Watermark       `json:"watermark"`
    Agents              []AgentPresence `json:"agents"`
    AgentsTotal         int             `json:"agents_total"`
    AgentsTruncated     bool            `json:"agents_truncated"`
    InboxHeadSeq        uint64          `json:"inbox_head_seq"`
    InboxPending        int             `json:"inbox_pending"`
    RecentSent          []SentSummary   `json:"recent_sent"`
    RecentSentTruncated bool            `json:"recent_sent_truncated"`
}
type StagePage struct {
    Watermark      Watermark        `json:"watermark"`
    MessageID      string           `json:"message_id"`
    Recipients     []RecipientStage `json:"recipients"`
    NextAfterAgent string           `json:"next_after_agent"`
    More           bool             `json:"more"`
}
type SessionEvent struct {
    Kind            string    `json:"kind"` // conversation_changed | inbox_changed | stage_changed | presence_changed | resync_required
    Watermark       Watermark `json:"watermark"`
    Seq             uint64    `json:"seq,omitempty"`              // conversation_changed
    MessageID       string    `json:"message_id,omitempty"`       // conversation_changed, stage_changed
    Direction       string    `json:"direction,omitempty"`        // sent | received
    HeadSeq         uint64    `json:"head_seq,omitempty"`         // inbox_changed
    Pending         *int      `json:"pending,omitempty"`          // inbox_changed
    AgentID         string    `json:"agent_id,omitempty"`         // stage_changed, presence_changed
    SessionID       string    `json:"session_id,omitempty"`       // stage_changed, presence_changed
    Stage           string    `json:"stage,omitempty"`            // stage_changed
    State           string    `json:"state,omitempty"`            // presence_changed: connected | disconnected
    ProtocolVersion int       `json:"protocol_version,omitempty"` // presence_changed
}
```

**Types and errors**

- Request types: `operator_session_open`, `operator_snapshot`,
  `operator_message_stages`.
- Response types:
  - `operator_session` (open)
  - `operator_snapshot`
  - `operator_message_stages`
  - PR1 responses (`ack`, `operator_page`, `error`), now carrying `request_id`
- Events: `{"type": "event", "event": {...}}`, with no `request_id`.
- Errors: `owner_busy` (with `owner`), `invalid_state`, `invalid_message`,
  plus the existing codes.
- Capabilities in the open response: `send`, `receive`, `received`, `ack`,
  `snapshot`, `message_stages`, `events`.

### Compatibility

- Agents are unaffected; no agent protocol bump.
- A broker without sessions answers `operator_session_open` with
  `unsupported_operation`, or `malformed_frame` for build.132. The CLI reports
  either clearly.

### Out of scope (PR2)

- History (PR2b): `operator_history`, newest-first operator-sent and
  operator-addressed messages with bodies, with a stable scan bound and byte
  budget, non-consuming.
- Takeover of a stuck session, read-only viewers, the chat UI (PR3), and team
  or forum groups (#73).
