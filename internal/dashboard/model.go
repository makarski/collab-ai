// Package dashboard presents read-only status snapshots; it never owns an inbox.
package dashboard

import (
	"context"
	"io"
	"time"

	tea "charm.land/bubbletea/v2"
	"collab-ai/internal/protocol"
	"collab-ai/internal/status"
)

type Config struct {
	Socket   string
	Interval time.Duration
	Timeout  time.Duration
	NoColor  bool
}

type fetchFunc func(context.Context, string) (protocol.StatusSnapshot, error)
type refreshMsg uint64
type resultMsg struct {
	id       uint64
	snapshot protocol.StatusSnapshot
	err      error
}

type model struct {
	ctx                    context.Context
	cancel                 context.CancelFunc
	cfg                    Config
	fetch                  fetchFunc
	request                uint64
	inFlight, quitting     bool
	latest                 protocol.StatusSnapshot
	snapshot               protocol.StatusSnapshot
	haveSnapshot, retained bool
	width, height          int
	tab                    int
	filter                 string
	editing                bool
	state                  int
	cursor                 int
	selected               identity
	detail, help           bool
	scroll                 int
	styles                 palette
}

// Run restores the terminal on exit. The same context bounds all status I/O.
func Run(ctx context.Context, cfg Config, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(ctx, cancel, cfg, status.Fetch)
	_, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output)).Run()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func newModel(ctx context.Context, cancel context.CancelFunc, cfg Config, fetch fetchFunc) *model {
	return &model{ctx: ctx, cancel: cancel, cfg: cfg, fetch: fetch, width: 80, height: 24,
		latest: protocol.UnavailableStatus(), styles: newPalette(!cfg.NoColor)}
}

func (m *model) Init() tea.Cmd { return m.refresh() }

func (m *model) refresh() tea.Cmd {
	if m.inFlight || m.quitting || m.ctx.Err() != nil {
		return nil
	}
	m.inFlight = true
	m.request++
	id, fetch, cfg, parent := m.request, m.fetch, m.cfg, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, cfg.Timeout)
		defer cancel()
		snapshot, err := fetch(ctx, cfg.Socket)
		return resultMsg{id: id, snapshot: snapshot, err: err}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.quitting {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case resultMsg:
		return m, m.receive(msg)
	case refreshMsg:
		if uint64(msg) == m.request {
			return m, m.refresh()
		}
	}
	return m, nil
}

func (m *model) receive(msg resultMsg) tea.Cmd {
	if !m.inFlight || msg.id != m.request || m.ctx.Err() != nil {
		return nil
	}
	m.inFlight = false
	m.latest = msg.snapshot
	if msg.err != nil {
		m.latest.Error = msg.err.Error()
	}
	valid := msg.snapshot.SnapshotAt != nil && (msg.snapshot.Health == "ready" || msg.snapshot.Health == "degraded")
	m.retained = !valid
	if valid {
		m.snapshot, m.haveSnapshot = msg.snapshot, true
		m.syncSelection()
	}
	id, ctx, delay := m.request, m.ctx, m.cfg.Interval
	return func() tea.Msg {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			return refreshMsg(id)
		}
	}
}

func (m *model) stale(now time.Time) bool {
	if !m.haveSnapshot {
		return false
	}
	return m.retained || now.Sub(*m.snapshot.SnapshotAt) > m.cfg.Interval+m.cfg.Timeout
}
