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
	if p.Budget == nil {
		return errors.New("restricted operator requires a budget")
	}
	if len(frame.Result) != 0 || len(frame.Error) != 0 {
		return errors.New("restricted operator cannot send server responses")
	}
	if len(frame.Params) > 64<<10 || !restrictedRequestID(frame) {
		return errors.New("invalid restricted request envelope")
	}
	params, err := p.restrictedParams(*frame)
	if err != nil {
		return fmt.Errorf("restricted operator: %w", err)
	}
	frame.Params, err = json.Marshal(params)
	return err
}

func restrictedRequestID(frame *Frame) bool {
	if frame.Method == "initialized" {
		return len(frame.ID) == 0
	}
	if len(frame.ID) == 0 || len(frame.ID) > 256 || internalID(frame.ID) {
		return false
	}
	var name string
	if json.Unmarshal(frame.ID, &name) == nil {
		return name != ""
	}
	var number int64
	return string(frame.ID) != "null" && json.Unmarshal(frame.ID, &number) == nil
}

func (p *Proxy) restrictedParams(frame Frame) (any, error) {
	switch frame.Method {
	case "initialize":
		return restrictedInitialize(frame.Params)
	case "initialized":
		if len(frame.Params) == 0 {
			return struct{}{}, nil
		}
		return restrictedDecode[struct{}](frame.Params)
	case "thread/start":
		if _, err := restrictedDecode[struct{}](frame.Params); err != nil {
			return nil, err
		}
		return map[string]any{"approvalPolicy": "never"}, nil
	case "turn/start", "turn/steer":
		return p.restrictedTurn(frame)
	case "turn/interrupt":
		params, err := restrictedDecode[restrictedInterrupt](frame.Params)
		if err != nil {
			return nil, err
		}
		if params.TurnID == "" {
			return nil, errors.New("turnId is required")
		}
		return params, p.requireOwnedThread(params.ThreadID)
	default:
		return nil, errors.New("method is not allowed")
	}
}

func restrictedDecode[T any](data json.RawMessage) (T, error) {
	var value T
	if len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
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
	if len(params.Input) == 0 || len(params.Input) > 16 {
		return nil, errors.New("input must contain 1 to 16 text items")
	}
	for _, input := range params.Input {
		if input.Type != "text" || input.Text == nil {
			return nil, errors.New("only literal text input is allowed")
		}
	}
	return params, p.requireOwnedThread(params.ThreadID)
}

func (p *Proxy) requireOwnedThread(id string) error {
	if id == "" || id != p.ThreadID() {
		return errors.New("request must target the managed thread")
	}
	return nil
}
