package hub

import (
	"context"
	"errors"
	"fmt"

	"collab-ai/internal/protocol"
	"collab-ai/internal/store"
)

// operatorReceive returns a page and changes no receipts. Its audit row holds
// bounds and counts only; without it, nothing is returned.
func (h *Hub) operatorReceive(ctx context.Context, req operatorRequest) protocol.Message {
	var page protocol.PageRequest
	if req.msg.PageRequest != nil {
		page = *req.msg.PageRequest
	}
	out, err := h.store.ReadOperatorPage(ctx, page)
	if err != nil {
		rerr := h.inboxError("receive page", err)
		h.auditRejected(ctx, "receive", req, rerr)
		return rerr.frame("")
	}
	detail := fmt.Sprintf("after=%d upper=%d count=%d more=%t", page.AfterSeq, out.UpperSeq, len(out.Messages), out.More)
	if _, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "receive", PrincipalUID: req.uid, Result: store.AuditAccepted, Detail: detail}); err != nil {
		h.log.Error("operator receive audit failed; page not returned", "error", err)
		return (&routingError{protocol.ErrInternal, "operator audit unavailable"}).frame("")
	}
	return protocol.Message{Type: protocol.TypeOperatorPage, Page: &out}
}

// operatorReceived confirms that the client adopted these messages.
func (h *Hub) operatorReceived(ctx context.Context, clients map[string]*Client, req operatorRequest) protocol.Message {
	ids, rerr := receivedIDs(req.msg.MessageIDs)
	if rerr != nil {
		h.auditRejected(ctx, "received", req, rerr)
		return rerr.frame("")
	}
	audit := store.OperatorAudit{Action: "received", PrincipalUID: req.uid, Result: store.AuditAccepted, Detail: fmt.Sprintf("count=%d", len(ids))}
	receipts, transitions, err := h.store.MarkOperatorReceived(ctx, ids, audit)
	if err != nil {
		rerr := h.inboxError("mark received", err)
		h.auditRejected(ctx, "received", req, rerr)
		return rerr.frame("")
	}
	for _, t := range transitions {
		h.notifySender(clients, t)
	}
	return protocol.Message{Type: protocol.TypeAck, Stage: protocol.StageAdapterReceived, AgentID: protocol.OperatorID, Receipts: receipts}
}

// operatorAck records the human's explicit receipt; it is not approval.
func (h *Hub) operatorAck(ctx context.Context, clients map[string]*Client, req operatorRequest) protocol.Message {
	id := req.msg.MessageID
	if id == "" || len(id) > 128 {
		rerr := &routingError{protocol.ErrMalformedFrame, "message_id must be 1 to 128 bytes"}
		h.auditRejected(ctx, "ack", req, rerr)
		return rerr.frame(id)
	}
	transition, err := h.store.AcknowledgeOperator(ctx, id, store.OperatorAudit{Action: "ack", PrincipalUID: req.uid, MessageID: id, Result: store.AuditAccepted})
	if err != nil {
		rerr := h.inboxError("acknowledge", err)
		h.auditRejected(ctx, "ack", req, rerr)
		return rerr.frame(id)
	}
	if transition != nil {
		h.notifySender(clients, *transition)
	}
	return protocol.Message{Type: protocol.TypeAck, MessageID: id, Stage: protocol.StageAgentAcknowledged, AgentID: protocol.OperatorID}
}

// receivedIDs requires a non-empty list of valid IDs and removes duplicates.
func receivedIDs(ids []string) ([]string, *routingError) {
	if len(ids) == 0 || len(ids) > protocol.OperatorPageMax {
		return nil, &routingError{protocol.ErrMalformedFrame, "message_ids must hold 1 to 32 IDs"}
	}
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > 128 {
			return nil, &routingError{protocol.ErrMalformedFrame, "each message ID must be 1 to 128 bytes"}
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// notifySender tells only the original sender session, and only on the first
// transition; a later owner of the same ID recovers stages by retry snapshot.
func (h *Hub) notifySender(clients map[string]*Client, t store.OperatorTransition) {
	if c := sessionOwner(clients, t.Sender); c != nil {
		h.enqueue(clients, c, protocol.Message{Type: protocol.TypeAck, MessageID: t.MessageID, Stage: t.Stage, AgentID: protocol.OperatorID})
	}
}

func (h *Hub) inboxError(action string, err error) *routingError {
	switch {
	case errors.Is(err, store.ErrInvalidAck):
		return &routingError{protocol.ErrInvalidAck, "message is not in the operator inbox, or not yet received"}
	case errors.Is(err, store.ErrInvalidCursor), errors.Is(err, store.ErrInvalidLimit):
		return &routingError{protocol.ErrMalformedFrame, err.Error()}
	}
	h.log.Error("operator inbox operation failed", "action", action, "error", err)
	return &routingError{protocol.ErrInternal, "operator inbox unavailable; nothing changed"}
}
