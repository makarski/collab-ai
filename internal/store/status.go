package store

import (
	"context"
	"database/sql"

	"collab-ai/internal/protocol"
)

type StatusRecords struct {
	Sessions []protocol.SessionStatus
	Pending  protocol.PendingStatus
}

// ReadStatus reads a consistent, bounded view without updating session history,
// claiming inboxes, or fetching message payloads. An extra session marks overflow.
func (s *Store) ReadStatus(ctx context.Context) (StatusRecords, error) {
	var out StatusRecords
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	out.Sessions, err = recentSessions(ctx, tx)
	if err != nil {
		return StatusRecords{}, err
	}
	out.Pending, err = pendingStatus(ctx, tx)
	return out, err
}

func recentSessions(ctx context.Context, tx *sql.Tx) ([]protocol.SessionStatus, error) {
	rows, err := tx.QueryContext(ctx, `SELECT agent_id, session_id, connected_at, disconnected_at FROM agents ORDER BY connected_at DESC, agent_id, session_id LIMIT ?`, protocol.StatusLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.SessionStatus{}
	for rows.Next() {
		var session protocol.SessionStatus
		if err := rows.Scan(&session.AgentID, &session.SessionID, &session.ConnectedAt, &session.DisconnectedAt); err != nil {
			return nil, err
		}
		session.State = "stale"
		if session.DisconnectedAt != nil {
			session.State = "disconnected"
		}
		out = append(out, session)
	}
	return out, rows.Err()
}

func pendingStatus(ctx context.Context, tx *sql.Tx) (protocol.PendingStatus, error) {
	out := protocol.PendingStatus{Recipients: []protocol.PendingRecipient{}, RecipientLimit: protocol.StatusLimit}
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM durable_inbox WHERE acknowledged_at IS NULL`).Scan(&out.Total)
	if err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT agent_id, COUNT(*) FROM durable_inbox WHERE acknowledged_at IS NULL GROUP BY agent_id ORDER BY agent_id LIMIT ?`, protocol.StatusLimit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var r protocol.PendingRecipient
		if err := rows.Scan(&r.AgentID, &r.Pending); err != nil {
			return out, err
		}
		out.Recipients = append(out.Recipients, r)
	}
	out.RecipientsTruncated = len(out.Recipients) > protocol.StatusLimit
	if out.RecipientsTruncated {
		out.Recipients = out.Recipients[:protocol.StatusLimit]
	}
	return out, rows.Err()
}
