package chat_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestChatHistoryScrollLoadsOlderAfterInboxWinsStartupRace(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 80, Height: 12})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox(chatIncoming("live-five", 5)))
	receipt := h.take(protocol.TypeOperatorReceived)
	h.reply(receipt, chatReceipt("live-five"))
	h.await(func() bool { return h.m.State().Entries[0].Receipt == chat.ReceiptConfirmed })
	page := chatHistory(chatIncoming("live-five", 5), chatIncoming("four", 4))
	page.History.More = true
	h.reply(history, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for range 8 {
		h.input(chatKey(tea.KeyPgUp, 0))
	}
	older := h.take(protocol.TypeOperatorHistory)
	chatEqual(t, "older cursor", older.PageRequest.BeforeSeq, uint64(4))
	chatEqual(t, "frozen history bound", older.PageRequest.UpperSeq, uint64(5))
	chatEqual(t, "repeated PgUp coalesces request", h.m.State().InFlight, 1)
	previous := slices.Index(strings.Split(ansi.Strip(h.m.View().Content), "\n"), "  body four")
	if previous < 0 {
		t.Fatal("message at reading position is not visible")
	}
	page = chatHistory(chatIncoming("three", 3), chatIncoming("two", 2))
	page.History.UpperSeq, page.History.More = 5, true
	h.reply(older, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	current := slices.Index(strings.Split(ansi.Strip(h.m.View().Content), "\n"), "  body four")
	chatEqual(t, "prepending history preserves reading position", current, previous)
	for range 8 {
		h.input(chatKey(tea.KeyPgUp, 0))
	}
	older = h.take(protocol.TypeOperatorHistory)
	chatEqual(t, "next older cursor", older.PageRequest.BeforeSeq, uint64(2))
	chatEqual(t, "next page keeps frozen bound", older.PageRequest.UpperSeq, uint64(5))
	page = chatHistory(chatIncoming("oldest-one", 1))
	page.History.UpperSeq = 5
	h.reply(older, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for range 8 {
		h.input(chatKey(tea.KeyPgUp, 0))
	}
	if !strings.Contains(ansi.Strip(h.m.View().Content), "body oldest-one") {
		t.Fatal("loaded oldest message remains unreachable")
	}
	chatEqual(t, "end of history does not request again", h.m.State().InFlight, 0)
	if h.m.State().Status == "" {
		t.Fatal("end of history needs visible feedback")
	}
	h.input(chatKey(tea.KeyEnd, tea.ModCtrl))
	chatEqual(t, "Ctrl+End restores newest", h.m.State().ScrollOffset, 0)
	if !strings.Contains(ansi.Strip(h.m.View().Content), "body live-five") {
		t.Fatal("newest message lost after paging")
	}
}

func TestChatHistoryScrollPageKeysReachEveryLoadedMessage(t *testing.T) {
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 60, Height: 12})
	var messages []protocol.Message
	for i := 20; i >= 1; i-- {
		messages = append(messages, chatIncoming(fmt.Sprintf("marker-%02d", i), uint64(i)))
	}
	v2History(h, messages...)
	for _, direction := range []rune{tea.KeyPgUp, tea.KeyPgDown} {
		var seen strings.Builder
		for range 30 {
			seen.WriteString(ansi.Strip(h.m.View().Content))
			seen.WriteByte('\n')
			h.input(chatKey(direction, 0))
			v2FrameFits(t, h, 60, 12)
		}
		for i := 1; i <= 20; i++ {
			if !strings.Contains(seen.String(), fmt.Sprintf("body marker-%02d", i)) {
				t.Fatalf("key %d cannot reach message %d", direction, i)
			}
		}
	}
	chatEqual(t, "PgDown returns to newest", h.m.State().ScrollOffset, 0)
}
