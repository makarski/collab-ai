package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"collab-ai/internal/bridge"
)

func TestTerminalDiagnosticsKeepBoundedTail(t *testing.T) {
	for _, chunks := range [][]string{
		{"first warning\n", "second warning"},
		{strings.Repeat("a", terminalDiagnosticLimit+100), "last warning\n"},
		{strings.Repeat("b", terminalDiagnosticLimit-5), "last warning\n"},
	} {
		d := &terminalDiagnostics{}
		for _, chunk := range chunks {
			n, err := io.WriteString(d, chunk)
			if n != len(chunk) || err != nil {
				t.Fatalf("diagnostic write: %d, %v", n, err)
			}
		}
		want := strings.Join(chunks, "")
		truncated := len(want) > terminalDiagnosticLimit
		if truncated {
			want = want[len(want)-terminalDiagnosticLimit:]
		}
		if string(d.tail) != want || d.truncated != truncated {
			t.Fatal("diagnostic tail or truncation marker incorrect")
		}
		assertDiagnosticReport(t, d, want, truncated)
	}
}

func assertDiagnosticReport(t *testing.T, d *terminalDiagnostics, want string, truncated bool) {
	t.Helper()
	var output bytes.Buffer
	d.report(&output)
	if !strings.Contains(output.String(), want) || !strings.HasSuffix(output.String(), "\n") {
		t.Fatal("diagnostics lost or unterminated")
	}
	if strings.Contains(output.String(), "earlier output omitted") != truncated {
		t.Fatal("report did not describe truncation correctly")
	}
}

func TestQuietTerminalHasNoDiagnosticReport(t *testing.T) {
	var output bytes.Buffer
	(&terminalDiagnostics{}).report(&output)
	if output.Len() != 0 {
		t.Fatal("empty diagnostics produced output")
	}
}

func TestAppServerFailureRetainsCapturedDiagnostics(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fake-codex")
	terminalCheck(t, os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'project trust warning\\n' >&2\nprintf 'invalid-json\\n'\n"), 0700))
	input, writer := io.Pipe()
	defer writer.Close()
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	terminalCheck(t, err)
	defer output.Close()
	diagnostics := &terminalDiagnostics{}
	launcher := codexLauncher{client: bridge.ClientConfig{AgentID: "diagnostic-test", SocketPath: "/unused"},
		binary: binary, appStderr: diagnostics}
	if err := launcher.runOperator(context.Background(), operatorIO{input: input, output: output}); err == nil {
		t.Fatal("malformed server output should fail")
	}
	var report bytes.Buffer
	diagnostics.report(&report)
	if !strings.Contains(report.String(), "project trust warning") {
		t.Fatal("server failure lost its diagnostic")
	}
}
