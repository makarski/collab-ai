package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/coder/websocket"
)

// WebSocketStream adapts one JSON object per text message to the proxy's JSONL
// stream. It is used only on a private local Unix socket for the operator UI.
type WebSocketStream struct {
	ctx     context.Context
	conn    *websocket.Conn
	pending *bytes.Reader
}

func NewWebSocketStream(ctx context.Context, conn *websocket.Conn) *WebSocketStream {
	conn.SetReadLimit(maxHostFrameBytes)
	return &WebSocketStream{ctx: ctx, conn: conn}
}

func (s *WebSocketStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if s.pending == nil || s.pending.Len() == 0 {
		if err := s.readMessage(); err != nil {
			return 0, err
		}
	}
	return s.pending.Read(p)
}

func (s *WebSocketStream) readMessage() error {
	kind, data, err := s.conn.Read(s.ctx)
	if normalSocketClose(err) {
		return io.EOF
	}
	if err != nil {
		return err
	}
	if kind != websocket.MessageText {
		return errors.New("operator WebSocket frame must contain JSON text")
	}
	// Validate and compact one JSON value before the line-based decoder.
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return err
	}
	s.pending = bytes.NewReader(append(compact.Bytes(), '\n'))
	return nil
}

func (s *WebSocketStream) Write(p []byte) (int, error) {
	if err := s.conn.Write(s.ctx, websocket.MessageText, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *WebSocketStream) Close() error { return s.conn.CloseNow() }

func normalSocketClose(err error) bool {
	switch websocket.CloseStatus(err) {
	case websocket.StatusNormalClosure, websocket.StatusGoingAway, websocket.StatusNoStatusRcvd:
		return true
	default:
		return false
	}
}
