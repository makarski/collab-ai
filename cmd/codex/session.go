package main

import (
	"collab-ai/internal/host"
	"context"
	"errors"
	"io"
)

func (l codexLauncher) beginSession() (func(bool) error, error) {
	if !l.restricted {
		return func(bool) error { return nil }, nil
	}
	if err := l.budget.BeginSession(); err != nil {
		return nil, err
	}
	return l.budget.EndSession, nil
}

func readHostFrames(ctx context.Context, p *host.Proxy, output io.Reader) error {
	err := host.ReadFrames(output, func(f host.Frame) error { return p.FromHost(ctx, f) })
	if p.RestrictedOperator && ctx.Err() == nil {
		return errors.New("restricted native client disconnected unexpectedly; accounting is unconfirmed")
	}
	return err
}
