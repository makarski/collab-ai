// Package budget tracks observed Codex tokens. It is a soft stop policy, not a
// provider request gate or a security boundary against the local account.
package budget

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
)

type state struct {
	Version int              `json:"version"`
	Limit   int64            `json:"limit"`
	Threads map[string]int64 `json:"threads"`
	Stopped string           `json:"stopped,omitempty"`
}

var errLimit = errors.New("soft token cap reached")

// Budget has one owning process; a second launcher must not reuse its file.
type Budget struct {
	mu    sync.Mutex
	path  string
	lock  *os.File
	state state
	spent int64
	err   error
	done  chan struct{}
}

// Open preserves the cap and per-thread high-water marks across restarts. A
// different cap requires an explicitly separate budget file.
func Open(path string, limit int64) (*Budget, error) {
	if path == "" {
		return nil, errors.New("--budget-file is required with --token-cap")
	}
	if limit <= 0 {
		return nil, errors.New("--token-cap must be positive")
	}
	lock, err := lockBudgetFile(path)
	if err != nil {
		return nil, err
	}
	b := &Budget{path: path, lock: lock, done: make(chan struct{}),
		state: state{Version: 1, Limit: limit, Threads: make(map[string]int64)}}
	if err := b.initialize(limit); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

func (b *Budget) initialize(limit int64) error {
	if err := b.load(); err != nil {
		return err
	}
	if b.state.Limit != limit {
		return fmt.Errorf("saved token cap is %d; refusing to change it to %d", b.state.Limit, limit)
	}
	if err := b.save(); err != nil {
		return err
	}
	b.checkLimit()
	return b.Err()
}

// Observe counts cumulative totalTokens once per thread. A regression stops
// accounting because a delayed snapshot cannot be distinguished from a reset.
// Historical totals from resumed/forked threads are included.
func (b *Budget) Observe(thread string, total int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	previous := b.state.Threads[thread]
	if err := b.validateObservation(thread, total); err != nil {
		b.failAccounting(err)
		return
	}
	if total < previous {
		b.failAccounting(errors.New("reported token total regressed; stopping because accounting is unknown"))
		return
	}
	if total == previous {
		return
	}
	b.spent += total - previous
	b.state.Threads[thread] = total
	if err := b.save(); err != nil {
		b.failAccounting(fmt.Errorf("cannot persist token usage: %w", err))
		return
	}
	b.checkLimit()
}

func (b *Budget) validateObservation(thread string, total int64) error {
	if err := validateThreadUsage(thread, total); err != nil {
		return fmt.Errorf("invalid token usage; stopping because accounting is unknown: %w", err)
	}
	if total-b.state.Threads[thread] > math.MaxInt64-b.spent {
		return errors.New("token usage overflow; stopping because accounting is unknown")
	}
	return nil
}

func (b *Budget) checkLimit() {
	if b.spent >= b.state.Limit {
		b.fail(errLimit)
	}
}

func (b *Budget) Fail(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failAccounting(err)
}

func (b *Budget) failAccounting(err error) {
	b.state.Stopped = err.Error()
	// Best effort when the original failure was storage itself. The caller must
	// repair storage and reconcile unknown usage before reusing such a file.
	_ = b.save()
	b.fail(err)
}

func (b *Budget) fail(err error) {
	if b.err == nil {
		b.err = err
		close(b.done)
	} else if errors.Is(b.err, errLimit) && !errors.Is(err, errLimit) {
		b.err = err
	}
}

func (b *Budget) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if errors.Is(b.err, errLimit) {
		return fmt.Errorf("%w: %d reported / %d cap (%d overshoot)", errLimit, b.spent, b.state.Limit, b.spent-b.state.Limit)
	}
	return b.err
}

func (b *Budget) Done() <-chan struct{} { return b.done }

// Close releases ownership. Call only after the supervisor has stopped.
func (b *Budget) Close() error { return b.lock.Close() }
