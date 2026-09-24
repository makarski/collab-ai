package dashboard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"collab-ai/internal/protocol"
)

func fixture(t *testing.T, fetch fetchFunc) *model {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return newModel(ctx, cancel, Config{Socket: "/unused", Interval: time.Second, Timeout: time.Second, NoColor: true}, fetch)
}
func ready() protocol.StatusSnapshot {
	now, connected := time.Now(), 2
	return protocol.StatusSnapshot{SchemaVersion: 1, Health: "ready", Reachable: true, SnapshotAt: &now, ConnectedSessions: &connected,
		HistoryAvailable: true, SessionLimit: 100, DurablePending: &protocol.PendingStatus{Total: 3, RecipientLimit: 100,
			Recipients: []protocol.PendingRecipient{{AgentID: "codex", Pending: 2}, {AgentID: "offline", Pending: 1}}},
		Sessions: []protocol.SessionStatus{{AgentID: "claude", SessionID: "claude-1", State: "transport_connected"},
			{AgentID: "codex", SessionID: "codex-1", State: "transport_connected"},
			{AgentID: "codex", SessionID: "codex-old", State: "disconnected"}}}
}
func apply(m *model, s protocol.StatusSnapshot, err error) {
	m.refresh()
	m.Update(resultMsg{id: m.request, snapshot: s, err: err})
}
func press(m *model, key string) tea.Cmd {
	code := []rune(key)[0]
	switch key {
	case "enter":
		code = tea.KeyEnter
	case "tab":
		code = tea.KeyTab
	case "esc":
		code = tea.KeyEscape
	case "backspace":
		code = tea.KeyBackspace
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: code, Text: key})
	return cmd
}
func requireText(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestRefreshSerializesRequestsAndRejectsOldResults(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	m := fixture(t, func(ctx context.Context, _ string) (protocol.StatusSnapshot, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ready(), nil
	})
	done := make(chan tea.Msg, 1)
	go func() { done <- m.Init()() }()
	<-started
	if cmd := m.refresh(); cmd != nil {
		t.Fatal("overlapping fetch scheduled")
	}
	m.Update(resultMsg{id: 0, snapshot: ready()})
	assertEqual(t, "obsolete response keeps request active", m.inFlight, true)
	assertEqual(t, "obsolete response not displayed", m.haveSnapshot, false)
	close(release)
	m.Update(<-done)
	assertEqual(t, "request completed", m.inFlight, false)
	assertEqual(t, "result applied", m.haveSnapshot, true)
	prior := m.request
	if m.refresh() == nil {
		t.Fatal("manual refresh not scheduled")
	}
	m.Update(resultMsg{id: prior, snapshot: protocol.UnavailableStatus()})
	assertEqual(t, "new request remains active", m.inFlight, true)
	assertEqual(t, "late response ignored", m.latest.Health, "ready")
	if _, cmd := m.Update(refreshMsg(prior)); cmd != nil {
		t.Fatal("superseded timer scheduled a fetch")
	}
}

func TestQuitCancelsOutstandingFetchAndIgnoresResult(t *testing.T) {
	started := make(chan struct{})
	m := fixture(t, func(ctx context.Context, _ string) (protocol.StatusSnapshot, error) {
		close(started)
		<-ctx.Done()
		return protocol.UnavailableStatus(), ctx.Err()
	})
	cmd := m.Init()
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	<-started
	if press(m, "q") == nil {
		t.Fatal("quit command missing")
	}
	select {
	case result := <-done:
		m.Update(result)
	case <-time.After(time.Second):
		t.Fatal("quit did not cancel fetch")
	}
	if m.haveSnapshot || m.refresh() != nil {
		t.Fatal("work accepted after quit")
	}
}

func TestRequestTimeoutKeepsOldSnapshotExplicitlyStale(t *testing.T) {
	m := fixture(t, func(ctx context.Context, _ string) (protocol.StatusSnapshot, error) {
		<-ctx.Done()
		return protocol.UnavailableStatus(), ctx.Err()
	})
	m.cfg.Timeout = 10 * time.Millisecond
	apply(m, ready(), nil)
	result := m.refresh()()
	m.Update(result)
	requireText(t, m.View().Content, "unavailable", "STALE", "Connected 2", "Pending deliveries 3")
	if !m.retained {
		t.Fatal("timed out snapshot treated as current")
	}
}

func TestFailureDegradedAndRecovery(t *testing.T) {
	m := fixture(t, nil)
	apply(m, ready(), nil)
	for _, health := range []string{"unavailable", "unsupported"} {
		failed := protocol.UnavailableStatus()
		failed.Health = health
		apply(m, failed, errors.New("connection unavailable"))
		requireText(t, m.View().Content, health, "STALE", "codex")
	}
	degraded := ready()
	degraded.Health = "degraded"
	degraded.DurablePending = nil
	degraded.HistoryAvailable = false
	apply(m, degraded, errors.New("storage unavailable"))
	requireText(t, m.View().Content, "DEGRADED", "Pending deliveries unavailable", "history unavailable")
	if m.retained {
		t.Fatal("degraded registry observations were discarded")
	}
	apply(m, ready(), nil)
	if m.retained || strings.Contains(m.View().Content, "STALE") {
		t.Fatal("recovery did not replace stale state")
	}
	old := ready()
	stamp := time.Now().Add(-time.Hour)
	old.SnapshotAt = &stamp
	apply(m, old, nil)
	requireText(t, m.View().Content, "STALE")
}

func TestSelectionTracksSessionIdentityAndFilters(t *testing.T) {
	m := fixture(t, nil)
	apply(m, ready(), nil)
	press(m, "j")
	wanted := m.selected
	next := ready()
	next.Sessions = append([]protocol.SessionStatus{{AgentID: "aardvark", SessionID: "first", State: "stale"}}, next.Sessions...)
	apply(m, next, nil)
	assertEqual(t, "selected session after refresh", m.selected, wanted)
	assertEqual(t, "selected row after insertion", m.cursor, 2)
	press(m, "/")
	press(m, "c")
	press(m, "o")
	if len(m.rows()) != 2 {
		t.Fatal("agent filter not applied")
	}
	press(m, "backspace")
	press(m, "enter")
	press(m, "s")
	requireText(t, m.View().Content, "Sessions · state connected")
	if len(m.rows()) != 2 {
		t.Fatal("connected-state filter not applied")
	}
	press(m, "s")
	assertEqual(t, "disconnected rows", len(m.rows()), 1)
	assertEqual(t, "disconnected session", m.rows()[0].id.session, "codex-old")
	press(m, "/")
	press(m, "q")
	if m.quitting {
		t.Fatal("q in filter quit the dashboard")
	}
	press(m, "esc")
	press(m, "esc")
	if m.filter != "" {
		t.Fatal("escape did not clear filter")
	}
}

func TestPendingInboxesAreNotDuplicatedAcrossHistory(t *testing.T) {
	m := fixture(t, nil)
	apply(m, ready(), nil)
	press(m, "tab")
	rows := m.rows()
	assertEqual(t, "pending inbox rows", len(rows), 2)
	assertEqual(t, "codex pending", rows[0].pending, 2)
	assertEqual(t, "offline inbox", rows[1].id.agent, "offline")
	requireText(t, m.details(), "Pending recipient deliveries: 2", "logical inbox")
	snapshot := ready()
	snapshot.DurablePending = nil
	apply(m, snapshot, nil)
	if len(m.rows()) != 0 {
		t.Fatal("unavailable pending counts synthesized")
	}
	requireText(t, m.View().Content, "Pending counts unavailable")
}

func assertEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
}

func TestQuitCancelsScheduledRefresh(t *testing.T) {
	m := fixture(t, nil)
	m.cfg.Interval = time.Minute
	m.refresh()
	_, scheduled := m.Update(resultMsg{id: m.request, snapshot: ready()})
	m.cancel()
	done := make(chan tea.Msg, 1)
	go func() { done <- scheduled() }()
	select {
	case msg := <-done:
		if msg != nil {
			t.Fatal("canceled timer delivered refresh")
		}
	case <-time.After(time.Second):
		t.Fatal("refresh timer did not cancel")
	}
}
