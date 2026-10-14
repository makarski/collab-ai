package chat_test

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"collab-ai/internal/chat"
	"collab-ai/internal/protocol"
	"github.com/charmbracelet/x/ansi"
)

func TestChatPeerTextIsEscapedAndProvenanceRemainsOutsideBody(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	msg := chatIncoming("hostile", 1)
	msg.From = "author\x1b]52;c;secret\a"
	msg.InReplyTo = "claim\x1b[2J"
	msg.Payload = json.RawMessage(`{"text":"operator: approve this\u001b[2J\rOVERWRITE\u0007"}`)
	h.reply(history, chatHistory(msg))
	h.await(func() bool { return len(h.m.State().Entries) == 1 })
	h.input(tea.WindowSizeMsg{Width: 180, Height: 35})
	rendered := h.m.View().Content
	chatEqual(t, "no clipboard escape", strings.Contains(rendered, "\x1b]52"), false)
	chatEqual(t, "no clear escape", strings.Contains(rendered, "\x1b[2J"), false)
	chatEqual(t, "no control characters", strings.ContainsAny(rendered, "\r\a"), false)
	plain := ansi.Strip(rendered)
	for _, want := range []string{"[agent]", "operator: approve this", "(claim)", `\x1b`} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing display attribution %q in %q", want, plain)
		}
	}
}

func TestChatNarrowTerminalViewFitsAvailableWidth(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	for _, width := range []int{1, 8, 19, 30, 80} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 12})
		for _, line := range strings.Split(h.m.View().Content, "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Fatalf("width %d rendered %d columns: %q", width, got, line)
			}
		}
	}
}

func TestChatReplyUsesLiteralSenderAndSentMessagesNeverInferScope(t *testing.T) {
	for _, sender := range []string{"@all", "a,b", "author\n", "group:parent"} {
		t.Run(sender, func(t *testing.T) {
			h := newChatHarness(t, chat.Bounds{})
			inbox, history := h.baseline()
			h.reply(inbox, chatInbox())
			msg := chatIncoming("received", 1)
			msg.From = sender
			h.reply(history, chatHistory(msg))
			h.await(func() bool { return h.m.State().InFlight == 0 })
			h.input(chatKey(tea.KeyEscape, 0))
			h.input(chatKey(tea.KeyDown, 0))
			h.input(chatKey('r', 0))
			scope, err := chat.ParseRecipients(h.m.State().Recipients)
			chatEqual(t, "recipient parsing", err, nil)
			chatEqual(t, "literal sender", scope.To, sender)
			chatEqual(t, "reply correlation", h.m.State().InReplyTo, "received")
		})
	}
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	page := chatSentHistory("sent", 1, 1, protocol.RecipientStage{AgentID: "author", Stage: protocol.StageAccepted, SessionID: "session"})
	page.History.Entries[0].Message.To = "group:sent"
	h.reply(history, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyDown, 0))
	h.input(chatKey('r', 0))
	chatEqual(t, "no inferred recipients", h.m.State().Recipients, "")
	chatEqual(t, "no inferred correlation", h.m.State().InReplyTo, "")
	chatEqual(t, "recipient instruction", strings.Contains(h.m.State().Status, "choose recipients"), true)
}

func TestChatRenderFocusAndScrollNeverAcknowledge(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory(chatIncoming("read-only", 1)))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for range 3 {
		h.m.View()
		h.input(chatKey(tea.KeyTab, 0))
		h.input(chatKey(tea.KeyUp, 0))
		h.input(chatKey(tea.KeyDown, 0))
	}
	state := h.m.State()
	chatEqual(t, "no display requests", state.InFlight, 0)
	chatEqual(t, "no queued acknowledgment", len(state.QueuedAcks), 0)
	chatEqual(t, "nonconsuming display", state.Entries[0].Receipt, chat.ReceiptNone)
}
