package main

import "testing"

func TestRestrictedModeRequiresNamedBudgetAndStdio(t *testing.T) {
	terminalCheck(t, (restrictedMode{enabled: true, budgetName: "task"}).validate(false, "", nil))
	terminalCheck(t, (restrictedMode{}).validate(true, "", []string{"resume"}))
	for _, test := range []struct {
		name     string
		terminal bool
		relay    string
		args     []string
	}{
		{}, {name: "task", terminal: true}, {name: "task", relay: "/relay"},
		{name: "task", args: []string{"-c", "features.hooks=true"}},
	} {
		mode := restrictedMode{enabled: true, budgetName: test.name}
		if err := mode.validate(test.terminal, test.relay, test.args); err == nil {
			t.Fatal("unsupported restricted launch accepted")
		}
	}
}
