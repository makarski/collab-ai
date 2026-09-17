package hub

import (
	"context"
	"time"

	"collab-ai/internal/protocol"
)

func (h *Hub) connect(clients map[string]*Client, c *Client) {
	if owner := clients[c.ID]; owner != nil {
		rejectRegistration(c, protocol.Message{Code: protocol.ErrDuplicateID, OwnerSessionID: owner.SessionID,
			Detail: "agent id " + c.ID + " is owned by session " + owner.SessionID + "; disconnect that owner before retrying, or use a distinct agent_id"})
		return
	}
	if err := h.recordConnect(c); err != nil {
		rejectRegistration(c, protocol.Message{Code: protocol.ErrInternal, Detail: "session not persisted; registration rejected"})
		return
	}
	pending, err := h.claimPending(c)
	if err != nil {
		h.log.Error("recover inbox failed", "agent_id", c.ID, "error", err)
		h.recordDisconnect(c)
		rejectRegistration(c, protocol.Message{Code: protocol.ErrInternal, Detail: "durable inbox recovery failed; registration rejected; retry after fixing storage"})
		return
	}
	clients[c.ID] = c
	h.seq++
	h.enqueue(clients, c, protocol.Message{Type: protocol.TypeWelcome, AgentID: c.ID, SessionID: c.SessionID, Seq: h.seq, ProtocolVersion: c.ProtocolVersion})
	for _, msg := range pending {
		if !h.enqueue(clients, c, msg) {
			return
		}
	}
	h.log.Info("agent connected", "agent_id", c.ID, "session_id", c.SessionID, "replayed", len(pending), "online", len(clients))
}

func rejectRegistration(c *Client, frame protocol.Message) {
	frame.Type, frame.AgentID, frame.SessionID = protocol.TypeError, c.ID, c.SessionID
	c.Send <- frame
	close(c.Send)
}

func (h *Hub) claimPending(c *Client) ([]protocol.Message, error) {
	if c.ProtocolVersion < protocol.DurableVersion {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.store.ClaimPending(ctx, c.identity())
}

func validateDurability(in Inbound) *routingError {
	if !in.Msg.Durable {
		return nil
	}
	if in.From.ProtocolVersion < protocol.DurableVersion {
		return &routingError{protocol.ErrDurabilityUnavailable, "durable delivery requires protocol_version 3"}
	}
	if !in.Msg.AckRequested {
		return &routingError{protocol.ErrMalformedFrame, "durable delivery requires ack_requested"}
	}
	if in.Msg.To != protocol.Broadcast {
		if err := protocol.ValidateAgentID(in.Msg.To); err != nil {
			return &routingError{protocol.ErrMalformedFrame, err.Error()}
		}
	}
	return nil
}

func (h *Hub) resolveRecipients(ctx context.Context, clients map[string]*Client, in Inbound) ([]protocol.Recipient, *routingError) {
	if in.Msg.To == protocol.Broadcast {
		recipients, err := broadcastRecipients(clients, in.From)
		if err != nil {
			return nil, err
		}
		return recipients, checkRecipientVersions(clients, in.Msg, recipients)
	}
	if c := clients[in.Msg.To]; c != nil {
		recipients := []protocol.Recipient{c.identity()}
		return recipients, checkRecipientVersions(clients, in.Msg, recipients)
	}
	return h.offlineRecipient(ctx, in.Msg)
}

func checkRecipientVersions(clients map[string]*Client, msg protocol.Message, recipients []protocol.Recipient) *routingError {
	if !msg.Durable {
		return nil
	}
	for _, r := range recipients {
		if clients[r.AgentID].ProtocolVersion < protocol.DurableVersion {
			return &routingError{protocol.ErrDurabilityUnavailable, "recipient " + r.AgentID + " must upgrade to protocol_version 3; message not accepted"}
		}
	}
	return nil
}

func (h *Hub) offlineRecipient(ctx context.Context, msg protocol.Message) ([]protocol.Recipient, *routingError) {
	if msg.Durable {
		known, err := h.store.KnownDurableAgent(ctx, msg.To)
		if err != nil {
			return nil, &routingError{protocol.ErrInternal, "cannot determine durable recipient registration"}
		}
		if known {
			return []protocol.Recipient{{AgentID: msg.To}}, nil
		}
	}
	return nil, &routingError{protocol.ErrUnknownRecipient, "no eligible recipient with id " + msg.To + "; durable offline delivery requires prior v3 registration"}
}

func (h *Hub) reportRetry(ctx context.Context, clients map[string]*Client, in Inbound) bool {
	request := in.Msg
	request.From = in.From.ID
	snapshot, err := h.store.LookupRetry(ctx, request)
	if err != nil {
		h.enqueue(clients, in.From, h.persistenceError(request.MessageID, err).frame(request.MessageID))
		return true
	}
	if snapshot == nil {
		return false
	}
	h.enqueue(clients, in.From, protocol.Message{Type: protocol.TypeAck, MessageID: request.MessageID,
		Stage: protocol.StageAccepted, Seq: snapshot.Message.Seq, Durable: true, Repeated: true,
		Recipients: snapshot.Recipients, Receipts: snapshot.Receipts})
	return true
}

func disconnectDetail(durable bool) string {
	if durable {
		return "recipient session disconnected before agent acknowledgment; pending message retained for replay to its next v3 owner"
	}
	return "recipient session disconnected before agent acknowledgment; this non-durable message has no replay"
}
