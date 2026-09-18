package bridge

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

type delegatedRead struct {
	AfterCursor    uint64 `json:"after_cursor,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

func (a delegatedRead) defaults() (delegatedRead, error) {
	if a.Limit == 0 {
		a.Limit = 20
	}
	if a.TimeoutSeconds == 0 {
		a.TimeoutSeconds = 25
	}
	if a.TimeoutSeconds < 1 || a.TimeoutSeconds > 30 {
		return a, errors.New("timeout_seconds must be between 1 and 30")
	}
	return a, validateListenerRead(a.Limit, time.Duration(a.TimeoutSeconds)*time.Second)
}

func (d *listenerDelegation) serveWait(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/wait" {
		http.Error(w, "only POST /wait is supported", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+d.grant.Token)) != 1 || d.ctx.Err() != nil {
		http.Error(w, "listener delegation denied or expired", http.StatusForbidden)
		return
	}
	args, err := decodeDelegatedRead(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	select {
	case d.busy <- struct{}{}:
		defer func() { <-d.busy }()
	default:
		http.Error(w, "another delegated wait is active; use one listener", http.StatusConflict)
		return
	}
	out, err := d.client.observe(r.Context(), args.AfterCursor, args.Limit, time.Duration(args.TimeoutSeconds)*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func decodeDelegatedRead(w http.ResponseWriter, r *http.Request) (delegatedRead, error) {
	var args delegatedRead
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return args, errors.New("invalid delegated wait request")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return args, errors.New("expected one delegated wait request")
	}
	return args.defaults()
}

// WaitDelegated contacts only the parent's read-only endpoint. It never dials
// the broker, even when this process inherited the parent's logical agent ID.
func WaitDelegated(ctx context.Context, socket, token string, args delegatedRead) (DelegatedInbox, error) {
	if !filepath.IsAbs(socket) || len(token) != 64 {
		return DelegatedInbox{}, errors.New("use the absolute socket_path and token returned by delegate_listener")
	}
	args, err := args.defaults()
	if err != nil {
		return DelegatedInbox{}, err
	}
	data, err := json.Marshal(args)
	if err != nil {
		return DelegatedInbox{}, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: ioTimeout}).DialContext(ctx, "unix", socket)
	}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 35 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://collab-listener/wait", bytes.NewReader(data))
	if err != nil {
		return DelegatedInbox{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return DelegatedInbox{}, fmt.Errorf("delegated listener unavailable (owner stopped, grant revoked/expired, or local access denied): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return DelegatedInbox{}, fmt.Errorf("delegated wait rejected (%d): %s", resp.StatusCode, body)
	}
	var out DelegatedInbox
	// The parent retains at most 4 MiB; allow room for envelope/JSON escaping.
	err = json.NewDecoder(io.LimitReader(resp.Body, 2*maxInboxBytes)).Decode(&out)
	return out, err
}
