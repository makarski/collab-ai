package budget

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

// ReadStatus never falls back to a local file when the controller is unavailable.
// This is observation for the workload, not an admission or enforcement API.
func ReadStatus(ctx context.Context, path, name string) (Snapshot, error) {
	var out Snapshot
	if !filepath.IsAbs(path) || !budgetName.MatchString(name) {
		return out, errors.New("status requires an absolute socket path and a valid budget name")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://budget/v1/status", nil)
	if err != nil {
		return out, err
	}
	response, err := client.Do(request)
	if err != nil {
		return out, err
	}
	defer response.Body.Close()
	return decodeStatus(response, name)
}

func decodeStatus(response *http.Response, name string) (Snapshot, error) {
	var out Snapshot
	if response.StatusCode != http.StatusOK {
		return out, errors.New("budget controller did not return a status snapshot")
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, response.Body, 65536)).Decode(&out); err != nil {
		return out, err
	}
	if out.SchemaVersion != 1 || out.Name != name || out.Cap <= 0 {
		return Snapshot{}, errors.New("budget controller returned an invalid or different budget")
	}
	return out, nil
}
