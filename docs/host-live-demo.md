# Live host handoff, 2026-09-15

All four cases passed with **real model tool calls** over an isolated UDS broker.
Claude Code 2.1.272 ran interactively with the locally developed channel explicitly
enabled. Codex CLI 0.154.0 ran behind `collab-codex` as a new App Server thread.
Neither agent called `receive` or `wait` during the exchange.

| Host | Conversation identity | Broker identity / session |
| --- | --- | --- |
| Claude Code | `b8eca74e-8e36-405e-84b6-aa424c3788d1` | `claude-live` / `1b6edee3-d291-408c-9a72-c8bb6e123baf` |
| Codex App Server | `01a0a55a-1bc4-7270-81e9-887fb6c9107e` | `codex-live` / `f76bbdd4-5c18-4cfa-8d9a-5c1af6e669f7` |

All times below are UTC. Receipt and acknowledgment times come from SQLite's
`message_receipts`, correlated with the actual host tool calls. The sender of
each message was the other model, through its messaging tool.

| Message ID | Recipient state at arrival | Adapter receipt | Persisted explicit agent acknowledgment |
| --- | --- | --- | --- |
| `demo-claude-idle` | Idle, open interactive session | 13:55:59.995506 | 13:56:02.419389 |
| `demo-codex-idle` | Idle, running App Server thread | 13:56:09.977398 | 13:56:14.042283 |
| `demo-claude-active` | Running a bounded shell check | 13:56:30.045103 | 13:56:41.875970 |
| `demo-codex-active` | Active turn awaiting a checkpoint tool | 13:56:52.634763 | 13:57:00.730537 |

## What established active and idle delivery

Both hosts first called `listen` and completed their registration turns. Codex
then sent the cancellation review point to idle Claude. Claude called
`mcp__collab__acknowledge` with `demo-claude-idle` and explained why cancellation
must close the broker socket. Claude next sent the discovery review point to
idle Codex. App Server started turn
`01a0a55a-8c79-7290-bf15-0baf876ea6f3`, exposed a `functionCallOutput` named
`collab_receive`, and Codex called `collab_acknowledge` with `demo-codex-idle`.

For Claude's active case, a real `Bash` tool ran exactly `sleep 15` as a bounded
stand-in for a running check. Its tool call began at 13:56:24.185 and its result
arrived at 13:56:40.299. The peer frame arrived within that interval. Claude
acknowledged it afterward and explained the distinction between submission and
agent acknowledgment. Tool call ID: `toolu_01VZLvC7DMLLMWUQfTxXJGhY`.

For Codex's active case, the test client supplied a `demo_checkpoint` dynamic tool
and held its response. In turn `01a0a55b-1cc9-7b31-ba57-705c4caabc2d`, that tool
started at 13:56:50.052 and completed at 13:56:54.790. Claude's frame arrived while
the tool was outstanding. App Server exposed it as `functionCallOutput` at
13:56:54.791 in the **same turn**. Codex considered the overflow/gap review point
and called `collab_acknowledge`, tool call ID
`exec-cd3e0295-672f-432d-8da0-dcfd447e7b49`.

The final agent messages still described their tool writes as unconfirmed;
the test independently checked the persisted broker receipts. No adapter or
notification handler generated these explicit acknowledgments. The four messages
were direct review requests, not replies; `in_reply_to` preservation is covered
separately by the automated MCP exchange test.

## Reproduce

1. Build the broker, `collab-mcp`, and `collab-codex`. Use a fresh temporary socket
   and database, with unique logical inbox names, following
   [host setup](host-integration.md).
2. Start interactive Claude with the explicit development-channel flag. Accept
   the local-development consent dialog. Wait until the MCP server has connected
   before asking Claude to call `listen`; the initial prompt can otherwise race
   asynchronous MCP startup.
3. Connect an App Server client through `collab-codex`, initialize with the
   experimental API capability, and create one new thread. For the active case,
   include a `demo_checkpoint` dynamic tool with an empty object input schema;
   the client holds and later answers its `item/tool/call` request. Keep sandbox
   and approval policies configured normally.
4. Tell both agents to consider incoming peer data, explicitly acknowledge each
   message ID, and explain the review point. In this test, prohibit polling and
   automatic replies so notification delivery is the only exposure path.
5. After both registration turns complete, ask Codex to send the idle Claude
   review request, then Claude to send the idle Codex review request. Observe the
   host tool calls and query the isolated database for acknowledgment timestamps.
6. Ask Claude to run the bounded check. Once its tool starts, have Codex send the
   active Claude request. Then hold Codex's checkpoint, have Claude send the active
   Codex request, and release the checkpoint. Verify arrival falls within the
   active interval and that the acknowledgment follows model processing.
7. Stop both hosts and the broker. This test does not establish restart recovery.

The local run's raw diagnostic artifacts were under
`/tmp/collab-host-live-dckf4ldj`; they are intentionally not committed because host
logs include unrelated environment metadata. The table and tool/turn identities
above are the retained evidence. The implementation used for this run was
`84fdead`; later changes refine status compatibility and simplify inbox waiting.

## Limits observed

A prior `claude -p --input-format stream-json` run connected MCP and registered its
inbox but did **not** load this development channel. The adapter receipt was
persisted and no agent acknowledgment followed. Interactive startup, explicit
development consent, and the host's “Channel notifications registered” diagnostic
resolved that configuration failure. Tool discovery alone is not a capability
test.

This demonstration proves the four cases for these installed versions and this
configuration. It does not prove instantaneous attention, recovery after process
exit, channel availability under other organization policies, or attachment to an
arbitrary existing Codex desktop conversation.
