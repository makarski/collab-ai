# Architecture

[Back to the README](../README.md).

```mermaid
flowchart TB
    subgraph local["One machine"]
        subgraph codexPath["Managed Codex: automatic listening"]
            human["Human operator"]
            codexLauncher["collab-codex --terminal<br/>Launch command"]
            codexUI["Normal Codex terminal UI"]
            codexProxy["Proxy and listener<br/>Inside collab-codex"]
            codexHost["Codex App Server<br/>One managed thread"]
            codexRelay["MCP stdio relay<br/>collab_runtime tools"]
            human -.->|"Launches"| codexLauncher
            codexLauncher -.->|"Starts"| codexUI
            codexLauncher -.->|"Runs"| codexProxy
            codexProxy -.->|"Starts"| codexHost
            codexHost -.->|"Starts"| codexRelay
            human <-->|"Prompts, output and approvals"| codexUI
            codexUI <-->|"WebSocket / private Unix socket"| codexProxy
            codexProxy <-->|"App Server / stdio<br/>Tools and incoming peer context"| codexHost
            codexHost <-->|"MCP / stdio"| codexRelay
            codexRelay <-->|"MCP / private Unix socket<br/>Same listener and inbox owner"| codexProxy
        end

        subgraph claudePath["Claude channel: automatic listening"]
            claude["Claude Code<br/>Channel opt-in and org policy required"]
            claudeMcp["collab-mcp<br/>--claude-channel --auto-listen"]
            claude <-->|"MCP / stdio<br/>Tools and channel notifications"| claudeMcp
        end

        subgraph manualPath["Alternative: manual inbox checks"]
            agent["Codex or Claude Code"]
            manualMcp["collab-mcp<br/>Default mode"]
            agent <-->|"MCP / stdio<br/>send, receive, wait_reply, acknowledge"| manualMcp
        end

        codexProxy <-->|"Broker protocol / Unix socket"| broker["collab-ai broker<br/>One owner per logical inbox"]
        claudeMcp <-->|"Broker protocol / Unix socket"| broker
        manualMcp <-->|"Broker protocol / Unix socket"| broker
        broker <-->|"Persist state and replay unacknowledged messages"| db[("SQLite<br/>Messages, sessions and receipts")]
    end
```

Dotted arrows show startup; solid arrows show communication. Choose one adapter
path per agent session and share one broker. Automatic listening lasts while the
host process is running. The broker currently supports **local Unix sockets
only**, including for durable inbox recovery.

See [host integration](host-integration.md) for lifecycle and compatibility details,
and the [wire protocol](protocol.md) for broker routing and storage.
