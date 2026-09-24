package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"collab-ai/internal/bridge"
)

func TestCodexArgumentsRemainCodexOwned(t *testing.T) {
	for _, args := range [][]string{
		{"resume", "--last", "-m", "chosen-model", "-C", "/path with spaces", "-s", "read-only", "-a", "never"},
		{"fork", "saved-id", "-c", "model_reasoning_effort=high"},
		{"--", "--remote=literal prompt"},
	} {
		terminalCheck(t, validateTerminalArgs(args))
	}
	for _, args := range [][]string{{"--remote", "unix:///other.sock"}, {"resume", "--remote=ws://other"}} {
		if validateTerminalArgs(args) == nil {
			t.Fatalf("accepted competing remote: %v", args)
		}
	}
}

func TestTerminalForwardsExactArgumentsToCodex(t *testing.T) {
	endpoint, _ := runtimeFixture(t)
	dir := t.TempDir()
	binary := filepath.Join(dir, "codex-fixture")
	record := filepath.Join(dir, "argv")
	t.Setenv("COLLAB_ARGV_TEST_PATH", record)
	terminalCheck(t, os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$COLLAB_ARGV_TEST_PATH\"\n"), 0700))
	args := []string{"resume", "saved-id", "-C", "/path with spaces", "-c", `model_reasoning_effort="high"`, "--", "--remote=literal prompt"}
	cfg := bridge.ClientConfig{AgentID: "args-test", SocketPath: endpoint.ln.Addr().String()}
	terminalCheck(t, runTerminal(context.Background(), cfg, binary, args))
	data, err := os.ReadFile(record)
	terminalCheck(t, err)
	got := strings.Split(string(bytes.TrimSuffix(data, []byte{0})), "\x00")
	if len(got) != len(args)+2 {
		t.Fatalf("argument count changed: %v", got)
	}
	if got[0] != "--remote" || !strings.HasPrefix(got[1], "unix://") {
		t.Fatalf("missing managed remote endpoint: %v", got[:2])
	}
	if !reflect.DeepEqual(got[2:], args) {
		t.Fatalf("Codex arguments changed: %v", got[2:])
	}
}
