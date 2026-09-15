// Command codex is an App Server stdio proxy for one explicitly managed thread.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"collab-ai/internal/bridge"
	"collab-ai/internal/host"
	"golang.org/x/sync/errgroup"
)

func main() {
	socket := flag.String("socket", "/tmp/collab-ai.sock", "broker Unix socket path")
	agent := flag.String("agent-id", "", "logical inbox ID (required)")
	binary := flag.String("codex", "codex", "Codex executable")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := bridge.ClientConfig{SocketPath: *socket, AgentID: *agent, Harness: "codex-app-server"}
	if err := run(ctx, cfg, *binary); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg bridge.ClientConfig, binary string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client, err := bridge.NewLazyClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	cmd := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")
	cmd.Stderr = os.Stderr
	cmd.WaitDelay = 2 * time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		input.Close()
		output.Close()
		return err
	}
	defer func() { cancel(); input.Close(); output.Close(); cmd.Wait() }()
	p := host.NewProxy(host.NewWire(input), host.NewWire(os.Stdout))
	p.Listener = bridge.NewListener(ctx, client, p)
	defer p.Listener.Close()
	p.Tools, err = host.OpenTools(ctx, p.Listener)
	if err != nil {
		return err
	}
	defer p.Tools.Close()
	return serve(ctx, p, output)
}

func serve(ctx context.Context, p *host.Proxy, output io.ReadCloser) error {
	group, ctx := errgroup.WithContext(ctx)
	stop := context.AfterFunc(ctx, func() { os.Stdin.Close(); output.Close() })
	defer stop()
	group.Go(func() error {
		return host.ReadFrames(os.Stdin, func(f host.Frame) error { return p.FromOperator(ctx, f) })
	})
	group.Go(func() error { return host.ReadFrames(output, func(f host.Frame) error { return p.FromHost(ctx, f) }) })
	group.Go(func() error { return p.ServeTools(ctx) })
	err := group.Wait()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
