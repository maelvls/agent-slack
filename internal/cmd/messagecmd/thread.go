package messagecmd

import (
	"context"
	"fmt"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/messages"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// threadSummary tells the reader of "get" that the message belongs to a
// thread, and how long that thread is (root included).
type threadSummary struct {
	TS     string `json:"ts"`
	Length int    `json:"length"`
}

// getThreadSummary returns nil when msg is not part of a thread.
func getThreadSummary(ctx context.Context, client *slackapi.Client, channelID string, msg messages.Message) (*threadSummary, error) {
	switch {
	case msg.ThreadTS == "" && msg.ReplyCount == 0:
		return nil, nil
	case msg.ThreadTS == "" || msg.ThreadTS == msg.TS:
		// msg is the root: it carries the reply count itself.
		return &threadSummary{TS: msg.TS, Length: 1 + msg.ReplyCount}, nil
	}

	// msg is a reply: only the root knows how many replies there are.
	resp, err := client.API(ctx, "conversations.replies", map[string]any{
		"channel": channelID,
		"ts":      msg.ThreadTS,
		"limit":   1,
	})
	if err != nil {
		return nil, fmt.Errorf("fetching the thread of message %s: %w", msg.TS, err)
	}
	summary := &threadSummary{TS: msg.ThreadTS, Length: 1}
	if roots := jsonx.Maps(resp["messages"]); len(roots) > 0 {
		if n, ok := jsonx.Int(roots[0]["reply_count"]); ok {
			summary.Length = 1 + n
		}
	}
	return summary, nil
}
