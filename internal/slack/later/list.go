package later

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/channels"
	"github.com/maelvls/slackcli/internal/slack/slacktext"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// State filters saved messages.
type State string

// The states of a saved message, plus StateAll to list them all.
const (
	StateInProgress State = "in_progress"
	StateArchived   State = "archived"
	StateCompleted  State = "completed"
	StateAll        State = "all"
)

// ParseState parses a --state value: in_progress (also in-progress,
// active, open), archived (archive), completed (complete, done) or all.
func ParseState(value string) (State, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "in_progress", "in-progress", "active", "open":
		return StateInProgress, nil
	case "archived", "archive":
		return StateArchived, nil
	case "completed", "complete", "done":
		return StateCompleted, nil
	case "all":
		return StateAll, nil
	}
	return "", fmt.Errorf("invalid --state value %q: use in_progress, archived, completed or all", value)
}

const (
	// DefaultLimit is the number of items listed when ListOptions.Limit is 0.
	DefaultLimit = 20
	// DefaultMaxBodyChars is the content length limit when
	// ListOptions.MaxBodyChars is 0.
	DefaultMaxBodyChars = 4000

	// pageSize is the saved.list page size. Composite cursors (see
	// encodeCursor) count items within a page, so it must not vary.
	pageSize = 50
	// hydrateConcurrency bounds the lookups made to hydrate items.
	hydrateConcurrency = 8
	cursorOffsetPrefix = "offset-"
)

// ListOptions selects saved messages.
type ListOptions struct {
	// State is the state to list; "" means StateInProgress.
	State State
	// Limit is the maximum number of items returned; 0 means DefaultLimit.
	Limit int
	// MaxBodyChars truncates message content to that many characters; 0
	// means DefaultMaxBodyChars, a negative value means no limit.
	MaxBodyChars int
	// CountsOnly returns the counts per state and no items.
	CountsOnly bool
	// Cursor is ListResult.NextCursor from the previous call.
	Cursor string
}

// Counts is the number of saved items per state, as Slack reports them
// (all item types, not only messages).
type Counts struct {
	InProgress int `json:"in_progress"`
	Archived   int `json:"archived"`
	Completed  int `json:"completed"`
	Total      int `json:"total"`
}

// Author is who posted a message.
type Author struct {
	UserID string `json:"user_id,omitempty"`
	BotID  string `json:"bot_id,omitempty"`
}

// Message is the content of a saved message.
type Message struct {
	Author *Author `json:"author,omitempty"`
	// Content is the message rendered as Markdown, possibly truncated.
	Content    string `json:"content,omitempty"`
	ThreadTS   string `json:"thread_ts,omitempty"`
	ReplyCount int    `json:"reply_count,omitempty"`
}

// Item is one saved message.
type Item struct {
	ChannelID   string `json:"channel_id"`
	ChannelName string `json:"channel_name,omitempty"`
	TS          string `json:"ts"`
	State       string `json:"state"`
	// DateSaved, DateCompleted and DateDue (the reminder) are Unix times in
	// seconds; the last two are 0 when not set.
	DateSaved     int64 `json:"date_saved"`
	DateCompleted int64 `json:"date_completed,omitempty"`
	DateDue       int64 `json:"date_due,omitempty"`
	// Message is nil when the message could not be fetched; MessageError
	// then says why ("message_not_found", or Slack's error code).
	Message      *Message `json:"message,omitempty"`
	MessageError string   `json:"message_error,omitempty"`
}

// ListResult is what `later list` prints.
type ListResult struct {
	// Counts is nil when Slack's answer carried none (it may only send
	// them with the first page), rather than a misleading row of zeros.
	Counts *Counts `json:"counts,omitempty"`
	Items  []Item  `json:"items"`
	// NextCursor is set when there may be more items: pass it as
	// ListOptions.Cursor to continue right after the last item returned.
	NextCursor string `json:"next_cursor,omitempty"`
}

// encodeCursor builds the cursor that resumes offset items into the
// saved.list page that slackCursor yields. Slack's cursors only move by
// whole pages, while a listing stops wherever its limit is reached:
// without the offset the rest of that page would be skipped.
func encodeCursor(slackCursor string, offset int) string {
	if offset == 0 {
		return slackCursor
	}
	return cursorOffsetPrefix + strconv.Itoa(offset) + ":" + slackCursor
}

func decodeCursor(cursor string) (slackCursor string, offset int, err error) {
	rest, ok := strings.CutPrefix(cursor, cursorOffsetPrefix)
	if !ok {
		return cursor, 0, nil
	}
	n, slackCursor, ok := strings.Cut(rest, ":")
	offset, convErr := strconv.Atoi(n)
	if !ok || convErr != nil || offset < 0 {
		return "", 0, fmt.Errorf("invalid cursor %q: pass the next_cursor value of the previous page", cursor)
	}
	return slackCursor, offset, nil
}

// List returns the saved messages in opts.State, in Slack's order (most
// recently saved first), with their channel name and content, plus the
// counts per state. Saved items that are not messages (reminders, files)
// are skipped.
//
// A message that cannot be fetched (deleted, channel no longer visible) is
// still listed, with Item.MessageError set.
func List(ctx context.Context, client *slackapi.Client, opts ListOptions) (ListResult, error) {
	if err := requireBrowser(client); err != nil {
		return ListResult{}, err
	}
	state := opts.State
	if state == "" {
		state = StateInProgress
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	slackCursor, offset, err := decodeCursor(opts.Cursor)
	if err != nil {
		return ListResult{}, err
	}

	var result ListResult
	matches := func(item map[string]any) bool {
		return jsonx.Str(item["item_type"]) == "message" &&
			(state == StateAll || State(itemState(item)) == state)
	}
	seen := map[string]bool{slackCursor: true}
	var raw []map[string]any
paging:
	for first := true; ; first = false {
		params := map[string]any{"limit": pageSize}
		if slackCursor != "" {
			params["cursor"] = slackCursor
		}
		resp, err := client.API(ctx, "saved.list", params)
		if err != nil {
			return ListResult{}, fmt.Errorf("listing saved items: %w", err)
		}
		if first {
			result.Counts = parseCounts(jsonx.Map(resp["counts"]))
			if opts.CountsOnly {
				return result, nil
			}
		}
		page := jsonx.Maps(resp["saved_items"])
		next := jsonx.GetStr(resp, "response_metadata", "next_cursor")
		// A cursor Slack already handed out would loop forever.
		if seen[next] {
			next = ""
		}
		for i := min(offset, len(page)); i < len(page); i++ {
			if !matches(page[i]) {
				continue
			}
			raw = append(raw, page[i])
			if len(raw) < limit {
				continue
			}
			// Full: resume right after this item, unless nothing is left.
			if anyMatch(page[i+1:], matches) {
				result.NextCursor = encodeCursor(slackCursor, i+1)
			} else {
				result.NextCursor = next
			}
			break paging
		}
		if next == "" {
			break
		}
		seen[next] = true
		slackCursor, offset = next, 0
	}

	result.Items, err = hydrate(ctx, client, raw, opts.MaxBodyChars)
	if err != nil {
		return ListResult{}, err
	}
	return result, nil
}

func anyMatch(items []map[string]any, matches func(map[string]any) bool) bool {
	for _, item := range items {
		if matches(item) {
			return true
		}
	}
	return false
}

func itemState(item map[string]any) string {
	if s := jsonx.Str(item["state"]); s != "" {
		return s
	}
	return string(StateInProgress)
}

func parseCounts(counts map[string]any) *Counts {
	if counts == nil {
		return nil
	}
	n := func(key string) int {
		v, _ := jsonx.Int(counts[key])
		return v
	}
	return &Counts{
		InProgress: n("uncompleted_count"),
		Archived:   n("archived_count"),
		Completed:  n("completed_count"),
		Total:      n("total_count"),
	}
}

func unixSeconds(v any) int64 {
	n, _ := jsonx.Num(v)
	return max(int64(n), 0)
}

// hydrate turns raw saved items into Items, resolving each channel name
// once and fetching each message, with bounded concurrency.
func hydrate(ctx context.Context, client *slackapi.Client, raw []map[string]any, maxBodyChars int) ([]Item, error) {
	if maxBodyChars == 0 {
		maxBodyChars = DefaultMaxBodyChars
	}
	items := make([]Item, len(raw))
	var channelIDs []string
	names := map[string]*string{}
	for i, r := range raw {
		items[i] = Item{
			ChannelID:     jsonx.Str(r["item_id"]),
			TS:            jsonx.Str(r["ts"]),
			State:         itemState(r),
			DateSaved:     unixSeconds(r["date_created"]),
			DateCompleted: unixSeconds(r["date_completed"]),
			DateDue:       unixSeconds(r["date_due"]),
		}
		if id := items[i].ChannelID; id != "" && names[id] == nil {
			names[id] = new(string)
			channelIDs = append(channelIDs, id)
		}
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	sem := make(chan struct{}, hydrateConcurrency)
	run := func(task func()) {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() == nil {
				task()
			}
		})
	}
	for _, id := range channelIDs {
		run(func() {
			// ResolveName answers the ID itself when it finds no name.
			if name := channels.ResolveName(ctx, client, id); name != id {
				*names[id] = name
			}
		})
	}
	for i := range items {
		item := &items[i]
		if item.ChannelID == "" || item.TS == "" {
			continue
		}
		run(func() {
			msg, err := fetchMessage(ctx, client, item.ChannelID, item.TS)
			switch {
			case isFatal(ctx, err):
				cancel(fmt.Errorf("fetching saved message %s in %s: %w", item.TS, item.ChannelID, err))
			case err != nil:
				item.MessageError = messageErrorCode(err)
			default:
				item.Message = toMessage(msg, maxBodyChars)
			}
		})
	}
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	for i := range items {
		if name := names[items[i].ChannelID]; name != nil {
			items[i].ChannelName = *name
		}
	}
	return items, nil
}

var errMessageNotFound = errors.New("message_not_found")

func messageErrorCode(err error) string {
	if code := slackapi.ErrorCode(err); code != "" {
		return code
	}
	return err.Error()
}

// fetchMessage returns the raw message with that ts: a top-level message
// of the channel, or a thread reply (replies are not part of the channel
// history, and a saved reply would otherwise be listed without content).
func fetchMessage(ctx context.Context, client *slackapi.Client, channelID, ts string) (map[string]any, error) {
	resp, err := client.API(ctx, "conversations.history", map[string]any{
		"channel": channelID, "latest": ts, "inclusive": true, "limit": 1,
	})
	if err != nil {
		return nil, err
	}
	if m := findTS(resp, ts); m != nil {
		return m, nil
	}
	// conversations.replies accepts the ts of any message of a thread and
	// returns that message first.
	resp, err = client.API(ctx, "conversations.replies", map[string]any{"channel": channelID, "ts": ts, "limit": 1})
	if isFatal(ctx, err) {
		return nil, err
	}
	if m := findTS(resp, ts); m != nil {
		return m, nil
	}
	return nil, errMessageNotFound
}

func findTS(resp map[string]any, ts string) map[string]any {
	for _, m := range jsonx.Maps(resp["messages"]) {
		if jsonx.Str(m["ts"]) == ts {
			return m
		}
	}
	return nil
}

func toMessage(msg map[string]any, maxBodyChars int) *Message {
	out := &Message{
		Content:  truncate(slacktext.RenderMessageContent(msg), maxBodyChars),
		ThreadTS: jsonx.Str(msg["thread_ts"]),
	}
	out.ReplyCount, _ = jsonx.Int(msg["reply_count"])
	if user, bot := jsonx.Str(msg["user"]), jsonx.Str(msg["bot_id"]); user != "" || bot != "" {
		out.Author = &Author{UserID: user, BotID: bot}
	}
	return out
}

// truncate cuts s to maxChars characters (negative: no limit), on a
// character boundary, and marks the cut.
func truncate(s string, maxChars int) string {
	if maxChars < 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s
	}
	return string(runes[:maxChars]) + "\n…"
}
