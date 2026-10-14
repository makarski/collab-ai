package chat

import (
	"encoding/json"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"collab-ai/internal/protocol"
)

// Send intent states. Nothing is shown as accepted until the broker says so.
const (
	IntentSending  = "sending"
	IntentUnknown  = "unknown"
	IntentAccepted = "accepted"
	IntentRejected = "rejected"
)

// intent is a frozen send: retries reuse exactly this ID, scope, payload and
// correlation. Editing the draft never changes it.
type intent struct {
	ID         string
	Scope      protocol.Message
	Payload    json.RawMessage
	InReplyTo  string
	State      string
	Recipients []protocol.Recipient
	Excluded   []string
	Detail     string
}

// broadcastConfirm holds an @all send until the operator answers y or n.
type broadcastConfirm struct {
	scope     protocol.Message
	payload   json.RawMessage
	inReplyTo string
	preview   int
}

type sendState struct {
	intent    *intent
	confirm   *broadcastConfirm
	inReplyTo string
}

func (s *sendState) unresolved() bool {
	return s.intent != nil && (s.intent.State == IntentSending || s.intent.State == IntentUnknown)
}

// disconnected leaves an in-flight send with an unknown outcome.
func (s *sendState) disconnected() {
	if s.intent != nil && s.intent.State == IntentSending {
		s.intent.State = IntentUnknown
	}
}

// submit freezes the draft into a send, asking first for @all.
func (m *Model) submit() tea.Cmd {
	text := m.editor.Value()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	scope, err := ParseRecipients(m.recipients.Value())
	if err != nil {
		m.status = err.Error()
		return nil
	}
	if m.send.unresolved() {
		m.status = "the previous send is still unresolved"
		return nil
	}
	payload, err := encodeText(text)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	if isBroadcast(scope) {
		m.send.confirm = &broadcastConfirm{scope: scope, payload: payload, inReplyTo: m.send.inReplyTo, preview: len(m.agents.list)}
		return nil
	}
	return m.dispatch(scope, payload, m.send.inReplyTo)
}

// encodeText builds the payload and enforces the operator payload cap on its
// encoded form, before anything is sent.
func encodeText(text string) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(json.RawMessage(payload))
	if err != nil {
		return nil, err
	}
	if len(encoded) > protocol.OperatorPayloadMax {
		return nil, errTooLarge
	}
	return payload, nil
}

func (m *Model) dispatch(scope protocol.Message, payload json.RawMessage, inReplyTo string) tea.Cmd {
	m.send.intent = &intent{ID: uuid.NewString(), Scope: scope, Payload: payload, InReplyTo: inReplyTo, State: IntentSending}
	m.send.inReplyTo = ""
	m.editor.Reset()
	return m.sendIntent()
}

func (m *Model) sendIntent() tea.Cmd {
	in := m.send.intent
	in.State = IntentSending
	msg := protocol.Message{Type: protocol.TypeOperatorSend, MessageID: in.ID, To: in.Scope.To, Group: in.Scope.Group, Payload: in.Payload, InReplyTo: in.InReplyTo}
	return m.request(kindSend, msg, in.ID)
}

// resendUnknown retries an unknown send with exactly the same intent.
func (m *Model) resendUnknown() tea.Cmd {
	if m.send.intent == nil || m.send.intent.State != IntentUnknown {
		return nil
	}
	return m.sendIntent()
}

func (m *Model) confirmAnswer(yes bool) tea.Cmd {
	c := m.send.confirm
	m.send.confirm = nil
	if !yes {
		m.status = "broadcast cancelled; draft kept"
		return nil
	}
	return m.dispatch(c.scope, c.payload, c.inReplyTo)
}

func (m *Model) sendResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	in := m.send.intent
	if in == nil || in.ID != req.ids[0] {
		return nil
	}
	m.quitArmed = false
	if sendAccepted(resp, in.ID) {
		in.State, in.Recipients, in.Excluded = IntentAccepted, resp.Recipients, resp.Excluded
		return m.startCatchup()
	}
	// Only an explicit broker error rejects; anything else leaves the outcome unknown.
	if resp.Type != protocol.TypeError {
		in.State = IntentUnknown
		return m.malformed("send response: " + brokerReason(resp))
	}
	in.State, in.Detail = IntentRejected, brokerReason(resp)
	if m.editor.Value() == "" {
		m.editor.SetValue(payloadText(in.Payload))
	}
	return nil
}

func sendAccepted(resp protocol.Message, id string) bool {
	if resp.Type != protocol.TypeAck {
		return false
	}
	return resp.Stage == protocol.StageAccepted && resp.MessageID == id
}

// reply addresses the sender of a received message; sent messages carry no
// trusted original scope, so they never get an automatic recipient.
func (m *Model) reply(e *entry) {
	if e.direction != "received" {
		m.status = "choose recipients for a new send"
		return
	}
	m.recipients.SetValue(literalRecipient(e.msg.From))
	m.send.inReplyTo = e.msg.MessageID
	m.setFocus(FocusEditor)
	m.status = "replying to " + strconv.Quote(e.msg.MessageID)
}

// literalRecipient writes an ID so ParseRecipients reads it back unchanged;
// anything that wouldn't round-trip as plain text is written as a JSON array.
func literalRecipient(id string) string {
	if scope, err := ParseRecipients(id); err == nil && scope.To == id {
		return id
	}
	data, _ := json.Marshal([]string{id})
	return string(data)
}

func payloadText(payload json.RawMessage) string {
	var body struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(payload, &body) == nil && body.Text != nil {
		return *body.Text
	}
	return string(payload)
}

// agentView is the current presence baseline; omitted agents are unknown.
type agentView struct {
	list    []protocol.AgentPresence
	total   int
	partial bool
}

func (a *agentView) apply(ev *protocol.SessionEvent) {
	for i, p := range a.list {
		if p.AgentID != ev.AgentID {
			continue
		}
		if ev.State == "disconnected" {
			a.list = append(a.list[:i], a.list[i+1:]...)
			a.total--
			return
		}
		a.list[i] = protocol.AgentPresence{AgentID: ev.AgentID, SessionID: ev.SessionID, ProtocolVersion: ev.ProtocolVersion}
		return
	}
	if ev.State == "connected" {
		a.list = append(a.list, protocol.AgentPresence{AgentID: ev.AgentID, SessionID: ev.SessionID, ProtocolVersion: ev.ProtocolVersion})
		a.total++
	}
}
