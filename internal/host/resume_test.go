package host

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestRuntimeToolsPreserveLifecycleOptions(t *testing.T) {
	for _, method := range []string{"thread/start", "thread/resume", "thread/fork"} {
		t.Run(method, func(t *testing.T) { checkRuntimeLifecycle(t, method) })
	}
}

func checkRuntimeLifecycle(t *testing.T, method string) {
	t.Helper()
	p, up, down := proxyFixture(t)
	p.RuntimeMCP = map[string]any{"command": "/launcher", "required": true}
	params := map[string]any{"threadId": "saved-thread", "cwd": "/project", "sandbox": "read-only",
		"approvalPolicy": "on-request", "model": "selected-model", "excludeTurns": true,
		"config": map[string]any{"model_reasoning_effort": "high"}}
	data, err := json.Marshal(params)
	check(t, err)
	check(t, p.FromOperator(context.Background(), Frame{ID: json.RawMessage(`1`), Method: method, Params: data}))
	forwarded := frameAt(t, up)
	var got map[string]any
	check(t, json.Unmarshal(forwarded.Params, &got))
	assertCallerParamsPreserved(t, got, params)
	config := got["config"].(map[string]any)
	if !reflect.DeepEqual(config["mcp_servers.collab_runtime"], p.RuntimeMCP) || config["model_reasoning_effort"] != "high" {
		t.Fatalf("runtime configuration lost overrides: %v", config)
	}
	if method != "thread/start" && got["developerInstructions"] != nil {
		t.Fatal("resume/fork replaced saved developer instructions")
	}
	if got["dynamicTools"] != nil {
		t.Fatal("runtime MCP must not add dynamic tools unsupported on resume")
	}
	check(t, p.FromHost(context.Background(), Frame{ID: forwarded.ID, Result: json.RawMessage(`{"thread":{"id":"selected-thread"}}`)}))
	frameAt(t, down)
	if p.ThreadID() != "selected-thread" {
		t.Fatal("did not bind the returned thread ID")
	}
}

func assertCallerParamsPreserved(t *testing.T, got, original map[string]any) {
	t.Helper()
	for key, want := range original {
		if key != "config" && !reflect.DeepEqual(got[key], want) {
			t.Fatalf("%s changed: got %v, want %v", key, got[key], want)
		}
	}
}

func TestFailedResumeAllowsRetryWithoutChangingIdentity(t *testing.T) {
	p, up, down := proxyFixture(t)
	p.RuntimeMCP = map[string]any{"command": "/launcher"}
	request := Frame{ID: json.RawMessage(`1`), Method: "thread/resume", Params: json.RawMessage(`{"threadId":"saved"}`)}
	check(t, p.FromOperator(context.Background(), request))
	frameAt(t, up)
	check(t, p.FromHost(context.Background(), Frame{ID: request.ID, Error: json.RawMessage(`{"code":-1,"message":"missing thread"}`)}))
	frameAt(t, down)
	if p.ThreadID() != "" {
		t.Fatal("failed resume bound an inbox destination")
	}
	request.ID = json.RawMessage(`2`)
	check(t, p.FromOperator(context.Background(), request))
	frameAt(t, up)
	check(t, p.FromHost(context.Background(), Frame{ID: request.ID, Result: json.RawMessage(`{"thread":{"id":"saved"}}`)}))
	frameAt(t, down)
	request.ID = json.RawMessage(`3`)
	request.Method = "thread/fork"
	check(t, p.FromOperator(context.Background(), request))
	if len(frameAt(t, down).Error) == 0 || p.ThreadID() != "saved" {
		t.Fatal("a second lifecycle request changed the managed destination")
	}
}
