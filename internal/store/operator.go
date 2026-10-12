package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"collab-ai/internal/protocol"
)

const operatorSchema = `
CREATE TABLE IF NOT EXISTS operator_requests (
 message_id TEXT PRIMARY KEY,
 scope_kind TEXT NOT NULL,
 members TEXT,
 recipients TEXT NOT NULL,
 excluded TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS operator_audit (
 id INTEGER PRIMARY KEY,
 ts TIMESTAMP NOT NULL,
 action TEXT NOT NULL,
 principal_uid INTEGER,
 message_id TEXT,
 scope TEXT,
 result TEXT NOT NULL,
 detail TEXT
);
`

// Operator request scopes.
const (
	ScopeDirect = "direct"
	ScopeGroup  = "group"
	ScopeAll    = "all"
)

// Audit results. An intent row is written before routing and finished afterwards.
const (
	AuditPending   = "pending"
	AuditAccepted  = "accepted"
	AuditRepeated  = "repeated"
	AuditRejected  = "rejected"
	AuditForbidden = "forbidden"
)

// OperatorScope is the canonical request scope: sorted, deduplicated members.
// Members are nil for ScopeAll.
type OperatorScope struct {
	Kind    string
	Members []string
}

// Equal compares scopes for idempotent retries.
func (s OperatorScope) Equal(other OperatorScope) bool {
	return s.Kind == other.Kind && slices.Equal(s.Members, other.Members)
}

// OperatorAudit is one audit row. It never holds message payloads.
type OperatorAudit struct {
	Action       string
	PrincipalUID int
	MessageID    string
	Scope        string
	Result       string
	Detail       string
}

// RecordOperatorAudit commits an audit row on its own and returns its ID.
func (s *Store) RecordOperatorAudit(ctx context.Context, a OperatorAudit) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO operator_audit (ts, action, principal_uid, message_id, scope, result, detail) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		time.Now().UTC(), a.Action, a.PrincipalUID, nullString(a.MessageID), nullString(a.Scope), a.Result, nullString(a.Detail))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishOperatorAudit records the outcome of an earlier intent row.
func (s *Store) FinishOperatorAudit(ctx context.Context, id int64, result, detail string) error {
	return finishAudit(ctx, s.db, id, result, detail)
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func finishAudit(ctx context.Context, db execer, id int64, result, detail string) error {
	res, err := db.ExecContext(ctx, `UPDATE operator_audit SET result = ?, detail = ? WHERE id = ? AND result = ?`, result, nullString(detail), id, AuditPending)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, errors.New("operator audit intent missing or already finished"))
	}
	return nil
}

// PersistOperatorMessage commits the message, its frozen scope and the audit
// result atomically, so a routed operator message is never left unaudited.
func (s *Store) PersistOperatorMessage(ctx context.Context, msg protocol.Message, scope OperatorScope, recipients []protocol.Recipient, excluded []string, auditID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := persistMessage(ctx, tx, msg, "", recipients); err != nil {
		return err
	}
	members, err := jsonOrNull(scope.Members)
	if err != nil {
		return err
	}
	frozen, err := json.Marshal(recipients)
	if err != nil {
		return err
	}
	left, err := json.Marshal(nonNil(excluded))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO operator_requests (message_id, scope_kind, members, recipients, excluded) VALUES (?, ?, ?, ?, ?)`, msg.MessageID, scope.Kind, members, string(frozen), string(left)); err != nil {
		return err
	}
	if err := finishAudit(ctx, tx, auditID, AuditAccepted, ""); err != nil {
		return err
	}
	return tx.Commit()
}

// OperatorRetry is the retained outcome of an earlier identical operator send.
type OperatorRetry struct {
	Seq        uint64
	Recipients []protocol.Recipient
	Excluded   []string
	Receipts   []protocol.ReceiptState
}

// LookupOperatorRetry returns nil when the ID is unused. A used ID matches only
// a trusted operator message with the same scope, payload and correlation;
// anything else, including legacy rows claiming the operator sender, is a duplicate.
func (s *Store) LookupOperatorRetry(ctx context.Context, request protocol.Message, scope OperatorScope) (*OperatorRetry, error) {
	var seq uint64
	var origin sql.NullString
	var inReplyTo, payload string
	err := s.db.QueryRowContext(ctx, `SELECT m.seq, mm.origin, mm.in_reply_to, m.payload FROM message_metadata mm JOIN messages m USING(seq) WHERE mm.message_id = ?`, request.MessageID).Scan(&seq, &origin, &inReplyTo, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if origin.String != protocol.OriginOperator || inReplyTo != request.InReplyTo || !bytes.Equal(compactPayload(json.RawMessage(payload)), compactPayload(request.Payload)) {
		return nil, ErrDuplicateMessage
	}
	stored, retry, err := s.operatorRequest(ctx, request.MessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDuplicateMessage
	}
	if err != nil {
		return nil, err
	}
	if !stored.Equal(scope) {
		return nil, ErrDuplicateMessage
	}
	retry.Seq = seq
	snapshot := RetrySnapshot{Message: protocol.Message{MessageID: request.MessageID}}
	if err := s.retryReceipts(ctx, &snapshot); err != nil {
		return nil, err
	}
	retry.Receipts = snapshot.Receipts
	return retry, nil
}

func (s *Store) operatorRequest(ctx context.Context, id string) (OperatorScope, *OperatorRetry, error) {
	var scope OperatorScope
	var members sql.NullString
	var recipients, excluded string
	err := s.db.QueryRowContext(ctx, `SELECT scope_kind, members, recipients, excluded FROM operator_requests WHERE message_id = ?`, id).Scan(&scope.Kind, &members, &recipients, &excluded)
	if err != nil {
		return scope, nil, err
	}
	if members.Valid {
		if err := json.Unmarshal([]byte(members.String), &scope.Members); err != nil {
			return scope, nil, err
		}
	}
	retry := &OperatorRetry{}
	if err := json.Unmarshal([]byte(recipients), &retry.Recipients); err != nil {
		return scope, nil, err
	}
	if err := json.Unmarshal([]byte(excluded), &retry.Excluded); err != nil {
		return scope, nil, err
	}
	return scope, retry, nil
}

func jsonOrNull(members []string) (sql.NullString, error) {
	if members == nil {
		return sql.NullString{}, nil
	}
	data, err := json.Marshal(members)
	return sql.NullString{String: string(data), Valid: true}, err
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// RecipientCapacity reports whether one more durable message of the given wire
// size fits the recipient's own pending limits.
func (s *Store) RecipientCapacity(ctx context.Context, agentID string, wireBytes int) (bool, error) {
	var count, bytes int
	query := `SELECT COUNT(*), COALESCE(SUM(d.wire_bytes),0) FROM durable_inbox i JOIN durable_messages d USING(message_id) WHERE i.agent_id = ? AND i.acknowledged_at IS NULL`
	if err := s.db.QueryRowContext(ctx, query, agentID).Scan(&count, &bytes); err != nil {
		return false, err
	}
	return count < MaxPendingMessages && bytes+wireBytes <= MaxPendingBytes, nil
}
