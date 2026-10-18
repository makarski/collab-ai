package chat

import (
	"encoding/json"

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

type sendState struct {
	intent *intent
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

// submit freezes the draft, its audience and reply into a send intent. The
// hint line shows the audience beforehand, so there is no confirmation.
func (m *Model) submit() tea.Cmd {
	if !m.draftBody() {
		if !m.compose.draftEmpty() {
			m.status = "message is empty"
		}
		return nil
	}
	if m.tooSmall() {
		m.status = "terminal too small to send"
		return nil
	}
	aud := m.resolveAudience()
	if aud.err != "" {
		m.status = aud.err
		return nil
	}
	scope, err := aud.scope()
	if err != nil {
		m.status = err.Error()
		return nil
	}
	if !m.ready() {
		m.status = "not connected; the draft is kept"
		return nil
	}
	if m.send.unresolved() {
		m.status = "the previous send is still unresolved"
		return nil
	}
	payload, err := encodeText(m.outgoingText())
	if err != nil {
		m.status = err.Error()
		return nil
	}
	inReplyTo := ""
	if r := m.compose.reply; r != nil {
		inReplyTo = r.messageID
	}
	return m.dispatch(scope, payload, inReplyTo)
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

// dispatch queues the frozen intent; only once it is queued does the composer
// let go of the draft and its audience.
func (m *Model) dispatch(scope protocol.Message, payload json.RawMessage, inReplyTo string) tea.Cmd {
	m.send.intent = &intent{ID: uuid.NewString(), Scope: scope, Payload: payload, InReplyTo: inReplyTo, State: IntentSending}
	cmd := m.sendIntent()
	m.compose.resetDraft()
	m.sizeComposer()
	m.status = ""
	m.follow()
	return cmd
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
	// Give the text back only to an empty composer; never touch a newer draft.
	if m.compose.draftEmpty() && m.compose.cmd == nil {
		m.compose.editor.SetValue(payloadText(in.Payload))
		m.compose.pasted = true // restored text is literal; its audience isn't re-read
		m.sizeComposer()
	}
	return nil
}

func sendAccepted(resp protocol.Message, id string) bool {
	if resp.Type != protocol.TypeAck {
		return false
	}
	return resp.Stage == protocol.StageAccepted && resp.MessageID == id
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
