package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"collab-ai/internal/protocol"
)

var ErrInvalidMessage = errors.New("message is not an operator-sent message")

// OperatorInboxState returns the newest pending operator-inbox sequence and
// the pending count.
func (s *Store) OperatorInboxState(ctx context.Context) (uint64, int, error) {
	var head uint64
	var pending int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(mm.seq), 0), COUNT(*) FROM durable_inbox i JOIN message_metadata mm USING(message_id) WHERE i.agent_id = ? AND i.acknowledged_at IS NULL`, protocol.OperatorID).Scan(&head, &pending)
	return head, pending, err
}

// RecentOperatorSent returns up to limit operator-sent messages, newest first,
// each with up to stageLimit current recipient stages (stageLimit+1 marks more).
func (s *Store) RecentOperatorSent(ctx context.Context, limit, stageLimit int) ([]protocol.SentSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mm.message_id, m.seq, m.ts, m.recipient FROM message_metadata mm JOIN messages m USING(seq) WHERE mm.origin = 'operator' ORDER BY m.seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	var sent []protocol.SentSummary
	for rows.Next() {
		var m protocol.SentSummary
		if err := rows.Scan(&m.MessageID, &m.Seq, &m.TS, &m.To); err != nil {
			rows.Close()
			return nil, err
		}
		sent = append(sent, m)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i := range sent {
		stages, err := s.recipientStages(ctx, sent[i].MessageID, "", stageLimit+1)
		if err != nil {
			return nil, err
		}
		sent[i].ReceiptsTruncated = len(stages) > stageLimit
		sent[i].Recipients = stages[:min(len(stages), stageLimit)]
	}
	return sent, nil
}

// MessageStages pages current recipient stages of one operator-sent message
// in agent-ID order. It changes nothing.
func (s *Store) MessageStages(ctx context.Context, id, after string, limit int) (protocol.StagePage, error) {
	page := protocol.StagePage{MessageID: id, Recipients: []protocol.RecipientStage{}, NextAfterAgent: after}
	var origin sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT origin FROM message_metadata WHERE message_id = ?`, id).Scan(&origin)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && origin.String != protocol.OriginOperator) {
		return page, ErrInvalidMessage
	}
	if err != nil {
		return page, err
	}
	if err := s.checkAgentCursor(ctx, id, after); err != nil {
		return page, err
	}
	stages, err := s.recipientStages(ctx, id, after, limit+1)
	if err != nil {
		return page, err
	}
	page.More = len(stages) > limit
	page.Recipients = stages[:min(len(stages), limit)]
	if n := len(page.Recipients); n > 0 {
		page.NextAfterAgent = page.Recipients[n-1].AgentID
	}
	return page, nil
}

func (s *Store) checkAgentCursor(ctx context.Context, id, after string) error {
	if after == "" {
		return nil
	}
	var found int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_receipts WHERE message_id = ? AND agent_id = ?`, id, after).Scan(&found); err != nil {
		return err
	}
	if found == 0 {
		return ErrInvalidCursor
	}
	return nil
}

func (s *Store) recipientStages(ctx context.Context, id, after string, limit int) ([]protocol.RecipientStage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.agent_id, COALESCE(i.session_id, r.session_id), i.received_at, i.acknowledged_at FROM message_receipts r LEFT JOIN durable_inbox i ON i.message_id = r.message_id AND i.agent_id = r.agent_id WHERE r.message_id = ? AND r.agent_id > ? ORDER BY r.agent_id LIMIT ?`, id, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stages := []protocol.RecipientStage{}
	for rows.Next() {
		var st protocol.RecipientStage
		var received, acknowledged sql.NullTime
		if err := rows.Scan(&st.AgentID, &st.SessionID, &received, &acknowledged); err != nil {
			return nil, err
		}
		st.Stage = currentStage(received, acknowledged)
		stages = append(stages, st)
	}
	return stages, rows.Err()
}

func currentStage(received, acknowledged sql.NullTime) string {
	switch {
	case acknowledged.Valid:
		return protocol.StageAgentAcknowledged
	case received.Valid:
		return protocol.StageAdapterReceived
	}
	return protocol.StageAccepted
}

// RecordSessionAudit writes a session open or close row.
func (s *Store) RecordSessionAudit(ctx context.Context, action string, uid int, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO operator_audit (ts, action, principal_uid, scope, result) VALUES (?, ?, ?, ?, ?)`, time.Now().UTC(), action, uid, sessionID, AuditAccepted)
	return err
}
