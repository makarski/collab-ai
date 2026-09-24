package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"collab-ai/internal/dashboard"
	"github.com/charmbracelet/x/term"
)

func runDashboard(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, code := dashboardOptions(args, stderr)
	if code != -1 {
		return code
	}
	output := dashboardTerminal(stdout)
	if output == nil {
		fmt.Fprintln(stderr, "collab dashboard requires an interactive terminal; use collab status or collab status --json")
		return 2
	}
	if err := dashboard.Run(ctx, cfg, os.Stdin, output); err != nil {
		fmt.Fprintf(stderr, "dashboard: %v\n", err)
		return 1
	}
	return 0
}

func dashboardOptions(args []string, stderr io.Writer) (dashboard.Config, int) {
	cfg := dashboard.Config{NoColor: os.Getenv("NO_COLOR") != ""}
	flags := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.Socket, "socket", defaultSocket(), "broker Unix socket path")
	flags.DurationVar(&cfg.Interval, "interval", 2*time.Second, "delay between requests, from 1s to 1m")
	flags.DurationVar(&cfg.Timeout, "timeout", 3*time.Second, "request timeout, greater than zero and at most 30s")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, 0
		}
		return cfg, 2
	}
	if err := validateDashboard(cfg, flags.NArg()); err != nil {
		fmt.Fprintln(stderr, err)
		return cfg, 2
	}
	return cfg, -1
}

func validateDashboard(cfg dashboard.Config, positional int) error {
	if positional != 0 || cfg.Socket == "" {
		return errors.New("dashboard requires a socket path and no positional arguments")
	}
	if cfg.Interval < time.Second || cfg.Interval > time.Minute {
		return errors.New("dashboard interval must be between 1s and 1m")
	}
	if cfg.Timeout <= 0 || cfg.Timeout > 30*time.Second {
		return errors.New("dashboard timeout must be greater than zero and at most 30s")
	}
	return nil
}

func defaultSocket() string {
	if path := os.Getenv("COLLAB_SOCKET_PATH"); path != "" {
		return path
	}
	return "/tmp/collab-ai.sock"
}

func dashboardTerminal(stdout io.Writer) *os.File {
	output, ok := stdout.(*os.File)
	if !ok || output == nil {
		return nil
	}
	if !term.IsTerminal(output.Fd()) || !term.IsTerminal(os.Stdin.Fd()) {
		return nil
	}
	return output
}
