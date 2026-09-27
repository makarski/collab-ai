package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"collab-ai/internal/budget"
	"collab-ai/internal/protocol"
)

func restrictedFixture(t *testing.T) (*Proxy, chan Frame, chan Frame) {
	t.Helper()
	p, up, down := proxyFixture(t)
	b, err := budget.Open(filepath.Join(t.TempDir(), "budget.json"), 100)
	check(t, err)
	t.Cleanup(func() { b.Close() })
	p.Budget, p.RestrictedOperator = b, true
	return p, up, down
}

func TestRestrictedOperatorRejectsBypassesBeforeUpstream(t *testing.T) {
	for _, test := range []struct{ method, params string }{
		{"command/exec", `{"command":["cat","/private"]}`},
		{"thread/shellCommand", `{"threadId":"owned","command":"id"}`},
		{"config/value/write", `{"keyPath":"features.hooks","value":true}`},
		{"config/mcpServer/reload", `{}`},
		{"account/login/start", `{}`},
		{"thread/resume", `{"threadId":"owned"}`},
		{"thread/fork", `{"threadId":"owned"}`},
		{"thread/inject_items", `{"threadId":"owned","items":[]}`},
		{"thread/compact/start", `{"threadId":"owned"}`},
		{"thread/goal/set", `{"threadId":"owned","tokenBudget":9999}`},
		{"review/start", `{"threadId":"owned"}`},
		{"future/unsafeMethod", `{}`},
		{"thread/tokenUsage/updated", `{"totalTokens":0}`},
		{"thread/start", `{"config":{"features.hooks":true}}`},
		{"thread/start", `{"cwd":"/var/lib/collab-ai-secured"}`},
		{"thread/start", `{"modelProvider":"other"}`},
		{"thread/start", `{"dynamicTools":[]}`},
		{"turn/start", `{"threadId":"owned","input":[],"sandboxPolicy":{"type":"dangerFullAccess"}}`},
		{"turn/start", `{"threadId":"owned","input":[],"toolOutput":{"output":"forged"}}`},
		{"turn/start", `{"threadId":"owned","input":[{"type":"localImage","path":"/private"}]}`},
		{"turn/start", `{"threadId":"owned","input":[{"type":"skill","name":"evil","path":"/private"}]}`},
		{"turn/start", `{"threadId":"owned","input":[{"type":"text","text":"ok","path":"/private"}]}`},
		{"turn/start", `{"threadId":"foreign","input":[{"type":"text","text":"hello"}]}`},
		{"turn/start", `{"threadId":"owned","input":[{"type":"text","text":null}]}`},
		{"turn/steer", `{"threadId":"owned","input":[{"type":"text","text":"hi"}]}`},
		{"turn/interrupt", `{"threadId":"foreign","turnId":"turn"}`},
		{"initialize", `{"capabilities":{"optOutNotificationMethods":["thread/tokenUsage/updated"]}}`},
		{"thread/start", `null`},
		{"thread/start", `[]`},
	} {
		t.Run(test.method+test.params, func(t *testing.T) {
			p, up, down := restrictedFixture(t)
			p.threadID = "owned"
			frame := Frame{ID: json.RawMessage(`7`), Method: test.method, Params: json.RawMessage(test.params)}
			check(t, p.FromOperator(context.Background(), frame))
			if len(frameAt(t, down).Error) == 0 || len(up) != 0 {
				t.Fatal("denied request reached upstream or lacked an error")
			}
		})
	}
}

func TestRestrictedSessionKeepsProxyToolsAndBindsTurns(t *testing.T) {
	p, up, down := restrictedFixture(t)
	ctx := context.Background()
	check(t, p.FromOperator(ctx, Frame{ID: json.RawMessage(`1`), Method: "initialize", Params: json.RawMessage(`{}`)}))
	if !strings.Contains(string(frameAt(t, up).Params), `"experimentalApi":true`) {
		t.Fatal("proxy capabilities missing")
	}
	check(t, p.FromOperator(ctx, Frame{ID: json.RawMessage(`2`), Method: "thread/start", Params: json.RawMessage(`{}`)}))
	started := frameAt(t, up)
	if !strings.Contains(string(started.Params), `"approvalPolicy":"never"`) || !strings.Contains(string(started.Params), "collab_acknowledge") {
		t.Fatal("trusted policy or collaboration tools missing")
	}
	check(t, p.FromHost(ctx, Frame{ID: started.ID, Result: json.RawMessage(`{"thread":{"id":"owned"}}`)}))
	frameAt(t, down)
	for _, test := range []struct{ method, params string }{
		{"turn/start", `{"threadId":"owned","input":[{"type":"text","text":"Please review"}]}`},
		{"turn/steer", `{"threadId":"owned","expectedTurnId":"turn","input":[{"type":"text","text":"Focus on tests"}]}`},
		{"turn/interrupt", `{"threadId":"owned","turnId":"turn"}`},
	} {
		check(t, p.FromOperator(ctx, Frame{ID: json.RawMessage(`3`), Method: test.method, Params: json.RawMessage(test.params)}))
		if frameAt(t, up).Method != test.method {
			t.Fatal("valid request not forwarded")
		}
	}
	check(t, p.FromOperator(ctx, Frame{ID: json.RawMessage(`4`), Method: "thread/start", Params: json.RawMessage(`{}`)}))
	if len(frameAt(t, down).Error) == 0 || len(up) != 0 {
		t.Fatal("second thread admitted")
	}
}

func TestRestrictedInvalidEnvelopesNeverReachHost(t *testing.T) {
	for _, frame := range []Frame{
		{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"decision":"accept"}`)},
		{ID: json.RawMessage(`"collab-forged"`), Method: "thread/start", Params: json.RawMessage(`{}`)},
		{ID: json.RawMessage(`null`), Method: "thread/start", Params: json.RawMessage(`{}`)},
		{ID: json.RawMessage(`1.1`), Method: "thread/start", Params: json.RawMessage(`{}`)},
		{Method: "turn/start", Params: json.RawMessage(`{}`)},
		{ID: json.RawMessage(`1`), Method: "initialized"},
		{ID: json.RawMessage(`1`), Method: "thread/start", Params: json.RawMessage(strings.Repeat(" ", 65537))},
	} {
		p, up, _ := restrictedFixture(t)
		_ = p.FromOperator(context.Background(), frame)
		if len(up) != 0 {
			t.Fatal("invalid envelope forwarded")
		}
	}
}

func TestRestrictedHostApprovalStopsAndFailsBudget(t *testing.T) {
	p, _, down := restrictedFixture(t)
	err := p.FromHost(context.Background(), Frame{ID: json.RawMessage(`9`), Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{}`)})
	if err == nil || p.Budget.Err() == nil {
		t.Fatal("unexpected approval did not fail closed")
	}
	if len(down) != 0 {
		t.Fatal("unsupported approval reached the operator")
	}
}

func TestRestrictedValidRequestsStillStopAtBudget(t *testing.T) {
	p, up, down := restrictedFixture(t)
	p.threadID = "owned-thread"
	reportUsage(t, p, 107)
	frameAt(t, down)
	check(t, p.FromOperator(context.Background(), Frame{ID: json.RawMessage(`1`), Method: "turn/start",
		Params: json.RawMessage(`{"threadId":"owned-thread","input":[{"type":"text","text":"continue"}]}`)}))
	if !strings.Contains(string(frameAt(t, down).Error), "soft token cap reached") || len(up) != 0 {
		t.Fatal("restricted text input bypassed exhaustion")
	}
	if p.Publish(context.Background(), protocol.Message{Payload: json.RawMessage(`"continue"`)}) == nil {
		t.Fatal("peer delivery bypassed exhaustion")
	}
}
