package main

import (
	"context"
	"strings"
	"testing"

	"collab-ai/internal/bridge"
)

func TestAutoListenRequiresChannel(t *testing.T) {
	err := run(context.Background(), bridge.ClientConfig{AgentID: "worker", SocketPath: "/unused"}, false, true)
	if err == nil || !strings.Contains(err.Error(), "--auto-listen requires --claude-channel") {
		t.Fatalf("invalid startup must fail before opening MCP: %v", err)
	}
}
