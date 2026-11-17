package messages

import "testing"

// These tests assert on output produced by package slacktext (mrkdwn →
// Markdown, block rendering). They only pass against its real
// implementation, not against a pass-through placeholder.

func TestParseMessageConvertsMrkdwn(t *testing.T) {
	msg := ParseMessage("C123", map[string]any{
		"ts":   "5.000001",
		"text": "See <https://example.com|this>, <@U12345678|nick> in <#C12345678|general> &amp; ship it :rocket:",
	})
	if want := "See [this](https://example.com), @nick in #general & ship it 🚀"; msg.Markdown != want {
		t.Errorf("markdown = %q, want %q", msg.Markdown, want)
	}
	if msg.Text != "See <https://example.com|this>, <@U12345678|nick> in <#C12345678|general> &amp; ship it :rocket:" {
		t.Errorf("text was modified: %q", msg.Text)
	}
}

func TestToCompactRendersContent(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "text only",
			msg:  Message{Text: "See <https://example.com|this> :rocket:"},
			want: "See [this](https://example.com) 🚀",
		},
		{
			name: "blocks win over text",
			msg: Message{
				Text: "fallback",
				Blocks: []any{map[string]any{
					"type": "section",
					"text": map[string]any{"type": "mrkdwn", "text": "Deploy <https://ci.example.com/1|#1> done"},
				}},
			},
			want: "Deploy [#1](https://ci.example.com/1) done",
		},
		{
			name: "attachments are appended to blocks",
			msg: Message{
				Blocks: []any{map[string]any{
					"type": "section",
					"text": map[string]any{"type": "plain_text", "text": "Alert"},
				}},
				Attachments: []any{map[string]any{"text": "disk <https://example.com/d|full>"}},
			},
			want: "Alert\n\ndisk [full](https://example.com/d)",
		},
		{
			name: "nothing to render",
			msg:  Message{Text: "   "},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToCompact(tt.msg, CompactOptions{}).Content; got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
		})
	}
}
