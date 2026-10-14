package chat

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"collab-ai/internal/protocol"
)

// ParseRecipients turns the recipient field into an operator_send scope: To
// set to "*" or one ID, or Group set to sorted, deduplicated IDs.
//
//   - "@all" or "*" addresses every eligible agent (a chat keyword).
//   - "a" or "a, b" are literal agent IDs.
//   - A JSON array such as ["@all", "a,b"] addresses IDs literally, including
//     ones that look like keywords or contain commas.
func ParseRecipients(field string) (protocol.Message, error) {
	field = strings.TrimSpace(field)
	if field == "@all" || field == protocol.Broadcast {
		return protocol.Message{To: protocol.Broadcast}, nil
	}
	ids, err := recipientIDs(field)
	if err != nil {
		return protocol.Message{}, err
	}
	return scopeFor(ids)
}

func recipientIDs(field string) ([]string, error) {
	if strings.HasPrefix(field, "[") {
		var ids []string
		if err := json.Unmarshal([]byte(field), &ids); err != nil {
			return nil, errors.New(`recipients: use a JSON array of strings, e.g. ["a,b", "@all"]`)
		}
		return ids, nil
	}
	if field == "" {
		return nil, errors.New("recipients: enter an agent ID, several separated by commas, or @all")
	}
	parts := strings.Split(field, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts, nil
}

func scopeFor(ids []string) (protocol.Message, error) {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		return protocol.Message{}, errors.New("recipients: no agent IDs given")
	}
	for _, id := range ids {
		if err := protocol.ValidateAgentID(id); err != nil {
			return protocol.Message{}, errors.New("recipients: " + err.Error())
		}
	}
	if len(ids) > protocol.MaxRecipients {
		return protocol.Message{}, errors.New("recipients: at most 256 agents")
	}
	if len(ids) == 1 {
		return protocol.Message{To: ids[0]}, nil
	}
	return protocol.Message{Group: ids}, nil
}

// isBroadcast reports whether a parsed scope addresses every eligible agent.
func isBroadcast(scope protocol.Message) bool {
	return scope.To == protocol.Broadcast
}
