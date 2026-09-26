// Package budget tracks observed Codex tokens. It is a soft stop policy, not a
// provider request gate or a security boundary against the local account.
package budget

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"syscall"
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
	if path == "" || limit <= 0 {
		return nil, errors.New("--token-cap must be positive and requires --budget-file")
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("budget already in use or cannot be locked: %w", err)
	}
	b := &Budget{path: path, lock: lock, done: make(chan struct{}),
		state: state{Version: 1, Limit: limit, Threads: make(map[string]int64)}}
	if err := b.load(); err != nil {
		b.Close()
		return nil, err
	}
	if b.state.Limit != limit {
		b.Close()
		return nil, fmt.Errorf("saved token cap is %d; refusing to change it to %d", b.state.Limit, limit)
	}
	if err := b.save(); err != nil {
		b.Close()
		return nil, err
	}
	b.checkLimit()
	if b.err != nil {
		b.Close()
		return nil, b.Err()
	}
	return b, nil
}

func (b *Budget) load() error {
	data, err := os.ReadFile(b.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved state
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("invalid budget file: %w", err)
	}
	b.state = saved
	if b.state.Version != 1 || b.state.Limit <= 0 || b.state.Threads == nil {
		return errors.New("invalid budget file schema")
	}
	if b.state.Stopped != "" {
		return fmt.Errorf("budget previously stopped with unknown accounting: %s", b.state.Stopped)
	}
	for thread, total := range b.state.Threads {
		if thread == "" || total < 0 || total > math.MaxInt64-b.spent {
			return errors.New("invalid saved token usage")
		}
		b.spent += total
	}
	return nil
}

// Observe counts cumulative totalTokens once per thread. A regression stops
// accounting because a delayed snapshot cannot be distinguished from a reset.
// Historical totals from resumed/forked threads are included.
func (b *Budget) Observe(thread string, total int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	previous := b.state.Threads[thread]
	if thread == "" || total < 0 || total-previous > math.MaxInt64-b.spent {
		b.failAccounting(errors.New("invalid token usage; stopping because accounting is unknown"))
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

func (b *Budget) save() error {
	data, err := json.MarshalIndent(b.state, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(b.path), ".collab-budget-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), b.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(b.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
