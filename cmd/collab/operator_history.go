package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorHistoryUsage = "usage: pairtask operator history [--socket PATH] [--before SEQ] [--upper SEQ] [--limit N] [--json] [--timeout 5s]"

func runOperatorHistory(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, page, code := parseHistory(args, stderr)
	if code != -1 {
		return code
	}
	resp, err := call.expect(ctx, protocol.Message{Type: protocol.TypeOperatorHistory, PageRequest: &page}, expectation{protocol.TypeOperatorHistory, "operator history"}, stderr)
	if err != nil {
		return 1
	}
	if err := validateHistory(page, resp.History); err != nil {
		fmt.Fprintln(stderr, "operator history returned an invalid page:", err)
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, resp)
	}
	if err := writeHistory(stdout, resp.History); err != nil {
		fmt.Fprintln(stderr, "operator history output failed:", err)
		return 1
	}
	return 0
}

func parseHistory(args []string, stderr io.Writer) (operatorCall, protocol.PageRequest, int) {
	var call operatorCall
	var page protocol.PageRequest
	flags := flag.NewFlagSet("operator history", flag.ContinueOnError)
	call.register(flags)
	flags.Uint64Var(&page.BeforeSeq, "before", 0, "return messages older than this sequence; 0 starts at the newest")
	flags.Uint64Var(&page.UpperSeq, "upper", 0, "upper_seq from the previous page; 0 starts a new scan")
	flags.IntVar(&page.Limit, "limit", protocol.HistoryDefault, "entries per page, 1 to 32")
	code := parseFlags(flags, args, operatorHistoryUsage, stderr, func(rest []string) error {
		if len(rest) != 0 {
			return errors.New("history takes no positional arguments")
		}
		if page.Limit < 1 || page.Limit > protocol.HistoryMax {
			return errors.New("limit must be 1 to 32")
		}
		return call.validate()
	})
	return call, page, code
}

// historyChecks reject a page that doesn't match the requested scan.
var historyChecks = []func(protocol.PageRequest, *protocol.HistoryPage) error{
	checkHistoryCursor,
	checkHistorySize,
	checkHistoryEntries,
}

func validateHistory(req protocol.PageRequest, page *protocol.HistoryPage) error {
	if page == nil {
		return errors.New("missing history")
	}
	for _, check := range historyChecks {
		if err := check(req, page); err != nil {
			return err
		}
	}
	return nil
}

func checkHistoryCursor(req protocol.PageRequest, page *protocol.HistoryPage) error {
	if req.UpperSeq != 0 && page.UpperSeq != req.UpperSeq {
		return errors.New("upper_seq differs from the request")
	}
	if max(req.BeforeSeq, page.NextBeforeSeq) > page.UpperSeq {
		return errors.New("cursor lies outside the scan")
	}
	return nil
}

func checkHistorySize(req protocol.PageRequest, page *protocol.HistoryPage) error {
	if len(page.Entries) > req.Limit {
		return errors.New("page holds more entries than requested")
	}
	if len(page.Entries) == 0 && page.More {
		return errors.New("empty page claims older entries")
	}
	return nil
}

// checkHistoryEntries requires unique entries in descending sequence within
// the scan, ending at next_before_seq.
func checkHistoryEntries(req protocol.PageRequest, page *protocol.HistoryPage) error {
	seen := make(map[string]bool, len(page.Entries))
	last := uint64(0)
	for i, e := range page.Entries {
		if !validEntry(e) || seen[e.Message.MessageID] {
			return errors.New("page holds a duplicate or malformed entry")
		}
		if !inHistoryScan(e.Message.Seq, i == 0, last, historyBound{req.BeforeSeq, page.UpperSeq}) {
			return errors.New("entries are out of order or outside the scan")
		}
		seen[e.Message.MessageID] = true
		last = e.Message.Seq
	}
	if nextBefore(req, page, last) != page.NextBeforeSeq {
		return errors.New("next_before_seq doesn't match the oldest entry or the requested cursor")
	}
	return nil
}

// nextBefore is the oldest returned seq, or the requested cursor for an empty page.
func nextBefore(req protocol.PageRequest, page *protocol.HistoryPage, oldest uint64) uint64 {
	if len(page.Entries) == 0 {
		return req.BeforeSeq
	}
	return oldest
}

// historyEntryChecks reject entries that aren't well-formed operator conversation.
var historyEntryChecks = []func(protocol.HistoryEntry) bool{
	isMessage,
	hasIdentity,
	validBody,
	validDirection,
}

func isMessage(e protocol.HistoryEntry) bool {
	return e.Message.Type == protocol.TypeMsg
}

func hasIdentity(e protocol.HistoryEntry) bool {
	return e.Message.MessageID != "" && e.Message.Seq > 0
}

func validEntry(e protocol.HistoryEntry) bool {
	for _, ok := range historyEntryChecks {
		if !ok(e) {
			return false
		}
	}
	return true
}

// validBody: an omitted body has no payload, and a present body needs one.
// JSON null is still an inline payload.
func validBody(e protocol.HistoryEntry) bool {
	return e.BodyOmitted == (len(e.Message.Payload) == 0)
}

// validDirection ties each direction to its provenance and stage fields.
func validDirection(e protocol.HistoryEntry) bool {
	switch e.Direction {
	case "sent":
		return e.Message.Origin == protocol.OriginOperator && validStages(e.Recipients)
	case "received":
		return e.Message.To == protocol.OperatorID && validStage(e.OperatorStage)
	}
	return false
}

func validStages(stages []protocol.RecipientStage) bool {
	for _, r := range stages {
		if !validStage(r.Stage) {
			return false
		}
	}
	return true
}

func validStage(stage string) bool {
	switch stage {
	case protocol.StageAccepted, protocol.StageAdapterReceived, protocol.StageAgentAcknowledged:
		return true
	}
	return false
}

// historyBound is the scan window: seq <= upper, and below before when set.
type historyBound struct{ before, upper uint64 }

func (b historyBound) contains(seq uint64) bool {
	if seq > b.upper {
		return false
	}
	return b.before == 0 || seq < b.before
}

func inHistoryScan(seq uint64, first bool, previous uint64, bound historyBound) bool {
	return bound.contains(seq) && (first || seq < previous)
}

func writeHistory(w io.Writer, page *protocol.HistoryPage) error {
	var b strings.Builder
	for _, e := range page.Entries {
		writeHistoryEntry(&b, e)
	}
	fmt.Fprintf(&b, "older: --before %d --upper %d (more: %t)\n", page.NextBeforeSeq, page.UpperSeq, page.More)
	_, err := io.WriteString(w, b.String())
	return err
}

// writeHistoryEntry quotes peer-controlled text so terminal controls stay inert.
func writeHistoryEntry(b *strings.Builder, e protocol.HistoryEntry) {
	m := e.Message
	fmt.Fprintf(b, "seq=%d %s id=%q from=%q to=%q origin=%q", m.Seq, e.Direction, m.MessageID, m.From, m.To, protocol.DisplayOrigin(m.Origin))
	if m.InReplyTo != "" {
		fmt.Fprintf(b, " in_reply_to=%q (correlation claim)", m.InReplyTo)
	}
	b.WriteString("\n")
	if e.BodyOmitted {
		b.WriteString("  [text not available: this message exceeds the history page size]\n")
	} else {
		fmt.Fprintf(b, "  %s\n", payloadText(m.Payload))
	}
	writeHistoryStages(b, e)
}

func writeHistoryStages(b *strings.Builder, e protocol.HistoryEntry) {
	if e.Direction == "received" {
		fmt.Fprintf(b, "  operator stage: %s\n", e.OperatorStage)
		return
	}
	for _, r := range e.Recipients {
		fmt.Fprintf(b, "  %q: %s\n", r.AgentID, r.Stage)
	}
	if e.ReceiptsTruncated {
		fmt.Fprintf(b, "  (more recipients: page them with operator_message_stages for %q)\n", e.Message.MessageID)
	}
}
