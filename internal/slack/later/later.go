// Package later manages the messages saved for later (Slack's "Later"
// tab): listing them, saving, completing, archiving, reopening and
// removing them, and setting their reminder.
//
// It uses Slack's internal saved.* methods, which only accept browser
// session credentials.
package later

import (
	"context"
	"errors"
	"fmt"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// MarkAction is a state change accepted by saved.update.
type MarkAction string

// The state changes of a saved message.
const (
	MarkCompleted   MarkAction = "completed"
	MarkUncompleted MarkAction = "uncompleted"
	MarkArchived    MarkAction = "archived"
	MarkUnarchived  MarkAction = "unarchived"
)

// requireBrowser fails early, with something to act on, instead of
// letting Slack answer not_allowed_token_type (or an unknown method error)
// to a bot or user token.
func requireBrowser(client *slackapi.Client) error {
	if client.Auth().IsBrowser() {
		return nil
	}
	return fmt.Errorf(`Slack's Later (saved.*) API only works with browser session credentials, not with a bot or user token (xoxb-/xoxp-). `+
		`Import browser credentials with "%s auth import-desktop" (also: import-chrome, import-brave, import-firefox), or set SLACK_TOKEN to an xoxc- token together with SLACK_COOKIE_D`, appinfo.Name)
}

func itemParams(channelID, ts string) map[string]any {
	return map[string]any{"item_id": channelID, "item_type": "message", "ts": ts}
}

// isFatal reports whether err must abort an operation rather than be
// worked around: the command was cancelled, the credentials are dead (the
// caller refreshes them and retries), or Slack is rate limiting us.
func isFatal(ctx context.Context, err error) bool {
	var rl *slackapi.RateLimitError
	return err != nil && (ctx.Err() != nil || slackapi.IsAuthError(err) || errors.As(err, &rl))
}

// Mark changes the state of a saved message (saved.update).
func Mark(ctx context.Context, client *slackapi.Client, channelID, ts string, mark MarkAction) error {
	if err := requireBrowser(client); err != nil {
		return err
	}
	params := itemParams(channelID, ts)
	params["mark"] = string(mark)
	// saved.update silently ignores form-encoded parameters.
	_, err := client.APIMultipart(ctx, "saved.update", params)
	return err
}

// Reopen moves a completed or archived saved message back to "in
// progress". Which of the two it was is not known without listing every
// saved item, so both transitions are tried: Reopen succeeds if either
// does, and fails with Slack's first answer if neither does.
func Reopen(ctx context.Context, client *slackapi.Client, channelID, ts string) error {
	if err := requireBrowser(client); err != nil {
		return err
	}
	errUncompleted := Mark(ctx, client, channelID, ts, MarkUncompleted)
	if isFatal(ctx, errUncompleted) {
		return errUncompleted
	}
	errUnarchived := Mark(ctx, client, channelID, ts, MarkUnarchived)
	if isFatal(ctx, errUnarchived) {
		return errUnarchived
	}
	if errUncompleted == nil || errUnarchived == nil {
		return nil
	}
	return fmt.Errorf("reopening saved message %s in %s: %w", ts, channelID, errUncompleted)
}

// Save saves a message for later (saved.add).
func Save(ctx context.Context, client *slackapi.Client, channelID, ts string) error {
	if err := requireBrowser(client); err != nil {
		return err
	}
	_, err := client.API(ctx, "saved.add", itemParams(channelID, ts))
	return err
}

// Remove deletes a message from Later entirely (saved.delete).
func Remove(ctx context.Context, client *slackapi.Client, channelID, ts string) error {
	if err := requireBrowser(client); err != nil {
		return err
	}
	_, err := client.API(ctx, "saved.delete", itemParams(channelID, ts))
	return err
}

// SetReminder sets when Slack reminds the user about a saved message.
// dateDue is a Unix time in seconds (see ParseReminderTime).
func SetReminder(ctx context.Context, client *slackapi.Client, channelID, ts string, dateDue int64) error {
	if err := requireBrowser(client); err != nil {
		return err
	}
	params := itemParams(channelID, ts)
	params["date_due"] = dateDue
	_, err := client.APIMultipart(ctx, "saved.update", params)
	return err
}
