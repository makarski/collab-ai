// Command collab provides read-only operator tools for the local broker.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"collab-ai/internal/status"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "dashboard" {
		return runDashboard(ctx, args[1:], stdout, stderr)
	}
	cfg, code := parseOptions(args, stderr)
	if code != -1 {
		return code
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	out, queryErr := status.Fetch(ctx, cfg.socket)
	var writeErr error
	if cfg.asJSON {
		writeErr = json.NewEncoder(stdout).Encode(out)
	} else {
		writeErr = status.WriteText(stdout, out)
	}
	if writeErr != nil {
		fmt.Fprintln(stderr, writeErr)
		return 1
	}
	if queryErr != nil {
		return 1
	}
	return 0
}

type options struct {
	socket  string
	asJSON  bool
	timeout time.Duration
}

// A -1 code means parsing succeeded and execution should continue.
func parseOptions(args []string, stderr io.Writer) (options, int) {
	var cfg options
	if len(args) == 0 || args[0] != "status" {
		fmt.Fprintln(stderr, "usage: collab status [--socket PATH] [--json] [--timeout 3s]\n       collab dashboard [--socket PATH] [--interval 2s] [--timeout 3s]")
		return cfg, 2
	}
	socketDefault := defaultSocket()
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.socket, "socket", socketDefault, "broker Unix socket path")
	flags.BoolVar(&cfg.asJSON, "json", false, "emit versioned JSON, including unavailable/error results")
	flags.DurationVar(&cfg.timeout, "timeout", 3*time.Second, "overall timeout, greater than zero and at most 30s")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, 0
		}
		return cfg, 2
	}
	if err := cfg.validate(flags.NArg()); err != nil {
		fmt.Fprintln(stderr, err)
		return cfg, 2
	}
	return cfg, -1
}

func (cfg options) validate(positional int) error {
	if positional != 0 || cfg.socket == "" {
		return errors.New("status requires a socket path and no positional arguments")
	}
	if cfg.timeout <= 0 || cfg.timeout > 30*time.Second {
		return errors.New("status timeout must be greater than zero and at most 30s")
	}
	return nil
}
