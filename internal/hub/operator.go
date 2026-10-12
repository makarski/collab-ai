package hub

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"time"

	"collab-ai/internal/protocol"
	"collab-ai/internal/store"
)

type operatorRequest struct {
	ctx   context.Context
	uid   int
	msg   protocol.Message
	reply chan protocol.Message
}

// SubmitOperator routes one request from an authenticated operator connection
// and returns its response frame. The caller has already verified the peer UID.
func (h *Hub) SubmitOperator(ctx context.Context, uid int, msg protocol.Message) protocol.Message {
	req := operatorRequest{ctx: ctx, uid: uid, msg: msg, reply: make(chan protocol.Message, 1)}
	unavailable := protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, Detail: "broker is shutting down", MessageID: msg.MessageID}
	select {
	case <-h.done:
		return unavailable
	case <-ctx.Done():
		return unavailable
	case h.operator <- req:
	}
	select {
	case <-h.done:
		return unavailable
	case out := <-req.reply:
		return out
	}
}

// AuditConnection records an operator connection that never produced a routed
// request: a denied peer, or an authorized peer whose frame could not be read.
// The detail is a fixed, payload-free reason.
func (h *Hub) AuditConnection(ctx context.Context, uid int, result, detail string) error {
	_, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "connect", PrincipalUID: uid, Result: result, Detail: detail})
	return err
}

func (h *Hub) routeOperator(ctx context.Context, clients map[string]*Client, req operatorRequest) protocol.Message {
	msg := req.msg
	scope, rerr := operatorScope(msg)
	if rerr != nil {
		h.auditRejected(ctx, req.uid, msg.MessageID, "", rerr)
		return rerr.frame(msg.MessageID)
	}
	auditID, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "send", PrincipalUID: req.uid, MessageID: msg.MessageID, Scope: scope.Kind, Result: store.AuditPending})
	if err != nil {
		h.log.Error("operator audit intent failed; request not routed", "message_id", msg.MessageID, "error", err)
		return (&routingError{protocol.ErrInternal, "operator audit unavailable; message not routed"}).frame(msg.MessageID)
	}
	out, rerr := h.admitOperator(ctx, clients, msg, scope, auditID)
	if rerr != nil {
		h.finishAudit(ctx, auditID, store.AuditRejected, rerr.code)
		return rerr.frame(msg.MessageID)
	}
	return out
}

func (h *Hub) admitOperator(ctx context.Context, clients map[string]*Client, msg protocol.Message, scope store.OperatorScope, auditID int64) (protocol.Message, *routingError) {
	retry, err := h.store.LookupOperatorRetry(ctx, msg, scope)
	if err != nil {
		return protocol.Message{}, h.persistenceError(msg.MessageID, err)
	}
	if retry != nil {
		if err := h.store.FinishOperatorAudit(ctx, auditID, store.AuditRepeated, ""); err != nil {
			h.log.Error("operator audit result failed", "message_id", msg.MessageID, "error", err)
			return protocol.Message{}, &routingError{protocol.ErrInternal, "operator audit unavailable"}
		}
		return protocol.Message{Type: protocol.TypeAck, MessageID: msg.MessageID, Stage: protocol.StageAccepted, Seq: retry.Seq,
			Durable: true, Repeated: true, Recipients: retry.Recipients, Receipts: retry.Receipts, Excluded: retry.Excluded}, nil
	}
	ts := time.Now().UTC()
	out := protocol.Message{
		Type: protocol.TypeMsg, Seq: h.seq + 1, TS: &ts, From: protocol.OperatorID, To: operatorTo(msg, scope),
		Origin: protocol.OriginOperator, Payload: msg.Payload, MessageID: msg.MessageID, InReplyTo: msg.InReplyTo,
		AckRequested: true, Durable: true,
	}
	recipients, excluded, rerr := h.operatorRecipients(ctx, clients, out, scope)
	if rerr != nil {
		return protocol.Message{}, rerr
	}
	if rerr := checkFrameSize(out); rerr != nil {
		return protocol.Message{}, rerr
	}
	h.seq++
	if err := h.store.PersistOperatorMessage(ctx, out, scope, recipients, excluded, auditID); err != nil {
		return protocol.Message{}, h.persistenceError(msg.MessageID, err)
	}
	for _, r := range recipients {
		if c := sessionOwner(clients, r); c != nil {
			h.enqueue(clients, c, out) // An undelivered durable frame stays pending for replay.
		}
	}
	return protocol.Message{Type: protocol.TypeAck, MessageID: out.MessageID, Stage: protocol.StageAccepted, Seq: out.Seq,
		Durable: true, Recipients: recipients, Excluded: excluded}, nil
}

func (h *Hub) auditRejected(ctx context.Context, uid int, id, scope string, rerr *routingError) {
	_, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "send", PrincipalUID: uid, MessageID: id, Scope: scope, Result: store.AuditRejected, Detail: rerr.code})
	if err != nil {
		h.log.Error("operator audit failed for rejected request", "error", err)
	}
}

func (h *Hub) finishAudit(ctx context.Context, id int64, result, detail string) {
	if err := h.store.FinishOperatorAudit(ctx, id, result, detail); err != nil {
		h.log.Error("operator audit result failed", "audit_id", id, "error", err)
	}
}

// operatorScope validates the request shape and canonicalizes its scope.
func operatorScope(msg protocol.Message) (store.OperatorScope, *routingError) {
	malformed := func(detail string) (store.OperatorScope, *routingError) {
		return store.OperatorScope{}, &routingError{protocol.ErrMalformedFrame, detail}
	}
	if msg.Type != protocol.TypeOperatorSend {
		return malformed("expected operator_send")
	}
	if msg.MessageID == "" || len(msg.MessageID) > 128 || len(msg.InReplyTo) > 128 {
		return malformed("message_id is required; message_id and in_reply_to must be at most 128 bytes")
	}
	if msg.Origin != "" || len(msg.Excluded) != 0 {
		return malformed("origin and excluded are set by the broker")
	}
	if (msg.To == "") == (len(msg.Group) == 0) {
		return malformed("exactly one of to or group is required")
	}
	if msg.To == protocol.Broadcast {
		return store.OperatorScope{Kind: store.ScopeAll}, nil
	}
	members := msg.Group
	kind := store.ScopeGroup
	if msg.To != "" {
		members, kind = []string{msg.To}, store.ScopeDirect
	}
	members = slices.Clone(members)
	sort.Strings(members)
	members = slices.Compact(members)
	if len(members) > protocol.MaxRecipients {
		return malformed("group exceeds 256 recipients")
	}
	for _, id := range members {
		if err := protocol.ValidateAgentID(id); err != nil {
			return malformed(err.Error())
		}
	}
	return store.OperatorScope{Kind: kind, Members: members}, nil
}

// operatorTo is informational; operator_requests holds the canonical scope.
func operatorTo(msg protocol.Message, scope store.OperatorScope) string {
	if scope.Kind == store.ScopeGroup {
		return "group:" + msg.MessageID
	}
	return msg.To
}

func (h *Hub) operatorRecipients(ctx context.Context, clients map[string]*Client, out protocol.Message, scope store.OperatorScope) ([]protocol.Recipient, []string, *routingError) {
	if scope.Kind == store.ScopeAll {
		return h.operatorBroadcast(ctx, clients, out)
	}
	recipients := make([]protocol.Recipient, 0, len(scope.Members))
	for _, id := range scope.Members {
		r, rerr := h.operatorMember(ctx, clients, id)
		if rerr != nil {
			return nil, nil, rerr
		}
		recipients = append(recipients, r)
	}
	return recipients, nil, nil
}

// operatorMember admits a connected v4 agent, or an offline one whose last
// durable registration negotiated v4, so it can display the origin on replay.
func (h *Hub) operatorMember(ctx context.Context, clients map[string]*Client, id string) (protocol.Recipient, *routingError) {
	if c := clients[id]; c != nil {
		if c.ProtocolVersion < protocol.OriginVersion {
			return protocol.Recipient{}, &routingError{protocol.ErrRecipientUnavailable, "recipient " + id + " must upgrade to protocol_version 4 to receive operator messages"}
		}
		return c.identity(), nil
	}
	version, err := h.store.AgentProtocolVersion(ctx, id)
	if err != nil {
		return protocol.Recipient{}, &routingError{protocol.ErrInternal, "cannot determine recipient capability"}
	}
	if version < protocol.OriginVersion {
		return protocol.Recipient{}, &routingError{protocol.ErrUnknownRecipient, "no eligible recipient with id " + id + "; offline operator delivery requires a prior protocol_version 4 registration"}
	}
	return protocol.Recipient{AgentID: id}, nil
}

// operatorBroadcast freezes connected eligible agents in ID order. Agents below
// v4 or with a full inbox are excluded; global limits then apply to all or none.
func (h *Hub) operatorBroadcast(ctx context.Context, clients map[string]*Client, out protocol.Message) ([]protocol.Recipient, []string, *routingError) {
	ids := make([]string, 0, len(clients))
	for id := range clients {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	wire, err := store.DurableWireBytes(out)
	if err != nil {
		return nil, nil, &routingError{protocol.ErrMalformedFrame, "payload is not valid JSON"}
	}
	recipients := []protocol.Recipient{}
	excluded := []string{}
	for _, id := range ids {
		c := clients[id]
		if c.ProtocolVersion < protocol.OriginVersion {
			excluded = append(excluded, id)
			continue
		}
		fits, err := h.store.RecipientCapacity(ctx, id, wire)
		if err != nil {
			return nil, nil, &routingError{protocol.ErrInternal, "cannot determine recipient capacity"}
		}
		if !fits {
			excluded = append(excluded, id)
			continue
		}
		recipients = append(recipients, c.identity())
	}
	if len(recipients) == 0 {
		return nil, nil, &routingError{protocol.ErrRecipientUnavailable, "no eligible recipients"}
	}
	if len(recipients) > protocol.MaxRecipients {
		return nil, nil, &routingError{protocol.ErrMalformedFrame, "too many recipients; send to smaller groups"}
	}
	return recipients, excluded, nil
}

func checkFrameSize(out protocol.Message) *routingError {
	sized := out
	sized.Replayed = true
	encoded, err := json.Marshal(sized)
	if err != nil {
		return &routingError{protocol.ErrMalformedFrame, "payload is not valid JSON"}
	}
	if len(encoded)+1 > protocol.MaxFrameBytes {
		return &routingError{protocol.ErrMalformedFrame, "routed message exceeds the 1 MiB frame limit"}
	}
	return nil
}
