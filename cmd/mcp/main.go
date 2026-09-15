// Command mcp exposes the broker's messaging tools over MCP stdio.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"collab-ai/internal/bridge"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	socketDefault := os.Getenv("COLLAB_SOCKET_PATH")
	if socketDefault == "" {
		socketDefault = "/tmp/collab-ai.sock"
	}
	socket := flag.String("socket", socketDefault, "broker Unix socket path")
	agent := flag.String("agent-id", "", "logical inbox ID; one active session per ID (required)")
	harness := flag.String("harness", "mcp", "agent harness name")
	model := flag.String("model", "", "agent model name (optional)")
	channel := flag.Bool("claude-channel", false, "advertise the experimental Claude channel; also requires Claude's explicit channel opt-in")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := bridge.ClientConfig{SocketPath: *socket, AgentID: *agent, Harness: *harness, Model: *model}
	if err := run(ctx, cfg, *channel); err != nil {
		// stdout is reserved exclusively for MCP protocol frames.
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg bridge.ClientConfig, channel bool) error {
	c, err := bridge.NewLazyClient(cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	server := bridge.NewMCP(c)
	var transport mcp.Transport = &mcp.StdioTransport{}
	if channel {
		t := &bridge.ChannelTransport{Transport: transport}
		listener := bridge.NewListener(ctx, c, t)
		defer listener.Close()
		server, transport = bridge.NewHostMCP(listener, true), t
	}
	err = server.Run(ctx, transport)
	if ctx.Err() != nil {
		return nil
	}
	return err
}
