package main

import (
	"fmt"
	"io"
	"sync"
)

const terminalDiagnosticLimit = 64 * 1024

// Retain a bounded tail rather than letting long sessions grow memory or letting
// App Server stderr overwrite the active terminal UI.
type terminalDiagnostics struct {
	mu        sync.Mutex
	tail      []byte
	truncated bool
}

func (d *terminalDiagnostics) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(p)
	if n > terminalDiagnosticLimit {
		p = p[n-terminalDiagnosticLimit:]
		d.truncated = true
	}
	if excess := len(d.tail) + len(p) - terminalDiagnosticLimit; excess > 0 {
		d.tail = d.tail[excess:]
		d.truncated = true
	}
	d.tail = append(d.tail, p...)
	return n, nil
}

func (d *terminalDiagnostics) report(out io.Writer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.tail) == 0 {
		return
	}
	fmt.Fprintln(out, "\nCodex App Server diagnostics:")
	if d.truncated {
		fmt.Fprintln(out, "[earlier output omitted; showing the last 64 KiB]")
	}
	out.Write(d.tail)
	if d.tail[len(d.tail)-1] != '\n' {
		fmt.Fprintln(out)
	}
}
