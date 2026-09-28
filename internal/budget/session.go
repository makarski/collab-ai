package budget

import "errors"

// BeginSession writes an interruption marker before the native client starts.
// Version 2 makes older launchers reject the file instead of ignoring the marker.
func (b *Budget) BeginSession() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	if b.state.SessionActive {
		return errors.New("supervised session already active")
	}
	b.state.Version, b.state.SessionActive = 2, true
	if err := b.save(); err != nil {
		b.failAccounting(err)
		return err
	}
	return nil
}

// EndSession is called only after stopping the child. Close alone never clears
// the marker: a crash or disconnect during unaccounted work cannot refund usage.
func (b *Budget) EndSession(settled bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		// Exhaustion or another persisted failure already prevents relaunch.
		b.state.SessionActive = false
		return b.save()
	}
	if !settled {
		err := errors.New("supervised session ended with unconfirmed usage; reconciliation required")
		b.failAccounting(err)
		return err
	}
	b.state.SessionActive = false
	if err := b.save(); err != nil {
		b.failAccounting(err)
		return err
	}
	return nil
}
