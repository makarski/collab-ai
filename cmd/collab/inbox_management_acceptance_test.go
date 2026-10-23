package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func managementCLIStatus() map[string]any {
	return map[string]any{"type": "operator_inbox_status", "inbox": map[string]any{
		"head_seq": 10, "oldest_unacknowledged_seq": 4,
		"unacknowledged": 2, "unreceived": 1, "bytes": 500,
		"limits":   map[string]int{"sender_messages": 16, "sender_bytes": 524288, "inbox_messages": 256, "inbox_bytes": 8388608},
		"senders":  []any{map[string]any{"agent_id": "author", "unacknowledged": 2, "unreceived": 1, "bytes": 500}},
		"retained": map[string]int{"messages": 3, "bytes": 4000, "limit_messages": 100000, "limit_bytes": 268435456},
	}}
}

func managementCLIAck(next uint64, more bool) map[string]any {
	return map[string]any{"type": "ack", "stage": "agent_acknowledged", "agent_id": "operator",
		"ack_many": map[string]any{"acknowledged": 1, "already_acknowledged": 0, "skipped": 0, "next_after_seq": next, "more": more},
		"receipts": []any{map[string]string{"message_id": "first", "agent_id": "operator", "stage": "agent_acknowledged"}},
	}
}

func TestInboxManagementCLIStatusIsNonConsuming(t *testing.T) {
	path, requests := inboxCLISocket(t, func(inboxCLIRequest) any { return managementCLIStatus() })
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "inbox", "status", "--socket", path, "--json"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("status exit %d: %s", code, stderr.String())
	}
	if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), "author") {
		t.Fatalf("missing structured sender usage: %s", out.String())
	}
	assertStatusEqual(t, "read-only status operation", takeInboxCLIRequest(t, requests).Type, "operator_inbox_status")
	assertNoInboxCLIRequest(t, requests)
}

func TestInboxManagementCLIBulkContinuationPreservesFrozenScope(t *testing.T) {
	path, requests := inboxCLISocket(t, func(r inboxCLIRequest) any {
		if r.AckSelection.After == 3 {
			return managementCLIAck(5, true)
		}
		return managementCLIAck(10, false)
	})
	var out, stderr bytes.Buffer
	args := []string{"operator", "inbox", "ack", "--socket", path, "--from", "author", "--upper", "10", "--after", "3", "--limit", "2", "--all"}
	if code := run(context.Background(), args, &out, &stderr); code != 0 {
		t.Fatalf("bulk ack exit %d: %s", code, stderr.String())
	}
	for _, after := range []uint64{3, 5} {
		r := takeInboxCLIRequest(t, requests)
		assertStatusEqual(t, "bulk operation", r.Type, "operator_ack_many")
		assertStatusEqual(t, "sender filter remains frozen", r.AckSelection.From, "author")
		assertStatusEqual(t, "upper bound remains frozen", r.AckSelection.Upper, uint64(10))
		assertStatusEqual(t, "continuation uses returned cursor", r.AckSelection.After, after)
		assertStatusEqual(t, "limit preserved", r.AckSelection.Limit, 2)
	}
	assertNoInboxCLIRequest(t, requests)
}

func TestInboxManagementCLIReportsOwnerBusyWithoutFallbackMutation(t *testing.T) {
	path, requests := inboxCLISocket(t, func(inboxCLIRequest) any {
		return map[string]string{"type": "error", "code": "owner_busy", "detail": "operator chat owns this inbox"}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "inbox", "ack", "--socket", path, "--upper", "10", "--after", "0", "--all"}, &out, &stderr)
	assertStatusEqual(t, "owner conflict is failure", code, 1)
	if !strings.Contains(stderr.String(), "owner_busy") && !strings.Contains(stderr.String(), "owns") {
		t.Fatalf("owner conflict hidden: %s", stderr.String())
	}
	assertStatusEqual(t, "one attempted bulk mutation", takeInboxCLIRequest(t, requests).Type, "operator_ack_many")
	assertNoInboxCLIRequest(t, requests)
}

func TestInboxManagementCLIRejectsInvalidSelectionBeforeConnecting(t *testing.T) {
	for _, selection := range [][]string{
		{"--upper", "0"}, {"--upper", "bad"}, {"--upper", "10", "--after", "11"},
		{"--upper", "10", "--limit", "257"}, {"--upper", "10", "--limit", "-1"},
	} {
		t.Run(strings.Join(selection, " "), func(t *testing.T) {
			var out, stderr bytes.Buffer
			args := append([]string{"operator", "inbox", "ack", "--socket", "/does/not/exist"}, selection...)
			code := run(context.Background(), args, &out, &stderr)
			assertStatusEqual(t, "invalid selection exit", code, 2)
			assertStatusEqual(t, "invalid selection prints no success", out.Len(), 0)
		})
	}
}

func TestInboxManagementCLIDefaultBoundsAreCapturedOnce(t *testing.T) {
	path, requests := inboxCLISocket(t, func(r inboxCLIRequest) any {
		if r.Type == "operator_inbox_status" {
			return managementCLIStatus()
		}
		if r.AckSelection.After == 3 {
			return managementCLIAck(5, true)
		}
		return managementCLIAck(10, false)
	})
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"operator", "inbox", "ack", "--socket", path, "--all"}, &out, &stderr); code != 0 {
		t.Fatalf("default bounds failed: %s", stderr.String())
	}
	assertStatusEqual(t, "capture read once", takeInboxCLIRequest(t, requests).Type, "operator_inbox_status")
	for _, after := range []uint64{3, 5} {
		r := takeInboxCLIRequest(t, requests)
		assertStatusEqual(t, "mutation follows capture", r.Type, "operator_ack_many")
		assertStatusEqual(t, "start includes oldest row", r.AckSelection.After, after)
		assertStatusEqual(t, "captured upper never refreshes", r.AckSelection.Upper, uint64(10))
	}
	assertNoInboxCLIRequest(t, requests)
}

func TestInboxManagementCLIStatusEscapesSenderControls(t *testing.T) {
	path, _ := inboxCLISocket(t, func(inboxCLIRequest) any {
		r := managementCLIStatus()
		r["inbox"].(map[string]any)["senders"].([]any)[0].(map[string]any)["agent_id"] = "author\x1b[2J\nforged status"
		return r
	})
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"operator", "inbox", "status", "--socket", path}, &out, &stderr); code != 0 {
		t.Fatalf("status failed: %s", stderr.String())
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "\nforged status") {
		t.Fatalf("raw sender controls escaped into terminal output: %q", out.String())
	}
}

func TestInboxManagementCLIRejectsMalformedAckResults(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(map[string]any)
	}{
		{"wrong stage", func(r map[string]any) { r["stage"] = "adapter_received" }},
		{"missing result", func(r map[string]any) { delete(r, "ack_many") }},
		{"cursor beyond upper", func(r map[string]any) { r["ack_many"].(map[string]any)["next_after_seq"] = 11 }},
		{"no continuation progress", func(r map[string]any) { r["ack_many"].(map[string]any)["next_after_seq"] = 3 }},
		{"duplicate receipt IDs", func(r map[string]any) {
			r["ack_many"].(map[string]any)["acknowledged"] = 2
			receipts := r["receipts"].([]any)
			r["receipts"] = append(receipts, receipts[0])
		}},
		{"count overflow", func(r map[string]any) {
			result := r["ack_many"].(map[string]any)
			result["acknowledged"], result["already_acknowledged"], result["skipped"] = 0, int(^uint(0)>>1), 1
			r["receipts"] = []any{}
		}},
		{"final nonempty page without progress", func(r map[string]any) {
			result := r["ack_many"].(map[string]any)
			result["next_after_seq"], result["more"] = 3, false
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, requests := inboxCLISocket(t, func(inboxCLIRequest) any {
				r := managementCLIAck(5, true)
				tc.alter(r)
				return r
			})
			var out, stderr bytes.Buffer
			code := run(context.Background(), []string{"operator", "inbox", "ack", "--socket", path, "--upper", "10", "--after", "3", "--all"}, &out, &stderr)
			assertStatusEqual(t, "malformed response cannot claim success", code, 1)
			takeInboxCLIRequest(t, requests)
			assertNoInboxCLIRequest(t, requests)
		})
	}
}

func TestInboxManagementCLILaterFailureReportsConfirmedProgress(t *testing.T) {
	path, requests := inboxCLISocket(t, func(r inboxCLIRequest) any {
		if r.AckSelection.After == 3 {
			return managementCLIAck(5, true)
		}
		return map[string]string{"type": "error", "code": "internal", "detail": "database unavailable"}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "inbox", "ack", "--socket", path, "--from", "author", "--upper", "10", "--after", "3", "--all"}, &out, &stderr)
	assertStatusEqual(t, "later page failure exits nonzero", code, 1)
	report := out.String() + stderr.String()
	for _, want := range []string{"database unavailable", "acknowledged 1", "--after 5", "--upper 10", "--from", "author"} {
		if !strings.Contains(report, want) {
			t.Fatalf("partial completion report lacks %q: %q", want, report)
		}
	}
	takeInboxCLIRequest(t, requests)
	takeInboxCLIRequest(t, requests)
	assertNoInboxCLIRequest(t, requests)
}

func TestInboxManagementCLIRetryHintPreservesLiteralSender(t *testing.T) {
	for _, sender := range []string{"author$(printf expanded)", "author`printf expanded`", "agent'\"\\ $ONLY_TEST_VAR"} {
		t.Run(sender, func(t *testing.T) {
			path, requests := inboxCLISocket(t, func(inboxCLIRequest) any {
				return map[string]string{"type": "ack", "stage": "agent_acknowledged"}
			})
			var out, stderr bytes.Buffer
			code := run(context.Background(), []string{"operator", "inbox", "ack", "--socket", path, "--from", sender, "--after", "3", "--upper", "10"}, &out, &stderr)
			assertStatusEqual(t, "unverified response exits nonzero", code, 1)
			assertStatusEqual(t, "initial request preserves sender", takeInboxCLIRequest(t, requests).AckSelection.From, sender)
			report := stderr.String()
			if !strings.Contains(report, "no acknowledgments confirmed") || strings.Contains(report, "nothing was acknowledged") {
				t.Fatalf("unverified result must not claim no mutation: %q", report)
			}
			_, hint, ok := strings.Cut(report, "retry with ")
			if !ok {
				t.Fatalf("missing retry hint: %q", report)
			}
			// Only the fixed fixture strings above reach this shell. Exercise the
			// advertised copy/paste behavior, including expansion and quote cases.
			cmd := exec.Command("sh", "-c", "printf '%s\\0' "+strings.TrimSpace(hint))
			cmd.Env = []string{"ONLY_TEST_VAR=expanded"}
			got, err := cmd.Output()
			if err != nil {
				t.Fatalf("retry hint is not valid shell syntax: %q: %v", hint, err)
			}
			want := "--after\x003\x00--upper\x0010\x00--from\x00" + sender + "\x00"
			assertStatusEqual(t, "pasted retry preserves selection literally", string(got), want)
			assertNoInboxCLIRequest(t, requests)
		})
	}
}

func TestInboxManagementCLIRetryHintDoesNotPrintSenderControls(t *testing.T) {
	path, requests := inboxCLISocket(t, func(inboxCLIRequest) any {
		return map[string]string{"type": "ack", "stage": "agent_acknowledged"}
	})
	var out, stderr bytes.Buffer
	sender := "author\x1b[2J\nforged hint"
	code := run(context.Background(), []string{"operator", "inbox", "ack", "--socket", path, "--from", sender, "--after", "3", "--upper", "10"}, &out, &stderr)
	assertStatusEqual(t, "unverified response exits nonzero", code, 1)
	assertStatusEqual(t, "initial request keeps literal controls", takeInboxCLIRequest(t, requests).AckSelection.From, sender)
	report := stderr.String()
	if strings.Contains(report, "\x1b") || strings.Contains(report, "\nforged hint") {
		t.Fatalf("retry hint leaked terminal controls: %q", report)
	}
	if !strings.Contains(report, "same --from value passed literally") {
		t.Fatalf("retry guidance must retain the sender restriction: %q", report)
	}
	assertNoInboxCLIRequest(t, requests)
}
