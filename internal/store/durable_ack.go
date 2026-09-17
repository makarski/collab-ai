package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"collab-ai/internal/protocol"
)

func (s *Store) isDurable(ctx context.Context, id string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM durable_messages WHERE message_id = ?`, id).Scan(&count)
	return count != 0, err
}

type durableReceipt struct {
	sender       protocol.Recipient
	session      string
	received     sql.NullTime
	acknowledged sql.NullTime
}

func (s *Store) acknowledgeDurable(ctx context.Context, ack Acknowledgment) (protocol.Recipient, error) {
	if ack.ProtocolVersion < protocol.DurableVersion {
		return protocol.Recipient{}, ErrInvalidAck
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Recipient{}, err
	}
	defer tx.Rollback()
	receipt, err := readDurableReceipt(ctx, tx, ack)
	if err != nil {
		return protocol.Recipient{}, err
	}
	if err := receipt.validate(ack); err != nil {
		return protocol.Recipient{}, err
	}
	if !receipt.acknowledged.Valid {
		if err := updateDurableReceipt(ctx, tx, ack); err != nil {
			return protocol.Recipient{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return protocol.Recipient{}, err
	}
	return receipt.sender, nil
}

func readDurableReceipt(ctx context.Context, tx *sql.Tx, ack Acknowledgment) (durableReceipt, error) {
	var r durableReceipt
	query := `SELECT m.sender, mm.sender_session, i.session_id, i.received_at, i.acknowledged_at FROM durable_inbox i JOIN message_metadata mm USING(message_id) JOIN messages m USING(seq) WHERE i.message_id = ? AND i.agent_id = ?`
	err := tx.QueryRowContext(ctx, query, ack.MessageID, ack.Recipient.AgentID).Scan(&r.sender.AgentID, &r.sender.SessionID, &r.session, &r.received, &r.acknowledged)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrInvalidAck
	}
	return r, err
}

func (r durableReceipt) validate(ack Acknowledgment) error {
	if ack.Stage != protocol.StageAdapterReceived && ack.Stage != protocol.StageAgentAcknowledged {
		return ErrInvalidAck
	}
	// A committed acknowledgment can be retried by the current logical owner,
	// even when its confirmation was lost and the original session is gone.
	if r.acknowledged.Valid {
		return nil
	}
	if r.session != ack.Recipient.SessionID {
		return ErrInvalidAck
	}
	if ack.Stage == protocol.StageAgentAcknowledged && !r.received.Valid {
		return ErrInvalidAck
	}
	return nil
}

func updateDurableReceipt(ctx context.Context, tx *sql.Tx, ack Acknowledgment) error {
	column := "received_at"
	legacyColumn := "adapter_received_at"
	if ack.Stage == protocol.StageAgentAcknowledged {
		column, legacyColumn = "acknowledged_at", "agent_acknowledged_at"
	}
	now := time.Now().UTC()
	query := `UPDATE durable_inbox SET ` + column + ` = COALESCE(` + column + `, ?) WHERE message_id = ? AND agent_id = ?`
	if _, err := tx.ExecContext(ctx, query, now, ack.MessageID, ack.Recipient.AgentID); err != nil {
		return err
	}
	query = `UPDATE message_receipts SET ` + legacyColumn + ` = COALESCE(` + legacyColumn + `, ?) WHERE message_id = ? AND agent_id = ?`
	_, err := tx.ExecContext(ctx, query, now, ack.MessageID, ack.Recipient.AgentID)
	return err
}
