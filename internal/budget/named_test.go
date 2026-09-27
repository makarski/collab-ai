package budget

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func namedFixture(t *testing.T) Store {
	t.Helper()
	s := Store{Directory: t.TempDir()}
	if _, err := s.Create("task", 100); err != nil {
		t.Fatal(err)
	}
	return s
}

func namedOpen(t *testing.T, s Store) *Budget {
	t.Helper()
	b, err := s.Open("task")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func namedStatus(t *testing.T, s Store) Snapshot {
	t.Helper()
	out, err := s.Status("task")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNamedBudgetStatusWhileRunningAndAfterRestart(t *testing.T) {
	s := namedFixture(t)
	out := namedStatus(t, s)
	if out.UsageReported || out.LastReportedAt != nil {
		t.Fatal("new budget invents usage", out)
	}
	b := namedOpen(t, s)
	b.Observe("thread", 0)
	out = namedStatus(t, s)
	if !out.UsageReported || out.LastReportedAt == nil {
		t.Fatal("zero report was not recorded", out)
	}
	b.Observe("thread", 60)
	b.Close()
	b = namedOpen(t, s)
	defer b.Close()
	b.Observe("thread", 60)
	out = namedStatus(t, s)
	if out.ReportedTokens != 60 || out.RemainingTokens != 40 {
		t.Fatal("restart lost or duplicated usage", out)
	}
	if next, err := s.Open("task"); err == nil {
		next.Close()
		t.Fatal("second launcher admitted")
	}
}

func TestNamedExhaustionAndUnknownAccountingStayVisible(t *testing.T) {
	for _, failure := range []bool{false, true} {
		s := namedFixture(t)
		b := namedOpen(t, s)
		b.Observe("thread", 107)
		if failure {
			b.Fail(errors.New("test accounting failure"))
		}
		b.Close()
		out := namedStatus(t, s)
		if out.OvershootTokens != 7 || out.RemainingTokens != 0 {
			t.Fatal(out)
		}
		want := "exhausted"
		if failure {
			want = "stopped_unknown"
		}
		if out.State != want {
			t.Fatal(out)
		}
		if next, err := s.Open("task"); err == nil {
			next.Close()
			t.Fatal("stopped budget reopened")
		}
	}
}

func TestCreateCannotReplaceAnExistingBudget(t *testing.T) {
	s := namedFixture(t)
	b := namedOpen(t, s)
	b.Observe("thread", 60)
	b.Close()
	_, err := s.Create("task", 1000)
	requireErrorContains(t, err, "already exists")
	out := namedStatus(t, s)
	if out.Cap != 100 || out.ReportedTokens != 60 {
		t.Fatal("creation reset budget", out)
	}
}

func TestConcurrentCreatorsHaveOneWinner(t *testing.T) {
	s := Store{Directory: t.TempDir()}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Create("task", 100); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal(successes.Load())
	}
}

func TestNamedBudgetRejectsTraversalAndMissingFiles(t *testing.T) {
	s := Store{Directory: t.TempDir()}
	for _, name := range []string{"", "../task", "/tmp/task", ".", "a/b", "task\n"} {
		if _, err := s.Create(name, 100); err == nil {
			t.Fatal("invalid name accepted", name)
		}
	}
	if b, err := s.Open("missing"); err == nil {
		b.Close()
		t.Fatal("missing budget created")
	}
	if _, err := s.Status("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestOpenMissingBudgetDoesNotCreateLock(t *testing.T) {
	parent := t.TempDir()
	for _, directory := range []string{parent, filepath.Join(parent, "absent")} {
		s := Store{Directory: directory}
		b, err := s.Open("missing")
		if b != nil {
			b.Close()
			t.Fatal("missing budget opened")
		}
		requireErrorContains(t, err, `open budget "missing"`)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(directory, "missing.json.lock")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("opening a missing budget left a lock file", err)
		}
	}
}

func TestLegacyInlineBudgetStatusAndDirectorySelection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COLLAB_BUDGET_DIR", dir)
	s, err := DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	data := `{"version":1,"limit":100,"threads":{"thread":60}}`
	if err := os.WriteFile(filepath.Join(dir, "task.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	out := namedStatus(t, s)
	if !out.UsageReported || out.LastReportedAt != nil {
		t.Fatal("legacy report age invented", out)
	}
	b := namedOpen(t, s)
	b.Close()
	t.Setenv("COLLAB_BUDGET_DIR", "relative")
	if _, err := DefaultStore(); err == nil {
		t.Fatal("relative directory accepted")
	}
}
