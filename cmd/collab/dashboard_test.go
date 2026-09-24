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
	if code != -1 || cfg.Socket != "/custom.sock" || cfg.Interval != 4*time.Second || cfg.Timeout != time.Second {
		t.Fatalf("wrong options: %+v code=%d", cfg, code)
	}
	code = run(context.Background(), []string{"dashboard"}, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "collab status --json") {
		t.Fatalf("non-TTY invocation: %d %s", code, &stderr)
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
