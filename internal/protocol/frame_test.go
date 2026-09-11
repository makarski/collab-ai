package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// Regression: reading frames must not buffer-and-drop subsequent frames
// (the original json.Decoder-per-frame bug).
func TestReadFrameSequential(t *testing.T) {
	input := `{"type":"hello","agent_id":"a"}` + "\n" +
		`{"type":"msg","to":"*"}` + "\n" +
		`{"type":"msg","to":"b"}` + "\n"
	r := bufio.NewReader(strings.NewReader(input))

	var m1, m2, m3 Message
	if err := ReadFrame(r, &m1); err != nil {
		t.Fatalf("frame 1: %v", err)
	}
	if err := ReadFrame(r, &m2); err != nil {
		t.Fatalf("frame 2: %v", err)
	}
	if err := ReadFrame(r, &m3); err != nil {
		t.Fatalf("frame 3: %v", err)
	}

	if m1.Type != TypeHello || m2.To != "*" || m3.To != "b" {
		t.Fatalf("decoded wrongly: %+v %+v %+v", m1, m2, m3)
	}
}

func TestReadFrameTooLarge(t *testing.T) {
	big := strings.Repeat("x", MaxFrameBytes) + "\n"
	r := bufio.NewReader(strings.NewReader(big))
	var msg Message
	err := ReadFrame(r, &msg)
	if err == nil {
		t.Fatal("expected error for oversized frame")
	}
}

// A newline-free stream must be rejected without reading it to EOF.
func TestReadFrameBoundsUnterminatedInput(t *testing.T) {
	r := &countingReader{}
	var msg Message
	if err := ReadFrame(bufio.NewReader(r), &msg); err == nil {
		t.Fatal("accepted oversized stream")
	}
	if r.n > MaxFrameBytes+4096 {
		t.Fatalf("read %d bytes before rejecting", r.n)
	}
}

type countingReader struct{ n int }

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	r.n += len(p)
	if r.n > 2*MaxFrameBytes {
		return 0, io.ErrUnexpectedEOF
	}
	return len(p), nil
}

func TestReadFrameAtLimit(t *testing.T) {
	prefix := `{"type":"msg","payload":"`
	suffix := `"}` + "\n"
	frame := prefix + strings.Repeat("x", MaxFrameBytes-len(prefix)-len(suffix)) + suffix
	var msg Message
	if err := ReadFrame(bufio.NewReaderSize(strings.NewReader(frame), 17), &msg); err != nil {
		t.Fatal(err)
	}
}

func TestWriteOversizedFrameWritesNothing(t *testing.T) {
	var out bytes.Buffer
	payload, _ := json.Marshal(strings.Repeat("x", MaxFrameBytes))
	if err := WriteFrame(&out, Message{Type: TypeMsg, Payload: payload}); err == nil {
		t.Fatal("accepted oversized frame")
	}
	if out.Len() != 0 {
		t.Fatal("wrote partial oversized frame")
	}
}
