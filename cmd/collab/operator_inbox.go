package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"collab-ai/internal/protocol"
)

const (
	operatorReceiveUsage = "usage: collab operator receive [--socket PATH] [--after SEQ] [--upper SEQ] [--limit N] [--json] [--timeout 5s]"
	operatorAckUsage     = "usage: collab operator ack [--socket PATH] [--json] [--timeout 5s] MESSAGE_ID"
	legacyHint           = "if the broker predates protocol_version 5, upgrade the sandbox"
)

// operatorCall holds options shared by one-shot operator requests.
type operatorCall struct {
	socket  string
	asJSON  bool
	timeout time.Duration
}

func (c *operatorCall) register(flags *flag.FlagSet) {
	flags.StringVar(&c.socket, "socket", defaultOperatorSocket, "operator Unix socket path (secured only)")
	flags.BoolVar(&c.asJSON, "json", false, "emit the broker response as JSON")
	flags.DurationVar(&c.timeout, "timeout", 5*time.Second, "timeout per request, greater than zero and at most 30s")
}

func (c operatorCall) validate() error {
	if c.socket == "" {
		return errors.New("a socket path is required")
	}
	if c.timeout <= 0 || c.timeout > 30*time.Second {
		return errors.New("timeout must be greater than zero and at most 30s")
	}
	return nil
}

func (c operatorCall) send(ctx context.Context, req protocol.Message) (protocol.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return sendOperator(ctx, c.socket, req)
}

// parseFlags returns -1 to continue, or the exit code for a usage error.
func parseFlags(flags *flag.FlagSet, args []string, usage string, stderr io.Writer, validate func([]string) error) int {
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if err := validate(flags.Args()); err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, usage)
		return 2
	}
	return -1
}

func runOperatorReceive(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var call operatorCall
	var page protocol.PageRequest
	flags := flag.NewFlagSet("operator receive", flag.ContinueOnError)
	call.register(flags)
	flags.Uint64Var(&page.AfterSeq, "after", 0, "return messages after this sequence")
	flags.Uint64Var(&page.UpperSeq, "upper", 0, "upper_seq from the previous page; 0 starts a new scan")
	flags.IntVar(&page.Limit, "limit", protocol.OperatorPageDefault, "messages per page, 1 to 32")
	code := parseFlags(flags, args, operatorReceiveUsage, stderr, func(rest []string) error {
		if len(rest) != 0 {
			return errors.New("receive takes no positional arguments")
		}
		if page.Limit < 1 || page.Limit > protocol.OperatorPageMax {
			return errors.New("limit must be 1 to 32")
		}
		return call.validate()
	})
	if code != -1 {
		return code
	}
	resp, err := call.send(ctx, protocol.Message{Type: protocol.TypeOperatorReceive, PageRequest: &page})
	if err != nil {
		fmt.Fprintln(stderr, "operator receive failed:", err)
		return 1
	}
	if resp.Type != protocol.TypeOperatorPage {
		reportBrokerError(stderr, "operator receive rejected", resp)
		return 1
	}
	if err := validatePage(page, resp.Page); err != nil {
		fmt.Fprintln(stderr, "operator receive returned an invalid page; nothing was confirmed:", err)
		return 1
	}
	// Only a page that was fully written is confirmed as received.
	if err := writePage(stdout, resp, call.asJSON); err != nil {
		fmt.Fprintln(stderr, "operator receive output failed; nothing was confirmed:", err)
		return 1
	}
	return confirmReceived(ctx, call, resp.Page, stderr)
}

// validatePage rejects a response that doesn't match the requested scan.
func validatePage(req protocol.PageRequest, page *protocol.OperatorPage) error {
	switch {
	case page == nil:
		return errors.New("missing page")
	case req.UpperSeq != 0 && page.UpperSeq != req.UpperSeq:
		return errors.New("upper_seq differs from the request")
	case req.AfterSeq > page.UpperSeq || page.NextAfterSeq > page.UpperSeq:
		return errors.New("cursor lies outside the scan")
	case len(page.Messages) > req.Limit:
		return errors.New("page holds more messages than requested")
	case len(page.Messages) == 0 && page.More:
		return errors.New("empty page claims more pending messages")
	}
	last, err := checkPageMessages(req.AfterSeq, page)
	if err != nil {
		return err
	}
	if page.NextAfterSeq != last {
		return errors.New("next_after_seq doesn't match the last returned message")
	}
	return nil
}

// checkPageMessages requires unique operator-inbox messages in ascending
// sequence within the scan, and returns the last sequence.
func checkPageMessages(after uint64, page *protocol.OperatorPage) (uint64, error) {
	seen := make(map[string]bool, len(page.Messages))
	last := after
	for _, m := range page.Messages {
		if m.Type != protocol.TypeMsg || m.MessageID == "" || m.To != protocol.OperatorID || seen[m.MessageID] {
			return 0, errors.New("page holds a duplicate or non-operator message")
		}
		if m.Seq <= last || m.Seq > page.UpperSeq {
			return 0, errors.New("page messages are out of order or outside the scan")
		}
		seen[m.MessageID] = true
		last = m.Seq
	}
	return last, nil
}

func writePage(w io.Writer, resp protocol.Message, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(resp)
	}
	page := resp.Page
	var b strings.Builder
	for _, m := range page.Messages {
		// Peer-controlled text is quoted so terminal controls stay inert.
		fmt.Fprintf(&b, "seq=%d id=%q from=%q origin=%q", m.Seq, m.MessageID, m.From, protocol.DisplayOrigin(m.Origin))
		if m.InReplyTo != "" {
			fmt.Fprintf(&b, " in_reply_to=%q (correlation claim)", m.InReplyTo)
		}
		fmt.Fprintf(&b, "\n  %s\n", payloadText(m.Payload))
	}
	fmt.Fprintf(&b, "next: --after %d --upper %d (more: %t)\n", page.NextAfterSeq, page.UpperSeq, page.More)
	_, err := io.WriteString(w, b.String())
	return err
}

func payloadText(payload json.RawMessage) string {
	var body struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(payload, &body) == nil && body.Text != nil {
		return fmt.Sprintf("%q", *body.Text)
	}
	return fmt.Sprintf("%q", string(payload))
}

func confirmReceived(ctx context.Context, call operatorCall, page *protocol.OperatorPage, stderr io.Writer) int {
	if len(page.Messages) == 0 {
		return 0
	}
	ids := make([]string, len(page.Messages))
	for i, m := range page.Messages {
		ids[i] = m.MessageID
	}
	resp, err := call.send(ctx, protocol.Message{Type: protocol.TypeOperatorReceived, MessageIDs: ids})
	switch {
	case err != nil:
		fmt.Fprintln(stderr, "receipt confirmation failed; the messages will be returned again:", err)
	case resp.Type != protocol.TypeAck:
		reportBrokerError(stderr, "receipt confirmation rejected; the messages will be returned again", resp)
	case !receiptsCover(resp, ids):
		fmt.Fprintln(stderr, "receipt confirmation incomplete; the messages will be returned again")
	default:
		return 0
	}
	return 1
}

// receiptsCover requires a received (or already acknowledged) stage for every ID.
func receiptsCover(resp protocol.Message, ids []string) bool {
	if resp.Stage != protocol.StageAdapterReceived {
		return false
	}
	stages := make(map[string]string, len(resp.Receipts))
	for _, r := range resp.Receipts {
		stages[r.MessageID] = r.Stage
	}
	for _, id := range ids {
		if stage := stages[id]; stage != protocol.StageAdapterReceived && stage != protocol.StageAgentAcknowledged {
			return false
		}
	}
	return true
}

func runOperatorAck(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var call operatorCall
	flags := flag.NewFlagSet("operator ack", flag.ContinueOnError)
	call.register(flags)
	var id string
	code := parseFlags(flags, args, operatorAckUsage, stderr, func(rest []string) error {
		if len(rest) != 1 || rest[0] == "" || len(rest[0]) > 128 {
			return errors.New("exactly one MESSAGE_ID of 1 to 128 bytes is required")
		}
		id = rest[0]
		return call.validate()
	})
	if code != -1 {
		return code
	}
	resp, err := call.send(ctx, protocol.Message{Type: protocol.TypeOperatorAck, MessageID: id})
	if err != nil {
		fmt.Fprintln(stderr, "operator ack failed:", err)
		return 1
	}
	if resp.Type != protocol.TypeAck {
		reportBrokerError(stderr, "operator ack rejected", resp)
		return 1
	}
	if resp.MessageID != id || resp.Stage != protocol.StageAgentAcknowledged {
		fmt.Fprintln(stderr, "operator ack response doesn't confirm this message; acknowledgment not verified")
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, resp)
	}
	fmt.Fprintf(stdout, "%q acknowledged by operator (received, not approval)\n", resp.MessageID)
	return 0
}

// reportBrokerError keeps the broker's reason visible. A malformed_frame may
// come from an older broker, so it adds a hint without asserting that.
func reportBrokerError(stderr io.Writer, prefix string, resp protocol.Message) {
	fmt.Fprintf(stderr, "%s: %q: %q\n", prefix, resp.Code, resp.Detail)
	if resp.Code == protocol.ErrMalformedFrame {
		fmt.Fprintln(stderr, legacyHint)
	}
}

func encodeJSON(stdout, stderr io.Writer, v any) int {
	if err := json.NewEncoder(stdout).Encode(v); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
