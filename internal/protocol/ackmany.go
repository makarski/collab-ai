package protocol

import "errors"

// ValidateAckMany checks an operator_ack_many response against the selection
// it answers before a client trusts it: the operator's acknowledgment stage
// and principal, counts within the page, receipts matching the new
// acknowledgments, and a cursor inside the bound that advances when there is
// more. A malformed broker can then neither loop a client nor move it past
// the frozen upper bound.
func ValidateAckMany(sel AckSelection, resp Message) error {
	acknowledgment := resp.Type == TypeAck && resp.Stage == StageAgentAcknowledged
	if !acknowledgment || resp.AgentID != OperatorID {
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
	examined, err := examinedRows(sel.pageLimit(), res)
	if err != nil {
		return err
	}
	return validateAckCursor(sel, res, examined)
}

// pageLimit is the selection's row limit with the default applied.
func (sel AckSelection) pageLimit() int {
	if sel.Limit == 0 {
		return AckManyDefault
	}
	return sel.Limit
}

// examinedRows checks each count, then their sum, against the page limit
// and returns how many rows the page examined.
func examinedRows(limit int, res *AckManyResult) (int, error) {
	examined := 0
	for _, n := range []int{res.Acknowledged, res.AlreadyAcknowledged, res.Skipped} {
		if n < 0 || n > limit {
			return 0, errors.New("counts are outside the page")
		}
		examined += n
	}
	if examined > limit {
		return 0, errors.New("counts are outside the page")
	}
	return examined, nil
}

// validateAckCursor keeps the cursor inside the selection, advancing past
// every examined row and whenever more is promised.
func validateAckCursor(sel AckSelection, res *AckManyResult, examined int) error {
	if res.NextAfterSeq < sel.AfterSeq || res.NextAfterSeq > sel.UpperSeq {
		return errors.New("cursor is outside the selection")
	}
	mustAdvance := res.More || examined > 0
	if mustAdvance && res.NextAfterSeq <= sel.AfterSeq {
		return errors.New("the cursor didn't advance past the examined rows")
	}
	return nil
}
