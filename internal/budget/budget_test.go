package budget

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openTest(t *testing.T, path string, limit int64) *Budget {
	t.Helper()
	b, err := Open(path, limit)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func requireErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func assertCannotOpen(t *testing.T, path string, limit int64) error {
	t.Helper()
	b, err := Open(path, limit)
	if err == nil {
		b.Close()
		t.Fatal("budget unexpectedly reopened")
	}
	return err
}

func readSavedState(t *testing.T, path string) state {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved state
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestUsageSurvivesRestartWithoutDuplicateCharges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	b := openTest(t, path, 100)
	b.Observe("a", 60)
	b.Close()
	b = openTest(t, path, 100)
	defer b.Close()
	b.Observe("a", 60)
	b.Observe("b", 39)
	if b.Err() != nil {
		t.Fatal(b.Err())
	}
	b.Observe("b", 40)
	requireErrorContains(t, b.Err(), "100 reported / 100 cap (0 overshoot)")
	b.Observe("a", 75) // continue recording reports during shutdown
	requireErrorContains(t, b.Err(), "115 reported / 100 cap (15 overshoot)")
	saved := readSavedState(t, path)
	if saved.Threads["a"] != 75 || saved.Threads["b"] != 40 {
		t.Fatal(saved)
	}
	b.Close()
	requireErrorContains(t, assertCannotOpen(t, path, 100), "soft token cap reached")
}

func TestConcurrentObservationsAndExclusiveOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	b := openTest(t, path, 1000)
	defer b.Close()
	requireErrorContains(t, assertCannotOpen(t, path, 1000), "already in use")
	var wg sync.WaitGroup
	for i := int64(1); i <= 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); b.Observe("a", 100) }()
	}
	wg.Wait()
	if b.spent != 100 {
		t.Fatal(b.spent)
	}
	b.Close()
	requireErrorContains(t, assertCannotOpen(t, path, 1001), "refusing to change")
}

func TestRejectsInvalidSavedState(t *testing.T) {
	for _, data := range []string{`{}`, `{"version":1,"limit":10}`, `{"version":1,"limit":10,"threads":{"a":-1}}`, `{"version":1,"limit":10,"threads":{"a":9223372036854775807,"b":1}}`, `not json`} {
		t.Run(data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "budget.json")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			assertCannotOpen(t, path, 10)
		})
	}
}

func TestInvalidFlagsIdentifyTheMissingSetting(t *testing.T) {
	for _, limit := range []int64{-1, 0} {
		err := assertCannotOpen(t, filepath.Join(t.TempDir(), "budget"), limit)
		requireErrorContains(t, err, "--token-cap must be positive")
	}
	requireErrorContains(t, assertCannotOpen(t, "", 10), "--budget-file is required")
}

func TestAccountingFailureStopsAndStaysStopped(t *testing.T) {
	for _, total := range []int64{-1, math.MaxInt64} {
		path := filepath.Join(t.TempDir(), "budget.json")
		b := openTest(t, path, math.MaxInt64)
		b.Observe("a", 1)
		b.Observe("b", total)
		if b.Err() == nil {
			t.Fatal("invalid accounting accepted")
		}
		b.Close()
		requireErrorContains(t, assertCannotOpen(t, path, math.MaxInt64), "unknown accounting")
	}
}

func TestPersistenceFailureStopsAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	b := openTest(t, path, 100)
	defer b.Close()
	// A directory cannot be replaced by the atomic file rename (also works as root).
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	b.Observe("a", 1)
	requireErrorContains(t, b.Err(), "persist")
	select {
	case <-b.Done():
	default:
		t.Fatal("supervisor not woken")
	}
}

func TestRegressingCounterCannotSilentlyResetUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	b := openTest(t, path, 100)
	b.Observe("a", 60)
	b.Observe("a", 40)
	requireErrorContains(t, b.Err(), "regressed")
	if b.spent != 60 {
		t.Fatal("refunded tokens", b.spent)
	}
	b.Close()
	requireErrorContains(t, assertCannotOpen(t, path, 100), "unknown accounting")
}
