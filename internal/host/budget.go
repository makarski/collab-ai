package host

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

func (p *Proxy) budgetError() error {
	if p.Budget == nil {
		return nil
	}
	return p.Budget.Err()
}

func (p *Proxy) operatorBudgetError(frame Frame) error {
	// Allow responses (including declined approvals) during shutdown.
	if frame.Method == "" {
		return nil
	}
	return p.budgetError()
}

type tokenUsageReport struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Usage    struct {
		Total struct {
			Tokens *int64 `json:"totalTokens"`
		} `json:"total"`
	} `json:"tokenUsage"`
}

func (r tokenUsageReport) validate() error {
	if r.ThreadID == "" || r.TurnID == "" {
		return errors.New("missing thread or turn identity")
	}
	if r.Usage.Total.Tokens == nil {
		return errors.New("missing cumulative totalTokens")
	}
	return nil
}

func parseTokenUsage(data json.RawMessage) (tokenUsageReport, error) {
	var report tokenUsageReport
	if err := json.Unmarshal(data, &report); err != nil {
		return report, err
	}
	return report, report.validate()
}

func (p *Proxy) observeBudget(frame Frame) {
	if p.Budget == nil || frame.Method != "thread/tokenUsage/updated" {
		return
	}
	args, err := parseTokenUsage(frame.Params)
	if err != nil {
		p.Budget.Fail(errors.New("invalid token usage notification; stopping because accounting is unknown"))
		return
	}
	// Capture the turn before closing Budget.Done, so the supervisor can request
	// interruption without waiting for another host frame.
	p.mu.Lock()
	p.budgetThread, p.budgetTurn = args.ThreadID, args.TurnID
	p.mu.Unlock()
	p.Budget.Observe(args.ThreadID, *args.Usage.Total.Tokens)
}

// ServeBudget runs separately from the host reader: Calls.Call needs that
// reader to resolve the interrupt reply. Even a successful reply only confirms
// acceptance; the supervisor ends the process after a bounded grace period.
func (p *Proxy) ServeBudget(ctx context.Context) error {
	if p.Budget == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.Budget.Done():
	}
	return p.stopForBudget(ctx, 2*time.Second)
}

func (p *Proxy) stopForBudget(ctx context.Context, grace time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, grace)
	defer cancel()
	p.mu.Lock()
	thread, turn := p.budgetThread, p.budgetTurn
	p.mu.Unlock()
	if thread != "" && turn != "" {
		// A failed or unanswered interrupt still leads to process termination.
		_, _ = p.Calls.Call(ctx, "turn/interrupt", map[string]any{"threadId": thread, "turnId": turn})
	}
	<-ctx.Done()
	return p.Budget.Err()
}
