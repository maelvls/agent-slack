// Package channels resolves, lists and marks Slack conversations (public
// and private channels, DMs, group DMs).
package channels

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slackapi"
	"github.com/maelvls/slackcli/internal/slackid"
)

// scanPageSize is the page size for name lookups. The list methods are
// rate limited per call (about 20 a minute), so the largest page Slack
// accepts gets through a big workspace five times faster than the
// recommended 200.
const scanPageSize = 1000

// NotFoundError says that no visible channel has the requested name.
type NotFoundError struct {
	// Name is the channel name, without "#".
	Name string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf(`channel "#%s" not found: no public channel, and no private channel these credentials are a member of, has exactly that name in this workspace. `+
		`Run "%s channel list" to see the names, pass the channel ID (C…) or a message URL instead, or select another workspace with --workspace.`,
		e.Name, appinfo.Name)
}

// isFatal reports whether err makes further API calls pointless: the
// command was cancelled or the credentials are dead. Fallbacks must not
// swallow these (auth errors in particular trigger a credentials refresh).
func isFatal(ctx context.Context, err error) bool {
	return err != nil && (ctx.Err() != nil || slackapi.IsAuthError(err))
}

func isRateLimit(err error) bool {
	var rl *slackapi.RateLimitError
	return errors.As(err, &rl)
}

// OpenDM opens (or reuses) the DM with a user and returns its channel ID.
func OpenDM(ctx context.Context, client *slackapi.Client, userID string) (string, error) {
	resp, err := client.API(ctx, "conversations.open", map[string]any{"users": userID})
	if err != nil {
		return "", fmt.Errorf("opening DM with %s: %w", userID, err)
	}
	id := jsonx.GetStr(resp, "channel", "id")
	if id == "" {
		return "", fmt.Errorf("could not open a DM with user %s: conversations.open returned no channel", userID)
	}
	return id, nil
}

// ResolveID turns a channel reference ("#general", "general" or an ID)
// into a conversation ID. IDs are returned as-is without any API call.
//
// Slack has no name→ID lookup, so names are resolved by, in order:
//
//  1. search.messages "in:#name" (one call; needs a user or browser token
//     and at least one message in the channel);
//  2. users.conversations: the channels the caller is a member of,
//     private ones included;
//  3. conversations.list: every public channel, archived ones included.
//
// The name must match exactly. When nothing matches the error is a
// *NotFoundError.
func ResolveID(ctx context.Context, client *slackapi.Client, input string) (string, error) {
	in := slackid.ParseChannelInput(input)
	if in.IsID {
		return in.Value, nil
	}
	// Channel names are always lower-case.
	name := strings.ToLower(in.Value)
	if name == "" {
		return "", errors.New("channel name is empty: pass #name or a channel ID (C…)")
	}

	if canSearch(client, name) {
		id, err := searchByName(ctx, client, name)
		if isFatal(ctx, err) {
			return "", fmt.Errorf("resolving channel #%s: %w", name, err)
		}
		// Any other search failure (missing search:read scope, rate
		// limit on the search tier …) leaves the listing methods to try.
		if id != "" {
			return id, nil
		}
	}

	const joinedTypes = "public_channel,private_channel"
	id, err := scanByName(ctx, client, "users.conversations", joinedTypes, name)
	if id != "" {
		return id, nil
	}
	if isFatal(ctx, err) || isRateLimit(err) {
		return "", fmt.Errorf("resolving channel #%s: %w", name, err)
	}

	// Private channels the caller is not in are invisible, so only public
	// ones are left to look at. Archived channels are included: they can
	// still be read.
	id, err = scanByName(ctx, client, "conversations.list", "public_channel", name)
	if id != "" {
		return id, nil
	}
	if isFatal(ctx, err) || isRateLimit(err) {
		return "", fmt.Errorf("resolving channel #%s: %w", name, err)
	}
	if err != nil {
		// Typically enterprise_is_restricted (browser tokens on
		// Enterprise Grid) or missing_scope.
		return "", fmt.Errorf("resolving channel #%s: %w (these credentials cannot list channels: pass the channel ID (C…) or a message URL instead)", name, err)
	}
	return "", &NotFoundError{Name: name}
}

// canSearch reports whether the search.messages shortcut is worth trying:
// bot tokens cannot call search.*, and a name with spaces or quotes cannot
// be a channel name nor be embedded in a query.
func canSearch(client *slackapi.Client, name string) bool {
	if strings.HasPrefix(client.Auth().BearerToken(), "xoxb-") {
		return false
	}
	return !strings.ContainsAny(name, " \t\n\"")
}

// searchByName finds the channel through its most recent message.
func searchByName(ctx context.Context, client *slackapi.Client, name string) (string, error) {
	resp, err := client.API(ctx, "search.messages", map[string]any{
		"query":    "in:#" + name,
		"count":    1,
		"sort":     "timestamp",
		"sort_dir": "desc",
	})
	if err != nil {
		return "", err
	}
	for _, m := range jsonx.Maps(jsonx.Get(resp, "messages", "matches")) {
		ch := jsonx.Map(m["channel"])
		// Search is fuzzy about what "in:" means when the channel does
		// not exist; only trust a result from a channel with that name.
		if id := jsonx.Str(ch["id"]); id != "" && strings.EqualFold(jsonx.Str(ch["name"]), name) {
			return id, nil
		}
	}
	return "", nil
}

// scanByName pages through a conversations listing until a channel is
// called name. It returns "" with a nil error when the listing ends
// without a match.
func scanByName(ctx context.Context, client *slackapi.Client, method, types, name string) (string, error) {
	cursor := ""
	for {
		params := map[string]any{"types": types, "limit": scanPageSize, "exclude_archived": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		resp, err := client.API(ctx, method, params)
		if err != nil {
			// A token without groups:read cannot list private channels
			// but can still list public ones.
			if slackapi.IsErrorCode(err, "missing_scope") && types != "public_channel" {
				types, cursor = "public_channel", ""
				continue
			}
			return "", err
		}
		for _, ch := range jsonx.Maps(resp["channels"]) {
			if id := jsonx.Str(ch["id"]); id != "" && strings.EqualFold(jsonx.Str(ch["name"]), name) {
				return id, nil
			}
		}
		next := jsonx.GetStr(resp, "response_metadata", "next_cursor")
		if next == "" || next == cursor {
			return "", nil
		}
		cursor = next
	}
}

// ResolveName returns a human-readable name for a conversation ID (see
// LookupName), or the ID itself when the name cannot be determined,
// whatever the reason: it cannot report dead credentials or a rate limit.
// Code that makes further calls should use LookupName.
func ResolveName(ctx context.Context, client *slackapi.Client, channelID string) string {
	if name, err := LookupName(ctx, client, channelID); err == nil && name != "" {
		return name
	}
	return channelID
}

// LookupName returns a human-readable name for a conversation ID: the
// channel name ("general"), or for a DM the other user's display name,
// else their real name, else their handle. It returns "" when Slack has
// no name for the conversation.
//
// The error is that of the failed conversations.info or users.info call.
// A name is an enrichment (see package enrich): callers go on without it,
// except when enrich.MustAbort(ctx, err), and stop looking names up once
// one lookup has been rate limited.
func LookupName(ctx context.Context, client *slackapi.Client, channelID string) (string, error) {
	resp, err := client.API(ctx, "conversations.info", map[string]any{"channel": channelID})
	if err != nil {
		return "", err
	}
	ch := jsonx.Map(resp["channel"])
	if !jsonx.Bool(ch["is_im"]) {
		return cmp.Or(jsonx.Str(ch["name"]), jsonx.Str(ch["name_normalized"])), nil
	}
	userID := jsonx.Str(ch["user"])
	if userID == "" {
		return "", nil
	}
	resp, err = client.API(ctx, "users.info", map[string]any{"user": userID})
	if err != nil {
		return "", err
	}
	user := jsonx.Map(resp["user"])
	profile := jsonx.Map(user["profile"])
	return cmp.Or(
		jsonx.Str(profile["display_name"]),
		jsonx.Str(user["real_name"]),
		jsonx.Str(profile["real_name"]),
		jsonx.Str(user["name"]),
	), nil
}
