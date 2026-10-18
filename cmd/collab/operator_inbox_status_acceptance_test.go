package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"regexp"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestOperatorInboxStatusHumanPendingAndUnknown(t *testing.T) {
	for _, tc := range []struct{ name, value, want string }{{"known", "7", "7"}, {"legacy", "null", "unknown"}} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := readyStatus()
			if err := json.Unmarshal([]byte(`{"operator_pending":`+tc.value+`}`), &snapshot); err != nil {
				t.Fatal(err)
			}
			path := statusSocket(t, func(c net.Conn) {
				var req protocol.Message
				protocol.ReadFrame(bufio.NewReader(c), &req)
				protocol.WriteFrame(c, protocol.Message{Type: "status", Status: &snapshot})
			})
			var out, stderr bytes.Buffer
			code := run(context.Background(), []string{"status", "--socket", path}, &out, &stderr)
			assertStatusEqual(t, "status exit", code, 0)
			if !regexp.MustCompile(`(?i)operator[^\n]*\b` + tc.want + `\b`).MatchString(out.String()) {
				t.Fatalf("operator pending %s missing:\n%s", tc.want, out.String())
			}
		})
	}
}
