package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestAppServerShutdownKillsUncooperativeDescendants(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	cmd := appServerCommand(ctx, os.Args[0], "-test.run=^TestProcessGroupHelper$")
	cmd.Env = append(os.Environ(), "COLLAB_GROUP_TEST=wrapper", "COLLAB_GROUP_HEARTBEAT="+heartbeat)
	output, err := cmd.StdoutPipe()
	terminalCheck(t, err)
	terminalCheck(t, cmd.Start())
	defer output.Close()
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
	before, err := os.ReadFile(heartbeat)
	terminalCheck(t, err)
	time.Sleep(150 * time.Millisecond)
	after, err := os.ReadFile(heartbeat)
	terminalCheck(t, err)
	if string(before) != string(after) {
		t.Fatal("grandchild continued running after escalation")
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
		runHeartbeatChild()
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

func runHeartbeatChild() {
	end := time.Now().Add(10 * time.Second)
	for i := 0; time.Now().Before(end); i++ {
		if err := os.WriteFile(os.Getenv("COLLAB_GROUP_HEARTBEAT"), []byte(strconv.Itoa(i)), 0600); err != nil {
			os.Exit(2)
		}
		if i == 0 {
			fmt.Println("ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
