package host

import (
	"encoding/json"
	"testing"
)

func sessionReport(s *sessionAccounting) {
	s.event(Frame{Method: "thread/tokenUsage/updated", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","tokenUsage":{"total":{"totalTokens":20}}}`)})
}

func sessionComplete(s *sessionAccounting) {
	s.event(Frame{Method: "turn/completed", Params: json.RawMessage(`{"turn":{"id":"turn","status":"completed"}}`)})
}

func TestSessionRequiresReplyCompletionAndUsage(t *testing.T) {
	var s sessionAccounting
	s.begin("request", "")
	if s.settled() {
		t.Fatal("pending admission settled")
	}
	s.reply("request", json.RawMessage(`{"turn":{"id":"turn"}}`), false)
	sessionComplete(&s)
	if s.settled() {
		t.Fatal("missing usage settled")
	}
	sessionReport(&s)
	if !s.settled() {
		t.Fatal("complete reported turn not settled")
	}
	s.begin("steer", "turn")
	s.reply("steer", json.RawMessage(`{"turnId":"turn"}`), false)
	if s.settled() {
		t.Fatal("steering reused a previous completion")
	}
}

func TestSessionRetainsEventsBeforeReply(t *testing.T) {
	var s sessionAccounting
	s.begin("request", "")
	sessionReport(&s)
	sessionComplete(&s)
	if s.settled() {
		t.Fatal("unanswered admission settled")
	}
	s.reply("request", json.RawMessage(`{"turn":{"id":"turn"}}`), false)
	if !s.settled() {
		t.Fatal("early observations lost")
	}
}

func TestFailedAdmissionCannotBecomeSettled(t *testing.T) {
	var s sessionAccounting
	s.begin("request", "")
	s.reply("request", nil, true)
	sessionReport(&s)
	sessionComplete(&s)
	if s.settled() {
		t.Fatal("uncertain admission cleared")
	}
}
