# Operator dashboard

Watch the broker without repeating `collab status`:

```sh
go build -o collab ./cmd/collab
./collab dashboard
./collab dashboard --socket /path/to/collab-ai.sock --interval 2s --timeout 3s
```

The socket defaults to `COLLAB_SOCKET_PATH`, then `/tmp/collab-ai.sock`.
The dashboard requires interactive stdin and stdout. For a pipe, script, or log,
use `collab status` or `collab status --json`. Set `NO_COLOR=1` for plain rendering.
Bubble Tea 2.0.9 keeps the project's Go 1.25 requirement.

## Controls

| Key | Action |
| --- | --- |
| Up / Down, `j` / `k` | Select a row; scroll full details or help |
| Page Up / Page Down | Move a page |
| Enter | Open or close full details |
| Tab | Switch between sessions and pending inboxes |
| `/` | Filter by agent ID; Enter or Escape finishes editing |
| `s` | Cycle session states: all, connected, disconnected, stale |
| Escape | Close details/help and clear the text filter |
| `r` | Refresh now, if no request is running |
| `?` | Open or close help |
| `q` / Ctrl+C | Quit the dashboard |

On wide terminals, the selected row's details appear alongside the roster.
On narrower terminals, Enter opens them. Selection follows the same agent and
session across refreshes, rather than a row number. If that row disappears, the
nearest remaining row is selected. The filter applies to either tab; the state
filter applies only to sessions. Terminals smaller than 35 columns or 12 rows
show a resize hint, while quit and refresh controls remain available.

## What the screen means

The header shows the latest request's broker health and socket reachability.
The roster and counts belong to the displayed, timestamped snapshot. Refreshes
run asynchronously, with **at most one status request in flight**. The default
delay is two seconds after a request completes; `--interval` accepts 1s–1m.
Each request has a three-second timeout by default; `--timeout` must be greater
than zero and no more than 30s. Manual refresh cannot overlap an active request.

After a failed or unsupported response, the last snapshot stays visible with a
**STALE** label. A snapshot also becomes stale when its broker timestamp is older
than the refresh interval plus request timeout. A degraded response replaces old
observations with the available live registry and explicitly unavailable metrics.
Enter opens details including the full bounded error, history availability, and
list limits. Refresh continues so a restarted or repaired broker can recover.

Session states describe transport ownership, historical disconnects, and stale
history—not whether a model is thinking, idle, or listening. A stale historical
session differs from a STALE retained snapshot. Last inbound activity is a broker
frame observation, not model activity. Legacy delivery counts remain unavailable.

**Pending inboxes** lists counts once per logical agent ID, including offline
recipients. These counts are not duplicated across historical session rows.
Only explicit, committed agent acknowledgment removes pending delivery; a reply,
adapter receipt, or host submission alone does not. The aggregate total covers
all pending recipient deliveries, even when the recipient list is truncated.
Truncated lists may omit identities; absence is not proof of zero pending work
or an offline agent. See the [status contract](status.md) for precise semantics.

## Read-only boundary

The dashboard uses the existing status client and Unix socket. It never opens
SQLite, registers an agent, reads message bodies, consumes an inbox, acknowledges
delivery, or allocates a message sequence. Closing it cancels outstanding I/O
and restores the terminal; the broker and agent processes keep running.
Peer-supplied identifiers and errors are escaped before rendering. Long errors
are bounded and explicitly marked as truncated; color is never the only indicator.

Message timelines and disconnect controls are separate follow-up work.

## Recorded validation

A PTY smoke test on macOS with Go 1.25.3 (2026-09-24) used an isolated broker,
two connected fixture agents, one disconnected agent, and two pending deliveries
including one offline recipient. At 80×24 and 120×32, the roster, selected-session
details, pending-inbox filter, and help were exercised. Pausing the test broker
produced a timeout and a STALE retained snapshot; resuming it restored ready
observations automatically. Both `q` and Ctrl+C exited successfully and restored
the alternate screen and cursor. A subsequent status request still reported the
same two owners and two pending deliveries. The fixture was then stopped.

The full race suite and vet passed. Automated tests cover cancellation and obsolete
responses, timeout/recovery, selection and filtering, null/truncated observations,
terminal sizes, escaping, and real-broker non-interference. No live user inbox was
used for this validation.
