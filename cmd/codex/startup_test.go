package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"collab-ai/internal/bridge"
)

func TestMissingBrokerSocketFailsBeforeStartingCodex(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "missing.sock")
	cfg := bridge.ClientConfig{AgentID: "startup-test", SocketPath: socket}
	err := runTerminal(context.Background(), cfg, "/must-not-start-codex", nil)
	if err == nil || !strings.Contains(err.Error(), socket) {
		t.Fatalf("expected missing broker path diagnostic, got %v", err)
	}
}

func TestBrokerSocketCannotBeRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-socket")
	terminalCheck(t, os.WriteFile(path, nil, 0600))
	err := validateBrokerSocket(path)
	if err == nil || !strings.Contains(err.Error(), "not a Unix socket") {
		t.Fatalf("expected non-socket diagnostic, got %v", err)
	}
}
