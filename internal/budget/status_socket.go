package budget

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// OpenStatusSocket exposes one launcher's observations, never budget mutations.
// Deploy only the socket directory into the workload, read-only. Budget storage
// and the launcher must remain outside the workload's filesystem/process access.
func OpenStatusSocket(path, name string, b *Budget) (*http.Server, error) {
	if b == nil || !budgetName.MatchString(name) {
		return nil, errors.New("a status socket requires a named launcher budget")
	}
	if err := validateStatusDirectory(path); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// Other container identities may read this API. There are no write routes.
	if err := os.Chmod(path, 0666); err != nil {
		listener.Close()
		return nil, err
	}
	server := &http.Server{Handler: statusHandler(name, b), ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 4096}
	go func() { _ = server.Serve(listener) }()
	return server, nil
}

func validateStatusDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("budget status socket must be an absolute path")
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || owner.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
		return errors.New("budget status directory must belong to the launcher and not be group/world writable")
	}
	return nil
}

func statusHandler(name string, b *Budget) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/v1/status" || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "read-only budget endpoint", http.StatusMethodNotAllowed)
			return
		}
		out, err := b.Snapshot(name)
		if err != nil {
			http.Error(w, "budget accounting unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

// Snapshot reads the launcher's current state, including late observations.
func (b *Budget) Snapshot(name string) (Snapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return snapshot(name, b.state)
}
