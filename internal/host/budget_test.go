package host

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"collab-ai/internal/budget"
	"collab-ai/internal/protocol"
)

func budgetFixture(t *testing.T) (*Proxy, chan Frame, chan Frame) {
	t.Helper()
	p, up, down := proxyFixture(t)
	b, err := budget.Open(filepath.Join(t.TempDir(), "budget.json"), 100)
	check(t, err)
	t.Cleanup(func() { b.Close() })
	p.Budget = b
	startThread(t, p)
	frameAt(t, up)
	frameAt(t, down)
	return p, up, down
}

func reportUsage(t *testing.T, p *Proxy, total int) {
	t.Helper()
	check(t, p.FromHost(context.Background(), Frame{Method: "thread/tokenUsage/updated", Params: json.RawMessage(fmt.Sprintf(
		`{"threadId":"owned-thread","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":%d,"inputTokens":90,"cachedInputTokens":80,"outputTokens":10,"reasoningOutputTokens":5}}}`, total))}))
}

func TestBudgetBlocksOperatorAndPeerWorkAfterReportedLimit(t *testing.T) {
	p, up, down := budgetFixture(t)
	reportUsage(t, p, 99)
	frameAt(t, down)
	if p.Budget.Err() != nil {
		t.Fatal("double-counted token breakdown", p.Budget.Err())
	}
	reportUsage(t, p, 107)
	if frameAt(t, down).Method != "thread/tokenUsage/updated" {
		t.Fatal("usage hidden from operator")
	}
	for _, method := range []string{"turn/start", "turn/steer", "review/start", "thread/goal/set", "thread/fork"} {
		check(t, p.FromOperator(context.Background(), Frame{ID: json.RawMessage(`2`), Method: method, Params: json.RawMessage(`{}`)}))
		if !strings.Contains(string(frameAt(t, down).Error), "7 overshoot") {
			t.Fatal("missing cap rejection")
		}
	}
	if err := p.Publish(context.Background(), protocol.Message{}); err == nil {
		t.Fatal("peer work admitted")
	}
	select {
	case f := <-up:
		t.Fatal("work reached host", f)
	default:
	}
	// Approval responses can still reach the host; the supervisor never approves them.
	check(t, p.FromOperator(context.Background(), Frame{ID: json.RawMessage(`"approval"`), Result: json.RawMessage(`{"decision":"decline"}`)}))
	if string(frameAt(t, up).Result) != `{"decision":"decline"}` {
		t.Fatal("approval changed")
	}
}

func TestBudgetInterruptIsBoundedWithOrWithoutHostReply(t *testing.T) {
	for _, reply := range []bool{false, true} {
		t.Run(fmt.Sprint(reply), func(t *testing.T) {
			p, up, _ := budgetFixture(t)
			reportUsage(t, p, 100)
			done := make(chan error, 1)
			go func() { done <- p.stopForBudget(context.Background(), 100*time.Millisecond) }()
			request := frameAt(t, up)
			if request.Method != "turn/interrupt" || !strings.Contains(string(request.Params), `"turnId":"turn-1"`) {
				t.Fatal(request)
			}
			if reply {
				check(t, p.FromHost(context.Background(), Frame{ID: request.ID, Result: json.RawMessage(`{}`)}))
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("missing shutdown reason")
				}
			case <-time.After(time.Second):
				t.Fatal("interrupt blocked supervisor")
			}
		})
	}
}

func TestMissingOrMalformedUsageStopsBudgetedProxy(t *testing.T) {
	for _, params := range []string{`{}`, `{"threadId":"a","turnId":"t","tokenUsage":{"total":{}}}`, `{"threadId":"a","turnId":"t","tokenUsage":{"total":{"totalTokens":-1}}}`, `{"threadId":"a","turnId":"t","tokenUsage":{"total":{"totalTokens":"100"}}}`} {
		t.Run(params, func(t *testing.T) {
			p, _, _ := budgetFixture(t)
			check(t, p.FromHost(context.Background(), Frame{Method: "thread/tokenUsage/updated", Params: json.RawMessage(params)}))
			if p.Budget.Err() == nil {
				t.Fatal("missing/invalid accounting treated as zero")
			}
		})
	}
}
