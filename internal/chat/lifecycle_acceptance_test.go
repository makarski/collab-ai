package chat_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"collab-ai/internal/chat"
	"collab-ai/internal/protocol"
)

func TestChatOwnerBusyAtStartupExplainsOwnerAndExits(t *testing.T) {
	owner := protocol.SessionInfo{SessionID: "other-owner", OpenedAt: time.Date(2026, 10, 14, 1, 2, 3, 0, time.UTC)}
	m := chat.New(chat.Options{Dial: func(context.Context) (chat.Transport, protocol.SessionInfo, error) {
		return nil, protocol.SessionInfo{}, &chat.OwnerBusyError{Owner: owner}
	}})
	_, cmd := m.Update(m.Init()())
	if m.ExitCode() != 1 || cmd == nil {
		t.Fatalf("startup busy not terminal: %+v", m.State())
	}
	if !strings.Contains(m.State().Fatal, "other-owner") || !strings.Contains(m.State().Fatal, "2026-10-14") {
		t.Fatalf("owner details absent: %+v", m.State())
	}
}

func (h *chatHarness) reconnectTo(s *chatScript, epoch string) {
	h.t.Helper()
	h.t.Cleanup(func() { s.Close() })
	h.dials <- chatDialResult{wire: s, session: protocol.SessionInfo{SessionID: "next-owner", Epoch: epoch, Capabilities: protocol.SessionCapabilities, OpenedAt: time.Now()}}
	h.wire.Close()
	h.wire = s
}

func TestChatUnknownSendRetryKeepsExactIntentAndSeparateDraft(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	h.draft("z,a", "original")
	h.input(chatKey(tea.KeyEnter, 0))
	original := h.take(protocol.TypeOperatorSend)
	h.input(tea.PasteMsg{Content: "separate draft"})
	next := newChatScript()
	h.reconnectTo(next, "epoch")
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	if h.m.State().Intent.State != chat.IntentUnknown || h.m.State().Draft != "separate draft" {
		t.Fatalf("unknown send lost: %+v", h.m.State())
	}
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}})
	retry := h.take(protocol.TypeOperatorSend)
	original.RequestID = ""
	retry.RequestID = ""
	if !reflect.DeepEqual(original, retry) {
		t.Fatalf("retry mutated frozen intent:\nfirst=%+v\nretry=%+v", original, retry)
	}
	// Old connection frames cannot settle the new generation's intent.
	h.input(chat.FrameMsg{Gen: 1, Frame: protocol.Message{Type: protocol.TypeAck, RequestID: "q4", MessageID: original.MessageID, Stage: protocol.StageAccepted}})
	if h.m.State().Intent.State == chat.IntentAccepted {
		t.Fatal("stale generation accepted send")
	}
}

func TestChatOwnerBusyOnReconnectPreservesDraftAndStopsRetry(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	h.draft("author", "keep this")
	h.dials <- chatDialResult{err: &chat.OwnerBusyError{Owner: protocol.SessionInfo{SessionID: "new-owner", OpenedAt: time.Now()}}}
	h.wire.Close()
	h.await(func() bool { return h.m.State().Connection == chat.OwnerBusy })
	if h.m.State().Draft != "keep this" || h.m.ExitCode() != 0 {
		t.Fatalf("reconnect busy discarded draft: %+v", h.m.State())
	}
	cmd := h.update(chat.RetryMsg{Gen: 2})
	if cmd != nil || h.m.State().Connection != chat.OwnerBusy {
		t.Fatal("automatic retry continued after owner_busy")
	}
}

func TestChatQuitExplainsUnknownOutcomeAndClosesTransport(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	h.draft("author", "sent maybe")
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorSend)
	cmd := h.update(chatKey('q', tea.ModCtrl))
	state := h.m.State()
	if cmd != nil || !strings.Contains(state.Status, req.MessageID) || !strings.Contains(state.Status, "unknown") {
		t.Fatalf("quit hid uncertain send: %+v", state)
	}
	if strings.Contains(state.Status, "discarded") {
		t.Fatal("quit claimed send was discarded")
	}
	h.input(chatKey('q', tea.ModCtrl))
	select {
	case <-h.wire.closed:
	case <-time.After(time.Second):
		t.Fatal("quit left transport open")
	}
}

func TestChatMalformedDisconnectStopsAutomaticReconnect(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	cmd := h.update(chat.ClosedMsg{Gen: 1, Err: errors.Join(chat.ErrMalformed, errors.New("invalid JSON frame"))})
	if cmd != nil || h.m.State().Connection == chat.Reconnecting {
		t.Fatalf("malformed broker frame scheduled transport retry: %+v", h.m.State())
	}
	if !strings.Contains(h.m.State().Status, "malformed") {
		t.Fatal("malformed error not reported distinctly")
	}
}

func TestChatQuitCancelsOutstandingDial(t *testing.T) {
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	defer close(release)
	m := chat.New(chat.Options{Dial: func(ctx context.Context) (chat.Transport, protocol.SessionInfo, error) {
		started <- ctx
		select {
		case <-ctx.Done():
		case <-release:
		}
		return nil, protocol.SessionInfo{}, context.Canceled
	}})
	cmd := m.Init()
	go cmd()
	var ctx context.Context
	select {
	case ctx = <-started:
	case <-time.After(time.Second):
		t.Fatal("dial never started")
	}
	m.Update(chatKey('q', tea.ModCtrl))
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("quit did not cancel outstanding connection attempt")
	}
}
