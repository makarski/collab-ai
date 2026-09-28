package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"collab-ai/internal/bridge"
	"collab-ai/internal/budget"
	"collab-ai/internal/host"
)

func TestRestrictedMalformedHostOutputPreservesParseError(t *testing.T) {
	p := &host.Proxy{RestrictedOperator: true}
	err := readHostFrames(context.Background(), p, strings.NewReader("not-json\n"))
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		t.Fatalf("parse error hidden by disconnect handling: %v", err)
	}
}

func TestRestrictedUnexpectedHostDisconnectPoisonsAccounting(t *testing.T) {
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	terminalCheck(t, err)
	defer output.Close()
	p := host.NewProxy(host.NewWire(output), host.NewWire(output))
	p.RestrictedOperator = true
	err = readHostFrames(context.Background(), p, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "accounting is unconfirmed") {
		t.Fatalf("unexpected native EOF was normalized: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := readHostFrames(ctx, p, strings.NewReader("")); err != io.EOF {
		t.Fatalf("intentional shutdown changed: %v", err)
	}
}

func TestRestrictedFailedExecDoesNotPoisonBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	b, err := budget.Open(path, 100)
	terminalCheck(t, err)
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	terminalCheck(t, err)
	defer output.Close()
	launcher := codexLauncher{client: bridge.ClientConfig{AgentID: "test", SocketPath: "/unused"},
		binary: filepath.Join(t.TempDir(), "missing"), budget: b, restricted: true}
	err = launcher.runOperator(context.Background(), operatorIO{
		input: io.NopCloser(strings.NewReader("")), output: output})
	terminalCheck(t, b.Close())
	if err == nil {
		t.Fatal("missing executable succeeded")
	}
	b, err = budget.Open(path, 100)
	terminalCheck(t, err)
	b.Close()
}
