package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Restricted ingress is a protocol boundary, not a complete sandbox. The
// administrator still owns the native client's config, environment and process.
// Unknown methods/fields are rejected rather than forwarded to newer servers.
func (p *Proxy) restrictOperator(frame *Frame) error {
	if !p.RestrictedOperator {
		return nil
	}
	if p.Budget == nil {
		return errors.New("restricted operator requires a budget")
	}
	if err := restrictedEnvelope(*frame); err != nil {
		return err
	}
	params, err := p.restrictedParams(*frame)
	if err != nil {
		return fmt.Errorf("restricted operator: %w", err)
	}
	frame.Params, err = json.Marshal(params)
	return err
}

func restrictedEnvelope(frame Frame) error {
	if len(frame.Result) != 0 || len(frame.Error) != 0 {
		return errors.New("restricted operator cannot send server responses")
	}
	if len(frame.Params) > 64<<10 {
		return errors.New("restricted parameters exceed 64 KiB")
	}
	if !restrictedRequestID(&frame) {
		return errors.New("invalid restricted request identity")
	}
	return nil
}

func restrictedRequestID(frame *Frame) bool {
	if frame.Method == "initialized" {
		return len(frame.ID) == 0
	}
	if len(frame.ID) == 0 || len(frame.ID) > 256 {
		return false
	}
	return restrictedIDValue(frame.ID)
}

func restrictedIDValue(id json.RawMessage) bool {
	if internalID(id) {
		return false
	}
	var name string
	if json.Unmarshal(id, &name) == nil {
		return name != ""
	}
	var number int64
	return string(id) != "null" && json.Unmarshal(id, &number) == nil
}

func (p *Proxy) restrictedParams(frame Frame) (any, error) {
	switch frame.Method {
	case "initialize":
		return restrictedInitialize(frame.Params)
	case "initialized":
		return restrictedInitialized(frame.Params)
	case "thread/start":
		return restrictedThreadStart(frame.Params)
	case "turn/start", "turn/steer":
		return p.restrictedTurn(frame)
	case "turn/interrupt":
		return p.restrictedInterrupt(frame.Params)
	default:
		return nil, errors.New("method is not allowed")
	}
}

func restrictedInitialized(data json.RawMessage) (any, error) {
	if len(data) == 0 {
		return struct{}{}, nil
	}
	return restrictedDecode[struct{}](data)
}

func restrictedThreadStart(data json.RawMessage) (any, error) {
	if _, err := restrictedDecode[struct{}](data); err != nil {
		return nil, err
	}
	return map[string]any{"approvalPolicy": "never"}, nil
}

func restrictedDecode[T any](data json.RawMessage) (T, error) {
	var value T
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return value, errors.New("params must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, errors.New("unsupported or malformed parameters")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return value, errors.New("trailing parameters")
	}
	return value, nil
}

func restrictedInitialize(data json.RawMessage) (any, error) {
	type clientInfo struct {
		Name    string `json:"name"`
		Title   string `json:"title,omitempty"`
		Version string `json:"version"`
	}
	type capabilities struct {
		ExperimentalAPI bool `json:"experimentalApi"`
	}
	params, err := restrictedDecode[struct {
		ClientInfo   clientInfo   `json:"clientInfo"`
		Capabilities capabilities `json:"capabilities"`
	}](data)
	if err != nil {
		return nil, err
	}
	// Do not let a frontend opt out of usage notifications. Experimental API
	// is needed for the proxy's own collaboration tool output.
	params.ClientInfo = clientInfo{Name: "collab_restricted", Version: "1"}
	params.Capabilities.ExperimentalAPI = true
	return params, nil
}

type restrictedText struct {
	Type string  `json:"type"`
	Text *string `json:"text"`
}

type restrictedTurn struct {
	ThreadID       string           `json:"threadId"`
	Input          []restrictedText `json:"input"`
	ExpectedTurnID string           `json:"expectedTurnId,omitempty"`
}

type restrictedInterrupt struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

func (p *Proxy) restrictedTurn(frame Frame) (any, error) {
	params, err := restrictedDecode[restrictedTurn](frame.Params)
	if err != nil {
		return nil, err
	}
	if (frame.Method == "turn/steer") != (params.ExpectedTurnID != "") {
		return nil, errors.New("expectedTurnId is required only for steering")
	}
	if err := restrictedInput(params.Input); err != nil {
		return nil, err
	}
	return params, p.requireOwnedThread(params.ThreadID)
}

func restrictedInput(items []restrictedText) error {
	if len(items) == 0 || len(items) > 16 {
		return errors.New("input must contain 1 to 16 text items")
	}
	for _, input := range items {
		if input.Type != "text" || input.Text == nil {
			return errors.New("only literal text input is allowed")
		}
	}
	return nil
}

func (p *Proxy) restrictedInterrupt(data json.RawMessage) (any, error) {
	params, err := restrictedDecode[restrictedInterrupt](data)
	if err != nil {
		return nil, err
	}
	if params.TurnID == "" {
		return nil, errors.New("turnId is required")
	}
	return params, p.requireOwnedThread(params.ThreadID)
}

func (p *Proxy) requireOwnedThread(id string) error {
	if id == "" || id != p.ThreadID() {
		return errors.New("request must target the managed thread")
	}
	return nil
}

func (p *Proxy) restrictedHostRequest(frame Frame) error {
	if !p.RestrictedOperator {
		return nil
	}
	if frame.Method == "" || len(frame.ID) == 0 {
		return nil
	}
	err := errors.New("restricted operator does not support host approval or input requests; stopping session")
	if p.Budget != nil {
		p.Budget.Fail(err)
	}
	return err
}
