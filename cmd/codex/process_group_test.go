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

func TestAppServerShutdownKillsUncooperativeDescendants(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := appServerCommand(ctx, os.Args[0], "-test.run=^TestProcessGroupHelper$")
	cmd.Env = append(os.Environ(), "COLLAB_GROUP_TEST=wrapper")
	// Own the pipe: Cmd.Wait closes StdoutPipe even if a descendant is still
	// alive. Here EOF proves that both inherited writers have actually closed.
	output, writer, err := os.Pipe()
	terminalCheck(t, err)
	defer output.Close()
	defer writer.Close()
	cmd.Stdout = writer
	terminalCheck(t, cmd.Start())
	terminalCheck(t, writer.Close())
	lines := make(chan string, 2)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	assertProcessLine(t, lines, "ready")
	cancel()
	stopAppServer(cmd)
	select {
	case line, open := <-lines:
		if open {
			t.Fatalf("unexpected subprocess output after shutdown: %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("grandchild kept its pipe open after escalation")
	}
}

func TestProcessGroupHelper(t *testing.T) {
	role := os.Getenv("COLLAB_GROUP_TEST")
	if role == "" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	if role == "wrapper" {
		runUncooperativeWrapper()
	} else {
		fmt.Println("ready")
		time.Sleep(10 * time.Second) // bound leaks if process-group cleanup breaks
	}
	os.Exit(0)
}

func runUncooperativeWrapper() {
	child := exec.Command(os.Args[0], "-test.run=^TestProcessGroupHelper$")
	child.Env = append(os.Environ(), "COLLAB_GROUP_TEST=child")
	child.Stdout = os.Stdout
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	time.Sleep(10 * time.Second) // bound leaks if the test fails
	_ = child.Process.Kill()
	_ = child.Wait()
}
