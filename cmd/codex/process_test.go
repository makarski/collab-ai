package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

func TestCodexCancellationReachesWrappedChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := codexCommand(ctx, os.Args[0], "-test.run=^TestCodexProcessHelper$")
	cmd.Env = append(os.Environ(), "COLLAB_PROCESS_TEST=wrapper")
	output, err := cmd.StdoutPipe()
	terminalCheck(t, err)
	terminalCheck(t, cmd.Start())
	defer output.Close()
	lines := make(chan string, 2)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	assertProcessLine(t, lines, "ready")
	cancel()
	assertProcessLine(t, lines, "child stopped")
	// Cancellation is expected to make Wait return an error, even when the
	// wrapper and child both exited successfully after forwarding SIGTERM.
	cmd.Wait()
}

func assertProcessLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	select {
	case got := <-lines:
		if got != want {
			t.Fatalf("subprocess output = %q, want %q", got, want)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("timed out waiting for %q", want)
	}
}

// Exercise a launcher that forwards signals, like Codex's npm entrypoint.
func TestCodexProcessHelper(t *testing.T) {
	role := os.Getenv("COLLAB_PROCESS_TEST")
	if role == "" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	// Keep a broken implementation from leaking helper processes indefinitely.
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if role == "child" {
		fmt.Println("ready")
		<-ctx.Done()
		if ctx.Err() == context.Canceled {
			fmt.Println("child stopped")
		}
		os.Exit(0)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestCodexProcessHelper$")
	child.Env = append(os.Environ(), "COLLAB_PROCESS_TEST=child")
	child.Stdout = os.Stdout
	terminalCheck(t, child.Start())
	<-ctx.Done()
	child.Process.Signal(syscall.SIGTERM)
	child.Wait()
	os.Exit(0)
}
