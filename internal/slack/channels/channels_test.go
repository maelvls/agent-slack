package channels

import (
	"context"
	"errors"
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
	serve func(method string, params map[string]any) (map[string]any, error)
}

func (r *recorder) client(opts ...slackapi.Option) *slackapi.Client {
	return slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		r.mu.Lock()
		r.calls = append(r.calls, call{method, params})
		r.mu.Unlock()
		return r.serve(method, params)
	}, opts...)
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

func static(resp map[string]any) *recorder {
	return &recorder{serve: func(string, map[string]any) (map[string]any, error) { return resp, nil }}
}

func ids(page ConversationsPage) []string {
	var out []string
	for _, c := range page.Channels {
		out = append(out, c["id"].(string))
	}
	return out
}

func TestListUserAndListAll(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		list       func(context.Context, *slackapi.Client, ListOptions) (ConversationsPage, error)
		opts       ListOptions
		wantMethod string
		wantParams map[string]any
	}{
		{"ListUser defaults", ListUser, ListOptions{}, "users.conversations",
			map[string]any{"types": DefaultTypes, "exclude_archived": true, "limit": 100}},
		{"ListUser forwards user/limit/cursor", ListUser, ListOptions{User: "U123", Limit: 77, Cursor: "abc"}, "users.conversations",
			map[string]any{"user": "U123", "limit": 77, "cursor": "abc", "types": DefaultTypes, "exclude_archived": true}},
		{"ListUser passes through small limits", ListUser, ListOptions{Limit: 3}, "users.conversations",
			map[string]any{"types": DefaultTypes, "exclude_archived": true, "limit": 3}},
		{"ListUser clamps high limits to 1000", ListUser, ListOptions{Limit: 5000}, "users.conversations",
			map[string]any{"types": DefaultTypes, "exclude_archived": true, "limit": 1000}},
		{"ListUser types and archived", ListUser, ListOptions{Types: "im", IncludeArchived: true}, "users.conversations",
			map[string]any{"types": "im", "exclude_archived": false, "limit": 100}},
		{"ListAll defaults", ListAll, ListOptions{}, "conversations.list",
			map[string]any{"types": DefaultTypes, "exclude_archived": true, "limit": 100}},
		{"ListAll forwards limit/cursor and ignores user", ListAll, ListOptions{Limit: 50, Cursor: "xyz", User: "U123"}, "conversations.list",
			map[string]any{"types": DefaultTypes, "exclude_archived": true, "limit": 50, "cursor": "xyz"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := static(map[string]any{"channels": []any{map[string]any{"id": "C1"}}, "response_metadata": map[string]any{"next_cursor": "n1"}})
			page, err := tt.list(ctx, rec.client(), tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(rec.calls) != 1 || rec.calls[0].method != tt.wantMethod {
				t.Fatalf("calls = %v", rec.methods())
			}
			if !reflect.DeepEqual(rec.calls[0].params, tt.wantParams) {
				t.Errorf("params = %v, want %v", rec.calls[0].params, tt.wantParams)
			}
			if !reflect.DeepEqual(ids(page), []string{"C1"}) || page.NextCursor != "n1" {
				t.Errorf("page = %+v", page)
			}
		})
	}
}

func TestListPropagatesAPIErrors(t *testing.T) {
	rec := static(apiError("enterprise_is_restricted"))
	if _, err := ListAll(context.Background(), rec.client(), ListOptions{}); !slackapi.IsErrorCode(err, "enterprise_is_restricted") {
		t.Errorf("err = %v", err)
	}
}

func TestNormalizePage(t *testing.T) {
	page := NormalizePage(map[string]any{
		"channels":          []any{map[string]any{"id": "C1"}, "junk", map[string]any{"id": "D1"}},
		"response_metadata": map[string]any{"next_cursor": "next123"},
	})
	if !reflect.DeepEqual(ids(page), []string{"C1", "D1"}) || page.NextCursor != "next123" {
		t.Errorf("page = %+v", page)
	}
	if page := NormalizePage(map[string]any{"channels": []any{}}); len(page.Channels) != 0 || page.NextCursor != "" {
		t.Errorf("missing response_metadata: page = %+v", page)
	}
	if page := NormalizePage(map[string]any{"response_metadata": map[string]any{"next_cursor": ""}}); page.NextCursor != "" {
		t.Errorf("empty next_cursor: page = %+v", page)
	}
}

func countsRecorder(counts map[string]any, info func(id string) (map[string]any, error)) *recorder {
	return &recorder{serve: func(method string, params map[string]any) (map[string]any, error) {
		switch method {
		case "client.counts":
			return counts, nil
		case "conversations.info":
			return info(params["channel"].(string))
		}
		return nil, fmt.Errorf("unexpected method: %s", method)
	}}
}

func TestListViaCounts(t *testing.T) {
	rec := countsRecorder(map[string]any{
		"channels": []any{map[string]any{"id": "C1", "has_unreads": true}},
		"mpims":    []any{map[string]any{"id": "G1"}},
		"ims":      []any{map[string]any{"id": "D1"}},
	}, func(id string) (map[string]any, error) {
		return map[string]any{"channel": map[string]any{"id": id, "name": "name-" + id}}, nil
	})
	page, err := ListViaCounts(context.Background(), rec.client(), ListOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if rec.calls[0].method != "client.counts" || !reflect.DeepEqual(rec.calls[0].params, map[string]any{"thread_count_by_channel": true}) {
		t.Errorf("first call = %+v", rec.calls[0])
	}
	if n := len(rec.only("conversations.info")); n != 3 {
		t.Errorf("conversations.info calls = %d, want 3", n)
	}
	if !reflect.DeepEqual(ids(page), []string{"C1", "G1", "D1"}) || page.NextCursor != "" {
		t.Errorf("page = %+v", page)
	}
	if page.Channels[0]["name"] != "name-C1" {
		t.Errorf("first channel = %v", page.Channels[0])
	}
}

func TestListViaCountsKeepsIDWhenInfoFails(t *testing.T) {
	rec := countsRecorder(map[string]any{"channels": []any{map[string]any{"id": "C1"}}},
		func(string) (map[string]any, error) { return apiError("channel_not_found"), nil })
	page, err := ListViaCounts(context.Background(), rec.client(), ListOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(page.Channels, []map[string]any{{"id": "C1"}}) {
		t.Errorf("channels = %v", page.Channels)
	}
}

func TestListViaCountsPagesLocally(t *testing.T) {
	counts := map[string]any{"channels": []any{
		map[string]any{"id": "C1"}, map[string]any{"no_id": true}, map[string]any{"id": "C2"}, map[string]any{"id": "C3"},
	}}
	info := func(id string) (map[string]any, error) {
		return map[string]any{"channel": map[string]any{"id": id}}, nil
	}

	rec := countsRecorder(counts, info)
	page, err := ListViaCounts(context.Background(), rec.client(), ListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rec.only("conversations.info")); n != 2 {
		t.Errorf("conversations.info calls = %d, want 2", n)
	}
	// The TypeScript dropped the rest silently; the cursor now says there
	// is more and where to resume.
	if !reflect.DeepEqual(ids(page), []string{"C1", "C2"}) || page.NextCursor != "2" {
		t.Errorf("page 1 = %+v", page)
	}

	page, err = ListViaCounts(context.Background(), countsRecorder(counts, info).client(), ListOptions{Limit: 2, Cursor: page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(page), []string{"C3"}) || page.NextCursor != "" {
		t.Errorf("page 2 = %+v", page)
	}

	if _, err := ListViaCounts(context.Background(), countsRecorder(counts, info).client(), ListOptions{Cursor: "dXNlcjpVMQ=="}); err == nil || !strings.Contains(err.Error(), "invalid cursor") {
		t.Errorf("err = %v, want an invalid-cursor error", err)
	}
}

func TestListViaCountsBoundsConcurrency(t *testing.T) {
	var entries []any
	for i := range 50 {
		entries = append(entries, map[string]any{"id": fmt.Sprintf("C%09d", i)})
	}
	var mu sync.Mutex
	inFlight, peak := 0, 0
	rec := countsRecorder(map[string]any{"channels": entries}, func(id string) (map[string]any, error) {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return map[string]any{"channel": map[string]any{"id": id}}, nil
	})
	page, err := ListViaCounts(context.Background(), rec.client(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Channels) != 50 || page.Channels[49]["id"] != "C000000049" {
		t.Errorf("got %d channels, last %v", len(page.Channels), page.Channels[len(page.Channels)-1])
	}
	if peak > infoConcurrency {
		t.Errorf("peak concurrency = %d, want <= %d", peak, infoConcurrency)
	}
}

func TestOpenDM(t *testing.T) {
	rec := static(map[string]any{"channel": map[string]any{"id": "D123456789"}})
	id, err := OpenDM(context.Background(), rec.client(), "U123456789")
	if err != nil || id != "D123456789" {
		t.Fatalf("OpenDM = %q, %v", id, err)
	}
	if rec.calls[0].method != "conversations.open" || rec.calls[0].params["users"] != "U123456789" {
		t.Errorf("call = %+v", rec.calls[0])
	}

	if _, err := OpenDM(context.Background(), static(map[string]any{}).client(), "U123456789"); err == nil || !strings.Contains(err.Error(), "U123456789") {
		t.Errorf("err = %v, want an error naming the user", err)
	}
	if _, err := OpenDM(context.Background(), static(apiError("user_not_found")).client(), "U123456789"); !slackapi.IsErrorCode(err, "user_not_found") {
		t.Errorf("err = %v, want user_not_found", err)
	}
}

func TestMark(t *testing.T) {
	rec := static(map[string]any{})
	if err := Mark(context.Background(), rec.client(), "C123456789", "1770165109.628379"); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"channel": "C123456789", "ts": "1770165109.628379"}
	if rec.calls[0].method != "conversations.mark" || !reflect.DeepEqual(rec.calls[0].params, want) {
		t.Errorf("call = %+v", rec.calls[0])
	}
}

func TestResolveName(t *testing.T) {
	tests := []struct {
		name string
		info map[string]any
		user map[string]any
		want string
	}{
		{"channel", map[string]any{"channel": map[string]any{"id": "C123456789", "name": "general"}}, nil, "general"},
		{"channel without a name", map[string]any{"channel": map[string]any{"id": "C123456789"}}, nil, "C123456789"},
		{"info fails", apiError("channel_not_found"), nil, "C123456789"},
		{"DM uses the display name", map[string]any{"channel": map[string]any{"is_im": true, "user": "U123456789"}},
			map[string]any{"user": map[string]any{"profile": map[string]any{"display_name": "alice", "real_name": "Alice A"}}}, "alice"},
		{"DM falls back to the real name", map[string]any{"channel": map[string]any{"is_im": true, "user": "U123456789"}},
			map[string]any{"user": map[string]any{"profile": map[string]any{"display_name": "", "real_name": "Alice A"}}}, "Alice A"},
		{"DM whose user lookup fails", map[string]any{"channel": map[string]any{"is_im": true, "user": "U123456789"}},
			apiError("user_not_found"), "C123456789"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{serve: func(method string, _ map[string]any) (map[string]any, error) {
				if method == "users.info" {
					return tt.user, nil
				}
				return tt.info, nil
			}}
			if got := ResolveName(context.Background(), rec.client(), "C123456789"); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// workspace is a fake workspace for ResolveID tests.
type workspace struct {
	// search maps a channel name to the search.messages match channel.
	search    map[string]map[string]any
	searchErr string
	joined    [][]map[string]any // pages of users.conversations
	joinedErr string
	public    [][]map[string]any // pages of conversations.list
	publicErr string
}

func (w workspace) recorder() *recorder {
	page := func(pages [][]map[string]any, params map[string]any) map[string]any {
		i := 0
		if c, _ := params["cursor"].(string); c != "" {
			fmt.Sscanf(c, "page%d", &i)
		}
		if i >= len(pages) {
			return map[string]any{"channels": []any{}}
		}
		chans := make([]any, 0, len(pages[i]))
		for _, c := range pages[i] {
			chans = append(chans, c)
		}
		resp := map[string]any{"channels": chans}
		if i+1 < len(pages) {
			resp["response_metadata"] = map[string]any{"next_cursor": fmt.Sprintf("page%d", i+1)}
		}
		return resp
	}
	return &recorder{serve: func(method string, params map[string]any) (map[string]any, error) {
		switch method {
		case "search.messages":
			if w.searchErr != "" {
				return apiError(w.searchErr), nil
			}
			var matches []any
			if ch, ok := w.search[strings.TrimPrefix(params["query"].(string), "in:#")]; ok {
				matches = append(matches, map[string]any{"channel": ch})
			}
			return map[string]any{"messages": map[string]any{"matches": matches}}, nil
		case "users.conversations":
			if w.joinedErr != "" {
				return apiError(w.joinedErr), nil
			}
			return page(w.joined, params), nil
		case "conversations.list":
			if w.publicErr != "" {
				return apiError(w.publicErr), nil
			}
			return page(w.public, params), nil
		}
		return nil, fmt.Errorf("unexpected method: %s", method)
	}}
}

func ch(id, name string) map[string]any { return map[string]any{"id": id, "name": name} }

func TestResolveID(t *testing.T) {
	tests := []struct {
		name        string
		ws          workspace
		input       string
		want        string
		wantMethods []string
	}{
		{
			name:  "IDs need no API call",
			input: "C060RS20UMV", want: "C060RS20UMV",
		},
		{
			name:        "search fast path",
			ws:          workspace{search: map[string]map[string]any{"general": ch("C000000001", "general")}},
			input:       "#general",
			want:        "C000000001",
			wantMethods: []string{"search.messages"},
		},
		{
			name:        "bare, mixed-case name",
			ws:          workspace{search: map[string]map[string]any{"general": ch("C000000001", "general")}},
			input:       " General ",
			want:        "C000000001",
			wantMethods: []string{"search.messages"},
		},
		{
			// The TypeScript took whatever channel the first search hit was in.
			name: "a search hit from another channel is not trusted",
			ws: workspace{
				search: map[string]map[string]any{"dev": ch("C000000009", "dev-ops")},
				joined: [][]map[string]any{{ch("C000000002", "dev")}},
			},
			input:       "#dev",
			want:        "C000000002",
			wantMethods: []string{"search.messages", "users.conversations"},
		},
		{
			name: "private channel the caller is in, on a later page",
			ws: workspace{joined: [][]map[string]any{
				{ch("C000000001", "general")},
				{ch("G000000002", "secret")},
			}},
			input:       "#secret",
			want:        "G000000002",
			wantMethods: []string{"search.messages", "users.conversations", "users.conversations"},
		},
		{
			name: "public channel the caller is not in",
			ws: workspace{
				joined: [][]map[string]any{{ch("C000000001", "general")}},
				public: [][]map[string]any{{ch("C000000001", "general")}, {ch("C000000003", "random")}},
			},
			input:       "random",
			want:        "C000000003",
			wantMethods: []string{"search.messages", "users.conversations", "conversations.list", "conversations.list"},
		},
		{
			name: "prefix of another name does not match",
			ws: workspace{
				joined: [][]map[string]any{{ch("C000000004", "general-chat")}},
				public: [][]map[string]any{{ch("C000000001", "general")}},
			},
			input: "general",
			want:  "C000000001",
		},
		{
			name: "search failure falls back to listing",
			ws: workspace{
				searchErr: "missing_scope",
				joined:    [][]map[string]any{{ch("C000000001", "general")}},
			},
			input:       "general",
			want:        "C000000001",
			wantMethods: []string{"search.messages", "users.conversations"},
		},
		{
			name: "users.conversations failure falls back to conversations.list",
			ws: workspace{
				joinedErr: "method_not_supported_for_channel_type",
				public:    [][]map[string]any{{ch("C000000001", "general")}},
			},
			input: "general",
			want:  "C000000001",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := tt.ws.recorder()
			got, err := ResolveID(context.Background(), rec.client(), tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			if tt.wantMethods != nil && !reflect.DeepEqual(rec.methods(), tt.wantMethods) {
				t.Errorf("methods = %v, want %v", rec.methods(), tt.wantMethods)
			}
			if tt.wantMethods == nil && tt.ws.search == nil && tt.ws.joined == nil && tt.ws.public == nil && len(rec.calls) != 0 {
				t.Errorf("unexpected calls %v", rec.methods())
			}
		})
	}
}

func TestResolveIDIncludesArchivedChannels(t *testing.T) {
	rec := workspace{public: [][]map[string]any{{ch("C000000005", "old-project")}}}.recorder()
	got, err := ResolveID(context.Background(), rec.client(), "#old-project")
	if err != nil || got != "C000000005" {
		t.Fatalf("ResolveID = %q, %v", got, err)
	}
	for _, c := range append(rec.only("users.conversations"), rec.only("conversations.list")...) {
		if c.params["exclude_archived"] != false {
			t.Errorf("%s called with exclude_archived=%v, want false", c.method, c.params["exclude_archived"])
		}
		if c.params["limit"] != scanPageSize {
			t.Errorf("%s called with limit=%v", c.method, c.params["limit"])
		}
	}
	if types := rec.only("users.conversations")[0].params["types"]; types != "public_channel,private_channel" {
		t.Errorf("users.conversations types = %v", types)
	}
}

func TestResolveIDSkipsSearchForBotTokens(t *testing.T) {
	rec := workspace{joined: [][]map[string]any{{ch("C000000001", "general")}}}.recorder()
	client := slackapi.New(slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxb-test"}, "", slackapi.WithHandler(
		func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
			rec.calls = append(rec.calls, call{method, params})
			return rec.serve(method, params)
		}))
	got, err := ResolveID(context.Background(), client, "general")
	if err != nil || got != "C000000001" {
		t.Fatalf("ResolveID = %q, %v", got, err)
	}
	if !reflect.DeepEqual(rec.methods(), []string{"users.conversations"}) {
		t.Errorf("methods = %v", rec.methods())
	}
}

func TestResolveIDRetriesWithoutPrivateChannelsOnMissingScope(t *testing.T) {
	rec := &recorder{}
	rec.serve = func(method string, params map[string]any) (map[string]any, error) {
		if method == "search.messages" {
			return map[string]any{}, nil
		}
		if strings.Contains(params["types"].(string), "private_channel") {
			return apiError("missing_scope"), nil
		}
		return map[string]any{"channels": []any{ch("C000000001", "general")}}, nil
	}
	got, err := ResolveID(context.Background(), rec.client(), "general")
	if err != nil || got != "C000000001" {
		t.Fatalf("ResolveID = %q, %v", got, err)
	}
}

func TestResolveIDNotFound(t *testing.T) {
	rec := workspace{
		joined: [][]map[string]any{{ch("C000000001", "general")}},
		public: [][]map[string]any{{ch("C000000001", "general")}},
	}.recorder()
	_, err := ResolveID(context.Background(), rec.client(), "#nope")
	var nf *NotFoundError
	if !errors.As(err, &nf) || nf.Name != "nope" {
		t.Fatalf("err = %v, want *NotFoundError for nope", err)
	}
	for _, want := range []string{`"#nope"`, "channel list", "channel ID", "--workspace"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestResolveIDErrors(t *testing.T) {
	ctx := context.Background()

	for _, in := range []string{"", "#", " # "} {
		if _, err := ResolveID(ctx, workspace{}.recorder().client(), in); err == nil || !strings.Contains(err.Error(), "channel name is empty") {
			t.Errorf("ResolveID(%q): err = %v", in, err)
		}
	}

	t.Run("expired credentials are not swallowed by the search fallback", func(t *testing.T) {
		rec := workspace{searchErr: "invalid_auth"}.recorder()
		_, err := ResolveID(ctx, rec.client(), "general")
		if !slackapi.IsAuthError(err) {
			t.Errorf("err = %v, want an auth error", err)
		}
		if !reflect.DeepEqual(rec.methods(), []string{"search.messages"}) {
			t.Errorf("methods = %v", rec.methods())
		}
	})

	t.Run("listing forbidden", func(t *testing.T) {
		rec := workspace{joinedErr: "enterprise_is_restricted", publicErr: "enterprise_is_restricted"}.recorder()
		_, err := ResolveID(ctx, rec.client(), "general")
		if !slackapi.IsErrorCode(err, "enterprise_is_restricted") || !strings.Contains(err.Error(), "channel ID") {
			t.Errorf("err = %v, want enterprise_is_restricted with a hint", err)
		}
		var nf *NotFoundError
		if errors.As(err, &nf) {
			t.Error("a listing failure must not be reported as channel-not-found")
		}
	})

	t.Run("rate limit while listing", func(t *testing.T) {
		rec := &recorder{serve: func(method string, _ map[string]any) (map[string]any, error) {
			if method == "search.messages" {
				return map[string]any{}, nil
			}
			return nil, &slackapi.RateLimitError{Method: method, RetryAfter: 30 * time.Second}
		}}
		_, err := ResolveID(ctx, rec.client(), "general")
		var rl *slackapi.RateLimitError
		if !errors.As(err, &rl) {
			t.Errorf("err = %v, want a rate limit error", err)
		}
		if !reflect.DeepEqual(rec.methods(), []string{"search.messages", "users.conversations"}) {
			t.Errorf("methods = %v", rec.methods())
		}
	})

	t.Run("a cursor that does not advance ends the scan", func(t *testing.T) {
		rec := &recorder{serve: func(string, map[string]any) (map[string]any, error) {
			return map[string]any{"channels": []any{}, "response_metadata": map[string]any{"next_cursor": "same"}}, nil
		}}
		_, err := ResolveID(ctx, rec.client(), "general")
		var nf *NotFoundError
		if !errors.As(err, &nf) {
			t.Errorf("err = %v, want *NotFoundError", err)
		}
		if n := len(rec.calls); n != 5 {
			t.Errorf("%d calls (%v), want 5", n, rec.methods())
		}
	})
}
