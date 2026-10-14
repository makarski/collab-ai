package chat_test

import (
	"reflect"
	"strings"
	"testing"

	"collab-ai/internal/chat"
	"collab-ai/internal/protocol"
)

func TestChatRecipientGrammarPreservesLiteralIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, input, to string
		group           []string
	}{
		{name: "broadcast keyword", input: "@all", to: "*"},
		{name: "broadcast alias", input: "*", to: "*"},
		{name: "plain", input: "claude-1", to: "claude-1"},
		{name: "trimmed", input: "  claude-1  ", to: "claude-1"},
		{name: "canonical group", input: "z,a,z", group: []string{"a", "z"}},
		{name: "literal keyword", input: `["@all"]`, to: "@all"},
		{name: "literal comma", input: `["a,b"]`, to: "a,b"},
		{name: "literal group", input: `["a,b","@all","a,b"]`, group: []string{"@all", "a,b"}},
		{name: "literal spaces", input: `[" a "]`, to: " a "},
		{name: "literal escape", input: `["a\u001bb"]`, to: "a\x1bb"},
		{name: "group-looking ID", input: "group:message-id", to: "group:message-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chat.ParseRecipients(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			want := protocol.Message{To: tc.to, Group: tc.group}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("scope = %#v, want %#v", got, want)
			}
		})
	}
}

func TestChatRecipientGrammarRejectsAmbiguousOrInvalidScope(t *testing.T) {
	for _, input := range []string{"", "  ", ",", "a,", ",a", "a,,b", "operator", "a,operator", "a,*", "[]", `["*"]`, `["operator"]`, `[""]`, `[1]`, `["a"`, `["a"] trailing`, strings.Repeat("x", 129), strings.Repeat("é", 65)} {
		t.Run(input, func(t *testing.T) {
			if got, err := chat.ParseRecipients(input); err == nil {
				t.Fatalf("invalid scope accepted: %#v", got)
			}
		})
	}
}
