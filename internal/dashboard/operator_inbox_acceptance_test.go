package dashboard

import (
	"encoding/json"
	"regexp"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestOperatorInboxDashboardPendingAndUnknown(t *testing.T) {
	for _, tc := range []struct{ name, value, want string }{{"known", "7", "7"}, {"legacy", "null", "unknown"}} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := ready()
			if err := json.Unmarshal([]byte(`{"operator_pending":`+tc.value+`}`), &snapshot); err != nil {
				t.Fatal(err)
			}
			m := fixture(t, nil)
			m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
			apply(m, snapshot, nil)
			pattern := regexp.MustCompile(`(?i)operator[^\n]*\b` + tc.want + `\b`)
			if !pattern.MatchString(m.View().Content) {
				t.Fatalf("operator pending %s not displayed:\n%s", tc.want, m.View().Content)
			}
		})
	}
}
