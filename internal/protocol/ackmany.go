package protocol

import "errors"

// ValidateAckMany checks an operator_ack_many response against the selection
// it answers before a client trusts it: the operator's acknowledgment stage
// and principal, counts within the page, receipts matching the new
// acknowledgments, and a cursor inside the bound that advances when there is
// more. A malformed broker can then neither loop a client nor move it past
// the frozen upper bound.
func ValidateAckMany(sel AckSelection, resp Message) error {
	if resp.Type != TypeAck || resp.Stage != StageAgentAcknowledged || resp.AgentID != OperatorID {
		return errors.New("stage or principal is not the operator's acknowledgment")
	}
	res := resp.AckMany
	if res == nil {
		return errors.New("no result")
	}
	if err := validateAckCounts(sel, res); err != nil {
		return err
	}
	if len(resp.Receipts) != res.Acknowledged {
		return errors.New("receipts don't match the acknowledged count")
	}
	for _, r := range resp.Receipts {
		if r.Stage != StageAgentAcknowledged || r.AgentID != OperatorID || r.MessageID == "" {
			return errors.New("a receipt isn't an operator acknowledgment")
		}
	}
	return nil
}

func validateAckCounts(sel AckSelection, res *AckManyResult) error {
	limit := sel.Limit
	if limit == 0 {
		limit = AckManyDefault
	}
	if res.Acknowledged < 0 || res.AlreadyAcknowledged < 0 || res.Skipped < 0 || res.Acknowledged+res.AlreadyAcknowledged+res.Skipped > limit {
		return errors.New("counts are outside the page")
	}
	if res.NextAfterSeq < sel.AfterSeq || res.NextAfterSeq > sel.UpperSeq {
		return errors.New("cursor is outside the selection")
	}
	if res.More && res.NextAfterSeq <= sel.AfterSeq {
		return errors.New("continuation makes no progress")
	}
	return nil
}
