package host

import (
	"encoding/json"
	"errors"
	"sync"
)

type observedTurn struct{ completed, reported bool }

// Session accounting is conservative: rejected/ambiguous admissions and any
// turn without both completion and usage require reconciliation at shutdown.
type sessionAccounting struct {
	mu      sync.Mutex
	pending map[string]bool
	turns   map[string]observedTurn
	unknown bool
}

func (s *sessionAccounting) begin(id, steeredTurn string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = make(map[string]bool)
	}
	if s.turns == nil {
		s.turns = make(map[string]observedTurn)
	}
	s.pruneSettled()
	if steeredTurn != "" {
		s.turns[steeredTurn] = observedTurn{}
	}
	if s.pending[id] {
		s.unknown = true
	}
	s.pending[id] = true
}

// Caller holds mu. Keep incomplete observations until the session closes.
func (s *sessionAccounting) pruneSettled() {
	for id, turn := range s.turns {
		if turn.completed && turn.reported {
			delete(s.turns, id)
		}
	}
}

func (s *sessionAccounting) reply(id string, result json.RawMessage, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pending[id] {
		return
	}
	delete(s.pending, id)
	turn, err := replyTurnID(result)
	if failed || err != nil {
		s.unknown = true
		return
	}
	if _, exists := s.turns[turn]; !exists {
		s.turns[turn] = observedTurn{}
	}
}

func replyTurnID(result json.RawMessage) (string, error) {
	var out struct {
		TurnID string `json:"turnId"`
		Turn   struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		return "", err
	}
	if out.TurnID != "" {
		return out.TurnID, nil
	}
	if out.Turn.ID == "" {
		return "", errors.New("missing turn ID")
	}
	return out.Turn.ID, nil
}

func (s *sessionAccounting) event(frame Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turns == nil {
		s.turns = make(map[string]observedTurn)
	}
	switch frame.Method {
	case "thread/tokenUsage/updated":
		report, err := parseTokenUsage(frame.Params)
		if err != nil {
			s.unknown = true
			return
		}
		turn := s.turns[report.TurnID]
		turn.reported = true
		s.turns[report.TurnID] = turn
	case "turn/completed":
		s.complete(frame.Params)
	case "turn/started":
		id, err := replyTurnID(frame.Params)
		if err != nil {
			s.unknown = true
			return
		}
		s.turns[id] = observedTurn{}
	}
}

// Caller holds mu.
func (s *sessionAccounting) complete(data json.RawMessage) {
	var out struct {
		Turn struct{ ID, Status string } `json:"turn"`
	}
	if json.Unmarshal(data, &out) != nil || out.Turn.ID == "" {
		s.unknown = true
		return
	}
	turn := s.turns[out.Turn.ID]
	turn.completed = out.Turn.Status == "completed"
	s.turns[out.Turn.ID] = turn
}

func (s *sessionAccounting) settled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unknown || len(s.pending) != 0 {
		return false
	}
	for _, turn := range s.turns {
		if !turn.completed || !turn.reported {
			return false
		}
	}
	return true
}

func (p *Proxy) trackSubmission(frame Frame) {
	if !p.RestrictedOperator {
		return
	}
	if frame.Method == "turn/start" || frame.Method == "turn/steer" {
		var params struct {
			ExpectedTurnID string `json:"expectedTurnId"`
		}
		_ = json.Unmarshal(frame.Params, &params) // Already validated by restricted ingress.
		p.session.begin(string(frame.ID), params.ExpectedTurnID)
	}
}

func (p *Proxy) observeSession(frame Frame) {
	if !p.RestrictedOperator {
		return
	}
	if frame.Method == "" {
		p.session.reply(string(frame.ID), frame.Result, len(frame.Error) != 0)
		return
	}
	p.session.event(frame)
}

func (p *Proxy) SessionSettled() bool { return p.session.settled() }
