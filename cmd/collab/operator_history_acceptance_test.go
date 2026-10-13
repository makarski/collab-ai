package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func historyCLIEntry(seq int, id string) map[string]any {
	return map[string]any{
		"message":   map[string]any{"type": "msg", "message_id": id, "seq": seq, "from": "alpha", "to": "operator", "origin": "agent", "durable": true, "ack_requested": true, "payload": map[string]string{"text": "hello"}},
		"direction": "received", "sender_session": "original", "operator_stage": "accepted",
	}
}

func historyCLIPage() map[string]any {
	return map[string]any{"type": "operator_history", "history": map[string]any{
		"watermark": map[string]any{"epoch": "epoch", "revision": 4}, "upper_seq": 10, "next_before_seq": 8, "more": false,
		"entries": []any{historyCLIEntry(9, "recent"), historyCLIEntry(8, "older")},
	}}
}

func historyCLIEntries(page map[string]any) []any { return page["entries"].([]any) }
func historyCLIFirst(page map[string]any) map[string]any {
	return historyCLIEntries(page)[0].(map[string]any)
}
func historyCLIMessage(entry map[string]any) map[string]any { return entry["message"].(map[string]any) }

func TestOperatorHistoryCLIWireAndNoReceipt(t *testing.T) {
	requests := make(chan map[string]json.RawMessage, 1)
	path := statusSocket(t, func(c net.Conn) {
		var request map[string]json.RawMessage
		if json.NewDecoder(bufio.NewReader(c)).Decode(&request) != nil {
			return
		}
		requests <- request
		json.NewEncoder(c).Encode(historyCLIPage())
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "history", "--socket", path, "--before", "10", "--upper", "10", "--limit", "2", "--json", "--timeout", "1s"}, &out, &stderr)
	assertStatusEqual(t, "history success", code, 0)
	assertStatusEqual(t, "valid JSON", json.Valid(out.Bytes()), true)
	select {
	case request := <-requests:
		var page struct {
			Before, Upper uint64
			Limit         int
		}
		var wire map[string]uint64
		if err := json.Unmarshal(request["page_request"], &wire); err != nil {
			t.Fatal(err)
		}
		page.Before, page.Upper, page.Limit = wire["before_seq"], wire["upper_seq"], int(wire["limit"])
		assertStatusEqual(t, "wire before", page.Before, uint64(10))
		assertStatusEqual(t, "wire upper", page.Upper, uint64(10))
		assertStatusEqual(t, "wire limit", page.Limit, 2)
	case <-time.After(time.Second):
		t.Fatal("no history request")
	}
}

func TestOperatorHistoryCLIQuotesTextAndShowsContinuation(t *testing.T) {
	response := historyCLIPage()
	page := response["history"].(map[string]any)
	entry := historyCLIFirst(page)
	m := historyCLIMessage(entry)
	m["from"] = "alpha\x1b[31m"
	m["in_reply_to"] = "parent\nclaim"
	m["payload"] = map[string]string{"text": "hello\x1b[2J\nworld"}
	path, requests := inboxCLISocket(t, func(inboxCLIRequest) any { return response })
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "history", "--socket", path}, &out, &stderr)
	assertStatusEqual(t, "text history success", code, 0)
	assertStatusEqual(t, "terminal escapes inert", strings.ContainsRune(out.String(), '\x1b'), false)
	assertStatusEqual(t, "correlation marked", strings.Contains(out.String(), "correlation claim"), true)
	assertStatusEqual(t, "continuation", strings.Contains(out.String(), "--before 8 --upper 10"), true)
	assertStatusEqual(t, "history request only", takeInboxCLIRequest(t, requests).Type, "operator_history")
	assertNoInboxCLIRequest(t, requests)
}

func TestOperatorHistoryCLINullAndOmittedBodiesAreDistinct(t *testing.T) {
	response := historyCLIPage()
	page := response["history"].(map[string]any)
	entries := historyCLIEntries(page)
	historyCLIMessage(entries[0].(map[string]any))["payload"] = nil
	omitted := entries[1].(map[string]any)
	omitted["body_omitted"] = true
	delete(historyCLIMessage(omitted), "payload")
	path, requests := inboxCLISocket(t, func(inboxCLIRequest) any { return response })
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "history", "--socket", path}, &out, &stderr)
	assertStatusEqual(t, "null and omitted valid", code, 0)
	assertStatusEqual(t, "omission visible", strings.Contains(out.String(), "not available"), true)
	takeInboxCLIRequest(t, requests)
	assertNoInboxCLIRequest(t, requests)
}

type historyCLICase struct {
	name   string
	mutate func(map[string]any)
}

func historyCLIInvalidPages() []historyCLICase {
	return []historyCLICase{
		{"changed upper", func(p map[string]any) { p["upper_seq"] = 11 }},
		{"wrong next", func(p map[string]any) { p["next_before_seq"] = 7 }},
		{"too many entries", func(p map[string]any) {
			p["entries"] = append(historyCLIEntries(p), historyCLIEntry(7, "extra"))
			p["next_before_seq"] = 7
		}},
		{"duplicate ID", func(p map[string]any) { historyCLIMessage(historyCLIFirst(p))["message_id"] = "older" }},
		{"duplicate seq", func(p map[string]any) { historyCLIMessage(historyCLIFirst(p))["seq"] = 8 }},
		{"zero seq", func(p map[string]any) { p["entries"] = []any{historyCLIEntry(0, "zero")}; p["next_before_seq"] = 0 }},
		{"at exclusive before", func(p map[string]any) { historyCLIMessage(historyCLIFirst(p))["seq"] = 10 }},
		{"empty with more", func(p map[string]any) { p["entries"] = []any{}; p["next_before_seq"] = 10; p["more"] = true }},
		{"empty changes cursor", func(p map[string]any) { p["entries"] = []any{}; p["next_before_seq"] = 9 }},
		{"missing payload unmarked", func(p map[string]any) { delete(historyCLIMessage(historyCLIFirst(p)), "payload") }},
		{"omitted but payload present", func(p map[string]any) { historyCLIFirst(p)["body_omitted"] = true }},
		{"wrong message type", func(p map[string]any) { historyCLIMessage(historyCLIFirst(p))["type"] = "ack" }},
		{"unknown direction", func(p map[string]any) { historyCLIFirst(p)["direction"] = "sideways" }},
		{"received outside operator inbox", func(p map[string]any) { historyCLIMessage(historyCLIFirst(p))["to"] = "other" }},
		{"sent without trusted origin", func(p map[string]any) { historyCLIFirst(p)["direction"] = "sent" }},
		{"invalid operator stage", func(p map[string]any) { historyCLIFirst(p)["operator_stage"] = "done\x1b[2J" }},
	}
}

func TestOperatorHistoryCLIRejectsMalformedPagesBeforeOutput(t *testing.T) {
	for _, tc := range historyCLIInvalidPages() {
		t.Run(tc.name, func(t *testing.T) {
			response := historyCLIPage()
			tc.mutate(response["history"].(map[string]any))
			path, requests := inboxCLISocket(t, func(inboxCLIRequest) any { return response })
			var out, stderr bytes.Buffer
			code := run(context.Background(), []string{"operator", "history", "--socket", path, "--before", "10", "--upper", "10", "--limit", "2", "--json"}, &out, &stderr)
			assertStatusEqual(t, "invalid page exit", code, 1)
			assertStatusEqual(t, "no partial output", out.Len(), 0)
			takeInboxCLIRequest(t, requests)
			assertNoInboxCLIRequest(t, requests)
		})
	}
}

func TestOperatorHistoryCLILegacyErrorPreservesDetail(t *testing.T) {
	path, _ := inboxCLISocket(t, func(inboxCLIRequest) any {
		return map[string]string{"type": "error", "code": "malformed_frame", "detail": "original broker reason"}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "history", "--socket", path}, &out, &stderr)
	assertStatusEqual(t, "legacy failure", code, 1)
	assertStatusEqual(t, "detail retained", strings.Contains(stderr.String(), "original broker reason"), true)
	assertStatusEqual(t, "upgrade hint", strings.Contains(strings.ToLower(stderr.String()), "upgrade"), true)
	assertStatusEqual(t, "no history output", out.Len(), 0)
}

func TestOperatorHistoryCLIInvalidArguments(t *testing.T) {
	for _, flags := range [][]string{{"--before", "-1"}, {"--upper", "-1"}, {"--limit", "33"}, {"--limit", "-1"}, {"--timeout", "0s"}, {"unexpected"}} {
		var out, stderr bytes.Buffer
		args := append([]string{"operator", "history", "--socket", filepath.Join(t.TempDir(), "absent.sock")}, flags...)
		assertStatusEqual(t, "usage error", run(context.Background(), args, &out, &stderr), 2)
		assertStatusEqual(t, "usage leaves stdout empty", out.Len(), 0)
	}
}

func TestOperatorHistoryCLIOutputFailureNeverReceipts(t *testing.T) {
	path, requests := inboxCLISocket(t, func(inboxCLIRequest) any { return historyCLIPage() })
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "history", "--socket", path}, inboxBrokenOutput{}, &stderr)
	assertStatusEqual(t, "output error", code, 1)
	takeInboxCLIRequest(t, requests)
	assertNoInboxCLIRequest(t, requests)
}
