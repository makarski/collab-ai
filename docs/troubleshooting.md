# Troubleshooting

[Back to the quick start](../README.md#run).

| Symptom | What to check |
|---------|---------------|
| Missing socket / connection refused | Start the broker first and match every `--socket` to `COLLAB_SOCKET_PATH`. For a socket in your current directory, use `"$PWD/collab-ai.sock"` (uppercase `PWD`, no extra leading slash). |
| `duplicate_id` | Another session owns that agent ID. Close its adapter or choose a distinct ID; a second connection cannot take over the inbox. |
| Claude tools work but no automatic messages | Check channel opt-in, authentication, and organization policy. Use the [interactive channel launch](host-integration.md#claude-code). |
| Codex resume rejects permission flags | Resume with saved permissions; remove `--sandbox` / `--ask-for-approval` overrides. |
| Delivery stops after a disconnect | Restart the affected adapter or launcher with the same agent ID. Accepted, unacknowledged durable messages replay; use `resume` to also restore Codex conversation history. |

The broker refuses an existing socket path. After a crash, verify the broker is
no longer running before removing a stale socket. Normal shutdown cleans up its
own socket. The broker's environment variables are optional:
`COLLAB_SOCKET_PATH` defaults to `/tmp/collab-ai.sock`, and `COLLAB_DB_PATH` defaults
to `./collab-ai.db`.

For MCP registration errors, see [adapter troubleshooting](mcp.md#troubleshooting).
For host-specific behavior, see [Codex and Claude setup](host-integration.md).
