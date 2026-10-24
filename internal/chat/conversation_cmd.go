package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// chatCommand: /chat all, /chat <id> for the mention grammar, /chat "id" (a
// JSON string) for any other ID, including an agent literally named all.
func (m *Model) chatCommand(arg argument) tea.Cmd {
	key, err := chatTarget(string(arg))
	if err != nil {
		m.status = err.Error()
		return nil
	}
	m.refreshDirectory()
	m.selectView(key)
	return nil
}

func chatTarget(arg string) (viewKey, error) {
	switch {
	case arg == ConversationAll:
		return allKey, nil
	case strings.HasPrefix(arg, `"`):
		var id string
		if err := json.Unmarshal([]byte(arg), &id); err != nil {
			return allKey, fmt.Errorf("/chat takes all, an agent mention, or a JSON string: %w", err)
		}
		return viewKey(id), protocol.ValidateAgentID(id)
	case mentionID.MatchString(arg):
		return viewKey(arg), nil
	}
	return allKey, fmt.Errorf("/chat takes all, an agent mention, or a JSON string")
}

func chatArg(a argument) error {
	if a.empty() {
		return fmt.Errorf("takes all, an agent mention, or a JSON string")
	}
	_, err := chatTarget(string(a))
	return err
}
