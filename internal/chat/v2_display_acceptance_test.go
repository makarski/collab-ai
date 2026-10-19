package chat_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
)

func v2FrameFits(t *testing.T, h *chatHarness, width, height int) {
	t.Helper()
	view := h.m.View()
	if !view.AltScreen {
		t.Fatal("view left the alternate screen")
	}
	lines := strings.Split(view.Content, "\n")
	if len(lines) > height {
		t.Fatalf("frame has %d lines at height %d: %q", len(lines), height, view.Content)
	}
	for _, line := range lines {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("frame has %d columns at width %d: %q", got, width, line)
		}
	}
}

func TestChatV2EveryViewUsesAlternateScreenAndFitsTerminal(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.ready()
	for _, size := range [][2]int{{1, 1}, {8, 3}, {19, 4}, {20, 4}, {32, 8}, {80, 24}} {
		h.input(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		v2FrameFits(t, h, size[0], size[1])
		h.input(chatKey(tea.KeyF1, 0))
		v2FrameFits(t, h, size[0], size[1])
		h.input(chatKey(tea.KeyF1, 0))
		h.input(chatKey('k', tea.ModCtrl))
		v2FrameFits(t, h, size[0], size[1])
		h.input(chatKey(tea.KeyEscape, 0))
	}
}

func TestChatV2TooSmallTerminalPreservesUnsentDraft(t *testing.T) {
	for _, size := range [][2]int{{19, 20}, {80, 3}} {
		h := newChatHarness(t, chat.Bounds{})
		h.ready()
		v2Type(h, "body")
		h.input(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		h.input(chatKey(tea.KeyEnter, 0))
		chatEqual(t, "too small does not send", h.m.State().Intent, nil)
		chatEqual(t, "too small preserves draft", h.m.State().Draft, "body")
		h.input(tea.WindowSizeMsg{Width: 80, Height: 24})
		req := v2Send(h)
		chatEqual(t, "resize restores sending", v2Body(t, req), "body")
	}
}

func TestChatV2WrappedTranscriptLinesRemainReachable(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	msg := chatIncoming("long-message", 1)
	var body strings.Builder
	for i := range 30 {
		fmt.Fprintf(&body, "row%02d 界 é\n", i)
	}
	body.WriteString(strings.Repeat("wrapped ", 40) + "TAIL")
	msg.Payload, _ = json.Marshal(map[string]string{"text": body.String()})
	v2History(h, msg)
	for _, width := range []int{32, 50} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 10})
		h.input(chatKey(tea.KeyEnd, tea.ModCtrl))
		var seen strings.Builder
		for range 60 {
			v2FrameFits(t, h, width, 10)
			seen.WriteString(ansi.Strip(h.m.View().Content))
			seen.WriteByte('\n')
			h.input(chatKey(tea.KeyPgUp, 0))
		}
		for i := range 30 {
			marker := fmt.Sprintf("row%02d", i)
			if !strings.Contains(seen.String(), marker) {
				t.Fatalf("width %d: transcript line %s is unreachable", width, marker)
			}
		}
		if !strings.Contains(seen.String(), "TAIL") {
			t.Fatalf("width %d: wrapped final text is unreachable", width)
		}
		if strings.Contains(seen.String(), `\nrow`) {
			t.Fatal("message newlines rendered as literal escape text")
		}
	}
}
