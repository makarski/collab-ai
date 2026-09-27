package main

import "testing"

func TestRestrictedModeRequiresNamedBudgetAndStdio(t *testing.T) {
	terminalCheck(t, validateRestrictedMode(true, "task", false, "", nil))
	terminalCheck(t, validateRestrictedMode(false, "", true, "", []string{"resume"}))
	for _, test := range []struct {
		name     string
		terminal bool
		relay    string
		args     []string
	}{
		{}, {name: "task", terminal: true}, {name: "task", relay: "/relay"},
		{name: "task", args: []string{"-c", "features.hooks=true"}},
	} {
		if err := validateRestrictedMode(true, test.name, test.terminal, test.relay, test.args); err == nil {
			t.Fatal("unsupported restricted launch accepted")
		}
	}
}
