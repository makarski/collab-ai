package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"collab-ai/internal/bridge"
	"collab-ai/internal/protocol"
)

func runTerminal(ctx context.Context, cfg bridge.ClientConfig, binary string, args []string) error {
	if err := protocol.ValidateAgentID(cfg.AgentID); err != nil {
		return err
	}
	if err := validateTerminalArgs(args); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	endpoint, err := newTerminalEndpoint(ctx, func(ctx context.Context, stream io.ReadWriteCloser) error {
		return runWithOperator(ctx, cfg, binary, stream, stream)
	})
	if err != nil {
		return err
	}
	defer endpoint.Close()
	cmd := exec.CommandContext(ctx, binary, append([]string{"--remote", endpoint.URL()}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Codex terminal: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case err := <-endpoint.result:
		cancel()
		<-done
		return err
	}
}

// The remote endpoint belongs to this launcher. Forward model, directory,
// sandbox and approval settings unchanged; never let an argument route the UI
// to a different server while this process claims to manage communication.
func validateTerminalArgs(args []string) error {
	for _, arg := range args {
		if arg == "--remote" || strings.HasPrefix(arg, "--remote=") {
			return errors.New("--terminal manages --remote; pass other Codex options after --")
		}
	}
	return nil
}
