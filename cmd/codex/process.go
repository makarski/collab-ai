package main

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

func codexCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	// The npm launcher forwards SIGTERM to the native Codex child. The default
	// CommandContext SIGKILL kills only the wrapper and leaves that child alive.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	return cmd
}
