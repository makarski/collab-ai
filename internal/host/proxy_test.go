package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"collab-ai/internal/protocol"
)

type recordingWriter struct {
	sync.Mutex
	frames chan Frame
}

func (w *recordingWriter) Write(data []byte) (int, error) {
	w.Lock()
	defer w.Unlock()
	var f Frame
	if err := json.Unmarshal(data, &f); err != nil {
		return 0, err
	}
	w.frames <- f
	return len(data), nil
}
func (w *recordingWriter) Close() error { return nil }

func proxyFixture(t *testing.T) (*Proxy, chan Frame, chan Frame) {
	t.Helper()
	up := make(chan Frame, 32)
	down := make(chan Frame, 32)
	p := NewProxy(NewWire(&recordingWriter{frames: up}), NewWire(&recordingWriter{frames: down}))
	p.Tools = &ToolSession{Specs: []any{map[string]any{"type": "function", "name": "collab_acknowledge"}}}
	return p, up, down
}

func frameAt(t *testing.T, frames <-chan Frame) Frame {
	t.Helper()
	select {
	case f := <-frames:
		return f
	case <-time.After(time.Second):
		t.Fatal("frame timed out")
		return Frame{}
	}
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func startThread(t *testing.T, p *Proxy) {
	t.Helper()
	check(t, p.FromOperator(context.Background(), Frame{ID: json.RawMessage(`1`), Method: "thread/start", Params: json.RawMessage(`{"sandbox":"read-only","approvalPolicy":"on-request"}`)}))
	check(t, p.FromHost(context.Background(), Frame{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"thread":{"id":"owned-thread"}}`)}))
}

func TestProxyPreservesApprovalBoundary(t *testing.T) {
	p, up, down := proxyFixture(t)
	startThread(t, p)
	start := frameAt(t, up)
	var params map[string]any
	check(t, json.Unmarshal(start.Params, &params))
	if params["sandbox"] != "read-only" {
		t.Fatal("sandbox changed")
	}
	if params["approvalPolicy"] != "on-request" {
		t.Fatal("approval policy changed")
	}
	frameAt(t, down)
	request := Frame{ID: json.RawMessage(`"approval"`), Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"threadId":"owned-thread"}`)}
	check(t, p.FromHost(context.Background(), request))
	if frameAt(t, down).Method != request.Method {
		t.Fatal("approval was intercepted")
	}
	response := Frame{ID: request.ID, Result: json.RawMessage(`{"decision":"decline"}`)}
	check(t, p.FromOperator(context.Background(), response))
	if string(frameAt(t, up).Result) != string(response.Result) {
		t.Fatal("operator response changed")
	}
}

func TestProxyToolOutputWhileActiveAndIdle(t *testing.T) {
	for _, state := range []string{"active", "idle"} {
		t.Run(state, func(t *testing.T) { testToolOutput(t, state) })
	}
}

// These assertions cover our wire contract. Only a real host can prove that it
// queues output while active and starts work while idle; see the live report.
func testToolOutput(t *testing.T, state string) {
	p, up, down := proxyFixture(t)
	startThread(t, p)
	frameAt(t, up)
	frameAt(t, down)
	check(t, p.FromHost(context.Background(), Frame{Method: "thread/status/changed", Params: json.RawMessage(`{"status":"` + state + `"}`)}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- p.Publish(ctx, protocol.Message{Type: protocol.TypeMsg, MessageID: state, From: "claude"})
	}()
	request := frameAt(t, up)
	if request.Method != "turn/start" {
		t.Fatal("wrong host method")
	}
	var params struct {
		ThreadID   string `json:"threadId"`
		Input      []any  `json:"input"`
		ToolOutput struct {
			Name   string
			Output string
		} `json:"toolOutput"`
	}
	check(t, json.Unmarshal(request.Params, &params))
	if params.ThreadID != "owned-thread" {
		t.Fatal("wrong conversation")
	}
	if len(params.Input) != 0 {
		t.Fatal("peer message became user input")
	}
	if params.ToolOutput.Name != "collab_receive" {
		t.Fatal("tool provenance missing")
	}
	var message protocol.Message
	check(t, json.Unmarshal([]byte(params.ToolOutput.Output), &message))
	if message.MessageID != state {
		t.Fatal("correlation lost")
	}
	check(t, p.FromHost(ctx, Frame{ID: request.ID, Result: json.RawMessage(`{"turn":{"id":"host-turn"}}`)}))
	check(t, <-done)
}

func TestProxyCancellationAndForeignTools(t *testing.T) {
	p, up, _ := proxyFixture(t)
	if !errors.Is(p.Publish(context.Background(), protocol.Message{}), errNoThread) {
		t.Fatal("unattached publish succeeded")
	}
	p.threadID = "owned-thread"
	foreign := Frame{Method: "item/tool/call", Params: json.RawMessage(`{"threadId":"other-thread","tool":"collab_acknowledge"}`)}
	if p.ownsToolCall(foreign) {
		t.Fatal("foreign conversation can acknowledge")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Publish(ctx, protocol.Message{}) }()
	frameAt(t, up)
	cancel()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("submission ignored cancellation")
	}
	if len(p.Calls.pending) != 0 {
		t.Fatal("pending call leaked")
	}
}

func TestReadFramesRejectsMalformedAndOversizedInput(t *testing.T) {
	for _, input := range [][]byte{[]byte("{broken}\n"), bytes.Repeat([]byte("x"), (8<<20)+1)} {
		err := ReadFrames(bytes.NewReader(input), func(Frame) error { t.Fatal("invalid frame accepted"); return nil })
		if err == nil {
			t.Fatal("invalid stream accepted")
		}
	}
	if !errors.Is(ReadFrames(bytes.NewReader(nil), func(Frame) error { return nil }), io.EOF) {
		t.Fatal("EOF not propagated")
	}
}

func TestLateInternalResponseStaysPrivate(t *testing.T) {
	p, upstream, operator := proxyFixture(t)
	p.threadID = "owned-thread"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Publish(ctx, protocol.Message{}) }()
	request := frameAt(t, upstream)
	cancel()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("expected canceled request")
	}
	late := Frame{ID: request.ID, Result: json.RawMessage(`{}`)}
	check(t, p.FromHost(context.Background(), late))
	select {
	case frame := <-operator:
		t.Fatalf("internal response leaked: %+v", frame)
	default:
	}
	late.ID = json.RawMessage(`"operator-request"`)
	check(t, p.FromHost(context.Background(), late))
	if string(frameAt(t, operator).ID) != string(late.ID) {
		t.Fatal("operator reply lost")
	}
}

func TestOperatorCannotReserveInternalRequestID(t *testing.T) {
	p, upstream, operator := proxyFixture(t)
	request := Frame{ID: json.RawMessage(`"collab-operator"`), Method: "thread/start", Params: json.RawMessage(`{}`)}
	check(t, p.FromOperator(context.Background(), request))
	if len(frameAt(t, operator).Error) == 0 {
		t.Fatal("reserved request ID accepted")
	}
	select {
	case <-upstream:
		t.Fatal("reserved request forwarded")
	default:
	}
	// Operator responses to host requests are not subject to this restriction.
	request.Method = ""
	request.Result = json.RawMessage(`{"decision":"decline"}`)
	check(t, p.FromOperator(context.Background(), request))
	if string(frameAt(t, upstream).ID) != string(request.ID) {
		t.Fatal("operator approval response lost")
	}
}
