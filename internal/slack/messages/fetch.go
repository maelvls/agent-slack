package messages

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const (
	// DefaultHistoryLimit is the number of messages FetchChannelHistory
	// returns when no limit is given.
	DefaultHistoryLimit = 25
	// MaxHistoryLimit is the most messages one FetchChannelHistory call
	// returns; larger limits are clamped.
	MaxHistoryLimit = 1000
	// pageSize is the page size for history and replies, as Slack
	// recommends.
	pageSize = 200
)

// ErrMessageNotFound means the message does not exist or is not visible
// to these credentials.
var ErrMessageNotFound = errors.New("message not found")

// CompareTS orders two Slack timestamps ("1770165109.628379")
// chronologically. Timestamps are compared digit by digit: as float64
// they lose their last digit of precision, which is enough to swap two
// messages posted in the same instant.
func CompareTS(a, b string) int {
	aSec, aFrac, _ := strings.Cut(a, ".")
	bSec, bFrac, _ := strings.Cut(b, ".")
	aSec, bSec = strings.TrimLeft(aSec, "0"), strings.TrimLeft(bSec, "0")
	if c := len(aSec) - len(bSec); c != 0 {
		return c
	}
	if c := strings.Compare(aSec, bSec); c != 0 {
		return c
	}
	return strings.Compare(strings.TrimRight(aFrac, "0"), strings.TrimRight(bFrac, "0"))
}

func sortChronologically(msgs []Message) {
	slices.SortStableFunc(msgs, func(a, b Message) int { return CompareTS(a.TS, b.TS) })
}

// metadataParam mirrors the TypeScript: reactions travel with the message
// either way, include_all_metadata additionally asks for message metadata.
func metadataParam(params map[string]any, include bool) {
	if include {
		params["include_all_metadata"] = true
	}
}

func nextCursor(resp map[string]any) string {
	return jsonx.GetStr(resp, "response_metadata", "next_cursor")
}

func findTS(resp map[string]any, ts string) map[string]any {
	for _, m := range jsonx.Maps(resp["messages"]) {
		if jsonx.Str(m["ts"]) == ts {
			return m
		}
	}
	return nil
}

// FetchMessage fetches the message ref points at, whether it is a
// top-level message or a thread reply (replies are not part of the channel
// history; ref.ThreadTSHint, when set, says which thread to look in).
//
// It fails with an error wrapping ErrMessageNotFound when the message
// cannot be found.
func FetchMessage(ctx context.Context, client *slackapi.Client, ref slackurl.MessageRef, includeReactions bool) (Message, error) {
	raw, err := findMessage(ctx, client, ref, includeReactions)
	if err != nil {
		return Message{}, err
	}
	msgs := []Message{ParseMessage(ref.ChannelID, raw)}
	if err := enrichFiles(ctx, client, msgs); err != nil {
		return Message{}, err
	}
	return msgs[0], nil
}

func findMessage(ctx context.Context, client *slackapi.Client, ref slackurl.MessageRef, includeReactions bool) (map[string]any, error) {
	channel, ts := ref.ChannelID, ref.MessageTS

	// 1. Top-level messages: the newest message at or before ts.
	params := map[string]any{"channel": channel, "latest": ts, "inclusive": true, "limit": 1}
	metadataParam(params, includeReactions)
	resp, err := client.API(ctx, "conversations.history", params)
	if err != nil {
		return nil, fmt.Errorf("fetching message %s in %s: %w", ts, channel, err)
	}
	if m := findTS(resp, ts); m != nil {
		return m, nil
	}

	// 2. A reply in the thread named by the permalink. Bounding the range
	// to ts makes Slack return the root and that one reply instead of
	// having us page through the whole thread.
	if hint := ref.ThreadTSHint; hint != "" && hint != ts {
		cursor := ""
		for {
			params := map[string]any{"channel": channel, "ts": hint, "oldest": ts, "latest": ts, "inclusive": true, "limit": pageSize}
			if cursor != "" {
				params["cursor"] = cursor
			}
			metadataParam(params, includeReactions)
			resp, err := client.API(ctx, "conversations.replies", params)
			if err != nil {
				if isFatal(ctx, err) {
					return nil, fmt.Errorf("fetching message %s in %s: %w", ts, channel, err)
				}
				break // e.g. thread_not_found: the hint was wrong
			}
			if m := findTS(resp, ts); m != nil {
				return m, nil
			}
			next := nextCursor(resp)
			if next == "" || next == cursor {
				break
			}
			cursor = next
		}
	}

	// 3. No (usable) hint: conversations.replies accepts the ts of any
	// message of a thread and returns that message first.
	params = map[string]any{"channel": channel, "ts": ts, "limit": 1}
	metadataParam(params, includeReactions)
	resp, err = client.API(ctx, "conversations.replies", params)
	if err != nil && isFatal(ctx, err) {
		return nil, fmt.Errorf("fetching message %s in %s: %w", ts, channel, err)
	}
	if err == nil {
		if m := findTS(resp, ts); m != nil {
			return m, nil
		}
	}

	return nil, fmt.Errorf("%w: no message with ts %s in channel %s. It may have been deleted, the ts or URL may be wrong or truncated (quote URLs containing \"&\"), or these credentials cannot see it",
		ErrMessageNotFound, ts, channel)
}

// HistoryOptions selects messages of a channel.
type HistoryOptions struct {
	// Limit is the number of messages to return: 0 means
	// DefaultHistoryLimit; values are clamped to 1..MaxHistoryLimit.
	Limit int
	// Latest and Oldest bound the range, exclusively: only messages
	// strictly before Latest and strictly after Oldest are returned. ""
	// means unbounded.
	Latest string
	Oldest string
	// IncludeReactions asks for the complete message payload.
	IncludeReactions bool
	// WithReactions keeps only messages carrying every one of these
	// reaction names; WithoutReactions drops messages carrying any of
	// them. With either set, the channel is scanned back to Oldest (set
	// it, or the scan covers the whole channel) until Limit messages
	// match.
	WithReactions    []string
	WithoutReactions []string
}

// FetchChannelHistory returns the most recent messages of a channel that
// match opts, in chronological order (oldest first). Thread replies are
// not included, only top-level messages.
func FetchChannelHistory(ctx context.Context, client *slackapi.Client, channelID string, opts HistoryOptions) ([]Message, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	limit = min(limit, MaxHistoryLimit)
	filtered := len(opts.WithReactions) > 0 || len(opts.WithoutReactions) > 0

	var out []Message
	latest, cursor := opts.Latest, ""
	for {
		params := map[string]any{"channel": channelID, "limit": pageSize}
		if !filtered {
			params["limit"] = min(limit-len(out), pageSize)
		}
		if latest != "" {
			params["latest"] = latest
		}
		if opts.Oldest != "" {
			params["oldest"] = opts.Oldest
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		metadataParam(params, opts.IncludeReactions || filtered)
		resp, err := client.API(ctx, "conversations.history", params)
		if err != nil {
			return nil, fmt.Errorf("fetching the history of %s: %w", channelID, err)
		}
		page := jsonx.Maps(resp["messages"])
		if len(page) == 0 {
			break
		}
		for _, m := range page {
			if filtered && !PassesReactionFilters(m, opts.WithReactions, opts.WithoutReactions) {
				continue
			}
			out = append(out, ParseMessage(channelID, m))
			if len(out) >= limit {
				break
			}
		}
		if len(out) >= limit || !jsonx.Bool(resp["has_more"]) {
			break
		}

		// Next page: Slack's cursor when it gives one, else everything
		// older than the last (oldest) message of this page.
		if next := nextCursor(resp); next != "" {
			if next == cursor {
				break
			}
			cursor = next
			continue
		}
		last := jsonx.Str(page[len(page)-1]["ts"])
		if last == "" || last == latest {
			break
		}
		latest, cursor = last, ""
	}

	if err := enrichFiles(ctx, client, out); err != nil {
		return nil, fmt.Errorf("fetching the history of %s: %w", channelID, err)
	}
	// conversations.history returns newest first.
	sortChronologically(out)
	return out, nil
}

// PassesReactionFilters reports whether a raw Slack message carries every
// reaction named in with and none of those named in without. Names are
// bare ("eyes", not ":eyes:").
func PassesReactionFilters(msg map[string]any, with, without []string) bool {
	names := map[string]bool{}
	for _, r := range jsonx.Maps(msg["reactions"]) {
		if name := strings.TrimSpace(jsonx.Str(r["name"])); name != "" {
			names[name] = true
		}
	}
	for _, name := range with {
		if !names[name] {
			return false
		}
	}
	for _, name := range without {
		if names[name] {
			return false
		}
	}
	return true
}

// FetchThread returns a whole thread, root first then every reply in
// chronological order. threadTS is normally the root's ts; given the ts
// of a reply, the thread it belongs to is returned all the same.
func FetchThread(ctx context.Context, client *slackapi.Client, channelID, threadTS string, includeReactions bool) ([]Message, error) {
	out, err := fetchReplies(ctx, client, channelID, threadTS, includeReactions)
	if err != nil {
		return nil, err
	}
	// Asked about a reply, Slack answers with that reply (alone, or
	// followed by whatever else it sees fit) instead of the thread: the
	// first message it returns then names the real root.
	if len(out) > 0 && out[0].ThreadTS != "" && out[0].ThreadTS != threadTS {
		out, err = fetchReplies(ctx, client, channelID, out[0].ThreadTS, includeReactions)
		if err != nil {
			return nil, err
		}
	}
	if err := enrichFiles(ctx, client, out); err != nil {
		return nil, fmt.Errorf("fetching thread %s in %s: %w", threadTS, channelID, err)
	}
	sortChronologically(out)
	return out, nil
}

func fetchReplies(ctx context.Context, client *slackapi.Client, channelID, threadTS string, includeReactions bool) ([]Message, error) {
	var out []Message
	seen := map[string]bool{}
	cursor := ""
	for {
		params := map[string]any{"channel": channelID, "ts": threadTS, "limit": pageSize}
		if cursor != "" {
			params["cursor"] = cursor
		}
		metadataParam(params, includeReactions)
		resp, err := client.API(ctx, "conversations.replies", params)
		if err != nil {
			return nil, fmt.Errorf("fetching thread %s in %s: %w", threadTS, channelID, err)
		}
		for _, m := range jsonx.Maps(resp["messages"]) {
			// Slack repeats the thread root on every page.
			ts := jsonx.Str(m["ts"])
			if ts != "" && seen[ts] {
				continue
			}
			seen[ts] = true
			out = append(out, ParseMessage(channelID, m))
		}
		next := nextCursor(resp)
		if next == "" || next == cursor {
			return out, nil
		}
		cursor = next
	}
}
