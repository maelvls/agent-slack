package unreads

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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
	serve func(method string, params map[string]any) map[string]any
}

func (r *recorder) client() *slackapi.Client {
	return slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		r.mu.Lock()
		r.calls = append(r.calls, call{method, params})
		r.mu.Unlock()
		return r.serve(method, params), nil
	})
}

func (r *recorder) only(method string) []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []call
	for _, c := range r.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func apiError(code string) map[string]any { return map[string]any{"ok": false, "error": code} }

// workspace is a fake workspace: what client.counts answers, and the
// name, DM user and history of each conversation.
type workspace struct {
	counts  map[string]any
	names   map[string]string
	dmUsers map[string]string
	history map[string]map[string]any
}

func (w workspace) recorder() *recorder {
	return &recorder{serve: func(method string, params map[string]any) map[string]any {
		id, _ := params["channel"].(string)
		switch method {
		case "client.counts":
			return w.counts
		case "conversations.info":
			if user := w.dmUsers[id]; user != "" {
				return map[string]any{"channel": map[string]any{"id": id, "is_im": true, "user": user}}
			}
			if name := w.names[id]; name != "" {
				return map[string]any{"channel": map[string]any{"id": id, "name": name}}
			}
			return apiError("channel_not_found")
		case "users.info":
			return map[string]any{"user": map[string]any{"profile": map[string]any{"display_name": "user-" + params["user"].(string)}}}
		case "conversations.history":
			if h := w.history[id]; h != nil {
				return h
			}
			return map[string]any{"messages": []any{}}
		}
		return apiError("unknown_method")
	}}
}

func msg(ts, text string, extra ...string) map[string]any {
	m := map[string]any{"ts": ts, "text": text}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return m
}

func intp(n int) *int { return &n }

func TestFetch(t *testing.T) {
	ws := workspace{
		counts: map[string]any{
			"channels": []any{
				map[string]any{"id": "C1", "has_unreads": true, "mention_count": 0.0, "last_read": "100.000001"},
				map[string]any{"id": "C2", "has_unreads": false, "mention_count": 0.0, "last_read": "100.000001"},
				map[string]any{"id": "C3", "has_unreads": true, "mention_count": 2.0, "last_read": "300.000001"},
				map[string]any{"has_unreads": true},
			},
			"mpims":   []any{map[string]any{"id": "G1", "has_unreads": true, "last_read": "50.000001"}},
			"ims":     []any{map[string]any{"id": "D1", "has_unreads": true, "mention_count": 1.0, "last_read": "10.000001"}},
			"threads": map[string]any{"has_unreads": true, "mention_count": 4.0},
		},
		names:   map[string]string{"C1": "general", "C3": "alerts", "G1": "mpdm-a--b-1"},
		dmUsers: map[string]string{"D1": "U9"},
		history: map[string]map[string]any{
			// Slack answers newest first.
			"C1": {"messages": []any{
				msg("103.000002", "third", "user", "U1"),
				msg("102.000001", "joined", "subtype", "channel_join", "user", "U2"),
				msg("101.000009", "first", "bot_id", "B1", "thread_ts", "101.000009"),
			}},
			"C3": {"messages": []any{msg("301.000001", "@you", "user", "U3")}},
			"D1": {"messages": []any{msg("11.000001", "hi", "user", "U9")}},
		},
	}
	rec := ws.recorder()
	res, err := Fetch(context.Background(), rec.client(), Options{})
	if err != nil {
		t.Fatal(err)
	}

	want := Result{
		Channels: []Channel{
			{ChannelID: "C3", ChannelName: "alerts", ChannelType: "channel", UnreadCount: intp(1), MentionCount: 2,
				Messages: []Message{{TS: "301.000001", Author: &Author{UserID: "U3"}, Content: "@you"}}},
			{ChannelID: "D1", ChannelName: "user-U9", ChannelType: "dm", UnreadCount: intp(1), MentionCount: 1,
				Messages: []Message{{TS: "11.000001", Author: &Author{UserID: "U9"}, Content: "hi"}}},
			{ChannelID: "C1", ChannelName: "general", ChannelType: "channel", UnreadCount: intp(3), SystemMessagesHidden: 1,
				Messages: []Message{
					{TS: "101.000009", Author: &Author{BotID: "B1"}, Content: "first", ThreadTS: "101.000009"},
					{TS: "103.000002", Author: &Author{UserID: "U1"}, Content: "third"},
				}},
			{ChannelID: "G1", ChannelName: "mpdm-a--b-1", ChannelType: "mpim", Messages: []Message{}},
		},
		Threads: &Threads{HasUnreads: true, MentionCount: 4},
	}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("result =\n%s\nwant\n%s", dump(res), dump(want))
	}

	if got := rec.calls[0]; got.method != "client.counts" || !reflect.DeepEqual(got.params, map[string]any{"thread_count_by_channel": true}) {
		t.Errorf("first call = %+v", got)
	}
	if n := len(rec.only("client.counts")); n != 1 {
		t.Errorf("client.counts calls = %d", n)
	}
	// Conversations without unreads cost nothing.
	for _, c := range rec.calls {
		if c.params["channel"] == "C2" {
			t.Errorf("unexpected call for a read channel: %+v", c)
		}
	}
	for _, c := range rec.only("conversations.history") {
		if c.params["channel"] == "C1" {
			want := map[string]any{"channel": "C1", "oldest": "100.000001", "limit": 10, "inclusive": false}
			if !reflect.DeepEqual(c.params, want) {
				t.Errorf("history params = %v, want %v", c.params, want)
			}
		}
	}
}

func dump(res Result) string {
	var b strings.Builder
	for _, c := range res.Channels {
		count := "nil"
		if c.UnreadCount != nil {
			count = fmt.Sprint(*c.UnreadCount)
		}
		fmt.Fprintf(&b, "  %s %q %s unread=%s lower_bound=%v mentions=%d system_hidden=%d truncated=%v err=%q:", c.ChannelID, c.ChannelName, c.ChannelType, count, c.UnreadCountIsLowerBound, c.MentionCount, c.SystemMessagesHidden, c.MessagesTruncated, c.MessagesError)
		for _, m := range c.Messages {
			fmt.Fprintf(&b, " {%s %+v %q %s %d}", m.TS, m.Author, m.Content, m.ThreadTS, m.ReplyCount)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "  threads=%+v", res.Threads)
	return b.String()
}

func TestFetchCountsOnly(t *testing.T) {
	ws := workspace{
		counts: map[string]any{
			"channels": []any{
				map[string]any{"id": "C1", "has_unreads": true, "last_read": "100.000001"},
				map[string]any{"id": "C2", "has_unreads": true, "unread_count_display": 7.0, "unread_count": 9.0},
			},
			"threads": map[string]any{"has_unreads": false, "mention_count": 0.0},
		},
		names: map[string]string{"C1": "general", "C2": "random"},
	}
	rec := ws.recorder()
	res, err := Fetch(context.Background(), rec.client(), Options{CountsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	want := Result{Channels: []Channel{
		{ChannelID: "C2", ChannelName: "random", ChannelType: "channel", UnreadCount: intp(7)},
		// client.counts only says "has unreads": the TypeScript reported
		// that as "unread_count": 1.
		{ChannelID: "C1", ChannelName: "general", ChannelType: "channel"},
	}}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("result =\n%s\nwant\n%s", dump(res), dump(want))
	}
	if n := len(rec.only("conversations.history")); n != 0 {
		t.Errorf("conversations.history calls = %d, want 0", n)
	}
}

func TestFetchOptions(t *testing.T) {
	long := strings.Repeat("é", 50)
	ws := workspace{
		counts: map[string]any{"channels": []any{
			map[string]any{"id": "C1", "has_unreads": true, "last_read": "100.000001"},
			// Never opened: no read cursor.
			map[string]any{"id": "C2", "has_unreads": true},
		}},
		history: map[string]map[string]any{
			"C1": {"has_more": true, "messages": []any{
				msg("102.000001", long, "user", "U1"),
				msg("101.000001", "topic", "subtype", "channel_topic"),
			}},
			"C2": {"messages": []any{msg("5.000001", "welcome")}},
		},
	}
	rec := ws.recorder()
	res, err := Fetch(context.Background(), rec.client(), Options{MaxMessages: 2, MaxBodyChars: 10, IncludeSystem: true})
	if err != nil {
		t.Fatal(err)
	}
	c1 := res.Channels[0]
	if c1.ChannelID != "C1" || len(c1.Messages) != 2 || *c1.UnreadCount != 2 {
		t.Fatalf("C1 = %s", dump(res))
	}
	// More unread messages than --max-messages: the count is a lower
	// bound, and the output says so.
	if !c1.MessagesTruncated {
		t.Errorf("messages_truncated not set: %s", dump(res))
	}
	if c1.Messages[0].Content != "topic" || c1.Messages[1].Content != strings.Repeat("é", 10)+"\n..." {
		t.Errorf("messages = %+v", c1.Messages)
	}
	if c1.ChannelName != "" {
		t.Errorf("channel_name = %q, want none when conversations.info fails", c1.ChannelName)
	}

	c2 := res.Channels[1]
	if len(c2.Messages) != 1 || c2.MessagesTruncated {
		t.Errorf("C2 = %s", dump(res))
	}
	for _, c := range rec.only("conversations.history") {
		want := map[string]any{"channel": "C1", "oldest": "100.000001", "limit": 2, "inclusive": false}
		if c.params["channel"] == "C2" {
			want = map[string]any{"channel": "C2", "limit": 2, "inclusive": false}
		}
		if !reflect.DeepEqual(c.params, want) {
			t.Errorf("history params = %v, want %v", c.params, want)
		}
	}

	res, err = Fetch(context.Background(), ws.recorder().client(), Options{MaxBodyChars: -1})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Channels[0].Messages; len(got) != 1 || got[0].Content != long {
		t.Errorf("unlimited, without system messages: %+v", got)
	}
}

func TestFetchReportsUnreadableConversations(t *testing.T) {
	ws := workspace{
		counts: map[string]any{"channels": []any{
			map[string]any{"id": "C1", "has_unreads": true, "last_read": "1.000001"},
			map[string]any{"id": "C2", "has_unreads": true, "last_read": "1.000001"},
		}},
		history: map[string]map[string]any{
			"C1": apiError("not_in_channel"),
			"C2": {"messages": []any{msg("2.000001", "hello")}},
		},
	}
	res, err := Fetch(context.Background(), ws.recorder().client(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Channel{}
	for _, c := range res.Channels {
		byID[c.ChannelID] = c
	}
	// The TypeScript reported such a channel as having one unread
	// message and no error.
	if c := byID["C1"]; c.MessagesError != "not_in_channel" || c.UnreadCount != nil || c.Messages != nil {
		t.Errorf("C1 = %s", dump(res))
	}
	if c := byID["C2"]; c.MessagesError != "" || len(c.Messages) != 1 {
		t.Errorf("C2 = %s", dump(res))
	}
}

func TestFetchPropagatesFatalErrors(t *testing.T) {
	ws := workspace{
		counts:  map[string]any{"channels": []any{map[string]any{"id": "C1", "has_unreads": true, "last_read": "1.000001"}}},
		history: map[string]map[string]any{"C1": apiError("token_expired")},
	}
	if _, err := Fetch(context.Background(), ws.recorder().client(), Options{}); !slackapi.IsAuthError(err) {
		t.Errorf("err = %v, want an auth error", err)
	}

	ws = workspace{counts: apiError("invalid_auth")}
	if _, err := Fetch(context.Background(), ws.recorder().client(), Options{}); !slackapi.IsAuthError(err) {
		t.Errorf("err = %v, want an auth error", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ws = workspace{counts: map[string]any{"channels": []any{map[string]any{"id": "C1", "has_unreads": true}}}}
	if _, err := Fetch(ctx, ws.recorder().client(), Options{CountsOnly: true}); err == nil {
		t.Error("no error for a cancelled context")
	}
}

func TestFetchRefusesStandardTokens(t *testing.T) {
	client := slackapi.New(slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxb-test"}, "",
		slackapi.WithHandler(func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
			t.Errorf("unexpected call to %s", method)
			return nil, nil
		}))
	if _, err := Fetch(context.Background(), client, Options{}); err == nil || !strings.Contains(err.Error(), "browser session credentials") {
		t.Errorf("err = %v", err)
	}
}

func TestFetchBoundsConcurrency(t *testing.T) {
	var entries []any
	for i := range 60 {
		entries = append(entries, map[string]any{"id": fmt.Sprintf("C%03d", i), "has_unreads": true, "last_read": "1.000001"})
	}
	var mu sync.Mutex
	inFlight, peak := 0, 0
	rec := &recorder{serve: func(method string, params map[string]any) map[string]any {
		if method == "client.counts" {
			return map[string]any{"channels": entries}
		}
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		if method == "conversations.info" {
			return map[string]any{"channel": map[string]any{"name": "n" + params["channel"].(string)}}
		}
		return map[string]any{"messages": []any{msg("2.000001", "m")}}
	}}
	res, err := Fetch(context.Background(), rec.client(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if peak > fetchConcurrency {
		t.Errorf("peak concurrency = %d, want <= %d", peak, fetchConcurrency)
	}
	// Equal counts keep Slack's order.
	for i, c := range res.Channels {
		if id := fmt.Sprintf("C%03d", i); c.ChannelID != id || c.ChannelName != "n"+id || len(c.Messages) != 1 {
			t.Fatalf("channel %d = %+v", i, c)
		}
	}
}

// TestFetchUnreadCountIsHonest covers the conversations whose unread
// messages are (partly) system messages: live, they were reported as
// "unread_count": 0 with "messages_truncated": true.
func TestFetchUnreadCountIsHonest(t *testing.T) {
	join := func(ts string) map[string]any { return msg(ts, "joined", "subtype", "channel_join", "user", "U1") }
	entry := func(id string, extra ...any) map[string]any {
		e := map[string]any{"id": id, "has_unreads": true, "last_read": "100.000001"}
		for i := 0; i+1 < len(extra); i += 2 {
			e[extra[i].(string)] = extra[i+1]
		}
		return e
	}
	ws := workspace{
		counts: map[string]any{"channels": []any{
			entry("C1"), entry("C2"), entry("C3"), entry("C4"),
			entry("C5", "unread_count_display", 0.0),
			entry("C6", "unread_count_display", 7.0),
			entry("C7", "unread_count_display", 1.0),
		}},
		history: map[string]map[string]any{
			// Only joins, and more unread messages than were fetched.
			"C1": {"has_more": true, "messages": []any{join("102.000001"), join("101.000001")}},
			// Only joins, all of them fetched.
			"C2": {"messages": []any{join("102.000001"), join("101.000001")}},
			// A join and a message, with more.
			"C3": {"has_more": true, "messages": []any{msg("102.000001", "hello", "user", "U2"), join("101.000001")}},
			// C4 is unread without any unread message (thread replies).
			// A count of 0 from Slack means nothing next to has_unreads.
			"C5": {"messages": []any{msg("101.000001", "hello", "user", "U2")}},
			// Slack's own count is exact even when the page is not …
			"C6": {"has_more": true, "messages": []any{msg("102.000001", "b"), msg("101.000001", "a")}},
			// … but cannot be smaller than what was fetched.
			"C7": {"messages": []any{msg("102.000001", "b"), msg("101.000001", "a")}},
		},
	}
	res, err := Fetch(context.Background(), ws.recorder().client(), Options{MaxMessages: 2})
	if err != nil {
		t.Fatal(err)
	}
	type summary struct {
		count         int // -1: absent
		lowerBound    bool
		shown, hidden int
		truncated     bool
	}
	summarize := func(c Channel) summary {
		s := summary{count: -1, lowerBound: c.UnreadCountIsLowerBound, shown: len(c.Messages), hidden: c.SystemMessagesHidden, truncated: c.MessagesTruncated}
		if c.UnreadCount != nil {
			s.count = *c.UnreadCount
		}
		return s
	}
	got := map[string]summary{}
	for _, c := range res.Channels {
		got[c.ChannelID] = summarize(c)
	}
	want := map[string]summary{
		"C1": {count: 2, lowerBound: true, hidden: 2, truncated: true},
		"C2": {count: 2, hidden: 2},
		"C3": {count: 2, lowerBound: true, shown: 1, hidden: 1, truncated: true},
		"C4": {count: -1},
		"C5": {count: 1, shown: 1},
		"C6": {count: 7, shown: 2, truncated: true},
		"C7": {count: 2, shown: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got\n%+v\nwant\n%+v\n%s", got, want, dump(res))
	}

	// IncludeSystem shows the system messages instead of counting them.
	res, err = Fetch(context.Background(), ws.recorder().client(), Options{MaxMessages: 2, IncludeSystem: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Channels {
		if c.ChannelID != "C1" {
			continue
		}
		if want := (summary{count: 2, lowerBound: true, shown: 2, truncated: true}); summarize(c) != want {
			t.Errorf("C1 with system messages = %+v, want %+v", summarize(c), want)
		}
	}
}

// Names and messages are enrichments: a rate limit must not lose the
// list of unread conversations, which client.counts already gave.
func TestFetchDegradesWhenRateLimited(t *testing.T) {
	var counted []any
	for i := range 30 {
		counted = append(counted, map[string]any{"id": fmt.Sprintf("C%d", i), "has_unreads": true, "last_read": "1.000001", "mention_count": float64(i)})
	}
	for _, limited := range []string{"conversations.history", "conversations.info"} {
		t.Run(limited, func(t *testing.T) {
			var mu sync.Mutex
			calls := map[string]int{}
			client := slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
				mu.Lock()
				calls[method]++
				mu.Unlock()
				switch {
				case method == "client.counts":
					return map[string]any{"channels": counted}, nil
				case method == limited:
					return nil, &slackapi.RateLimitError{Method: method, RetryAfter: time.Minute}
				case method == "conversations.info":
					return map[string]any{"channel": map[string]any{"name": "name-" + params["channel"].(string)}}, nil
				}
				return map[string]any{"messages": []any{msg("2.000001", "hello")}}, nil
			})
			res, err := Fetch(context.Background(), client, Options{})
			if err != nil {
				t.Fatalf("err = %v, want the conversations without what was rate limited", err)
			}
			if len(res.Channels) != 30 || res.Channels[0].ChannelID != "C29" || res.Channels[0].MentionCount != 29 {
				t.Fatalf("channels = %s", dump(res))
			}
			// No further call of the kind that was refused.
			if n := calls[limited]; n > fetchConcurrency {
				t.Errorf("%d %s calls after being rate limited, want at most %d", n, limited, fetchConcurrency)
			}
			if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "30 of 30 conversations") || !strings.Contains(res.Warnings[0], "rate limited") {
				t.Errorf("warnings = %q", res.Warnings)
			}
			for _, c := range res.Channels {
				if limited == "conversations.history" {
					if c.MessagesError != "ratelimited" || c.Messages != nil || c.ChannelName != "name-"+c.ChannelID {
						t.Fatalf("channel = %+v", c)
					}
				} else if c.ChannelName != "" || c.MessagesError != "" || len(c.Messages) != 1 {
					t.Fatalf("channel = %+v", c)
				}
			}
		})
	}
}

// A name lookup that hits dead credentials must trigger the refresh, not
// be mistaken for a nameless conversation.
func TestFetchPropagatesAuthErrorsOfNameLookups(t *testing.T) {
	client := slackapi.NewFake(func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
		if method == "client.counts" {
			return map[string]any{"channels": []any{map[string]any{"id": "C1", "has_unreads": true}}}, nil
		}
		return apiError("invalid_auth"), nil
	})
	if _, err := Fetch(context.Background(), client, Options{CountsOnly: true}); !slackapi.IsAuthError(err) {
		t.Errorf("err = %v, want an auth error", err)
	}
}
