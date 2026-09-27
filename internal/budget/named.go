package budget

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Store names local task budgets. Each budget still has one managed launcher.
type Store struct{ Directory string }

var budgetName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func DefaultStore() (Store, error) {
	directory := os.Getenv("COLLAB_BUDGET_DIR")
	if directory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Store{}, err
		}
		directory = filepath.Join(home, ".local", "state", "collab-ai", "budgets")
	}
	if !filepath.IsAbs(directory) {
		return Store{}, errors.New("COLLAB_BUDGET_DIR must be an absolute path")
	}
	return Store{Directory: filepath.Clean(directory)}, nil
}

func (s Store) path(name string) (string, error) {
	if !filepath.IsAbs(s.Directory) {
		return "", errors.New("budget directory must be absolute")
	}
	if !budgetName.MatchString(name) {
		return "", errors.New("budget names must be 1-64 letters, digits, dots, hyphens or underscores, starting with a letter or digit")
	}
	return filepath.Join(s.Directory, name+".json"), nil
}

func (s Store) Create(name string, limit int64) (Snapshot, error) {
	path, err := s.path(name)
	if err != nil {
		return Snapshot{}, err
	}
	if limit <= 0 {
		return Snapshot{}, errors.New("--tokens must be positive")
	}
	if err := os.MkdirAll(s.Directory, 0700); err != nil {
		return Snapshot{}, err
	}
	lock, err := lockBudgetFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	defer lock.Close()
	if err := requireNewBudget(path); err != nil {
		return Snapshot{}, err
	}
	b := &Budget{path: path, state: state{Version: 1, Limit: limit, Threads: make(map[string]int64)}}
	if err := b.save(); err != nil {
		return Snapshot{}, err
	}
	return snapshot(name, b.state)
}

func requireNewBudget(path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return errors.New("budget already exists; creation never resets usage or changes its cap")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Open requires a previously created budget and uses its saved cap exclusively.
func (s Store) Open(name string) (*Budget, error) {
	path, err := s.path(name)
	if err != nil {
		return nil, err
	}
	lock, err := lockBudgetFile(path)
	if err != nil {
		return nil, err
	}
	b := &Budget{path: path, lock: lock, done: make(chan struct{})}
	if err := b.loadNamed(); err != nil {
		b.Close()
		return nil, fmt.Errorf("open budget %q: %w", name, err)
	}
	return b, nil
}

func (b *Budget) loadNamed() error {
	saved, err := readState(b.path)
	if err != nil {
		return err
	}
	if err := b.restore(saved); err != nil {
		return err
	}
	b.checkLimit()
	return b.Err()
}

// Status reads an atomic on-disk snapshot without acquiring the launcher's lock.
func (s Store) Status(name string) (Snapshot, error) {
	path, err := s.path(name)
	if err != nil {
		return Snapshot{}, err
	}
	saved, err := readState(path)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot(name, saved)
}
