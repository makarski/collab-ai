package main

import (
	"context"
	"net/http"

	"collab-ai/internal/budget"
)

func openBudgetStatus(ctx context.Context, path, name string, b *budget.Budget) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	server, err := budget.OpenStatusSocket(path, name, b)
	if err != nil {
		return nil, err
	}
	return closeStatusWithContext(ctx, server), nil
}

func closeStatusWithContext(ctx context.Context, server *http.Server) func() {
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	return func() { stop(); _ = server.Close() }
}
