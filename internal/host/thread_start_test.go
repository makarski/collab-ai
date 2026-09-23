package host

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMalformedThreadStartResponseIsVisibleAndAllowsRetry(t *testing.T) {
	for _, result := range []string{`{broken`, `{}`, `null`, `{"thread":{}}`, `{"thread":{"id":7}}`} {
		t.Run(result, func(t *testing.T) { assertRetryAfterBadStart(t, result) })
	}
}

func assertRetryAfterBadStart(t *testing.T, result string) {
	t.Helper()
	p, up, down := proxyFixture(t)
	request := Frame{ID: json.RawMessage(`1`), Method: "thread/start", Params: json.RawMessage(`{}`)}
	check(t, p.FromOperator(context.Background(), request))
	frameAt(t, up)
	check(t, p.FromHost(context.Background(), Frame{ID: request.ID, Result: json.RawMessage(result)}))
	response := frameAt(t, down)
	if len(response.Error) == 0 || len(response.Result) != 0 {
		t.Fatal("malformed host response did not become an operator error")
	}
	if p.ThreadID() != "" {
		t.Fatal("malformed response bound a conversation")
	}
	request.ID = json.RawMessage(`2`)
	check(t, p.FromOperator(context.Background(), request))
	frameAt(t, up)
	check(t, p.FromHost(context.Background(), Frame{ID: request.ID, Result: json.RawMessage(`{"thread":{"id":"retry-thread"}}`)}))
	frameAt(t, down)
	if p.ThreadID() != "retry-thread" {
		t.Fatal("malformed host response prevented a valid retry")
	}
}
