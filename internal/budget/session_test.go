package budget

import (
	"path/filepath"
	"testing"
)

func TestUnfinishedSessionCannotBeReopened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	b, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.BeginSession(); err != nil {
		t.Fatal(err)
	}
	b.Observe("thread", 20)
	b.Close() // Simulate loss of the owner without EndSession.
	_, err = Open(path, 100)
	requireErrorContains(t, err, "unfinished supervised session")
	saved, err := readState(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := snapshot("test", saved)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "supervised_unfinished" || out.ReportedTokens != 20 {
		t.Fatal(out)
	}
}

func TestSettledSessionPreservesUsageAndCanBeReopened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	b, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.BeginSession(); err != nil {
		t.Fatal(err)
	}
	b.Observe("thread", 20)
	if err := b.EndSession(true); err != nil {
		t.Fatal(err)
	}
	b.Close()
	b, err = Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.spent != 20 || b.state.Version != 2 {
		t.Fatal(b.state)
	}
}

func TestUnconfirmedSessionPersistsFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	b, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.BeginSession(); err != nil {
		t.Fatal(err)
	}
	requireErrorContains(t, b.EndSession(false), "unconfirmed usage")
	b.Close()
	_, err = Open(path, 100)
	if err == nil {
		t.Fatal("uncertain budget reopened")
	}
}
