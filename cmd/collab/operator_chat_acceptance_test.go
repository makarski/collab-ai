package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestOperatorChatRequiresInteractiveTerminal(t *testing.T) {
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "chat", "--socket", "/nonexistent/operator.sock"}, &out, &stderr)
	assertStatusEqual(t, "non-terminal exit", code, 2)
	if !strings.Contains(strings.ToLower(stderr.String()), "terminal") {
		t.Fatalf("expected terminal requirement before connecting, got %q", stderr.String())
	}
	assertStatusEqual(t, "no conversation output", out.Len(), 0)
}

func TestOperatorChatHelpAndInvalidArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"help", []string{"--help"}, 0},
		{"empty socket", []string{"--socket", ""}, 2},
		{"positional", []string{"unexpected"}, 2},
		{"unknown option", []string{"--unknown-option"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			code := run(context.Background(), append([]string{"operator", "chat"}, tc.args...), &out, &stderr)
			assertStatusEqual(t, "exit", code, tc.code)
			if tc.code == 0 && !strings.Contains(out.String()+stderr.String(), "socket") {
				t.Fatalf("help must describe the operator socket: stdout=%q stderr=%q", out.String(), stderr.String())
			}
		})
	}
}
