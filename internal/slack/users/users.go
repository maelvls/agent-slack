// Package users looks up and lists workspace members, opens DMs with
// them, and resolves the user IDs referenced by messages through an
// on-disk cache.
package users

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// CompactUser is the user shape the CLI prints.
type CompactUser struct {
	ID string `json:"id"`
	// Name is the handle (Slack's "username").
	Name        string `json:"name,omitempty"`
	RealName    string `json:"real_name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
	Title       string `json:"title,omitempty"`
	TZ          string `json:"tz,omitempty"`
	// IsBot and Deleted are nil when Slack did not say.
	IsBot   *bool `json:"is_bot,omitempty"`
	Deleted *bool `json:"deleted,omitempty"`
	// DMID is the ID of the existing DM with this user (ListUsers only).
	DMID        string `json:"dm_id,omitempty"`
	StatusText  string `json:"status_text,omitempty"`
	StatusEmoji string `json:"status_emoji,omitempty"`
	// StatusExpiration is a Unix time in seconds; 0 means the status does
	// not expire.
	StatusExpiration int64 `json:"status_expiration,omitempty"`
}

// ToCompactUser converts a raw Slack user object (users.info, users.list).
func ToCompactUser(u map[string]any) CompactUser {
	profile := jsonx.Map(u["profile"])
	out := CompactUser{
		ID:          jsonx.Str(u["id"]),
		Name:        jsonx.Str(u["name"]),
		RealName:    jsonx.Str(u["real_name"]),
		DisplayName: jsonx.Str(profile["display_name"]),
		Email:       jsonx.Str(profile["email"]),
		Title:       jsonx.Str(profile["title"]),
		TZ:          jsonx.Str(u["tz"]),
		StatusText:  jsonx.Str(profile["status_text"]),
		StatusEmoji: jsonx.Str(profile["status_emoji"]),
	}
	if out.RealName == "" {
		out.RealName = jsonx.Str(profile["real_name"])
	}
	if b, ok := u["is_bot"].(bool); ok {
		out.IsBot = &b
	}
	if b, ok := u["deleted"].(bool); ok {
		out.Deleted = &b
	}
	if n, ok := jsonx.Num(profile["status_expiration"]); ok {
		out.StatusExpiration = int64(n)
	}
	return out
}

// isFatal reports whether err must abort the operation rather than be
// worked around: the command was cancelled, the credentials are dead (the
// caller refreshes them and retries), or Slack is rate limiting us.
func isFatal(ctx context.Context, err error) bool {
	var rl *slackapi.RateLimitError
	return err != nil && (ctx.Err() != nil || slackapi.IsAuthError(err) || errors.As(err, &rl))
}

func nextCursor(resp map[string]any) string {
	return jsonx.GetStr(resp, "response_metadata", "next_cursor")
}

const (
	defaultListLimit = 200
	maxListLimit     = 1000
	// listPageSize is the users.list page size ListUsers asks for, as
	// Slack recommends.
	listPageSize = 200
)

// ListOptions selects a page of users.
type ListOptions struct {
	// Limit is the number of users to return: 0 means 200; values are
	// clamped to 1..1000.
	Limit int
	// Cursor is the NextCursor of the previous page.
	Cursor string
	// IncludeBots also lists bot users.
	IncludeBots bool
}

// UsersPage is one page of the user directory, as printed by `user list`.
type UsersPage struct {
	Users []CompactUser `json:"users"`
	// NextCursor is the cursor of the next page ("" when the directory is
	// exhausted).
	NextCursor string `json:"next_cursor,omitempty"`
}

// ListUsers lists workspace members (users.list), deactivated ones
// included, each with the ID of the caller's existing DM with them when
// there is one.
func ListUsers(ctx context.Context, client *slackapi.Client, opts ListOptions) (UsersPage, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	limit = min(limit, maxListLimit)

	dms := make(chan map[string]string, 1)
	go func() { dms <- dmChannels(ctx, client) }()

	page := UsersPage{}
	cursor := opts.Cursor
	for len(page.Users) < limit {
		// Never ask for more than is still wanted: users left over on a
		// page could not be resumed from, the cursor only moves by whole
		// pages.
		params := map[string]any{"limit": min(listPageSize, limit-len(page.Users))}
		if cursor != "" {
			params["cursor"] = cursor
		}
		resp, err := client.API(ctx, "users.list", params)
		if err != nil {
			return UsersPage{}, err
		}
		for _, m := range jsonx.Maps(resp["members"]) {
			if jsonx.Str(m["id"]) == "" || (!opts.IncludeBots && jsonx.Bool(m["is_bot"])) {
				continue
			}
			page.Users = append(page.Users, ToCompactUser(m))
		}
		// The cursor Slack returned with the last page fetched is the only
		// correct place to resume from: "" once the directory is exhausted.
		next := nextCursor(resp)
		if next == cursor {
			next = ""
		}
		page.NextCursor = next
		if next == "" {
			break
		}
		cursor = next
	}

	dmByUser := <-dms
	if err := ctx.Err(); err != nil {
		return UsersPage{}, err
	}
	for i := range page.Users {
		page.Users[i].DMID = dmByUser[page.Users[i].ID]
	}
	return page, nil
}

// dmChannels maps user IDs to the caller's DM channel with them. It is an
// enrichment: when DMs cannot be listed (missing im:read scope, Enterprise
// Grid restrictions) the result is simply incomplete.
func dmChannels(ctx context.Context, client *slackapi.Client) map[string]string {
	out := map[string]string{}
	cursor := ""
	for {
		params := map[string]any{"types": "im", "limit": 1000}
		if cursor != "" {
			params["cursor"] = cursor
		}
		resp, err := client.API(ctx, "conversations.list", params)
		if err != nil {
			return out
		}
		for _, ch := range jsonx.Maps(resp["channels"]) {
			id, user := jsonx.Str(ch["id"]), jsonx.Str(ch["user"])
			if id != "" && user != "" {
				out[user] = id
			}
		}
		next := nextCursor(resp)
		if next == "" || next == cursor {
			return out
		}
		cursor = next
	}
}

// GetUser returns one user, given a user ID, an email address, a
// "@handle" or a display name (see ResolveUserID).
func GetUser(ctx context.Context, client *slackapi.Client, input string) (CompactUser, error) {
	found, err := lookup(ctx, client, []string{input})
	if err != nil {
		return CompactUser{}, err
	}
	// A lookup by email or by directory scan already has the whole user.
	if found[0].raw != nil {
		return ToCompactUser(found[0].raw), nil
	}
	resp, err := client.API(ctx, "users.info", map[string]any{"user": found[0].id})
	if err != nil {
		return CompactUser{}, fmt.Errorf("looking up user %s: %w", found[0].id, err)
	}
	u := jsonx.Map(resp["user"])
	if jsonx.Str(u["id"]) == "" {
		return CompactUser{}, fmt.Errorf("looking up user %s: users.info returned no user", found[0].id)
	}
	return ToCompactUser(u), nil
}

// maxGroupDMUsers is Slack's limit on the users of a group DM.
const maxGroupDMUsers = 8

// DMChannel is the conversation opened by GetDMChannelForUsers, as printed
// by `user dm-open`.
type DMChannel struct {
	UserIDs     []string `json:"user_ids"`
	DMChannelID string   `json:"dm_channel_id"`
	// ChannelType is "dm" for one user, "group_dm" for several.
	ChannelType string `json:"channel_type"`
}

// GetDMChannelForUsers opens (or reuses) the DM with one user, or the
// group DM with several (at most 8). Users are given as for ResolveUserID;
// blank and duplicate entries are ignored.
func GetDMChannelForUsers(ctx context.Context, client *slackapi.Client, inputs []string) (DMChannel, error) {
	var wanted []string
	for _, in := range inputs {
		if in = strings.TrimSpace(in); in != "" {
			wanted = append(wanted, in)
		}
	}
	if len(wanted) == 0 {
		return DMChannel{}, errors.New("at least one user is required: pass user IDs (U…), emails or @handles")
	}
	if len(wanted) > maxGroupDMUsers {
		return DMChannel{}, fmt.Errorf("Slack supports a maximum of %d users in a group DM (got %d)", maxGroupDMUsers, len(wanted))
	}

	found, err := lookup(ctx, client, wanted)
	if err != nil {
		return DMChannel{}, err
	}
	var ids []string
	seen := map[string]bool{}
	for _, f := range found {
		if !seen[f.id] {
			seen[f.id] = true
			ids = append(ids, f.id)
		}
	}

	resp, err := client.API(ctx, "conversations.open", map[string]any{"users": strings.Join(ids, ",")})
	if err != nil {
		return DMChannel{}, fmt.Errorf("opening a DM with %s: %w", strings.Join(ids, ","), err)
	}
	channelID := jsonx.GetStr(resp, "channel", "id")
	if channelID == "" {
		return DMChannel{}, errors.New("conversations.open returned no channel")
	}
	channelType := "group_dm"
	if strings.HasPrefix(channelID, "D") {
		channelType = "dm"
	}
	return DMChannel{UserIDs: ids, DMChannelID: channelID, ChannelType: channelType}, nil
}
