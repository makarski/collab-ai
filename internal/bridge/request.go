package bridge

import (
	"encoding/json"
	"errors"

	"collab-ai/internal/protocol"

	"github.com/google/uuid"
)

type SendRequest struct {
	To        string `json:"to" jsonschema:"Recipient agent ID, or * to broadcast to other connected agents"`
	Text      string `json:"text" jsonschema:"Message text for the other agent"`
	MessageID string `json:"message_id,omitempty" jsonschema:"Optional stable ID for this message, maximum 128 bytes; generated when omitted. Reusing an accepted ID is rejected without routing again"`
	InReplyTo string `json:"in_reply_to,omitempty" jsonschema:"Message ID this message replies to"`
}

func (r SendRequest) message() (protocol.Message, error) {
	if r.To == "" || r.Text == "" {
		return protocol.Message{}, errors.New("to and text are required")
	}
	if len(r.MessageID) > 128 || len(r.InReplyTo) > 128 {
		return protocol.Message{}, errors.New("message_id and in_reply_to must be at most 128 bytes")
	}
	if r.MessageID == "" {
		r.MessageID = uuid.NewString()
	}
	payload, err := json.Marshal(struct {
		Text string `json:"text"`
	}{r.Text})
	return protocol.Message{Type: protocol.TypeMsg, To: r.To, Payload: payload, MessageID: r.MessageID, InReplyTo: r.InReplyTo, AckRequested: true}, err
}
