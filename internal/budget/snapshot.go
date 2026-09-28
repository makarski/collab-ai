package budget

import "time"

// Snapshot describes recorded observations, not a real-time provider balance.
type Snapshot struct {
	SchemaVersion     int        `json:"schema_version"`
	Name              string     `json:"name"`
	Cap               int64      `json:"cap"`
	ReportedTokens    int64      `json:"reported_tokens"`
	RemainingTokens   int64      `json:"remaining_from_reports"`
	OvershootTokens   int64      `json:"reported_overshoot"`
	UsageReported     bool       `json:"usage_reported"`
	LastReportedAt    *time.Time `json:"last_reported_at"`
	State             string     `json:"state"`
	AccountingError   string     `json:"accounting_error,omitempty"`
	SessionUnfinished bool       `json:"session_unfinished,omitempty"`
}

func snapshot(name string, saved state) (Snapshot, error) {
	spent, err := saved.totalUsage()
	if err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{SchemaVersion: 1, Name: name, Cap: saved.Limit, ReportedTokens: spent,
		RemainingTokens: max(saved.Limit-spent, 0), OvershootTokens: max(spent-saved.Limit, 0),
		UsageReported: len(saved.Threads) > 0, LastReportedAt: saved.ReportedAt,
		State: "ready", AccountingError: saved.Stopped}
	if saved.SessionActive {
		out.SessionUnfinished = true
		out.State = "supervised_unfinished"
	}
	if spent >= saved.Limit {
		out.State = "exhausted"
	}
	if saved.Stopped != "" {
		out.State = "stopped_unknown"
	}
	return out, nil
}
