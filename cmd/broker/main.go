// Command broker runs the collab-ai UDS message broker for local AI agents.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	settings "github.com/pairtask/pairtask/internal/config"
	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/server"
	"github.com/pairtask/pairtask/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg, err := loadConfig(os.LookupEnv)
	if err == nil {
		err = run(cfg, log)
	}
	if err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

type config struct {
	SocketPath string
	DBPath     string
	// OperatorSocketPath is empty when the operator endpoint is disabled.
	OperatorSocketPath string
	OperatorUID        int
}

func loadConfig(lookup settings.LookupFunc) (config, error) {
	var cfg config
	var err error
	if cfg.SocketPath, err = settings.Required(lookup, "SOCKET_PATH", "/tmp/collab-ai.sock"); err != nil {
		return cfg, err
	}
	if cfg.DBPath, err = settings.Required(lookup, "DB_PATH", "./collab-ai.db"); err != nil {
		return cfg, err
	}
	// The operator endpoint is optional: unset or empty disables it.
	cfg.OperatorSocketPath, _, _ = settings.LookupIn(lookup, "OPERATOR_SOCKET_PATH")
	uid, _, _ := settings.LookupIn(lookup, "OPERATOR_UID")
	if cfg.OperatorSocketPath == "" {
		if uid != "" {
			return cfg, errors.New(settings.Name("OPERATOR_UID") + " requires " + settings.Name("OPERATOR_SOCKET_PATH"))
		}
		return cfg, nil
	}
	parsed, err := strconv.Atoi(uid)
	if err != nil || parsed < 0 {
		return cfg, errors.New(settings.Name("OPERATOR_SOCKET_PATH") + " requires " + settings.Name("OPERATOR_UID") +
			", a non-negative numeric UID")
	}
	cfg.OperatorUID = parsed
	if filepath.Clean(cfg.OperatorSocketPath) == filepath.Clean(cfg.SocketPath) {
		return cfg, errors.New("the operator socket must differ from the agent socket")
	}
	return cfg, nil
}

func run(cfg config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	lastSeq, err := st.LastSeq(ctx)
	if err != nil {
		return err
	}
	log.Info("state loaded", "db", cfg.DBPath, "last_seq", lastSeq)

	h := hub.New(st, lastSeq, log)
	go h.Run(ctx)
	defer func() {
		stop()
		<-h.Done()
	}()

	operatorDone := make(chan error, 1)
	if cfg.OperatorSocketPath == "" {
		operatorDone <- nil
	} else {
		op := server.NewOperator(cfg.OperatorSocketPath, h, cfg.OperatorUID, log)
		defer op.Close()
		go func() {
			err := op.Listen(ctx)
			if err != nil {
				err = fmt.Errorf("operator endpoint: %w", err)
				stop() // The broker does not run with a broken operator endpoint.
			}
			operatorDone <- err
		}()
	}

	srv := server.New(cfg.SocketPath, h, log)
	defer srv.Close()

	err = srv.Listen(ctx)
	stop()
	err = errors.Join(err, <-operatorDone)
	<-h.Done()
	log.Info("shutdown complete")
	return err
}
