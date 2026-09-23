package host

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestReadFramesAcceptsLargeHostInventory(t *testing.T) {
	data := append([]byte(`{"id":"plugin-list","result":{"icon":"`), bytes.Repeat([]byte("x"), 10<<20)...)
	data = append(data, []byte("\"}}\n")...)
	count := 0
	err := ReadFrames(bytes.NewReader(data), func(frame Frame) error {
		count++
		if string(frame.ID) != `"plugin-list"` || len(frame.Result) < 10<<20 {
			t.Fatal("inventory truncated")
		}
		return nil
	})
	if !errors.Is(err, io.EOF) || count != 1 {
		t.Fatalf("inventory failed: count=%d err=%v", count, err)
	}
}
