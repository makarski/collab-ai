package budget

import (
	"path/filepath"
	"testing"
)

func TestCanonicalBudgetDirectoryAcceptance(t *testing.T) {
	canonical, legacy := filepath.Join(t.TempDir(), "canonical"), filepath.Join(t.TempDir(), "legacy")
	t.Setenv("PAIRTASK_BUDGET_DIR", canonical)
	t.Setenv("COLLAB_BUDGET_DIR", legacy)
	store, err := DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if store.Directory != canonical {
		t.Fatalf("selected %q, want %q", store.Directory, canonical)
	}
}

func TestInvalidCanonicalBudgetDirectoryCannotUseValidLegacyAcceptance(t *testing.T) {
	t.Setenv("PAIRTASK_BUDGET_DIR", "relative-path")
	t.Setenv("COLLAB_BUDGET_DIR", t.TempDir())
	if _, err := DefaultStore(); err == nil {
		t.Fatal("invalid canonical directory silently used legacy/default")
	}
}

func TestEmptyCanonicalBudgetDirectoryCannotUseLegacyOrDefaultAcceptance(t *testing.T) {
	t.Setenv("PAIRTASK_BUDGET_DIR", "")
	t.Setenv("COLLAB_BUDGET_DIR", t.TempDir())
	if _, err := DefaultStore(); err == nil {
		t.Fatal("explicitly empty directory silently used legacy/default")
	}
}
