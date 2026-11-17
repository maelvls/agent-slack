package channels

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/enrich"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// DefaultTypes is the conversation types listed when ListOptions.Types is
// empty.
const DefaultTypes = "public_channel,private_channel,im,mpim"

const (
	defaultListLimit = 100
	maxListLimit     = 1000
	// infoConcurrency bounds the conversations.info calls of ListViaCounts;
	// unbounded, a user in a thousand channels would fire a thousand
	// requests at once and get rate limited.
	infoConcurrency = 8
)

// ConversationsPage is one page of conversations, as printed by
// `channel list`.
type ConversationsPage struct {
	// Channels are raw Slack conversation objects.
	Channels []map[string]any `json:"channels"`
	// NextCursor is the cursor of the next page ("" on the last page).
	NextCursor string `json:"next_cursor,omitempty"`
	// Warnings say what is missing from Channels because Slack rate
	// limited the lookups (ListViaCounts), for the caller to show on
	// stderr.
	Warnings []string `json:"-"`
}

// ListOptions selects a page of conversations.
type ListOptions struct {
	// User lists the conversations of that user ID instead of the caller's
	// (ListUser only).
	User string
	// Limit is the page size: 0 means 100; values are clamped to 1..1000.
	Limit int
	// Cursor is the NextCursor of the previous page.
	Cursor string
	// Types is a comma-separated list of conversation types; "" means
	// DefaultTypes.
	Types string
	// IncludeArchived also lists archived channels.
	IncludeArchived bool
}

func (o ListOptions) params() map[string]any {
	params := map[string]any{
		"limit":            normalizeLimit(o.Limit),
		"types":            DefaultTypes,
		"exclude_archived": !o.IncludeArchived,
	}
	if o.Types != "" {
		params["types"] = o.Types
	}
	if o.Cursor != "" {
		params["cursor"] = o.Cursor
	}
	return params
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultListLimit
	}
	return min(limit, maxListLimit)
}

// ListUser lists the conversations a user is a member of
// (users.conversations): opts.User, or the caller when it is empty.
func ListUser(ctx context.Context, client *slackapi.Client, opts ListOptions) (ConversationsPage, error) {
	params := opts.params()
	if opts.User != "" {
		params["user"] = opts.User
	}
	resp, err := client.API(ctx, "users.conversations", params)
	if err != nil {
		return ConversationsPage{}, err
	}
	return NormalizePage(resp), nil
}

// ListAll lists every conversation visible to the caller
// (conversations.list). opts.User is ignored.
func ListAll(ctx context.Context, client *slackapi.Client, opts ListOptions) (ConversationsPage, error) {
	resp, err := client.API(ctx, "conversations.list", opts.params())
	if err != nil {
		return ConversationsPage{}, err
	}
	return NormalizePage(resp), nil
}

// ListViaCounts lists the caller's joined conversations with client.counts
// + conversations.info, the way the Slack web client fills its sidebar. It
// works on Enterprise Grid, where users.conversations and
// conversations.list answer enterprise_is_restricted to browser tokens.
//
// client.counts returns every conversation ID at once (channels, then
// group DMs, then DMs) and no names, so the page is cut locally and each
// ID is enriched with conversations.info; a conversation whose info cannot
// be read is returned as {"id": …}, and so are those left once
// conversations.info gets rate limited (see ConversationsPage.Warnings).
// Only opts.Limit and opts.Cursor are
// honoured; the cursor is the offset into that list, as returned in
// NextCursor.
func ListViaCounts(ctx context.Context, client *slackapi.Client, opts ListOptions) (ConversationsPage, error) {
	offset := 0
	if opts.Cursor != "" {
		n, err := strconv.Atoi(opts.Cursor)
		if err != nil || n < 0 {
			return ConversationsPage{}, fmt.Errorf("invalid cursor %q: pass the next_cursor value of the previous page", opts.Cursor)
		}
		offset = n
	}
	limit := normalizeLimit(opts.Limit)

	resp, err := client.API(ctx, "client.counts", map[string]any{"thread_count_by_channel": true})
	if err != nil {
		return ConversationsPage{}, err
	}
	var ids []string
	for _, key := range []string{"channels", "mpims", "ims"} {
		for _, entry := range jsonx.Maps(resp[key]) {
			if id := jsonx.Str(entry["id"]); id != "" {
				ids = append(ids, id)
			}
		}
	}

	page := ConversationsPage{}
	offset = min(offset, len(ids))
	end := min(offset+limit, len(ids))
	if end < len(ids) {
		page.NextCursor = strconv.Itoa(end)
	}
	ids = ids[offset:end]

	// The details of each conversation are an enrichment (see package
	// enrich): only dead credentials and cancellation fail the listing,
	// and a rate limit stops the lookups, leaving {"id": …} entries.
	page.Channels = make([]map[string]any, len(ids))
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	var limited enrich.Gate
	sem := make(chan struct{}, infoConcurrency)
	for i, id := range ids {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			page.Channels[i] = map[string]any{"id": id}
			if ctx.Err() != nil || limited.Closed() {
				return
			}
			info, err := client.API(ctx, "conversations.info", map[string]any{"channel": id})
			switch {
			case err == nil:
				if ch := jsonx.Map(info["channel"]); ch != nil {
					page.Channels[i] = ch
				}
			case enrich.MustAbort(ctx, err):
				cancel(fmt.Errorf("looking up conversation %s: %w", id, err))
			default:
				limited.Failed(err)
			}
		})
	}
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return ConversationsPage{}, err
	}
	if n, err := limited.Skipped(); n > 0 {
		page.Warnings = append(page.Warnings, fmt.Sprintf(
			"the details of %d of %d conversations were not looked up (%v): they are listed by ID only; %s",
			n, len(ids), err, enrich.Hint()))
	}
	return page, nil
}

// NormalizePage extracts the conversations and the next cursor from a
// conversations.list / users.conversations response.
func NormalizePage(resp map[string]any) ConversationsPage {
	return ConversationsPage{
		Channels:   jsonx.Maps(resp["channels"]),
		NextCursor: jsonx.GetStr(resp, "response_metadata", "next_cursor"),
	}
}

// Mark moves the caller's read cursor in a conversation to ts
// (conversations.mark).
func Mark(ctx context.Context, client *slackapi.Client, channelID, ts string) error {
	_, err := client.API(ctx, "conversations.mark", map[string]any{"channel": channelID, "ts": ts})
	return err
}
