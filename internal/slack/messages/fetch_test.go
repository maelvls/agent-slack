package messages

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/slackcli/internal/slack/slackurl"
	"github.com/maelvls/slackcli/internal/slackapi"
)

type call struct {
	method string
	params map[string]any
}

// recorder is a fake Slack API that records the calls it serves.
type recorder struct {
	mu    sync.Mutex
	calls []call
	serve func(method string, params map[string]any) (map[string]any, error)
}

func (r *recorder) client() *slackapi.Client {
	return slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		r.mu.Lock()
		r.calls = append(r.calls, call{method, params})
		r.mu.Unlock()
		return r.serve(method, params)
	})
}

func (r *recorder) methods() []string {
	var out []string
	for _, c := range r.calls {
		out = append(out, c.method)
	}
	return out
}

func (r *recorder) only(method string) []call {
	var out []call
	for _, c := range r.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func apiError(code string) map[string]any { return map[string]any{"ok": false, "error": code} }

func raw(ts, text string, extra ...any) map[string]any {
	m := map[string]any{"ts": ts, "text": text}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

func anys(msgs ...map[string]any) []any {
	out := make([]any, len(msgs))
	for i, m := range msgs {
		out[i] = m
	}
	return out
}

func tsList(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.TS
	}
	return out
}

func tsOf(n int) string { return fmt.Sprintf("17000%05d.000000", n) }

func TestCompareTS(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.000001", "2.000002", -1},
		{"2.000002", "1.000001", 1},
		{"1770165109.628379", "1770165109.628379", 0},
		// One microsecond apart: identical once parsed as float64.
		{"1770165109.628379", "1770165109.628380", -1},
		{"17701651090.628379", "17701651090.628380", -1},
		{"999999999.999999", "1000000000.000000", -1},
		{"1770165109.5", "1770165109.49", 1},
		{"1770165109.5", "1770165109.500000", 0},
		{"1770165109", "1770165109.000001", -1},
		{"", "1.000001", -1},
	}
	for _, tt := range tests {
		got := CompareTS(tt.a, tt.b)
		if (got < 0) != (tt.want < 0) || (got > 0) != (tt.want > 0) {
			t.Errorf("CompareTS(%q, %q) = %d, want sign of %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestFetchThread(t *testing.T) {
	rec := &recorder{serve: func(string, map[string]any) (map[string]any, error) {
		return map[string]any{"messages": anys(
			raw("2.000002", "reply", "thread_ts", "1.000001", "user", "U22222222"),
			raw("1.000001", "root", "user", "U11111111"),
		)}, nil
	}}
	msgs, err := FetchThread(context.Background(), rec.client(), "C12345678", "1.000001", false)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []call{{"conversations.replies", map[string]any{"channel": "C12345678", "ts": "1.000001", "limit": 200}}}
	if !reflect.DeepEqual(rec.calls, wantCalls) {
		t.Errorf("calls = %v", rec.calls)
	}
	if len(msgs) != 2 || msgs[0].TS != "1.000001" || msgs[0].Text != "root" || msgs[1].TS != "2.000002" || msgs[1].Text != "reply" {
		t.Errorf("messages = %+v", msgs)
	}
	if msgs[1].ChannelID != "C12345678" || msgs[1].ThreadTS != "1.000001" || msgs[1].User != "U22222222" {
		t.Errorf("reply = %+v", msgs[1])
	}
}

func TestFetchThreadFollowsCursorsAndDropsRepeatedRoot(t *testing.T) {
	root := raw(tsOf(0), "root", "thread_ts", tsOf(0), "reply_count", float64(4))
	pages := map[string]map[string]any{
		"": {"messages": anys(root, raw(tsOf(1), "r1", "thread_ts", tsOf(0)), raw(tsOf(2), "r2", "thread_ts", tsOf(0))),
			"response_metadata": map[string]any{"next_cursor": "c1"}},
		"c1": {"messages": anys(root, raw(tsOf(3), "r3", "thread_ts", tsOf(0))),
			"response_metadata": map[string]any{"next_cursor": "c2"}},
		"c2": {"messages": anys(root, raw(tsOf(4), "r4", "thread_ts", tsOf(0)))},
	}
	rec := &recorder{serve: func(_ string, params map[string]any) (map[string]any, error) {
		cursor, _ := params["cursor"].(string)
		return pages[cursor], nil
	}}
	msgs, err := FetchThread(context.Background(), rec.client(), "C12345678", tsOf(0), true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{tsOf(0), tsOf(1), tsOf(2), tsOf(3), tsOf(4)}; !reflect.DeepEqual(tsList(msgs), want) {
		t.Errorf("ts = %v, want %v", tsList(msgs), want)
	}
	if len(rec.calls) != 3 || rec.calls[2].params["cursor"] != "c2" || rec.calls[0].params["include_all_metadata"] != true {
		t.Errorf("calls = %v", rec.calls)
	}
	if msgs[0].ReplyCount != 4 {
		t.Errorf("root = %+v", msgs[0])
	}
}

func TestFetchThreadFromReplyTS(t *testing.T) {
	rec := &recorder{serve: func(_ string, params map[string]any) (map[string]any, error) {
		if params["ts"] == tsOf(2) {
			// Slack answers a reply's ts with that reply alone.
			return map[string]any{"messages": anys(raw(tsOf(2), "r2", "thread_ts", tsOf(0)))}, nil
		}
		return map[string]any{"messages": anys(
			raw(tsOf(0), "root", "thread_ts", tsOf(0)),
			raw(tsOf(1), "r1", "thread_ts", tsOf(0)),
			raw(tsOf(2), "r2", "thread_ts", tsOf(0)),
		)}, nil
	}}
	msgs, err := FetchThread(context.Background(), rec.client(), "C12345678", tsOf(2), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{tsOf(0), tsOf(1), tsOf(2)}; !reflect.DeepEqual(tsList(msgs), want) {
		t.Errorf("ts = %v, want %v", tsList(msgs), want)
	}
}

func TestFetchThreadStopsOnStuckCursorAndErrors(t *testing.T) {
	rec := &recorder{serve: func(string, map[string]any) (map[string]any, error) {
		return map[string]any{"messages": anys(raw(tsOf(0), "root")), "response_metadata": map[string]any{"next_cursor": "same"}}, nil
	}}
	msgs, err := FetchThread(context.Background(), rec.client(), "C12345678", tsOf(0), false)
	if err != nil || len(msgs) != 1 || len(rec.calls) != 2 {
		t.Errorf("msgs = %v, err = %v, calls = %d", tsList(msgs), err, len(rec.calls))
	}

	rec = &recorder{serve: func(string, map[string]any) (map[string]any, error) { return apiError("thread_not_found"), nil }}
	if _, err := FetchThread(context.Background(), rec.client(), "C12345678", tsOf(0), false); !slackapi.IsErrorCode(err, "thread_not_found") {
		t.Errorf("err = %v", err)
	}
}

func TestFetchThreadOrdersSameInstantReplies(t *testing.T) {
	rec := &recorder{serve: func(string, map[string]any) (map[string]any, error) {
		return map[string]any{"messages": anys(
			raw("17701651090.628380", "second"),
			raw("17701651090.628379", "first"),
		)}, nil
	}}
	msgs, err := FetchThread(context.Background(), rec.client(), "C12345678", "17701651090.628379", false)
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].Text != "first" || msgs[1].Text != "second" {
		t.Errorf("order = %v", tsList(msgs))
	}
}

// channel is a fake channel history, newest message first.
type channel struct {
	msgs       []map[string]any
	useCursors bool
}

func newChannel(n int, useCursors bool) *channel {
	c := &channel{useCursors: useCursors}
	for i := n; i >= 1; i-- {
		c.msgs = append(c.msgs, raw(tsOf(i), "m"+strconv.Itoa(i)))
	}
	return c
}

func (c *channel) history(params map[string]any) (map[string]any, error) {
	latest, _ := params["latest"].(string)
	oldest, _ := params["oldest"].(string)
	var matching []map[string]any
	for _, m := range c.msgs {
		ts := m["ts"].(string)
		if (latest == "" || CompareTS(ts, latest) < 0) && (oldest == "" || CompareTS(ts, oldest) > 0) {
			matching = append(matching, m)
		}
	}
	start := 0
	if cur, _ := params["cursor"].(string); cur != "" {
		start, _ = strconv.Atoi(cur)
	}
	end := min(start+params["limit"].(int), len(matching))
	resp := map[string]any{"messages": anys(matching[start:end]...), "has_more": end < len(matching)}
	if c.useCursors && end < len(matching) {
		resp["response_metadata"] = map[string]any{"next_cursor": strconv.Itoa(end)}
	}
	return resp, nil
}

func (c *channel) recorder() *recorder {
	return &recorder{serve: func(method string, params map[string]any) (map[string]any, error) {
		if method != "conversations.history" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return c.history(params)
	}}
}

func TestFetchChannelHistoryDefaults(t *testing.T) {
	rec := newChannel(60, true).recorder()
	msgs, err := FetchChannelHistory(context.Background(), rec.client(), "C12345678", HistoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []call{{"conversations.history", map[string]any{"channel": "C12345678", "limit": 25}}}
	if !reflect.DeepEqual(rec.calls, wantCalls) {
		t.Errorf("calls = %v", rec.calls)
	}
	// The 25 most recent, oldest first.
	if len(msgs) != 25 || msgs[0].TS != tsOf(36) || msgs[24].TS != tsOf(60) {
		t.Errorf("got %d messages: %v", len(msgs), tsList(msgs))
	}
	if msgs[0].ChannelID != "C12345678" || msgs[0].Text != "m36" {
		t.Errorf("first = %+v", msgs[0])
	}
}

func TestFetchChannelHistoryForwardsBounds(t *testing.T) {
	rec := newChannel(60, true).recorder()
	msgs, err := FetchChannelHistory(context.Background(), rec.client(), "C12345678",
		HistoryOptions{Limit: 5, Oldest: tsOf(10), Latest: tsOf(14), IncludeReactions: true})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"channel": "C12345678", "limit": 5, "oldest": tsOf(10), "latest": tsOf(14), "include_all_metadata": true}
	if !reflect.DeepEqual(rec.calls[0].params, want) {
		t.Errorf("params = %v", rec.calls[0].params)
	}
	// Both bounds are exclusive.
	if want := []string{tsOf(11), tsOf(12), tsOf(13)}; !reflect.DeepEqual(tsList(msgs), want) {
		t.Errorf("ts = %v, want %v", tsList(msgs), want)
	}
}

// The TypeScript silently capped --limit at 200.
func TestFetchChannelHistoryPagesBeyondOnePage(t *testing.T) {
	for _, useCursors := range []bool{true, false} {
		rec := newChannel(700, useCursors).recorder()
		msgs, err := FetchChannelHistory(context.Background(), rec.client(), "C12345678", HistoryOptions{Limit: 450})
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 450 || msgs[0].TS != tsOf(251) || msgs[449].TS != tsOf(700) {
			t.Errorf("cursors=%v: got %d messages, %s … %s", useCursors, len(msgs), msgs[0].TS, msgs[len(msgs)-1].TS)
		}
		var limits []any
		for _, c := range rec.calls {
			limits = append(limits, c.params["limit"])
		}
		if !reflect.DeepEqual(limits, []any{200, 200, 50}) {
			t.Errorf("cursors=%v: page limits = %v", useCursors, limits)
		}
		seen := map[string]bool{}
		for _, m := range msgs {
			if seen[m.TS] {
				t.Fatalf("cursors=%v: duplicate %s", useCursors, m.TS)
			}
			seen[m.TS] = true
		}
	}
}

func TestFetchChannelHistoryLimitClamping(t *testing.T) {
	rec := newChannel(1500, true).recorder()
	msgs, err := FetchChannelHistory(context.Background(), rec.client(), "C12345678", HistoryOptions{Limit: 5000})
	if err != nil || len(msgs) != MaxHistoryLimit {
		t.Errorf("got %d messages, err = %v", len(msgs), err)
	}

	rec = newChannel(60, true).recorder()
	msgs, err = FetchChannelHistory(context.Background(), rec.client(), "C12345678", HistoryOptions{Limit: -3})
	if err != nil || len(msgs) != DefaultHistoryLimit {
		t.Errorf("got %d messages, err = %v", len(msgs), err)
	}

	// Fewer messages than asked for: one call, no spinning.
	rec = newChannel(3, true).recorder()
	msgs, err = FetchChannelHistory(context.Background(), rec.client(), "C12345678", HistoryOptions{Limit: 100})
	if err != nil || len(msgs) != 3 || len(rec.calls) != 1 {
		t.Errorf("got %d messages in %d calls, err = %v", len(msgs), len(rec.calls), err)
	}
}

func TestFetchChannelHistoryReactionFilters(t *testing.T) {
	for _, useCursors := range []bool{true, false} {
		ch := newChannel(500, useCursors)
		// Every 100th message has :dart:; one of those also has :eyes:.
		for _, m := range ch.msgs {
			n, _ := strconv.Atoi(strings.TrimPrefix(m["text"].(string), "m"))
			if n%100 == 0 {
				m["reactions"] = anys(map[string]any{"name": "dart", "users": []any{"U11111111"}})
			}
			if n == 300 {
				m["reactions"] = append(m["reactions"].([]any), map[string]any{"name": "eyes"})
			}
		}
		rec := ch.recorder()
		msgs, err := FetchChannelHistory(context.Background(), rec.client(), "C12345678", HistoryOptions{
			Limit: 3, Oldest: tsOf(50), WithReactions: []string{"dart"}, WithoutReactions: []string{"eyes"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{tsOf(200), tsOf(400), tsOf(500)}; !reflect.DeepEqual(tsList(msgs), want) {
			t.Errorf("cursors=%v: ts = %v, want %v", useCursors, tsList(msgs), want)
		}
		for _, c := range rec.calls {
			if c.params["limit"] != 200 || c.params["include_all_metadata"] != true || c.params["oldest"] != tsOf(50) {
				t.Errorf("cursors=%v: params = %v", useCursors, c.params)
			}
		}
		if len(rec.calls) != 2 {
			t.Errorf("cursors=%v: %d calls, want 2", useCursors, len(rec.calls))
		}
		if len(msgs) > 0 && len(msgs[0].Reactions) != 1 {
			t.Errorf("reactions = %v", msgs[0].Reactions)
		}

		// Not enough matches: the scan ends at Oldest.
		rec = ch.recorder()
		msgs, err = FetchChannelHistory(context.Background(), rec.client(), "C12345678", HistoryOptions{
			Limit: 10, Oldest: tsOf(250), WithReactions: []string{"dart"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{tsOf(300), tsOf(400), tsOf(500)}; !reflect.DeepEqual(tsList(msgs), want) {
			t.Errorf("cursors=%v: ts = %v, want %v", useCursors, tsList(msgs), want)
		}
	}
}

func TestFetchChannelHistoryCannotSpin(t *testing.T) {
	tests := map[string]map[string]any{
		"same cursor forever": {
			"messages": anys(raw(tsOf(1), "m")), "has_more": true,
			"response_metadata": map[string]any{"next_cursor": "same"},
		},
		"has_more without cursor nor progress": {
			"messages": anys(raw(tsOf(1), "m")), "has_more": true,
		},
		"has_more with messages lacking ts": {
			"messages": anys(map[string]any{"text": "no ts"}), "has_more": true,
		},
	}
	for name, resp := range tests {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{serve: func(string, map[string]any) (map[string]any, error) { return resp, nil }}
			_, err := FetchChannelHistory(context.Background(), rec.client(), "C12345678", HistoryOptions{Limit: 1000})
			if err != nil {
				t.Fatal(err)
			}
			if len(rec.calls) > 3 {
				t.Errorf("%d calls", len(rec.calls))
			}
		})
	}
}

func TestFetchChannelHistoryError(t *testing.T) {
	rec := &recorder{serve: func(string, map[string]any) (map[string]any, error) { return apiError("channel_not_found"), nil }}
	_, err := FetchChannelHistory(context.Background(), rec.client(), "C12345678", HistoryOptions{})
	if !slackapi.IsErrorCode(err, "channel_not_found") || !strings.Contains(err.Error(), "C12345678") {
		t.Errorf("err = %v", err)
	}
}

func TestPassesReactionFilters(t *testing.T) {
	reactions := func(names ...string) map[string]any {
		var rs []any
		for _, n := range names {
			rs = append(rs, map[string]any{"name": n})
		}
		return map[string]any{"reactions": rs}
	}
	tests := []struct {
		name          string
		msg           map[string]any
		with, without []string
		want          bool
	}{
		{"required reaction present", reactions("dart"), []string{"dart"}, nil, true},
		{"required reaction missing", reactions("eyes"), []string{"dart"}, nil, false},
		{"excluded reaction present", reactions("dart"), nil, []string{"dart"}, false},
		{"excluded reaction absent", reactions("eyes"), nil, []string{"dart"}, true},
		{"with + without together", reactions("eyes", "white_check_mark"), []string{"eyes"}, []string{"dart"}, true},
		{"every required reaction is needed", reactions("eyes"), []string{"eyes", "dart"}, nil, false},
		{"no reactions, no filters", map[string]any{}, nil, nil, true},
		{"no reactions, required", map[string]any{}, []string{"dart"}, nil, false},
		{"names are trimmed", map[string]any{"reactions": []any{map[string]any{"name": " dart "}, "junk"}}, []string{"dart"}, nil, true},
	}
	for _, tt := range tests {
		if got := PassesReactionFilters(tt.msg, tt.with, tt.without); got != tt.want {
			t.Errorf("%s: got %v", tt.name, got)
		}
	}
}

var testRef = slackurl.MessageRef{WorkspaceURL: "https://test.slack.com", ChannelID: "C12345678", MessageTS: tsOf(5)}

func TestFetchMessageFromHistory(t *testing.T) {
	rec := &recorder{serve: func(string, map[string]any) (map[string]any, error) {
		return map[string]any{"messages": anys(raw(tsOf(5), "hello", "user", "U11111111", "bot_id", "B11111111",
			"reply_count", float64(2), "thread_ts", tsOf(5),
			"blocks", anys(map[string]any{"type": "divider"}),
			"attachments", anys(map[string]any{"text": "att"}),
			"reactions", anys(map[string]any{"name": "eyes"}),
		))}, nil
	}}
	msg, err := FetchMessage(context.Background(), rec.client(), testRef, true)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []call{{"conversations.history", map[string]any{
		"channel": "C12345678", "latest": tsOf(5), "inclusive": true, "limit": 1, "include_all_metadata": true,
	}}}
	if !reflect.DeepEqual(rec.calls, wantCalls) {
		t.Errorf("calls = %v", rec.calls)
	}
	if msg.ChannelID != "C12345678" || msg.TS != tsOf(5) || msg.Text != "hello" || msg.User != "U11111111" || msg.BotID != "B11111111" ||
		msg.ReplyCount != 2 || msg.ThreadTS != tsOf(5) || len(msg.Blocks) != 1 || len(msg.Attachments) != 1 || len(msg.Reactions) != 1 {
		t.Errorf("message = %+v", msg)
	}
}

func TestFetchMessageThreadReply(t *testing.T) {
	// History answers with the nearest older top-level message.
	history := map[string]any{"messages": anys(raw(tsOf(3), "older top-level"))}

	t.Run("with a thread_ts hint", func(t *testing.T) {
		ref := testRef
		ref.ThreadTSHint = tsOf(1)
		rec := &recorder{serve: func(method string, params map[string]any) (map[string]any, error) {
			if method == "conversations.history" {
				return history, nil
			}
			return map[string]any{"messages": anys(raw(tsOf(1), "root"), raw(tsOf(5), "the reply", "thread_ts", tsOf(1)))}, nil
		}}
		msg, err := FetchMessage(context.Background(), rec.client(), ref, false)
		if err != nil {
			t.Fatal(err)
		}
		if msg.Text != "the reply" || msg.ThreadTS != tsOf(1) {
			t.Errorf("message = %+v", msg)
		}
		// One bounded call instead of paging through the whole thread.
		want := map[string]any{"channel": "C12345678", "ts": tsOf(1), "oldest": tsOf(5), "latest": tsOf(5), "inclusive": true, "limit": 200}
		if replies := rec.only("conversations.replies"); len(replies) != 1 || !reflect.DeepEqual(replies[0].params, want) {
			t.Errorf("replies calls = %v", replies)
		}
	})

	t.Run("hint pages when Slack ignores the bounds", func(t *testing.T) {
		ref := testRef
		ref.ThreadTSHint = tsOf(1)
		rec := &recorder{serve: func(method string, params map[string]any) (map[string]any, error) {
			if method == "conversations.history" {
				return history, nil
			}
			if params["cursor"] == nil {
				return map[string]any{"messages": anys(raw(tsOf(1), "root"), raw(tsOf(2), "r")), "response_metadata": map[string]any{"next_cursor": "c1"}}, nil
			}
			return map[string]any{"messages": anys(raw(tsOf(1), "root"), raw(tsOf(5), "the reply"))}, nil
		}}
		msg, err := FetchMessage(context.Background(), rec.client(), ref, false)
		if err != nil || msg.Text != "the reply" {
			t.Errorf("message = %+v, err = %v", msg, err)
		}
	})

	t.Run("without a hint", func(t *testing.T) {
		rec := &recorder{serve: func(method string, params map[string]any) (map[string]any, error) {
			if method == "conversations.history" {
				return history, nil
			}
			return map[string]any{"messages": anys(raw(tsOf(5), "the reply", "thread_ts", tsOf(1)))}, nil
		}}
		msg, err := FetchMessage(context.Background(), rec.client(), testRef, false)
		if err != nil || msg.Text != "the reply" {
			t.Fatalf("message = %+v, err = %v", msg, err)
		}
		want := []call{
			{"conversations.history", map[string]any{"channel": "C12345678", "latest": tsOf(5), "inclusive": true, "limit": 1}},
			{"conversations.replies", map[string]any{"channel": "C12345678", "ts": tsOf(5), "limit": 1}},
		}
		if !reflect.DeepEqual(rec.calls, want) {
			t.Errorf("calls = %v", rec.calls)
		}
	})

	t.Run("wrong hint falls back to the message's own ts", func(t *testing.T) {
		ref := testRef
		ref.ThreadTSHint = tsOf(2)
		rec := &recorder{serve: func(method string, params map[string]any) (map[string]any, error) {
			switch {
			case method == "conversations.history":
				return history, nil
			case params["ts"] == tsOf(2):
				return apiError("thread_not_found"), nil
			}
			return map[string]any{"messages": anys(raw(tsOf(5), "the reply"))}, nil
		}}
		msg, err := FetchMessage(context.Background(), rec.client(), ref, false)
		if err != nil || msg.Text != "the reply" {
			t.Errorf("message = %+v, err = %v", msg, err)
		}
	})
}

func TestFetchMessageNotFound(t *testing.T) {
	rec := &recorder{serve: func(method string, _ map[string]any) (map[string]any, error) {
		if method == "conversations.history" {
			return map[string]any{"messages": anys()}, nil
		}
		return apiError("thread_not_found"), nil
	}}
	_, err := FetchMessage(context.Background(), rec.client(), testRef, false)
	if !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("err = %v, want ErrMessageNotFound", err)
	}
	for _, want := range []string{tsOf(5), "C12345678", "deleted"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestFetchMessageErrors(t *testing.T) {
	t.Run("history error propagates", func(t *testing.T) {
		rec := &recorder{serve: func(string, map[string]any) (map[string]any, error) { return apiError("channel_not_found"), nil }}
		_, err := FetchMessage(context.Background(), rec.client(), testRef, false)
		if !slackapi.IsErrorCode(err, "channel_not_found") || errors.Is(err, ErrMessageNotFound) {
			t.Errorf("err = %v", err)
		}
	})

	// The TypeScript reported these as "Message not found".
	fatal := map[string]func(method string) (map[string]any, error){
		"expired credentials": func(string) (map[string]any, error) { return apiError("invalid_auth"), nil },
		"rate limit": func(method string) (map[string]any, error) {
			return nil, &slackapi.RateLimitError{Method: method, RetryAfter: time.Minute}
		},
	}
	for name, fail := range fatal {
		for _, hint := range []string{"", tsOf(1)} {
			t.Run(name+" in the replies fallback, hint="+hint, func(t *testing.T) {
				ref := testRef
				ref.ThreadTSHint = hint
				rec := &recorder{serve: func(method string, _ map[string]any) (map[string]any, error) {
					if method == "conversations.history" {
						return map[string]any{"messages": anys()}, nil
					}
					return fail(method)
				}}
				_, err := FetchMessage(context.Background(), rec.client(), ref, false)
				var rl *slackapi.RateLimitError
				if errors.Is(err, ErrMessageNotFound) || !(slackapi.IsAuthError(err) || errors.As(err, &rl)) {
					t.Errorf("err = %v", err)
				}
				if len(rec.calls) != 2 {
					t.Errorf("calls = %v", rec.methods())
				}
			})
		}
	}
}

func TestParseFile(t *testing.T) {
	got, ok := ParseFile(map[string]any{
		"id": "F1", "name": "a.png", "title": "A", "mimetype": "image/png", "filetype": "png", "mode": "hosted",
		"permalink": "https://x.slack.com/files/U1/F1/a.png", "url_private": "https://files.slack.com/a",
		"url_private_download": "https://files.slack.com/a?dl", "size": float64(1234),
	})
	want := FileSummary{ID: "F1", Name: "a.png", Title: "A", Mimetype: "image/png", Filetype: "png", Mode: "hosted",
		Permalink: "https://x.slack.com/files/U1/F1/a.png", URLPrivate: "https://files.slack.com/a",
		URLPrivateDownload: "https://files.slack.com/a?dl", Size: 1234}
	if !ok || got != want {
		t.Errorf("ParseFile = %+v, %v", got, ok)
	}
	for _, v := range []any{nil, "F1", map[string]any{"name": "no id"}, map[string]any{"id": 12}} {
		if _, ok := ParseFile(v); ok {
			t.Errorf("ParseFile(%v) ok", v)
		}
	}
}

func TestFetchEnrichesFiles(t *testing.T) {
	complete := map[string]any{"id": "F1", "name": "a.png", "url_private_download": "https://files.slack.com/a"}
	snippet := map[string]any{"id": "F2", "mode": "snippet", "url_private_download": "https://files.slack.com/s"}
	partial := map[string]any{"id": "F3", "title": "Kept title"}
	broken := map[string]any{"id": "F4"}

	var mu sync.Mutex
	inFlight, peak := 0, 0
	rec := &recorder{}
	rec.serve = func(method string, params map[string]any) (map[string]any, error) {
		if method == "conversations.replies" {
			return map[string]any{"messages": anys(
				raw(tsOf(1), "root", "files", []any{complete, snippet, "junk"}),
				raw(tsOf(2), "reply", "files", anys(partial, broken)),
				raw(tsOf(3), "same snippet again", "files", anys(snippet)),
			)}, nil
		}
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		switch params["file"] {
		case "F2":
			return map[string]any{"file": map[string]any{"id": "F2", "name": "main.go", "filetype": "go", "content": "package main"}}, nil
		case "F3":
			return map[string]any{"file": map[string]any{"id": "F3", "title": "Other title", "mimetype": "application/pdf", "url_private_download": "https://files.slack.com/p"}}, nil
		}
		return apiError("file_not_found"), nil
	}
	msgs, err := FetchThread(context.Background(), rec.client(), "C12345678", tsOf(1), false)
	if err != nil {
		t.Fatal(err)
	}

	// One lookup per incomplete file, none for the complete one.
	var looked []string
	for _, c := range rec.only("files.info") {
		looked = append(looked, c.params["file"].(string))
	}
	if len(looked) != 3 || !slicesEqualUnordered(looked, []string{"F2", "F3", "F4"}) {
		t.Errorf("files.info lookups = %v", looked)
	}
	if peak > filesInfoConcurrency {
		t.Errorf("peak concurrency = %d", peak)
	}

	wantSnippet := FileSummary{ID: "F2", Name: "main.go", Filetype: "go", Mode: "snippet", URLPrivateDownload: "https://files.slack.com/s"}
	for _, got := range []FileSummary{msgs[0].Files[1], msgs[2].Files[0]} {
		if got.Snippet == nil || *got.Snippet != (FileSnippet{Content: "package main", Language: "go"}) {
			t.Fatalf("snippet = %+v", got.Snippet)
		}
		got.Snippet = nil
		if got != wantSnippet {
			t.Errorf("snippet file = %+v", got)
		}
	}
	if got := msgs[0].Files[0]; got != (FileSummary{ID: "F1", Name: "a.png", URLPrivateDownload: "https://files.slack.com/a"}) {
		t.Errorf("complete file = %+v", got)
	}
	if got := msgs[1].Files[0]; got != (FileSummary{ID: "F3", Title: "Kept title", Mimetype: "application/pdf", URLPrivateDownload: "https://files.slack.com/p"}) {
		t.Errorf("partial file = %+v", got)
	}
	// A failed lookup keeps the summary.
	if got := msgs[1].Files[1]; got != (FileSummary{ID: "F4"}) {
		t.Errorf("broken file = %+v", got)
	}
}

func slicesEqualUnordered(a, b []string) bool {
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestFetchFailsWhenFileLookupHitsExpiredCredentials(t *testing.T) {
	rec := &recorder{serve: func(method string, _ map[string]any) (map[string]any, error) {
		if method == "files.info" {
			return apiError("token_expired"), nil
		}
		return map[string]any{"messages": anys(raw(tsOf(5), "hi", "files", anys(map[string]any{"id": "F1"})))}, nil
	}}
	if _, err := FetchMessage(context.Background(), rec.client(), testRef, false); !slackapi.IsAuthError(err) {
		t.Errorf("err = %v, want an auth error", err)
	}
}
