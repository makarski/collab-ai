package main

import (
	"bytes"
	"context"
	"encoding/json"
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
