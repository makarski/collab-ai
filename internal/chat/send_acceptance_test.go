package chat_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"collab-ai/internal/chat"
	"collab-ai/internal/protocol"
)

func (h *chatHarness) ready() {
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func (h *chatHarness) draft(to, text string) {
	h.t.Helper()
	h.input(chatKey(tea.KeyTab, tea.ModShift))
	h.input(tea.PasteMsg{Content: to})
	h.input(chatKey(tea.KeyEnter, 0))
	h.input(tea.PasteMsg{Content: text})
}

func TestChatPasteAndFallbackNewlinesNeverSend(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	h.draft("author", "first\nsecond")
	if h.m.State().Draft != "first\nsecond" || h.m.State().Intent != nil {
		t.Fatalf("paste acted as send: %+v", h.m.State())
	}
	h.input(tea.KeyboardEnhancementsMsg{})
	for _, key := range []tea.KeyPressMsg{chatKey(tea.KeyEnter, tea.ModShift), chatKey(tea.KeyEnter, tea.ModAlt), chatKey('j', tea.ModCtrl)} {
		h.input(key)
	}
	if h.m.State().Draft != "first\nsecond\n\n\n" || h.m.State().Intent != nil {
		t.Fatalf("newline shortcut sent or lost text: %+v", h.m.State())
	}
	h.input(chatKey('?', 0))
	h.input(chatKey('a', 0))
	h.input(chatKey('r', 0))
	h.input(chatKey('s', 0))
	if h.m.State().Help || !strings.HasSuffix(h.m.State().Draft, "?ars") {
		t.Fatal("timeline/help key intercepted editor text")
	}
	h.input(chatKey(tea.KeyF1, 0))
	if !h.m.State().Help || h.m.State().KeyDisambiguation {
		t.Fatal("legacy keyboard capability misreported")
	}
	h.input(chatKey(tea.KeyF1, 0))
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorSend)
	var body struct{ Text string }
	if err := json.Unmarshal(req.Payload, &body); err != nil {
		t.Fatal(err)
	}
	if body.Text != "first\nsecond\n\n\n?ars" {
		t.Fatalf("sent text = %q", body.Text)
	}
	if h.m.State().Intent.State != chat.IntentSending {
		t.Fatal("optimistic send acceptance")
	}
}

func TestChatBroadcastConfirmationPreservesDraftOnCancel(t *testing.T) {
	for _, cancel := range []tea.KeyPressMsg{chatKey('n', 0), chatKey(tea.KeyEscape, 0)} {
		t.Run(cancel.String(), func(t *testing.T) {
			h := newChatHarness(t, chat.Bounds{})
			h.ready()
			h.draft("@all", "broadcast\nbody")
			h.input(chatKey(tea.KeyEnter, 0))
			if h.m.State().Confirm == nil || h.m.State().Intent != nil {
				t.Fatal("broadcast dispatched before confirmation")
			}
			h.input(cancel)
			if h.m.State().Confirm != nil || h.m.State().Draft != "broadcast\nbody" || h.m.State().Recipients != "@all" {
				t.Fatalf("cancel lost draft: %+v", h.m.State())
			}
			h.input(chatKey(tea.KeyEnter, 0))
			h.input(chatKey('y', 0))
			req := h.take(protocol.TypeOperatorSend)
			if req.To != protocol.Broadcast || len(req.Group) != 0 {
				t.Fatalf("broadcast narrowed to preview: %+v", req)
			}
			h.reply(req, protocol.Message{Type: protocol.TypeAck, MessageID: req.MessageID, Stage: protocol.StageAccepted, Seq: 1, Recipients: []protocol.Recipient{{AgentID: "new-member", SessionID: "new-session"}}, Excluded: []string{"old-client"}})
			h.await(func() bool { return h.m.State().Intent.State == chat.IntentAccepted })
			if len(h.m.State().Intent.Recipients) != 1 || !reflect.DeepEqual(h.m.State().Intent.Excluded, []string{"old-client"}) {
				t.Fatal("authoritative accepted membership lost")
			}
		})
	}
}

func TestChatBlankDraftNeverSends(t *testing.T) {
	for _, text := range []string{"", " \t\n"} {
		h := newChatHarness(t, chat.Bounds{})
		h.ready()
		h.draft("author", text)
		h.input(chatKey(tea.KeyEnter, 0))
		if h.m.State().Intent != nil {
			t.Fatalf("sent whitespace draft %q", text)
		}
	}
}

func TestChatEncodedPayloadLimitIsCheckedWithoutTruncatingDraft(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	// '<' costs six bytes in JSON, so the character count is below the cap.
	text := strings.Repeat("<", protocol.OperatorPayloadMax/6+1)
	h.draft("author", text)
	if h.m.State().Draft != text {
		t.Fatalf("editor silently truncated draft: got %d characters, want %d", len(h.m.State().Draft), len(text))
	}
	h.input(chatKey(tea.KeyEnter, 0))
	if h.m.State().Intent != nil || !strings.Contains(h.m.State().Status, "512") {
		t.Fatalf("encoded oversize not rejected locally: %+v", h.m.State().Intent)
	}
}

func TestChatMalformedSendResponseCannotClaimRejectionOrAcceptance(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	h.draft("author", "uncertain")
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorSend)
	h.reply(req, protocol.Message{Type: protocol.TypeAck, MessageID: "different-intent", Stage: protocol.StageAccepted})
	h.await(func() bool { return h.m.State().Status != "" })
	if h.m.State().Intent.State != chat.IntentUnknown {
		t.Fatalf("malformed response fabricated outcome: %+v", h.m.State().Intent)
	}
}
