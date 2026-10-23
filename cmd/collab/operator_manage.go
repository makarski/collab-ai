package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/pairtask/pairtask/internal/protocol"
)

const (
	operatorInboxUsage       = "usage: pairtask operator inbox status [--socket PATH] [--json] [--timeout 5s]\n       pairtask operator inbox ack [--socket PATH] [--from AGENT] [--after SEQ] [--upper SEQ] [--limit N] [--all] [--json] [--timeout 5s]"
	operatorInboxStatusUsage = "usage: pairtask operator inbox status [--socket PATH] [--json] [--timeout 5s]"
	operatorInboxAckUsage    = "usage: pairtask operator inbox ack [--socket PATH] [--from AGENT] [--after SEQ] [--upper SEQ] [--limit N] [--all] [--json] [--timeout 5s]"
)

// runOperatorInbox manages the operator inbox: usage against its bounds, and
// bulk acknowledgment over a stable, explicitly bounded selection.
func runOperatorInbox(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, operatorInboxUsage)
		return 2
	}
	switch args[0] {
	case "status":
		return runInboxStatus(ctx, args[1:], stdout, stderr)
	case "ack":
		return runInboxAck(ctx, args[1:], stdout, stderr)
	}
	fmt.Fprintln(stderr, operatorInboxUsage)
	return 2
}

func runInboxStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var call operatorCall
	flags := flag.NewFlagSet("operator inbox status", flag.ContinueOnError)
	call.register(flags)
	if code := parseFlags(flags, args, operatorInboxStatusUsage, stderr, func(rest []string) error {
		if len(rest) != 0 {
			return errors.New("inbox status takes no arguments")
		}
		return call.validate()
	}); code != -1 {
		return code
	}
	status, err := call.inboxStatus(ctx, stderr)
	if err != nil {
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, protocol.Message{Type: protocol.TypeOperatorInboxStatus, Inbox: &status})
	}
	writeInboxStatus(stdout, status)
	return 0
}

// inboxStatus reads usage; the broker audits the read and changes nothing.
func (c operatorCall) inboxStatus(ctx context.Context, stderr io.Writer) (protocol.InboxStatus, error) {
	resp, err := c.expect(ctx, protocol.Message{Type: protocol.TypeOperatorInboxStatus}, expectation{protocol.TypeOperatorInboxStatus, "operator inbox status"}, stderr)
	if err != nil {
		return protocol.InboxStatus{}, err
	}
	if resp.Inbox == nil {
		fmt.Fprintln(stderr, "operator inbox status response has no inbox; nothing read")
		return protocol.InboxStatus{}, errReported
	}
	return *resp.Inbox, nil
}

func writeInboxStatus(w io.Writer, s protocol.InboxStatus) {
	fmt.Fprintf(w, "operator inbox: %d unacknowledged (%d not yet received), %s of %s, %d/%d messages; head #%d, oldest unacknowledged #%d\n",
		s.Unacknowledged, s.Unreceived, humanBytes(s.Bytes), humanBytes(s.Limits.InboxBytes), s.Unacknowledged, s.Limits.InboxMessages, s.HeadSeq, s.OldestUnacknowledgedSeq)
	for _, u := range s.Senders {
		fmt.Fprintf(w, "  %-24q %3d/%d unacknowledged (%d not yet received), %s of %s%s\n", u.AgentID, u.Unacknowledged, s.Limits.SenderMessages, u.Unreceived, humanBytes(u.Bytes), humanBytes(s.Limits.SenderBytes), nearBound(u, s.Limits))
	}
	fmt.Fprintf(w, "retained history: %d/%d messages, %s of %s\n", s.Retained.Messages, s.Retained.LimitMessages, humanBytes(s.Retained.Bytes), humanBytes(s.Retained.LimitBytes))
}

// nearBound marks a sender at or near its bound, where new sends get refused.
func nearBound(u protocol.SenderUsage, l protocol.InboxLimits) string {
	switch {
	case u.Unacknowledged >= l.SenderMessages || u.Bytes >= l.SenderBytes:
		return "  AT BOUND: this sender's messages are refused until acknowledged"
	case u.Unacknowledged*4 >= l.SenderMessages*3 || u.Bytes*4 >= l.SenderBytes*3:
		return "  near bound"
	}
	return ""
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// inboxAck is one bulk acknowledgment run: a frozen upper bound, a start,
// and whether to continue until the selection is exhausted.
type inboxAck struct {
	call operatorCall
	sel  protocol.AckSelection
	all  bool
	set  map[string]bool
}

func runInboxAck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	run, code := parseInboxAck(args, stderr)
	if code != -1 {
		return code
	}
	if err := run.resolveBounds(ctx, stderr); err != nil {
		return 1
	}
	if run.sel.UpperSeq == 0 {
		fmt.Fprintln(stdout, "nothing to acknowledge")
		return 0
	}
	total, err := run.acknowledge(ctx, stdout, stderr)
	if err != nil {
		return 1
	}
	if !run.call.asJSON {
		fmt.Fprintf(stdout, "acknowledged %d (received, not approval); %d already acknowledged; %d not yet received and left pending\n", total.Acknowledged, total.AlreadyAcknowledged, total.Skipped)
	}
	return 0
}

func parseInboxAck(args []string, stderr io.Writer) (inboxAck, int) {
	var run inboxAck
	flags := flag.NewFlagSet("operator inbox ack", flag.ContinueOnError)
	run.call.register(flags)
	flags.StringVar(&run.sel.From, "from", "", "acknowledge messages from this agent only")
	flags.Uint64Var(&run.sel.AfterSeq, "after", 0, "start after this sequence; default: just before the oldest unacknowledged message")
	flags.Uint64Var(&run.sel.UpperSeq, "upper", 0, "acknowledge up to this sequence; default: the inbox head, frozen for the whole run")
	flags.IntVar(&run.sel.Limit, "limit", protocol.AckManyDefault, "rows per request, 1 to 256")
	flags.BoolVar(&run.all, "all", false, "continue with the same frozen upper bound until the selection is exhausted")
	code := parseFlags(flags, args, operatorInboxAckUsage, stderr, func(rest []string) error {
		if len(rest) != 0 {
			return errors.New("inbox ack takes no positional arguments")
		}
		run.set = map[string]bool{}
		flags.Visit(func(f *flag.Flag) { run.set[f.Name] = true })
		if err := run.validateSelection(); err != nil {
			return err
		}
		return run.call.validate()
	})
	return run, code
}

// validateSelection rejects bounds that could never form a valid page,
// before anything is sent.
func (r inboxAck) validateSelection() error {
	if r.sel.Limit < 1 || r.sel.Limit > protocol.AckManyMax {
		return errors.New("limit must be 1 to 256")
	}
	if r.set["upper"] && r.sel.UpperSeq == 0 {
		return errors.New("upper must be a committed sequence above 0; omit it to use the inbox head")
	}
	if r.set["upper"] && r.sel.AfterSeq >= r.sel.UpperSeq {
		return errors.New("after must be below upper")
	}
	if r.set["from"] {
		if err := protocol.ValidateAgentID(r.sel.From); err != nil {
			return fmt.Errorf("from: %w", err)
		}
	}
	return nil
}

// resolveBounds freezes the upper bound and the start from the inbox status
// when they weren't given, so new arrivals are never swept in.
func (r *inboxAck) resolveBounds(ctx context.Context, stderr io.Writer) error {
	if r.set["upper"] && r.set["after"] {
		return nil
	}
	status, err := r.call.inboxStatus(ctx, stderr)
	if err != nil {
		return err
	}
	if !r.set["upper"] {
		r.sel.UpperSeq = status.HeadSeq
	}
	if !r.set["after"] && status.OldestUnacknowledgedSeq > 0 {
		r.sel.AfterSeq = status.OldestUnacknowledgedSeq - 1
	}
	if status.Unacknowledged == 0 {
		r.sel.UpperSeq = 0 // nothing to acknowledge; never a malformed write
	}
	return nil
}

// acknowledge runs one request, or continues under the frozen upper bound
// until the selection is exhausted.
func (r *inboxAck) acknowledge(ctx context.Context, stdout, stderr io.Writer) (protocol.AckManyResult, error) {
	var total protocol.AckManyResult
	for {
		resp, err := r.call.expect(ctx, protocol.Message{Type: protocol.TypeOperatorAckMany, AckSelection: &r.sel}, expectation{protocol.TypeAck, "operator inbox ack"}, stderr)
		if err != nil {
			return total, err
		}
		if err := validateAckMany(r.sel, resp); err != nil {
			fmt.Fprintln(stderr, "operator inbox ack returned an invalid result; acknowledgment not verified:", err)
			return total, errReported
		}
		if r.call.asJSON {
			if encodeJSON(stdout, stderr, resp) != 0 {
				return total, errReported
			}
		}
		total.Acknowledged += resp.AckMany.Acknowledged
		total.AlreadyAcknowledged += resp.AckMany.AlreadyAcknowledged
		total.Skipped += resp.AckMany.Skipped
		total.NextAfterSeq, total.More = resp.AckMany.NextAfterSeq, resp.AckMany.More
		if !r.all || !resp.AckMany.More {
			return total, nil
		}
		r.sel.AfterSeq = resp.AckMany.NextAfterSeq
	}
}

// validateAckMany checks a page result before it is trusted: the stage and
// principal, counts within the page, receipts matching the new
// acknowledgments, and a cursor inside the bound that advances when there is
// more, so a malformed broker can neither loop the client nor move it past
// the frozen upper bound.
func validateAckMany(sel protocol.AckSelection, resp protocol.Message) error {
	if resp.Stage != protocol.StageAgentAcknowledged || resp.AgentID != protocol.OperatorID {
		return errors.New("stage or principal is not the operator's acknowledgment")
	}
	res := resp.AckMany
	if res == nil {
		return errors.New("no result")
	}
	if err := validateCounts(sel, res); err != nil {
		return err
	}
	if len(resp.Receipts) != res.Acknowledged {
		return errors.New("receipts don't match the acknowledged count")
	}
	for _, r := range resp.Receipts {
		if r.Stage != protocol.StageAgentAcknowledged || r.AgentID != protocol.OperatorID || r.MessageID == "" {
			return errors.New("a receipt isn't an operator acknowledgment")
		}
	}
	return nil
}

func validateCounts(sel protocol.AckSelection, res *protocol.AckManyResult) error {
	if res.Acknowledged < 0 || res.AlreadyAcknowledged < 0 || res.Skipped < 0 || res.Acknowledged+res.AlreadyAcknowledged+res.Skipped > sel.Limit {
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
