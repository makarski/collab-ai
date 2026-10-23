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
	return validateAckReceipts(resp.Receipts, res.Acknowledged)
}

// validateAckReceipts requires one distinct operator acknowledgment per
// newly acknowledged row.
func validateAckReceipts(receipts []ReceiptState, acknowledged int) error {
	if len(receipts) != acknowledged {
		return errors.New("receipts don't match the acknowledged count")
	}
	seen := make(map[string]bool, len(receipts))
	for _, r := range receipts {
		if !r.isOperatorAck() || seen[r.MessageID] {
			return errors.New("a receipt isn't a distinct operator acknowledgment")
		}
		seen[r.MessageID] = true
	}
	return nil
}

func (r ReceiptState) isOperatorAck() bool {
	return r.Stage == StageAgentAcknowledged && r.AgentID == OperatorID && r.MessageID != ""
}

func validateAckCounts(sel AckSelection, res *AckManyResult) error {
	limit := sel.Limit
	if limit == 0 {
		limit = AckManyDefault
	}
	for _, n := range []int{res.Acknowledged, res.AlreadyAcknowledged, res.Skipped} {
		if n < 0 || n > limit {
			return errors.New("counts are outside the page")
		}
	}
	examined := res.Acknowledged + res.AlreadyAcknowledged + res.Skipped
	if examined > limit {
		return errors.New("counts are outside the page")
	}
	if res.NextAfterSeq < sel.AfterSeq || res.NextAfterSeq > sel.UpperSeq {
		return errors.New("cursor is outside the selection")
	}
	if (res.More || examined > 0) && res.NextAfterSeq <= sel.AfterSeq {
		return errors.New("the cursor didn't advance past the examined rows")
	}
	return nil
}
