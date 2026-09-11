package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxFrameBytes includes the terminating newline.
const MaxFrameBytes = 1 << 20

// ReadFrame reads one newline-terminated JSON frame.
func ReadFrame(r *bufio.Reader, msg *Message) error {
	var line []byte
	for {
		fragment, err := r.ReadSlice('\n')
		if len(line)+len(fragment) > MaxFrameBytes {
			return fmt.Errorf("frame exceeds %d bytes", MaxFrameBytes)
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			if len(line) == MaxFrameBytes {
				return fmt.Errorf("frame exceeds %d bytes", MaxFrameBytes)
			}
			continue
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(line, msg)
	}
}

func WriteFrame(w io.Writer, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > MaxFrameBytes {
		return fmt.Errorf("frame exceeds %d bytes", MaxFrameBytes)
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if f, ok := w.(*bufio.Writer); ok {
		return f.Flush()
	}
	return nil
}
