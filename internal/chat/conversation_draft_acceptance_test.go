package chat_test

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

// These tests use only the operator-facing commands and existing StateView;
// the conversation implementation need not expose its internal state store.
func conversationHarness(t *testing.T, ids ...string) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 30})
	request := h.take(protocol.TypeOperatorSnapshot)
	var agents []protocol.AgentPresence
	for _, id := range ids {
		agents = append(agents, protocol.AgentPresence{AgentID: id, SessionID: "session-" + id, ProtocolVersion: protocol.Version})
	}
	h.reply(request, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1}, Agents: agents, AgentsTotal: len(agents),
	}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	target := chatIncoming("conversation-target", 7)
	target.From = "alice"
	h.reply(history, chatHistory(target))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func conversationOpen(h *chatHarness, id string) {
	h.t.Helper()
	value, err := json.Marshal(id)
	if err != nil {
		h.t.Fatal(err)
	}
	v2Command(h, "/chat "+string(value))
	if h.m.State().CommandLine != nil {
		h.t.Fatalf("could not open conversation %q: %s", id, h.m.State().Status)
	}
}

func TestChatConversationDraftsKeepTheirOriginalScope(t *testing.T) {
	h := conversationHarness(t, "alice", "bob")
	v2Type(h, "all draft")
	conversationOpen(h, "alice")
	chatEqual(t, "fresh direct draft", h.m.State().Draft, "")
	v2Type(h, "alice draft")
	conversationOpen(h, "bob")
	chatEqual(t, "fresh second direct draft", h.m.State().Draft, "")
	v2Type(h, "bob draft")
	conversationOpen(h, "alice")
	chatEqual(t, "first direct draft restored", h.m.State().Draft, "alice draft")
	v3Contains(t, h.m.State().Hint, "to alice")
	v2Command(h, "/chat all")
	chatEqual(t, "common draft restored", h.m.State().Draft, "all draft")
	v3Contains(t, h.m.State().Hint, "to everyone")
	conversationOpen(h, "bob")
	request := v2Send(h)
	chatEqual(t, "selected draft sent directly", request.To, "bob")
	chatEqual(t, "selected draft text", v2Body(t, request), "bob draft")
	chatEqual(t, "plain send does not claim a reply", request.InReplyTo, "")
	chatEqual(t, "navigation does not acknowledge", len(h.m.State().QueuedAcks), 0)
}

func TestChatConversationDirectMentionsCannotEscapeSelectedAgent(t *testing.T) {
	for _, draft := range []string{"@bob body", "@all body", "@alice @bob body"} {
		t.Run(draft, func(t *testing.T) {
			h := conversationHarness(t, "alice", "bob")
			conversationOpen(h, "alice")
			v2Type(h, draft)
			h.input(chatKey(tea.KeyEscape, 0))
			h.input(chatKey(tea.KeyEnter, 0))
			chatEqual(t, "misaddressed direct draft never sends", h.m.State().Intent, nil)
			chatEqual(t, "blocked draft preserved", h.m.State().Draft, draft)
			v3Contains(t, h.m.State().Audience.Error, "only in All")
		})
	}
	h := conversationHarness(t, "alice")
	conversationOpen(h, "alice")
	v2Type(h, "@alice allowed")
	request := v2Send(h)
	chatEqual(t, "same-agent mention allowed", request.To, "alice")
	chatEqual(t, "mention text unchanged", v2Body(t, request), "@alice allowed")
}

func TestChatConversationLiteralAllIsNotTheCommonView(t *testing.T) {
	h := conversationHarness(t, "all", "agent with spaces")
	conversationOpen(h, "all")
	v2Type(h, "literal agent")
	request := v2Send(h)
	chatEqual(t, "quoted all is an agent", request.To, "all")
	chatEqual(t, "quoted all is not broadcast", len(request.Group), 0)
}

func TestChatConversationReplyMovesOnlyUnaddressedCommonDraft(t *testing.T) {
	h := conversationHarness(t, "alice", "bob")
	v2Type(h, "reply draft")
	v2Command(h, "/reply #7")
	chatEqual(t, "reply text moved", h.m.State().Draft, "reply draft")
	chatEqual(t, "reply binding attached", h.m.State().InReplyTo, "conversation-target")
	v2Command(h, "/chat all")
	chatEqual(t, "common draft emptied by move", h.m.State().Draft, "")
	chatEqual(t, "binding stayed in direct view", h.m.State().InReplyTo, "")
	conversationOpen(h, "alice")
	request := v2Send(h)
	chatEqual(t, "reply addressed to original sender", request.To, "alice")
	chatEqual(t, "reply correlation retained", request.InReplyTo, "conversation-target")
}

func TestChatConversationReplyRefusesOccupiedDestination(t *testing.T) {
	h := conversationHarness(t, "alice", "bob")
	conversationOpen(h, "alice")
	v2Type(h, "protected alice draft")
	v2Command(h, "/chat all")
	v2Type(h, "common draft")
	v2Command(h, "/reply #7")
	chatEqual(t, "refused reply preserves source", h.m.State().Draft, "common draft")
	chatEqual(t, "refused reply creates no binding", h.m.State().InReplyTo, "")
	if !strings.Contains(h.m.State().Status, "unsent draft") {
		t.Fatal("missing conflict explanation")
	}
	h.input(chatKey(tea.KeyEscape, 0))
	conversationOpen(h, "alice")
	chatEqual(t, "destination survives refused move", h.m.State().Draft, "protected alice draft")
}

func TestChatConversationReplyMovePreservesPastedLiteralText(t *testing.T) {
	h := conversationHarness(t, "alice", "bob")
	h.input(tea.PasteMsg{Content: "@bob /quit literal reply text"})
	v2Command(h, "/reply #7")
	chatEqual(t, "moving reply keeps paste provenance", h.m.State().Pasted, true)
	chatEqual(t, "pasted text stays intact", h.m.State().Draft, "@bob /quit literal reply text")
	sent := v2Send(h)
	chatEqual(t, "paste never readdresses reply", sent.To, "alice")
	chatEqual(t, "explicit target retained", sent.InReplyTo, "conversation-target")
	chatEqual(t, "literal payload", v2Body(t, sent), "@bob /quit literal reply text")
}

func TestChatConversationLiteralAllStillRejectsBroadcastMention(t *testing.T) {
	h := conversationHarness(t, "all")
	conversationOpen(h, "all")
	v2Type(h, "@all broadcast keyword")
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "broadcast keyword cannot be reinterpreted as a direct ID", h.m.State().Intent, nil)
	v3Contains(t, h.m.State().Audience.Error, "only in All")
}

func TestChatConversationOfflineSavedDraftRemainsReachable(t *testing.T) {
	h := conversationHarness(t, "alice", "bob")
	conversationOpen(h, "bob")
	v2Type(h, "unsent draft for offline agent")
	v2Command(h, "/chat all")
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventPresenceChanged, Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2},
		AgentID: "bob", SessionID: "session-bob", State: "disconnected",
	}}
	h.await(func() bool { return h.m.State().Revision == 2 })
	conversationOpen(h, "bob")
	chatEqual(t, "saved draft can be reopened after presence vanishes", h.m.State().Draft, "unsent draft for offline agent")
	chatEqual(t, "reopened direct identity retained", h.m.State().Selected, "bob")
}
