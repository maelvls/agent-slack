package users

import (
	"regexp"
	"slices"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/messages"
	"github.com/maelvls/slackcli/internal/slackid"
)

// mentionRe matches <@U123> and <@U123|label>.
var mentionRe = regexp.MustCompile(`<@([^>|]+)(?:\|[^>]*)?>`)

// CollectReferencedUserIDs returns, sorted, the IDs of every user the
// messages refer to: authors, users mentioned in the text, blocks and
// attachments, and (with includeReactions) users who reacted.
func CollectReferencedUserIDs(msgs []messages.Message, includeReactions bool) []string {
	ids := map[string]bool{}
	for _, m := range msgs {
		if slackid.IsUserID(m.User) {
			ids[m.User] = true
		}
		collectMentions(m.Text, ids)
		collectFromValue(m.Blocks, ids)
		collectFromValue(m.Attachments, ids)
		if includeReactions {
			collectFromValue(m.Reactions, ids)
		}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// ToReferencedUsers builds the "referenced_users" object printed next to
// messages: the resolved users among userIDs, keyed by ID. It returns nil
// when none is resolved.
func ToReferencedUsers(userIDs []string, usersByID map[string]CompactUser) map[string]CompactUser {
	var out map[string]CompactUser
	for _, id := range dedupeUserIDs(userIDs) {
		if user, ok := usersByID[id]; ok {
			if out == nil {
				out = map[string]CompactUser{}
			}
			out[id] = user
		}
	}
	return out
}

// collectFromValue walks a raw Slack payload (blocks, attachments,
// reactions) looking for user IDs: "user"/"user_id" fields, "users"
// arrays, and mentions in any string.
func collectFromValue(v any, out map[string]bool) {
	switch x := v.(type) {
	case string:
		collectMentions(x, out)
	case []any:
		for _, item := range x {
			collectFromValue(item, out)
		}
	case map[string]any:
		for key, child := range x {
			switch {
			case key == "user" || key == "user_id":
				if s, ok := child.(string); ok {
					if slackid.IsUserID(s) {
						out[s] = true
					}
					continue
				}
			case key == "users":
				for _, u := range jsonx.Slice(child) {
					if id := jsonx.Str(u); slackid.IsUserID(id) {
						out[id] = true
					}
				}
				continue
			}
			collectFromValue(child, out)
		}
	}
}

func collectMentions(text string, out map[string]bool) {
	for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
		if slackid.IsUserID(m[1]) {
			out[m[1]] = true
		}
	}
}
