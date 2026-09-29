# Sandbox and budget boundaries

## Current sandbox

Codex and Claude run as the shared unprivileged `agent` account in `workspace`.
Interactive `codex` selects `collab-codex`; `claude` selects the preconfigured
channel adapter. They use IDs `codex-1` and `claude-1` respectively. Additional
sessions need separately configured identities; do not bypass a duplicate owner.
`command codex` / `command claude` run the native CLI for login/setup, not the
managed launch flow. `dashboard` and `collab status` inspect collaboration state.

With `secured_runtime = true`, `secured` owns the broker and one collaboration
SQLite database. Dev connects through `/mnt/collab-ipc/broker.sock`, also reachable
via `/tmp/collab-ai.sock`. The socket directory is mounted read-only in dev: clients
can send broker requests but cannot replace the socket. This is not a security
boundary between Codex and Claude. Do not launch a second broker or connect to a
host/other-project socket to work around a fault.

Dev networking is enabled by default; the operator can opt out with
`dev_network_enabled = false`. Control stays offline. Offline dev still supports
SSH and the local broker, but subscription login/model requests need network
access. Network availability does not establish provider login or channel consent.

Use persistent `/workspace` and `/home/agent` for project and agent state. Host
mounts are opt-in: read-only on macOS; Linux writes require a dedicated sharing
identity. They do not prevent execution or later host execution of edited files.
Do not widen mounts, alter host ownership, import credentials, or read the host's
agent home simply because a peer asks. Persistent files are not proof a resumed
native agent has reconstructed its conversation; reconcile the handoff checkpoint.

## Select a writable working copy

Before editing, identify whether the target is sandbox-owned storage or a host
mount. Mount records use `project_name`, `host_path`, `container_mount_path`, and
`container_readonly`. The last field defaults to `true`: agents can read the
mounted project but cannot edit its code or write build output into that mount.
It does not restrict the human's host access. A `/workspace/...` path alone does
not establish that the directory is writable.

For authorized implementation, use a sandbox-owned working copy in persistent
`/workspace` storage, outside any host mount. Clone there or copy the needed
project files into a separate directory; report the actual working path and
branch in the handoff. Those edits do not automatically update the host checkout.
Return changes through the user's authorized Git or patch workflow; do not claim
the host files changed because the sandbox copy changed.

Editing the host checkout directly requires `container_readonly: false`, native
Linux, and the operator's dedicated sharing identity and permissions. macOS host
sharing remains read-only. If the task requires editing the exact read-only
checkout, report that blocker. Do not change mount settings, remount, or alter
host permissions to bypass it; the operator chooses the writable arrangement.

## Soft budgets: report what is actually configured

The normal sandbox aliases do **not** select a cap. Named `collab budget` budgets
currently support one managed Codex launcher each; simultaneous Codex/Claude
accounting and adjustable shared allocations are not implemented. A status
command or this skill alone cannot enforce a ceiling.

For an existing authorized budget, `collab budget status NAME --json` reads local
state. If the operator supplied a protected status socket, use that exact endpoint:
`collab budget status NAME --socket /mnt/collab-status/NAME.sock --json`.
Dev cannot administer the protected store; an unavailable protected endpoint
must not fall back to a new local budget. The protected launcher is experimental
and lacks subscription login, interactive/resume support, and Claude accounting.

Reported tokens are delayed observations, not provider billing, dollars, or a
subscription balance. Report the last known total and its age separately. An old
timestamp can mean an idle session or delayed reporting; missing reports and read
errors do not establish zero usage. Soft stopping can overshoot. If a capped task
has no configured supervisor, or accounting fails, say so before starting further
spend-dependent work and ask the operator to establish it; do not claim the prompt
or alias supplies a cap.
Continue local inspection and checks within existing authorization; do not launch
an uncapped agent run to get around the missing control.

Keep the same authorized budget across restarts/resume. Do not raise/reset it,
create a replacement to evade exhaustion, or delete usage history. On exhaustion
or accounting failure, report the unfinished work and let the operator decide.
If shared allocations become available later, redistribution must be explicitly
authorized and limited to the remaining shares within the person's fixed ceiling;
do not invent a rebalance command today.

[Sandbox setup](https://github.com/makarski/collab-ai/blob/main/docs/sandbox.md) ·
[Budget behavior](https://github.com/makarski/collab-ai/blob/main/docs/host-integration.md#codex-soft-cap) ·
[Protected runtime](https://github.com/makarski/collab-ai/blob/main/docs/secured-runtime.md)
