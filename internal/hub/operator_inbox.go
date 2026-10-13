package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"collab-ai/internal/protocol"
	"collab-ai/internal/store"
)

// operatorReceive returns a page and changes no receipts. Its audit row holds
// bounds and counts only; without it, nothing is returned.
// pageRead reads one page that returns message text, as a response frame and
// a payload-free audit detail.
type pageRead func(context.Context, operatorRequest, protocol.PageRequest) (protocol.Message, string, error)

// operatorRead runs a page read and audits it; without the audit row, nothing
// is returned.
func (h *Hub) operatorRead(ctx context.Context, req operatorRequest, action string, read pageRead) protocol.Message {
	var page protocol.PageRequest
	if req.msg.PageRequest != nil {
		page = *req.msg.PageRequest
	}
	resp, detail, err := read(ctx, req, page)
	if err != nil {
		rerr := h.inboxError(action, err)
		h.auditRejected(ctx, action, req, rerr)
		return rerr.frame("")
	}
	if _, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: action, PrincipalUID: req.uid, Result: store.AuditAccepted, Detail: detail}); err != nil {
		h.log.Error("operator read audit failed; page not returned", "action", action, "error", err)
		return (&routingError{protocol.ErrInternal, "operator audit unavailable"}).frame("")
	}
	return resp
}

// readInbox returns unacknowledged operator-inbox messages; it changes no receipts.
func (h *Hub) readInbox(ctx context.Context, _ operatorRequest, page protocol.PageRequest) (protocol.Message, string, error) {
	out, err := h.store.ReadOperatorPage(ctx, page)
	detail := fmt.Sprintf("after=%d upper=%d count=%d more=%t", page.AfterSeq, out.UpperSeq, len(out.Messages), out.More)
	return protocol.Message{Type: protocol.TypeOperatorPage, Page: &out}, detail, err
}

func (h *Hub) operatorReceived(ctx context.Context, clients map[string]*Client, req operatorRequest) protocol.Message {
	if busy := h.ownerBusy(req); busy != nil {
		h.auditRejected(ctx, "received", req, &routingError{busy.Code, busy.Detail})
		return *busy
	}
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
	if len(transitions) > 0 {
		h.emitInbox(ctx)
	}
	return protocol.Message{Type: protocol.TypeAck, Stage: protocol.StageAdapterReceived, AgentID: protocol.OperatorID, Receipts: receipts}
}

// operatorAck records the human's explicit receipt; it is not approval.
func (h *Hub) operatorAck(ctx context.Context, clients map[string]*Client, req operatorRequest) protocol.Message {
	if busy := h.ownerBusy(req); busy != nil {
		h.auditRejected(ctx, "ack", req, &routingError{busy.Code, busy.Detail})
		return *busy
	}
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
		h.emitInbox(ctx)
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

// readHistory returns a read-only page of the operator's conversation.
func (h *Hub) readHistory(ctx context.Context, req operatorRequest, page protocol.PageRequest) (protocol.Message, string, error) {
	out, err := h.store.ReadOperatorHistory(ctx, store.HistoryQuery{Page: page, Budget: h.historyBudget(req.msg.RequestID)})
	out.Watermark = h.watermark()
	detail := fmt.Sprintf("before=%d upper=%d count=%d more=%t", page.BeforeSeq, out.UpperSeq, len(out.Entries), out.More)
	return protocol.Message{Type: protocol.TypeOperatorHistory, History: &out}, detail, err
}

// historyBudget is the frame limit minus the exact response envelope, with
// every numeric field at its widest. The first entry's comma (counted by the
// store) offsets the trailing newline.
func (h *Hub) historyBudget(requestID string) int {
	widest := ^uint64(0)
	envelope := protocol.Message{Type: protocol.TypeOperatorHistory, RequestID: requestID, History: &protocol.HistoryPage{
		Watermark: protocol.Watermark{Epoch: h.epoch, Revision: widest}, UpperSeq: widest, NextBeforeSeq: widest, More: false, // "false" is the wider boolean
		Entries: []protocol.HistoryEntry{}}}
	data, err := json.Marshal(envelope)
	if err != nil {
		return 0
	}
	return protocol.MaxFrameBytes - len(data)
}
