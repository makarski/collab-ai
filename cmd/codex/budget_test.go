package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"collab-ai/internal/bridge"
	"collab-ai/internal/budget"
)

func TestBudgetedOperatorStopsUnresponsiveAppServer(t *testing.T) {
	dir := t.TempDir()
	b, err := budget.Open(filepath.Join(dir, "budget.json"), 100)
	terminalCheck(t, err)
	defer b.Close()
	logPath := filepath.Join(dir, "interrupt.jsonl")
	t.Setenv("COLLAB_BUDGET_TEST_LOG", logPath)
	binary := filepath.Join(dir, "fake-codex")
	// No credentials or model requests. This host reports an overshoot, records
	// the interrupt, and deliberately never replies to it.
	terminalCheck(t, os.WriteFile(binary, []byte(`#!/bin/sh
trap '' TERM
printf '%s\n' '{"method":"thread/tokenUsage/updated","params":{"threadId":"fake-thread","turnId":"fake-turn","tokenUsage":{"total":{"totalTokens":107}}}}'
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$COLLAB_BUDGET_TEST_LOG"
done
`), 0700))
	input, writer := io.Pipe()
	defer writer.Close()
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	terminalCheck(t, err)
	defer output.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	launcher := codexLauncher{client: bridge.ClientConfig{AgentID: "budget-test", SocketPath: "/unused"}, binary: binary, budget: b}
	err = launcher.runOperator(ctx, operatorIO{input: input, output: output})
	if err == nil || !strings.Contains(err.Error(), "107 reported / 100 cap (7 overshoot)") {
		t.Fatalf("cap shutdown diagnostic: %v", err)
	}
	data, err := os.ReadFile(logPath)
	terminalCheck(t, err)
	if !strings.Contains(string(data), `"method":"turn/interrupt"`) || !strings.Contains(string(data), `"turnId":"fake-turn"`) {
		t.Fatalf("missing interrupt: %s", data)
	}
	if ctx.Err() != nil {
		t.Fatal("external timeout, not budget, ended process")
	}
}

type uninterruptibleInput struct {
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (r *uninterruptibleInput) Read([]byte) (int, error) { <-r.release; return 0, io.EOF }
func (r *uninterruptibleInput) Close() error             { r.once.Do(func() { close(r.closed) }); return nil }

func TestBudgetShutdownDoesNotWaitForUninterruptibleOperatorInput(t *testing.T) {
	dir := t.TempDir()
	b, err := budget.Open(filepath.Join(dir, "budget.json"), 100)
	terminalCheck(t, err)
	defer b.Close()
	binary := filepath.Join(dir, "fake-codex")
	terminalCheck(t, os.WriteFile(binary, []byte(`#!/bin/sh
printf '%s\n' '{"method":"thread/tokenUsage/updated","params":{"threadId":"fake","turnId":"turn","tokenUsage":{"total":{"totalTokens":107}}}}'
while IFS= read -r line; do :; done
`), 0700))
	input := &uninterruptibleInput{release: make(chan struct{}), closed: make(chan struct{})}
	defer close(input.release)
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	terminalCheck(t, err)
	defer output.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	launcher := codexLauncher{client: bridge.ClientConfig{AgentID: "budget-test", SocketPath: "/unused"}, binary: binary, budget: b}
	finished := make(chan error, 1)
	go func() { finished <- launcher.runOperator(ctx, operatorIO{input: input, output: output}) }()
	select {
	case err := <-finished:
		if err == nil || !strings.Contains(err.Error(), "107 reported / 100 cap") {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("uninterruptible stdin prevented budget shutdown")
	}
	select {
	case <-input.closed:
	case <-time.After(time.Second):
		t.Fatal("operator input was not closed")
	}
}

func TestOptionalBudgetRequiresACompleteLauncherConfiguration(t *testing.T) {
	b, err := optionalBudget("", 0, "")
	if err != nil || b != nil {
		t.Fatal("uncapped launch changed", err)
	}
	for _, args := range []struct {
		path  string
		limit int64
		relay string
	}{
		{"", 100, ""},
		{filepath.Join(t.TempDir(), "budget"), 0, ""},
		{filepath.Join(t.TempDir(), "budget"), 100, "relay.sock"},
	} {
		if b, err := optionalBudget(args.path, args.limit, args.relay); err == nil {
			if b != nil {
				b.Close()
			}
			t.Fatal("incomplete or relay-only cap accepted")
		}
	}
}

func TestNamedSelectionUsesSavedCapAndRejectsOverrides(t *testing.T) {
	t.Setenv("COLLAB_BUDGET_DIR", t.TempDir())
	store, err := budget.DefaultStore()
	terminalCheck(t, err)
	_, err = store.Create("task", 100)
	terminalCheck(t, err)
	for _, selection := range []budgetSelection{
		{name: "task", limit: 1000}, {name: "task", path: "override.json"},
	} {
		if b, err := selection.open(""); err == nil {
			b.Close()
			t.Fatal("named cap override accepted")
		}
	}
	if b, err := (budgetSelection{name: "task"}).open("relay.sock"); err == nil {
		b.Close()
		t.Fatal("relay accepted budget")
	}
	b, err := (budgetSelection{name: "task"}).open("")
	terminalCheck(t, err)
	defer b.Close()
	b.Observe("thread", 100)
	if b.Err() == nil {
		t.Fatal("saved cap was not enforced")
	}
}
