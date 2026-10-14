package chat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"collab-ai/internal/chat"
	"collab-ai/internal/protocol"
)

// The harness executes commands separately from Update. Its transport detects
// accidental network writes during Update, and responses may be withheld to
// exercise receipt ordering and uncertain outcomes without sleeping.
type chatScript struct {
	in            chan protocol.Message
	writes        chan protocol.Message
	closed        chan struct{}
	once          sync.Once
	wroteInUpdate atomic.Bool
}

func newChatScript() *chatScript {
	return &chatScript{in: make(chan protocol.Message, 64), writes: make(chan protocol.Message, 64), closed: make(chan struct{})}
}
func (s *chatScript) Request(msg protocol.Message) (string, error) {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if strings.HasSuffix(frame.Function, ".(*Model).Update") {
			s.wroteInUpdate.Store(true)
		}
		if !more {
			break
		}
	}
	s.writes <- msg
	return msg.RequestID, nil
}
func (s *chatScript) Incoming() <-chan protocol.Message { return s.in }
func (s *chatScript) Close() error {
	s.once.Do(func() { close(s.closed); close(s.in) })
	return nil
}

type chatHarness struct {
	t        *testing.T
	m        *chat.Model
	wire     *chatScript
	results  chan tea.Msg
	done     chan struct{}
	requests []protocol.Message
	dials    chan chatDialResult
}

type chatDialResult struct {
	wire    *chatScript
	session protocol.SessionInfo
	err     error
}

func newChatHarness(t *testing.T, bounds chat.Bounds) *chatHarness {
	t.Helper()
	s := newChatScript()
	h := &chatHarness{t: t, wire: s, results: make(chan tea.Msg, 128), done: make(chan struct{}), dials: make(chan chatDialResult, 8)}
	h.dials <- chatDialResult{wire: s, session: protocol.SessionInfo{SessionID: "owner", Epoch: "epoch", Capabilities: protocol.SessionCapabilities, OpenedAt: time.Now()}}
	h.m = chat.New(chat.Options{Bounds: bounds, Backoff: chat.BackoffPolicy{Base: time.Millisecond, Max: 5 * time.Millisecond, Healthy: time.Minute}, Dial: func(context.Context) (chat.Transport, protocol.SessionInfo, error) {
		select {
		case result := <-h.dials:
			return result.wire, result.session, result.err
		case <-h.done:
			return nil, protocol.SessionInfo{}, context.Canceled
		}
	}})
	t.Cleanup(func() { close(h.done); s.Close() })
	h.launch(h.m.Init())
	return h
}

func (h *chatHarness) launch(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				h.launch(child)
			}
			return
		}
		select {
		case h.results <- msg:
		case <-h.done:
		}
	}()
}

func (h *chatHarness) update(msg tea.Msg) tea.Cmd {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	if h.wire.wroteInUpdate.Load() {
		h.t.Fatal("Transport.Request executed during Update")
	}
	return cmd
}
func (h *chatHarness) input(msg tea.Msg) { h.launch(h.update(msg)) }

func (h *chatHarness) await(want func() bool) {
	h.t.Helper()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	for !want() {
		select {
		case msg := <-h.results:
			h.input(msg)
		case req := <-h.wire.writes:
			h.requests = append(h.requests, req)
		case <-timer.C:
			h.t.Fatalf("chat did not reach expected state; requests=%v state=%+v", h.requests, h.m.State())
		}
	}
}

func (h *chatHarness) take(kind string) protocol.Message {
	h.t.Helper()
	index := -1
	h.await(func() bool {
		for i, req := range h.requests {
			if req.Type == kind {
				index = i
				return true
			}
		}
		return false
	})
	req := h.requests[index]
	h.requests = append(h.requests[:index], h.requests[index+1:]...)
	return req
}

func (h *chatHarness) reply(req, response protocol.Message) {
	response.RequestID = req.RequestID
	h.wire.in <- response
}

func (h *chatHarness) baseline() (protocol.Message, protocol.Message) {
	h.t.Helper()
	req := h.take(protocol.TypeOperatorSnapshot)
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1}}})
	return h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
}

func chatIncoming(id string, seq uint64) protocol.Message {
	ts := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	return protocol.Message{Type: protocol.TypeMsg, MessageID: id, Seq: seq, From: "author", To: protocol.OperatorID, Origin: protocol.OriginAgent, Durable: true, AckRequested: true, TS: &ts, Payload: json.RawMessage(fmt.Sprintf(`{"text":%q}`, "body "+id))}
}

func chatInbox(messages ...protocol.Message) protocol.Message {
	var upper uint64
	if len(messages) > 0 {
		upper = messages[len(messages)-1].Seq
	}
	return protocol.Message{Type: protocol.TypeOperatorPage, Page: &protocol.OperatorPage{Messages: messages, UpperSeq: upper, NextAfterSeq: upper}}
}

func chatHistory(messages ...protocol.Message) protocol.Message {
	page := &protocol.HistoryPage{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1}}
	for _, msg := range messages {
		page.UpperSeq = max(page.UpperSeq, msg.Seq)
		page.NextBeforeSeq = msg.Seq
		page.Entries = append(page.Entries, protocol.HistoryEntry{Message: msg, Direction: "received", SenderSession: "sender-session", OperatorStage: protocol.StageAccepted})
	}
	return protocol.Message{Type: protocol.TypeOperatorHistory, History: page}
}

func chatKey(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	key := tea.KeyPressMsg{Code: code, Mod: mod}
	if code >= 32 && code < 127 && mod == 0 {
		key.Text = string(code)
	}
	return key
}
