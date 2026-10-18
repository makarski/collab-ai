package chat_test

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func (h *chatHarness) ready() {
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

// draft addresses text: "@all" means everyone (no audience), anything else is
// a comma-separated list set with /to, then the text is pasted.
func (h *chatHarness) draft(to, text string) {
	h.t.Helper()
	if to != "@all" {
		ids, _ := json.Marshal(strings.Split(to, ","))
		v2Command(h, "/to "+string(ids))
	}
	h.input(tea.PasteMsg{Content: text})
}

func TestChatPasteAndFallbackNewlinesNeverSend(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	h.draft("author", "first\nsecond")
	chatEqual(t, "multiline paste", h.m.State().Draft, "first\nsecond")
	chatEqual(t, "paste never sends", h.m.State().Intent, nil)
	h.input(tea.KeyboardEnhancementsMsg{})
	for _, key := range []tea.KeyPressMsg{chatKey(tea.KeyEnter, tea.ModShift), chatKey(tea.KeyEnter, tea.ModAlt), chatKey('j', tea.ModCtrl)} {
		h.input(key)
	}
	chatEqual(t, "newline shortcuts", h.m.State().Draft, "first\nsecond\n\n\n")
	chatEqual(t, "newlines never send", h.m.State().Intent, nil)
	h.input(chatKey('?', 0))
	h.input(chatKey('a', 0))
	h.input(chatKey('r', 0))
	h.input(chatKey('s', 0))
	chatEqual(t, "editor help literal", h.m.State().Help, false)
	chatEqual(t, "editor keys literal", strings.HasSuffix(h.m.State().Draft, "?ars"), true)
	h.input(chatKey(tea.KeyF1, 0))
	chatEqual(t, "global help", h.m.State().Help, true)
	chatEqual(t, "legacy capability", h.m.State().KeyDisambiguation, false)
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
