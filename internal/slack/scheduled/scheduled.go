// Package scheduled manages Slack scheduled messages: listing and
// cancelling them, and turning the --schedule / --schedule-in flag values
// into the post_at that chat.scheduleMessage expects.
package scheduled

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// ListOptions filters List. Zero values are not sent to Slack.
type ListOptions struct {
	// ChannelID restricts the list to one conversation.
	ChannelID string
	// Cursor is ListResult.NextCursor from the previous page.
	Cursor string
	// Oldest and Latest bound post_at, as Unix timestamps in seconds.
	Oldest, Latest string
	// Limit is the page size; 0 lets Slack choose.
	Limit int
}

// ListResult is one page of scheduled messages, in the shape the CLI
// prints.
type ListResult struct {
	OK bool `json:"ok"`
	// ScheduledMessages are Slack's raw entries (id, channel_id, post_at,
	// date_created, text).
	ScheduledMessages []map[string]any `json:"scheduled_messages"`
	// NextCursor is set when there are more pages: pass it as
	// ListOptions.Cursor.
	NextCursor string `json:"next_cursor"`
}

var unixBoundRe = regexp.MustCompile(`^\d+(\.\d+)?$`)

// List returns one page of the messages the user has scheduled
// (chat.scheduledMessages.list).
func List(ctx context.Context, client *slackapi.Client, opts ListOptions) (ListResult, error) {
	params := map[string]any{}
	set := func(key, val string) {
		if val != "" {
			params[key] = val
		}
	}
	set("channel", opts.ChannelID)
	set("cursor", opts.Cursor)
	for _, b := range []struct{ flag, key, val string }{
		{"--oldest", "oldest", strings.TrimSpace(opts.Oldest)},
		{"--latest", "latest", strings.TrimSpace(opts.Latest)},
	} {
		if b.val != "" && !unixBoundRe.MatchString(b.val) {
			return ListResult{}, fmt.Errorf("invalid %s value %q: must be a Unix timestamp in seconds, e.g. 1767225600", b.flag, b.val)
		}
		set(b.key, b.val)
	}
	if opts.Limit > 0 {
		params["limit"] = opts.Limit
	}

	resp, err := client.API(ctx, "chat.scheduledMessages.list", params)
	if err != nil {
		return ListResult{}, fmt.Errorf("listing scheduled messages: %w", err)
	}
	return ListResult{
		OK:                true,
		ScheduledMessages: jsonx.Maps(resp["scheduled_messages"]),
		NextCursor:        jsonx.GetStr(resp, "response_metadata", "next_cursor"),
	}, nil
}

// Cancel deletes a pending scheduled message
// (chat.deleteScheduledMessage). channelID must be the conversation the
// message was scheduled in.
func Cancel(ctx context.Context, client *slackapi.Client, channelID, scheduledMessageID string) error {
	_, err := client.API(ctx, "chat.deleteScheduledMessage", map[string]any{
		"channel":              channelID,
		"scheduled_message_id": scheduledMessageID,
	})
	if slackapi.IsErrorCode(err, "invalid_scheduled_message_id") {
		return fmt.Errorf("no pending scheduled message %s in channel %s: it was already sent or cancelled, is due within 60 seconds (too late to cancel), or was scheduled in another channel (list scheduled messages to check its channel_id): %w",
			scheduledMessageID, channelID, err)
	}
	if err != nil {
		return fmt.Errorf("cancelling scheduled message %s: %w", scheduledMessageID, err)
	}
	return nil
}
