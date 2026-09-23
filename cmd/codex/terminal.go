package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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
	if err := validateBrokerSocket(cfg.SocketPath); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	endpoint, err := newTerminalEndpoint(ctx, func(ctx context.Context, stream io.ReadWriteCloser) error {
		return runWithOperator(ctx, cfg, binary, operatorIO{input: stream, output: stream})
	})
	if err != nil {
		return err
	}
	defer endpoint.Close()
	cmd := codexCommand(ctx, binary, append([]string{"--remote", endpoint.URL()}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
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

func validateBrokerSocket(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("broker socket %q: %w; start the broker and check --socket before launching Codex", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("broker path %q is not a Unix socket", path)
	}
	return nil
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
