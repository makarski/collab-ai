package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIExecutableAliasAcceptance(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "pairtask")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	for _, name := range []string{"pk", "collab"} {
		if err := os.Symlink("pairtask", filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	missing := filepath.Join(directory, "absent.sock")
	for _, args := range [][]string{
		{"--help"},
		{"status", "--socket", missing, "--json"},
		{"status", "--socket", missing, "--timeout", "invalid"},
		{"operator", "receive", "--socket", missing, "--json"},
		{"not-a-command"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, code := invokeAlias(t, binary, args)
			for _, name := range []string{"pk", "collab"} {
				gotOut, gotErr, gotCode := invokeAlias(t, filepath.Join(directory, name), args)
				if gotOut != stdout || gotCode != code {
					t.Fatalf("%s changed stdout/exit: got %q/%d, want %q/%d", name, gotOut, gotCode, stdout, code)
				}
				if name == "collab" {
					warning, rest, found := strings.Cut(gotErr, "\n")
					if !found || !strings.Contains(strings.ToLower(warning), "deprecat") || !strings.Contains(warning, "pairtask") {
						t.Fatalf("legacy invocation missing deprecation line: %q", gotErr)
					}
					gotErr = rest
				}
				if gotErr != stderr {
					t.Fatalf("%s changed stderr beyond its warning: got %q, want %q", name, gotErr, stderr)
				}
			}
		})
	}
}

func invokeAlias(t *testing.T, binary string, args []string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "COLLAB_") && !strings.HasPrefix(entry, "PAIRTASK_") {
			command.Env = append(command.Env, entry)
		}
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil {
		t.Fatal("CLI invocation hung")
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	code := 0
	if exit != nil {
		code = exit.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}
