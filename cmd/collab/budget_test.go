package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"collab-ai/internal/budget"
)

func budgetCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, err bytes.Buffer
	code := run(context.Background(), append([]string{"budget"}, args...), &out, &err)
	return code, out.String(), err.String()
}

func TestBudgetCreateAndStatusThroughCLI(t *testing.T) {
	t.Setenv("COLLAB_BUDGET_DIR", t.TempDir())
	code, out, diagnostic := budgetCLI(t, "create", "task", "--tokens", "100", "--json")
	assertStatusEqual(t, "create", code, 0)
	assertStatusEqual(t, "stderr", diagnostic, "")
	var snapshot budget.Snapshot
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		t.Fatal(err)
	}
	assertStatusEqual(t, "cap", snapshot.Cap, int64(100))
	assertStatusEqual(t, "reported", snapshot.UsageReported, false)
	code, out, diagnostic = budgetCLI(t, "status", "task")
	assertStatusEqual(t, "status", code, 0)
	if !strings.Contains(out, "actual spend is unknown") {
		t.Fatal(out)
	}
	code, _, _ = budgetCLI(t, "create", "task", "--tokens", "200")
	assertStatusEqual(t, "duplicate", code, 1)
}

func TestBudgetCLIRejectsInvalidRequests(t *testing.T) {
	t.Setenv("COLLAB_BUDGET_DIR", t.TempDir())
	for _, args := range [][]string{{}, {"delete", "task"}, {"create", "task"},
		{"create", "../task", "--tokens", "100"}, {"status", "missing"}, {"status", "task", "--tokens", "1"}} {
		code, out, diagnostic := budgetCLI(t, args...)
		if code == 0 {
			t.Fatalf("invalid command accepted: %v", args)
		}
		assertStatusEqual(t, "failed command stdout", out, "")
		if diagnostic == "" {
			t.Fatalf("missing diagnostic: %v", args)
		}
	}
}

func TestBudgetHelpDoesNotRequireAStore(t *testing.T) {
	t.Setenv("COLLAB_BUDGET_DIR", "invalid-relative-path")
	code, out, diagnostic := budgetCLI(t, "--help")
	assertStatusEqual(t, "help", code, 0)
	assertStatusEqual(t, "stdout", out, "")
	if !strings.Contains(diagnostic, "budget create") {
		t.Fatal(diagnostic)
	}
}

func TestRemoteBudgetStatusNeverFallsBackToLocalBudget(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "budget-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	s := budget.Store{Directory: dir}
	if _, err := s.Create("task", 100); err != nil {
		t.Fatal(err)
	}
	b, err := s.Open("task")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	path := filepath.Join(dir, "status.sock")
	server, err := budget.OpenStatusSocket(path, "task", b)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	// A hostile local setting cannot redirect a remote status query.
	t.Setenv("COLLAB_BUDGET_DIR", "invalid-local-directory")
	code, out, diagnostic := budgetCLI(t, "status", "task", "--socket", path, "--json")
	if code != 0 || !strings.Contains(out, `"cap":100`) {
		t.Fatal(code, out, diagnostic)
	}
	server.Close()
	t.Setenv("COLLAB_BUDGET_DIR", dir)
	code, out, _ = budgetCLI(t, "status", "task", "--socket", path)
	if code == 0 || out != "" {
		t.Fatal("fell back to local budget", code, out)
	}
	code, _, _ = budgetCLI(t, "create", "other", "--tokens", "1000", "--socket", path)
	if code == 0 {
		t.Fatal("remote budget creation accepted")
	}
}
