package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestOperatorSessionCLIReceiveReportsPrintedButNotConfirmed(t *testing.T) {
	path, requests := inboxCLISocket(t, func(req inboxCLIRequest) any {
		if req.Type == "operator_receive" {
			return inboxCLIPage()
		}
		return map[string]any{"type": "error", "code": "owner_busy", "detail": "a live operator session owns the inbox", "owner": map[string]any{"session_id": "owner"}}
	})
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "receive", "--socket", path, "--json"}, &out, &stderr)
	assertStatusEqual(t, "owner busy exit", code, 1)
	assertStatusEqual(t, "page remains usable", strings.Contains(out.String(), "inbox-1"), true)
	assertStatusEqual(t, "partial success explicit", strings.Contains(strings.ToLower(stderr.String()), "printed but not confirmed"), true)
	assertStatusEqual(t, "receive first", takeInboxCLIRequest(t, requests).Type, "operator_receive")
	assertStatusEqual(t, "receipt attempted", takeInboxCLIRequest(t, requests).Type, "operator_received")
	assertNoInboxCLIRequest(t, requests)
}
