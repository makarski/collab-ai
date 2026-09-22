package hub

import (
	"context"
	"errors"
	"sort"
	"time"

	"collab-ai/internal/protocol"
)

type statusRequest struct {
	ctx   context.Context
	reply chan protocol.StatusSnapshot
}

// Status serializes observation with routing, without registering an agent or
// allocating a message sequence. The buffered reply cannot block the hub if
// the inspecting client disappears while the snapshot is being read.
func (h *Hub) Status(ctx context.Context) (protocol.StatusSnapshot, error) {
	req := statusRequest{ctx: ctx, reply: make(chan protocol.StatusSnapshot, 1)}
	select {
	case <-ctx.Done():
		return protocol.StatusSnapshot{}, ctx.Err()
	case <-h.done:
		return protocol.StatusSnapshot{}, errors.New("broker stopped")
	case h.status <- req:
	}
	select {
	case <-ctx.Done():
		return protocol.StatusSnapshot{}, ctx.Err()
	case <-h.done:
		return protocol.StatusSnapshot{}, errors.New("broker stopped")
	case out := <-req.reply:
		return out, nil
	}
}

func (h *Hub) snapshot(ctx context.Context, clients map[string]*Client) protocol.StatusSnapshot {
	now := time.Now().UTC()
	count := len(clients)
	out := protocol.UnavailableStatus()
	out.Reachable, out.Health = true, "ready"
	out.SnapshotAt, out.BrokerStartedAt = &now, &h.startedAt
	out.ProtocolVersion, out.ConnectedSessions = protocol.Version, &count
	out.Sessions, out.SessionsTruncated = liveSessions(clients)
	// Status storage reads must not hold up routing indefinitely.
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	records, err := h.store.ReadStatus(ctx)
	if err != nil {
		h.log.Error("status storage query failed", "error", err)
		out.Health, out.Error = "degraded", "session history and pending counts unavailable"
		return out
	}
	out.HistoryAvailable = true
	out.DurablePending = &records.Pending
	appendHistory(&out, clients, records.Sessions)
	return out
}

func liveSessions(clients map[string]*Client) ([]protocol.SessionStatus, bool) {
	ids := make([]string, 0, len(clients))
	for id := range clients {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	truncated := len(ids) > protocol.StatusLimit
	ids = ids[:min(len(ids), protocol.StatusLimit)]
	out := make([]protocol.SessionStatus, 0, len(ids))
	for _, id := range ids {
		c := clients[id]
		seen := c.lastSeenAt
		out = append(out, protocol.SessionStatus{AgentID: c.ID, SessionID: c.SessionID,
			State: "transport_connected", ConnectedAt: c.connectedAt, LastSeenAt: &seen})
	}
	return out, truncated
}

func appendHistory(out *protocol.StatusSnapshot, clients map[string]*Client, history []protocol.SessionStatus) {
	if len(history) > protocol.StatusLimit {
		out.SessionsTruncated = true
	}
	for _, session := range history {
		if c := clients[session.AgentID]; c != nil && c.SessionID == session.SessionID {
			continue
		}
		if len(out.Sessions) == protocol.StatusLimit {
			out.SessionsTruncated = true
			return
		}
		out.Sessions = append(out.Sessions, session)
	}
}
