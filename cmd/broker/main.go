// Command broker runs the collab-ai UDS message broker for local AI agents.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"collab-ai/internal/hub"
	"collab-ai/internal/server"
	"collab-ai/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := config{
		SocketPath: envOr("COLLAB_SOCKET_PATH", "/tmp/collab-ai.sock"),
		DBPath:     envOr("COLLAB_DB_PATH", "./collab-ai.db"),
	}

	if err := run(cfg, log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

type config struct {
	SocketPath string
	DBPath     string
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

	srv := server.New(cfg.SocketPath, h, log)
	defer srv.Close()

	err = srv.Listen(ctx)
	stop()
	<-h.Done()
	log.Info("shutdown complete")
	return err
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
