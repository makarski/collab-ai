package budget

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestExplicitBudgetDirectoryDoesNotRequireHomeAcceptance(t *testing.T) {
	for _, prefix := range []string{"PAIRTASK_", "COLLAB_"} {
		t.Run(prefix, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestBudgetWithoutHomeChild$")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "HOME=") && !strings.HasPrefix(entry, "PAIRTASK_") && !strings.HasPrefix(entry, "COLLAB_") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			directory := t.TempDir()
			cmd.Env = append(cmd.Env, prefix+"BUDGET_DIR="+directory, "PAIRTASK_BUDGET_NO_HOME_CHILD="+directory)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("explicit budget directory should work without HOME: %v\n%s", err, output)
			}
		})
	}
}

func TestBudgetWithoutHomeChild(t *testing.T) {
	want := os.Getenv("PAIRTASK_BUDGET_NO_HOME_CHILD")
	if want == "" {
		return
	}
	store, err := DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if store.Directory != want {
		t.Fatalf("got directory %q, want %q", store.Directory, want)
	}
}
