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
	if !strings.Contains(b.Err().Error(), "100 reported / 100 cap (0 overshoot)") {
		t.Fatal(b.Err())
	}
	b.Observe("a", 75) // continue recording reports during shutdown
	if !strings.Contains(b.Err().Error(), "115 reported / 100 cap (15 overshoot)") {
		t.Fatal(b.Err())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved state
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Threads["a"] != 75 || saved.Threads["b"] != 40 {
		t.Fatal(saved)
	}
	b.Close()
	if next, err := Open(path, 100); err == nil {
		next.Close()
		t.Fatal("exhausted budget reopened")
	}
}

func TestConcurrentObservationsAndExclusiveOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	b := openTest(t, path, 1000)
	defer b.Close()
	if next, err := Open(path, 1000); err == nil {
		next.Close()
		t.Fatal("concurrent launcher admitted")
	}
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
	if next, err := Open(path, 1001); err == nil {
		next.Close()
		t.Fatal("cap changed on restart")
	}
}

func TestRejectsInvalidSavedStateAndFlags(t *testing.T) {
	for _, data := range []string{`{}`, `{"version":1,"limit":10}`, `{"version":1,"limit":10,"threads":{"a":-1}}`, `{"version":1,"limit":10,"threads":{"a":9223372036854775807,"b":1}}`, `not json`} {
		t.Run(data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "budget.json")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if b, err := Open(path, 10); err == nil {
				b.Close()
				t.Fatal("invalid state accepted")
			}
		})
	}
	for _, limit := range []int64{-1, 0} {
		if b, err := Open(filepath.Join(t.TempDir(), "budget"), limit); err == nil {
			b.Close()
			t.Fatal("invalid cap accepted")
		}
	}
	if b, err := Open("", 10); err == nil {
		b.Close()
		t.Fatal("missing path accepted")
	}
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
		if next, err := Open(path, math.MaxInt64); err == nil {
			next.Close()
			t.Fatal("unknown accounting reopened")
		}
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
	if b.Err() == nil || !strings.Contains(b.Err().Error(), "persist") {
		t.Fatal(b.Err())
	}
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
	if b.Err() == nil || !strings.Contains(b.Err().Error(), "regressed") {
		t.Fatal("counter regression accepted", b.Err())
	}
	if b.spent != 60 {
		t.Fatal("refunded tokens", b.spent)
	}
	b.Close()
	if next, err := Open(path, 100); err == nil {
		next.Close()
		t.Fatal("unknown accounting reopened")
	}
}
