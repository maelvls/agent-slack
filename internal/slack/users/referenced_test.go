package users

import (
	"reflect"
	"testing"

	"github.com/maelvls/slackcli/internal/slack/messages"
)

func TestCollectReferencedUserIDs(t *testing.T) {
	msgs := []messages.Message{
		{
			ChannelID: "C1", TS: "1.000001", User: "U11111111",
			Text:      "hey <@U22222222>",
			Reactions: []any{map[string]any{"name": "eyes", "users": []any{"U11111111", "U33333333"}}},
		},
		{
			ChannelID: "C1", TS: "1.000002", User: "U11111111",
			Text: "follow-up for <@U33333333> and <@U22222222>",
		},
	}
	if got, want := CollectReferencedUserIDs(msgs, false), []string{"U11111111", "U22222222", "U33333333"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// Reaction users are excluded by default…
	msgs = []messages.Message{{
		ChannelID: "C1", TS: "1.000001", User: "W11111111",
		Text:      "hey <@W22222222> and <@B22222222>",
		Reactions: []any{map[string]any{"name": "eyes", "users": []any{"W11111111", "W44444444"}}},
	}}
	if got, want := CollectReferencedUserIDs(msgs, false), []string{"W11111111", "W22222222"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// …and included on request.
	if got, want := CollectReferencedUserIDs(msgs, true), []string{"W11111111", "W22222222", "W44444444"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCollectReferencedUserIDsFromBlocksAndAttachments(t *testing.T) {
	msgs := []messages.Message{{
		ChannelID: "C1", TS: "1.000001", BotID: "B11111111", User: "not-a-user",
		Text: "<@U11111111|alice> <@nope> <#C12345678|general>",
		Blocks: []any{map[string]any{
			"type": "rich_text",
			"elements": []any{map[string]any{
				"type": "rich_text_section",
				"elements": []any{
					map[string]any{"type": "user", "user_id": "U22222222"},
					map[string]any{"type": "text", "text": "cc <@U33333333>"},
					map[string]any{"type": "usergroup", "users": []any{"U44444444", "S12345678", 42}},
					map[string]any{"type": "user", "user_id": "lowercase"},
					map[string]any{"type": "context", "user": map[string]any{"user": "U55555555"}},
				},
			}},
		}},
		Attachments: []any{
			map[string]any{"author_id": "U99999999", "text": "forwarded from <@W66666666>", "user": "U77777777"},
			"junk",
		},
	}}
	want := []string{"U11111111", "U22222222", "U33333333", "U44444444", "U55555555", "U77777777", "W66666666"}
	if got := CollectReferencedUserIDs(msgs, false); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := CollectReferencedUserIDs(nil, true); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}

func TestToReferencedUsers(t *testing.T) {
	usersByID := map[string]CompactUser{
		"U11111111": {ID: "U11111111", Name: "alice", DisplayName: "Alice"},
		"U22222222": {ID: "U22222222", Name: "bob", DisplayName: "Bob"},
	}
	got := ToReferencedUsers([]string{"U11111111", "U11111111", "U99999999"}, usersByID)
	want := map[string]CompactUser{"U11111111": {ID: "U11111111", Name: "alice", DisplayName: "Alice"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if got := ToReferencedUsers([]string{"U99999999"}, usersByID); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}
