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
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *socket, *agent, *harness, *model); err != nil {
		// stdout is reserved exclusively for MCP protocol frames.
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, socket, agent, harness, model string) error {
	c, err := bridge.NewLazyClient(socket, agent, harness, model)
	if err != nil {
		return err
	}
	defer c.Close()
	err = bridge.NewMCP(c).Run(ctx, &mcp.StdioTransport{})
	if ctx.Err() != nil {
		return nil
	}
	return err
}
