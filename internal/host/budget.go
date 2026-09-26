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

func (p *Proxy) observeBudget(frame Frame) {
	if p.Budget == nil || frame.Method != "thread/tokenUsage/updated" {
		return
	}
	var args struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Usage    struct {
			Total struct {
				Tokens *int64 `json:"totalTokens"`
			} `json:"total"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(frame.Params, &args) != nil || args.ThreadID == "" || args.TurnID == "" || args.Usage.Total.Tokens == nil {
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
