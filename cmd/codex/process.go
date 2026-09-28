package main

import (
	"context"
	"io"
	"os/exec"
	"syscall"
	"time"
)

func appServerPipes(cmd *exec.Cmd) (io.WriteCloser, io.ReadCloser, error) {
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, nil, err
	}
	return input, output, nil
}

func codexCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	// The npm launcher forwards SIGTERM to the native Codex child. The default
	// CommandContext SIGKILL kills only the wrapper and leaves that child alive.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// Only the noninteractive App Server gets its own process group. Moving the
// terminal UI out of the foreground group would break terminal input.
func appServerCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := codexCommand(ctx, binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	return cmd
}

// Clean up the whole group, even when a launcher exits before its children.
// This is not containment for descendants deliberately escaping with setsid.
func stopAppServer(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
		if syscall.Kill(-cmd.Process.Pid, 0) != nil {
			return
		}
		<-timer.C
	case <-timer.C:
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-done
}
