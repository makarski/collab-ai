package budget

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"syscall"
)

func lockBudgetFile(path string) (*os.File, error) {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("budget already in use or cannot be locked: %w", err)
	}
	return lock, nil
}

func (b *Budget) load() error {
	saved, err := readState(b.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return b.restore(saved)
}

func readState(path string) (state, error) {
	var saved state
	data, err := os.ReadFile(path)
	if err != nil {
		return saved, err
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return saved, fmt.Errorf("invalid budget file: %w", err)
	}
	return saved, saved.validate()
}

func (b *Budget) restore(saved state) error {
	if saved.SessionActive {
		return errors.New("budget has an unfinished supervised session; accounting must be reconciled before reuse")
	}
	if saved.Stopped != "" {
		return fmt.Errorf("budget previously stopped with unknown accounting: %s", saved.Stopped)
	}
	spent, err := saved.totalUsage()
	if err != nil {
		return err
	}
	b.state, b.spent = saved, spent
	return nil
}

func (s state) validate() error {
	if s.Version != 1 && s.Version != 2 {
		return errors.New("invalid budget version")
	}
	if s.Limit <= 0 {
		return errors.New("invalid budget cap: must be positive")
	}
	if s.SessionActive && s.Version != 2 {
		return errors.New("supervised budget requires version 2")
	}
	if s.Threads == nil {
		return errors.New("missing saved thread usage")
	}
	return nil
}

func validateThreadUsage(thread string, total int64) error {
	if thread == "" || total < 0 {
		return errors.New("missing thread identity or negative token count")
	}
	return nil
}

func (s state) totalUsage() (int64, error) {
	var spent int64
	for thread, total := range s.Threads {
		if err := validateThreadUsage(thread, total); err != nil {
			return 0, fmt.Errorf("invalid saved token usage: %w", err)
		}
		if total > math.MaxInt64-spent {
			return 0, errors.New("saved token usage overflow")
		}
		spent += total
	}
	return spent, nil
}

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
