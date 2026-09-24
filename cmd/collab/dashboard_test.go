package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestDashboardOptionsAndNonTTY(t *testing.T) {
	t.Setenv("COLLAB_SOCKET_PATH", "/custom.sock")
	var stdout, stderr bytes.Buffer
	cfg, code := dashboardOptions([]string{"--interval", "4s", "--timeout", "1s"}, &stderr)
	assertStatusEqual(t, "parse code", code, -1)
	assertStatusEqual(t, "socket", cfg.Socket, "/custom.sock")
	assertStatusEqual(t, "interval", cfg.Interval, 4*time.Second)
	assertStatusEqual(t, "timeout", cfg.Timeout, time.Second)
	code = run(context.Background(), []string{"dashboard"}, &stdout, &stderr)
	assertStatusEqual(t, "non-TTY code", code, 2)
	assertStatusEqual(t, "non-TTY stdout", stdout.Len(), 0)
	if !strings.Contains(stderr.String(), "collab status --json") {
		t.Fatal("non-TTY fallback missing")
	}
}
func TestDashboardHelpAndInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"--interval", "0s"}, {"--interval", "61s"}, {"--timeout", "0s"}, {"--timeout", "31s"}, {"--socket", ""}, {"--json"}, {"extra"}} {
		var stderr bytes.Buffer
		if _, code := dashboardOptions(args, &stderr); code != 2 {
			t.Fatalf("accepted %v", args)
		}
	}
	var stdout, stderr bytes.Buffer
	if run(context.Background(), []string{"dashboard", "--help"}, &stdout, &stderr) != 0 {
		t.Fatal("help failed outside TTY")
	}
	if stdout.Len() != 0 {
		t.Fatal("help polluted stdout")
	}
}
