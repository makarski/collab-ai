package chat

import (
	"regexp"
	"slices"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Audience sources, shown in the hint line and the state view.
const (
	SourceEveryone = "everyone"
	SourceMentions = "mentions"
	SourceTo       = "to"
	SourceReply    = "reply"
)

// mentionID is the inline mention grammar: narrower than a protocol agent ID,
// which may contain commas, spaces or "@". Other IDs are addressed with /to.
var mentionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// audience is who a draft would go to, or why it can't be sent.
type audience struct {
	everyone bool
	ids      []string
	notSeen  []string
	source   string
	err      string
}

// prefix is the leading mention prefix of a draft.
type prefix struct {
	tokens  []string // mention IDs without "@", in order
	partial string   // an unfinished or invalid token, blocking
	body    string   // the text after the prefix
	escaped bool     // the draft started with `\@`
}

// parsePrefix reads the mentions at the start of a draft's first line.
// Leading spaces are ignored; spaces or tabs separate tokens; the first
// non-mention or a line break ends the prefix. `\@` escapes a literal "@".
func parsePrefix(draft string) prefix {
	rest := strings.TrimLeft(draft, " \t")
	if strings.HasPrefix(rest, `\@`) {
		return prefix{body: rest[1:], escaped: true}
	}
	var p prefix
	for strings.HasPrefix(rest, "@") {
		end := strings.IndexAny(rest, " \t\n")
		if end < 0 {
			end = len(rest)
		}
		token := rest[1:end]
		if !mentionID.MatchString(token) {
			p.partial = rest[:end]
			break
		}
		p.tokens = append(p.tokens, token)
		rest = strings.TrimLeft(rest[end:], " \t")
	}
	p.body = rest
	return p
}

// resolveAudience applies the addressing rules to the composer.
func (m *Model) resolveAudience() audience {
	c := &m.compose
	if c.pasted {
		return m.explicitAudience(nil)
	}
	p := parsePrefix(c.editor.Value())
	if p.partial != "" {
		return audience{err: "not an agent mention: " + safe(p.partial) + " (use /to for other IDs, \\@ for a literal @)"}
	}
	if len(p.tokens) == 0 {
		return m.explicitAudience(nil)
	}
	if c.reply != nil {
		return audience{err: "remove the mentions or /reply cancel"}
	}
	if c.to != nil {
		return audience{err: "use either /to or @mentions, not both"}
	}
	return m.mentionAudience(p.tokens)
}

// explicitAudience is the reply or /to audience, or everyone.
func (m *Model) explicitAudience(_ []string) audience {
	c := &m.compose
	switch {
	case c.reply != nil:
		return audience{ids: []string{c.reply.sender}, source: SourceReply, notSeen: m.unseen([]string{c.reply.sender})}
	case c.to != nil:
		return audience{ids: c.to, source: SourceTo, notSeen: m.unseen(c.to)}
	}
	return audience{everyone: true, source: SourceEveryone}
}

func (m *Model) mentionAudience(tokens []string) audience {
	if slices.Contains(tokens, "all") {
		if len(tokens) > 1 {
			return audience{err: "@all can't be combined with other mentions"}
		}
		return audience{everyone: true, source: SourceMentions}
	}
	ids := slices.Clone(tokens)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > protocol.MaxRecipients {
		return audience{err: "at most 256 recipients"}
	}
	return audience{ids: ids, source: SourceMentions, notSeen: m.unseen(ids)}
}

// unseen lists IDs that are neither online nor in the loaded conversation.
// Presence is advisory, so these are marked, not refused.
func (m *Model) unseen(ids []string) []string {
	var out []string
	for _, id := range ids {
		if !m.known(id) {
			out = append(out, id)
		}
	}
	return out
}

func (m *Model) known(id string) bool {
	return slices.Contains(m.agentCandidates(id), id)
}

// scope is the operator_send addressing for a sendable audience.
func (a audience) scope() (protocol.Message, error) {
	if a.everyone {
		return protocol.Message{To: protocol.Broadcast}, nil
	}
	return scopeFor(a.ids)
}

// outgoingText is the text that is sent: the draft as typed, except that a
// leading `\@` escape loses its backslash.
func (m *Model) outgoingText() string {
	text := m.compose.editor.Value()
	if m.compose.pasted {
		return text
	}
	if p := parsePrefix(text); p.escaped {
		lead := len(text) - len(strings.TrimLeft(text, " \t"))
		return text[:lead] + text[lead+1:]
	}
	return text
}

// draftBody reports whether the draft has text beyond its mention prefix.
func (m *Model) draftBody() bool {
	text := m.compose.editor.Value()
	if m.compose.pasted {
		return strings.TrimSpace(text) != ""
	}
	return strings.TrimSpace(parsePrefix(text).body) != ""
}
