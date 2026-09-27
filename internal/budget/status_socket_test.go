package budget

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func statusFixture(t *testing.T) (*Budget, string, *http.Server) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "budget-status-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	b := namedOpen(t, namedFixture(t))
	t.Cleanup(func() { b.Close() })
	path := filepath.Join(dir, "status.sock")
	server, err := OpenStatusSocket(path, "task", b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return b, path, server
}

func TestStatusSocketCannotMutateOrSelectBudget(t *testing.T) {
	b, path, _ := statusFixture(t)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	for _, attempt := range []struct{ method, route string }{
		{"POST", "/v1/status"}, {"PUT", "/v1/status"}, {"DELETE", "/v1/status"},
		{"POST", "/v1/observe"}, {"POST", "/v1/create"}, {"POST", "/v1/reset"},
		{"GET", "/v1/status?name=other&cap=999999"}, {"GET", "/v1/admin"},
	} {
		request, err := http.NewRequest(attempt.method, "http://budget"+attempt.route,
			strings.NewReader(`{"role":"admin","cap":999999,"reported_tokens":0}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer pretend-admin")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 404 && response.StatusCode != 405 {
			t.Fatal(attempt, response.Status)
		}
	}
	b.Observe("thread", 107)
	out, err := ReadStatus(context.Background(), path, "task")
	if err != nil {
		t.Fatal(err)
	}
	if out.Cap != 100 || out.ReportedTokens != 107 || out.State != "exhausted" {
		t.Fatal(out)
	}
	if _, err := ReadStatus(context.Background(), path, "other"); err == nil {
		t.Fatal("another budget selected")
	}
}

func TestStatusSocketLossReturnsError(t *testing.T) {
	_, path, server := statusFixture(t)
	if _, err := ReadStatus(context.Background(), path, "task"); err != nil {
		t.Fatal(err)
	}
	server.Close()
	if _, err := ReadStatus(context.Background(), path, "task"); err == nil {
		t.Fatal("socket loss invented status")
	}
}

func TestStatusSocketRefusesUnsafeDirectoryAndExistingPath(t *testing.T) {
	b, path, _ := statusFixture(t)
	if server, err := OpenStatusSocket(path, "task", b); err == nil {
		server.Close()
		t.Fatal("active endpoint replaced")
	}
	if err := os.Chmod(filepath.Dir(path), 0777); err != nil {
		t.Fatal(err)
	}
	if server, err := OpenStatusSocket(filepath.Join(filepath.Dir(path), "other.sock"), "task", b); err == nil {
		server.Close()
		t.Fatal("writable endpoint directory accepted")
	}
	if _, err := OpenStatusSocket("relative.sock", "task", b); err == nil {
		t.Fatal("relative socket accepted")
	}
}
