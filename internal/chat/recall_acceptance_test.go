package chat_test

import (
	"encoding/json"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func recallSent(id string, seq uint64, to string, group ...string) protocol.HistoryEntry {
	msg := chatIncoming(id, seq)
	msg.From, msg.To, msg.Group, msg.Origin = protocol.OperatorID, to, group, protocol.OriginOperator
	return protocol.HistoryEntry{Message: msg, Direction: "sent"}
}

func recallHarness(t *testing.T, entries ...protocol.HistoryEntry) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 24})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	page := &protocol.HistoryPage{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1}, Entries: entries}
	if len(entries) > 0 {
		page.UpperSeq, page.NextBeforeSeq = entries[0].Message.Seq, entries[len(entries)-1].Message.Seq
	}
	h.reply(history, protocol.Message{Type: protocol.TypeOperatorHistory, History: page})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func recallState(t *testing.T, h *chatHarness, index, total int) {
	t.Helper()
	r := h.m.State().Recall
	if r == nil || !r.Active {
		t.Fatal("recall state is not active")
	}
	chatEqual(t, "recall index", r.Index, index)
	chatEqual(t, "recall total", r.Total, total)
}

func recallEnded(t *testing.T, h *chatHarness) {
	t.Helper()
	if r := h.m.State().Recall; r != nil && r.Active {
		t.Fatal("recall state stayed active")
	}
}

func TestChatRecallAllRestoresOriginalScopeAndMakesANewSend(t *testing.T) {
	for _, entry := range []protocol.HistoryEntry{
		recallSent("direct", 1, "alice"), recallSent("group", 1, "", "alice", "bob"),
		recallSent("broadcast", 1, protocol.Broadcast), recallSent("literal", 1, "a,b"),
	} {
		t.Run(entry.Message.MessageID, func(t *testing.T) {
			// Historical payload text must not reinterpret the stored scope.
			entry.Message.Payload = json.RawMessage(`{"text":"@bob /quit literal historical text"}`)
			h := recallHarness(t, entry)
			h.input(chatKey(tea.KeyUp, 0))
			chatEqual(t, "recall preserves payload", h.m.State().Draft, "@bob /quit literal historical text")
			chatEqual(t, "recall keeps All selected", h.m.State().SelectedAll, true)
			chatEqual(t, "recalled payload stays literal", h.m.State().Pasted, true)
			recallState(t, h, 0, 1)
			if entry.Message.To == protocol.Broadcast {
				v3Contains(t, h.m.State().Hint, "to everyone")
			}
			sent := v2Send(h)
			recallEnded(t, h)
			chatEqual(t, "original destination", sent.To, entry.Message.To)
			if !reflect.DeepEqual(sent.Group, entry.Message.Group) {
				t.Fatalf("group = %q, want %q", sent.Group, entry.Message.Group)
			}
			if sent.MessageID == entry.Message.MessageID {
				t.Fatal("recall reused immutable historical identity")
			}
			chatEqual(t, "history entry immutable", h.m.State().Entries[0].Body, "@bob /quit literal historical text")
		})
	}
}

func TestChatRecallWalksSentEntriesAndReturnsToEmptyDraft(t *testing.T) {
	incoming := protocol.HistoryEntry{Message: chatIncoming("incoming", 4), Direction: "received", OperatorStage: protocol.StageAccepted}
	h := recallHarness(t, recallSent("newest", 5, "alice"), incoming, recallSent("oldest", 3, "bob"))
	h.input(chatKey(tea.KeyUp, 0))
	chatEqual(t, "newest sent", h.m.State().Draft, "body newest")
	recallState(t, h, 0, 2)
	h.input(chatKey(tea.KeyUp, 0))
	chatEqual(t, "received entries skipped", h.m.State().Draft, "body oldest")
	recallState(t, h, 1, 2)
	h.input(chatKey(tea.KeyUp, 0))
	chatEqual(t, "oldest boundary stable", h.m.State().Draft, "body oldest")
	recallState(t, h, 1, 2)
	h.input(chatKey(tea.KeyDown, 0))
	chatEqual(t, "walk forward", h.m.State().Draft, "body newest")
	recallState(t, h, 0, 2)
	h.input(chatKey(tea.KeyDown, 0))
	chatEqual(t, "beyond newest restores empty", h.m.State().Draft, "")
	recallEnded(t, h)
	chatEqual(t, "leaving recall restores default All audience", h.m.State().Audience.Everyone, true)
	chatEqual(t, "walking never sends", h.m.State().Intent, nil)
	chatEqual(t, "walking never acknowledges", len(h.m.State().QueuedAcks), 0)
}

func TestChatRecallDirectViewUsesOnlyExactOriginalDirectScope(t *testing.T) {
	h := recallHarness(t, recallSent("group-alice", 6, "", "alice", "bob"), recallSent("bob-direct", 5, "bob"),
		recallSent("broadcast", 4, protocol.Broadcast), recallSent("alice-new", 3, "alice"), recallSent("alice-old", 2, "alice"))
	conversationOpen(h, "alice")
	h.input(chatKey(tea.KeyUp, 0))
	chatEqual(t, "skip group and broadcast", h.m.State().Draft, "body alice-new")
	recallState(t, h, 0, 2)
	h.input(chatKey(tea.KeyUp, 0))
	chatEqual(t, "older direct only", h.m.State().Draft, "body alice-old")
	recallState(t, h, 1, 2)
	chatEqual(t, "recall never changes view", h.m.State().Selected, "alice")
	sent := v2Send(h)
	chatEqual(t, "direct scope preserved", sent.To, "alice")
	chatEqual(t, "direct recall never broadens", len(sent.Group), 0)
}

func TestChatRecallReplyClearsOldCorrelation(t *testing.T) {
	reply := recallSent("old-reply", 2, "alice")
	reply.Message.InReplyTo = "old-target"
	h := recallHarness(t, reply)
	h.input(chatKey(tea.KeyUp, 0))
	chatEqual(t, "old reply binding not restored", h.m.State().InReplyTo, "")
	sent := v2Send(h)
	chatEqual(t, "new send invents no reply", sent.InReplyTo, "")
	chatEqual(t, "old target not queued", len(h.m.State().QueuedAcks), 0)
}

func TestChatRecallEditingAndEscapeLeaveTheCurrentDraft(t *testing.T) {
	for _, mode := range []string{"edit", "paste", "escape"} {
		t.Run(mode, func(t *testing.T) {
			h := recallHarness(t, recallSent("new", 2, "alice"), recallSent("old", 1, "bob"))
			h.input(chatKey(tea.KeyUp, 0))
			want := "body new"
			if mode == "edit" {
				v2Type(h, " edited")
				want += " edited"
			} else if mode == "paste" {
				h.input(tea.PasteMsg{Content: " pasted"})
				want += " pasted"
			} else {
				h.input(chatKey(tea.KeyEscape, 0))
			}
			recallEnded(t, h)
			h.input(chatKey(tea.KeyUp, 0))
			chatEqual(t, "Up moves cursor after recall ends", h.m.State().Draft, want)
		})
	}
}

func TestChatRecallNeverOverwritesANonemptyDraft(t *testing.T) {
	h := recallHarness(t, recallSent("old", 1, "alice"))
	v2Type(h, "unsent draft")
	h.input(chatKey(tea.KeyUp, 0))
	chatEqual(t, "existing draft preserved", h.m.State().Draft, "unsent draft")
	chatEqual(t, "existing scope preserved", h.m.State().Audience.Everyone, true)
}

func TestChatRecallViewSwitchEndsRecallAndPreservesDraftScope(t *testing.T) {
	h := recallHarness(t, recallSent("new-alice", 2, "alice"), recallSent("old-bob", 1, "bob"))
	h.input(chatKey(tea.KeyUp, 0))
	conversationOpen(h, "bob")
	recallEnded(t, h)
	chatEqual(t, "another conversation keeps its empty draft", h.m.State().Draft, "")
	v2Command(h, "/chat all")
	chatEqual(t, "recalled draft saved in All", h.m.State().Draft, "body new-alice")
	recallEnded(t, h)
	h.input(chatKey(tea.KeyUp, 0))
	chatEqual(t, "returning does not revive recall", h.m.State().Draft, "body new-alice")
	sent := v2Send(h)
	chatEqual(t, "saved recalled scope is unchanged", sent.To, "alice")
}

func TestChatRecallSkipsUnavailableHistoricalBodies(t *testing.T) {
	omitted := recallSent("omitted", 3, "bob")
	omitted.Message.Payload, omitted.BodyOmitted = nil, true
	h := recallHarness(t, omitted, recallSent("available", 2, "alice"))
	h.input(chatKey(tea.KeyUp, 0))
	chatEqual(t, "recall uses complete historical text", h.m.State().Draft, "body available")
	recallState(t, h, 0, 1)
	chatEqual(t, "omitted entry remains immutable", h.m.State().Entries[1].BodyOmitted, true)
}
