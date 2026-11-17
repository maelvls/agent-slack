package users

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

	"github.com/maelvls/slackcli/internal/output"
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
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		out = append(out, c.method)
	}
	return out
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

func member(id, handle string, extra ...any) map[string]any {
	m := map[string]any{"id": id, "name": handle}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

func profile(kv ...string) map[string]any {
	p := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		p[kv[i]] = kv[i+1]
	}
	return p
}

// directory is a fake workspace: users.list pages, users.info, IM list.
type directory struct {
	pages        [][]map[string]any
	ims          []map[string]any
	imsErr       string
	lookupErr    string
	byEmail      map[string]map[string]any
	openChannel  string
	failInfoWith string
}

func (d directory) recorder() *recorder {
	return &recorder{serve: func(method string, params map[string]any) (map[string]any, error) {
		switch method {
		case "users.list":
			i := 0
			if c, _ := params["cursor"].(string); c != "" {
				i, _ = strconv.Atoi(strings.TrimPrefix(c, "page"))
			}
			resp := map[string]any{"members": []any{}}
			if i < len(d.pages) {
				members := make([]any, len(d.pages[i]))
				for j, m := range d.pages[i] {
					members[j] = m
				}
				resp["members"] = members
			}
			if i+1 < len(d.pages) {
				resp["response_metadata"] = map[string]any{"next_cursor": fmt.Sprintf("page%d", i+1)}
			}
			return resp, nil
		case "users.lookupByEmail":
			if d.lookupErr != "" {
				return apiError(d.lookupErr), nil
			}
			if u, ok := d.byEmail[params["email"].(string)]; ok {
				return map[string]any{"user": u}, nil
			}
			return apiError("users_not_found"), nil
		case "users.info":
			if d.failInfoWith != "" {
				return apiError(d.failInfoWith), nil
			}
			for _, page := range d.pages {
				for _, m := range page {
					if m["id"] == params["user"] {
						return map[string]any{"user": m}, nil
					}
				}
			}
			return apiError("user_not_found"), nil
		case "conversations.list":
			if d.imsErr != "" {
				return apiError(d.imsErr), nil
			}
			chans := make([]any, len(d.ims))
			for i, c := range d.ims {
				chans[i] = c
			}
			return map[string]any{"channels": chans}, nil
		case "conversations.open":
			return map[string]any{"channel": map[string]any{"id": d.openChannel}}, nil
		}
		return nil, fmt.Errorf("unexpected method %s", method)
	}}
}

func TestToCompactUser(t *testing.T) {
	yes, no := true, false
	got := ToCompactUser(map[string]any{
		"id": "U11111111", "name": "alice", "real_name": "Alice A", "tz": "Europe/Paris", "is_bot": false, "deleted": true,
		"profile": map[string]any{
			"display_name": "ali", "real_name": "Profile Name", "email": "alice@example.com", "title": "Engineer",
			"status_text": "away", "status_emoji": ":palm_tree:", "status_expiration": float64(1770165109),
		},
	})
	want := CompactUser{
		ID: "U11111111", Name: "alice", RealName: "Alice A", DisplayName: "ali", Email: "alice@example.com", Title: "Engineer",
		TZ: "Europe/Paris", IsBot: &no, Deleted: &yes, StatusText: "away", StatusEmoji: ":palm_tree:", StatusExpiration: 1770165109,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}

	got = ToCompactUser(map[string]any{"id": "U22222222", "profile": map[string]any{"real_name": "From Profile"}})
	if want := (CompactUser{ID: "U22222222", RealName: "From Profile"}); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if got := ToCompactUser(map[string]any{}); !reflect.DeepEqual(got, CompactUser{}) {
		t.Errorf("got %+v", got)
	}
}

func TestCompactUserJSON(t *testing.T) {
	no := false
	got, err := output.Marshal(CompactUser{ID: "U11111111", Name: "alice", DisplayName: "ali", IsBot: &no, DMID: "D11111111"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "id": "U11111111",
  "name": "alice",
  "display_name": "ali",
  "is_bot": false,
  "dm_id": "D11111111"
}`
	if string(got) != want {
		t.Errorf("JSON =\n%s", got)
	}
}

func TestResolveUserID(t *testing.T) {
	ctx := context.Background()

	t.Run("user IDs are returned unchanged without any API call", func(t *testing.T) {
		for _, id := range []string{"U01ABCDEFG", "W01ABCDEFG", "  U01ABCDEFG "} {
			rec := directory{}.recorder()
			got, err := ResolveUserID(ctx, rec.client(), id)
			if err != nil || got != strings.TrimSpace(id) || len(rec.calls) != 0 {
				t.Errorf("ResolveUserID(%q) = %q, %v after %v", id, got, err, rec.methods())
			}
		}
	})

	t.Run("@handle via users.list", func(t *testing.T) {
		rec := directory{pages: [][]map[string]any{{member("U02HANDLE0", "alice")}}}.recorder()
		got, err := ResolveUserID(ctx, rec.client(), "@alice")
		if err != nil || got != "U02HANDLE0" {
			t.Fatalf("got %q, %v", got, err)
		}
		if !reflect.DeepEqual(rec.methods(), []string{"users.list"}) || rec.calls[0].params["limit"] != scanPageSize {
			t.Errorf("calls = %v", rec.calls)
		}
	})

	t.Run("handle is case-insensitive and @ is optional", func(t *testing.T) {
		for _, in := range []string{"@Jinjing", "JINJING", " jinjing "} {
			rec := directory{pages: [][]map[string]any{{member("U05HANDLE0", "jinjing")}}}.recorder()
			if got, err := ResolveUserID(ctx, rec.client(), in); err != nil || got != "U05HANDLE0" {
				t.Errorf("ResolveUserID(%q) = %q, %v", in, got, err)
			}
		}
	})

	t.Run("email via users.lookupByEmail", func(t *testing.T) {
		rec := directory{byEmail: map[string]map[string]any{"alice@example.com": member("U03EMAIL00", "alice")}}.recorder()
		got, err := ResolveUserID(ctx, rec.client(), "alice@example.com")
		if err != nil || got != "U03EMAIL00" {
			t.Fatalf("got %q, %v", got, err)
		}
		want := []call{{"users.lookupByEmail", map[string]any{"email": "alice@example.com"}}}
		if !reflect.DeepEqual(rec.calls, want) {
			t.Errorf("calls = %v", rec.calls)
		}
	})

	t.Run("email falls back to users.list when lookupByEmail is unavailable", func(t *testing.T) {
		rec := directory{
			lookupErr: "missing_scope",
			pages:     [][]map[string]any{{member("U04FALLBAC", "alice", "profile", profile("email", "Alice@Example.com"))}},
		}.recorder()
		got, err := ResolveUserID(ctx, rec.client(), "alice@example.com")
		if err != nil || got != "U04FALLBAC" {
			t.Fatalf("got %q, %v", got, err)
		}
		if !reflect.DeepEqual(rec.methods(), []string{"users.lookupByEmail", "users.list"}) {
			t.Errorf("methods = %v", rec.methods())
		}
	})

	t.Run("an unknown email does not trigger a directory scan", func(t *testing.T) {
		rec := directory{pages: [][]map[string]any{{member("U02HANDLE0", "alice")}}}.recorder()
		_, err := ResolveUserID(ctx, rec.client(), "nobody@example.com")
		var nf *NotFoundError
		if !errors.As(err, &nf) || nf.Input != "nobody@example.com" {
			t.Errorf("err = %v", err)
		}
		if !reflect.DeepEqual(rec.methods(), []string{"users.lookupByEmail"}) {
			t.Errorf("methods = %v", rec.methods())
		}
	})

	t.Run("handle on a later page", func(t *testing.T) {
		rec := directory{pages: [][]map[string]any{
			{member("U00000001A", "bob")},
			{member("U00000002A", "carol")},
			{member("U00000003A", "alice")},
			{member("U00000004A", "dave")},
		}}.recorder()
		got, err := ResolveUserID(ctx, rec.client(), "alice")
		if err != nil || got != "U00000003A" {
			t.Fatalf("got %q, %v", got, err)
		}
		// Stops at the page that has the match.
		if n := len(rec.calls); n != 3 || rec.calls[2].params["cursor"] != "page2" {
			t.Errorf("calls = %v", rec.calls)
		}
	})

	t.Run("an active member wins over a deactivated one with the same handle", func(t *testing.T) {
		rec := directory{pages: [][]map[string]any{
			{member("U0000OLD0A", "alice", "deleted", true)},
			{member("U0000NEW0A", "alice")},
		}}.recorder()
		if got, err := ResolveUserID(ctx, rec.client(), "@alice"); err != nil || got != "U0000NEW0A" {
			t.Errorf("got %q, %v", got, err)
		}
	})

	t.Run("a deactivated member is still found", func(t *testing.T) {
		rec := directory{pages: [][]map[string]any{{member("U0000OLD0A", "alice", "deleted", true)}}}.recorder()
		if got, err := ResolveUserID(ctx, rec.client(), "@alice"); err != nil || got != "U0000OLD0A" {
			t.Errorf("got %q, %v", got, err)
		}
	})

	t.Run("display name or real name of a single member", func(t *testing.T) {
		pages := [][]map[string]any{{
			member("U00000001A", "asmith", "profile", profile("display_name", "Alice", "real_name", "Alice Smith")),
			member("U00000002A", "bjones", "real_name", "Bob Jones"),
		}}
		for in, want := range map[string]string{"@Alice": "U00000001A", "alice smith": "U00000001A", "Bob Jones": "U00000002A"} {
			if got, err := ResolveUserID(ctx, directory{pages: pages}.recorder().client(), in); err != nil || got != want {
				t.Errorf("ResolveUserID(%q) = %q, %v", in, got, err)
			}
		}
	})

	t.Run("a handle wins over somebody else's display name", func(t *testing.T) {
		rec := directory{pages: [][]map[string]any{
			{member("U00000001A", "someone", "profile", profile("display_name", "alice"))},
			{member("U00000002A", "alice")},
		}}.recorder()
		if got, err := ResolveUserID(ctx, rec.client(), "alice"); err != nil || got != "U00000002A" {
			t.Errorf("got %q, %v", got, err)
		}
	})

	t.Run("a display name shared by several members is ambiguous", func(t *testing.T) {
		rec := directory{pages: [][]map[string]any{{
			member("U00000001A", "asmith", "profile", profile("display_name", "Alex")),
			member("U00000002A", "ajones", "profile", profile("display_name", "alex")),
			member("U00000003A", "gone", "deleted", true, "profile", profile("display_name", "Alex")),
		}}}.recorder()
		_, err := ResolveUserID(ctx, rec.client(), "Alex")
		var amb *AmbiguousError
		if !errors.As(err, &amb) || len(amb.Matches) != 2 {
			t.Fatalf("err = %v", err)
		}
		for _, want := range []string{"U00000001A (@asmith)", "U00000002A (@ajones)"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("not found", func(t *testing.T) {
		for _, in := range []string{"@nobody", "@"} {
			rec := directory{pages: [][]map[string]any{{member("U00000001A", "alice")}}}.recorder()
			_, err := ResolveUserID(ctx, rec.client(), in)
			var nf *NotFoundError
			if !errors.As(err, &nf) || nf.Input != in || !strings.Contains(err.Error(), "user list") {
				t.Errorf("ResolveUserID(%q): err = %v", in, err)
			}
		}
		if _, err := ResolveUserID(ctx, directory{}.recorder().client(), "  "); err == nil || !strings.Contains(err.Error(), "user is empty") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("errors that must not be mistaken for not-found", func(t *testing.T) {
		rec := directory{lookupErr: "invalid_auth"}.recorder()
		if _, err := ResolveUserID(ctx, rec.client(), "alice@example.com"); !slackapi.IsAuthError(err) {
			t.Errorf("err = %v", err)
		}
		if !reflect.DeepEqual(rec.methods(), []string{"users.lookupByEmail"}) {
			t.Errorf("methods = %v", rec.methods())
		}

		rec = &recorder{serve: func(method string, _ map[string]any) (map[string]any, error) {
			return nil, &slackapi.RateLimitError{Method: method, RetryAfter: time.Minute}
		}}
		_, err := ResolveUserID(ctx, rec.client(), "@alice")
		var rl *slackapi.RateLimitError
		if !errors.As(err, &rl) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a cursor that does not advance ends the scan", func(t *testing.T) {
		rec := &recorder{serve: func(string, map[string]any) (map[string]any, error) {
			return map[string]any{"members": []any{}, "response_metadata": map[string]any{"next_cursor": "same"}}, nil
		}}
		_, err := ResolveUserID(ctx, rec.client(), "@alice")
		var nf *NotFoundError
		if !errors.As(err, &nf) || len(rec.calls) != 2 {
			t.Errorf("err = %v after %d calls", err, len(rec.calls))
		}
	})
}

func TestGetUser(t *testing.T) {
	ctx := context.Background()
	alice := member("U00000001A", "alice", "real_name", "Alice A", "profile", profile("display_name", "ali", "email", "alice@example.com"))
	dir := directory{pages: [][]map[string]any{{alice}}, byEmail: map[string]map[string]any{"alice@example.com": alice}}
	want := CompactUser{ID: "U00000001A", Name: "alice", RealName: "Alice A", DisplayName: "ali", Email: "alice@example.com"}

	tests := []struct {
		input       string
		wantMethods []string
	}{
		{"U00000001A", []string{"users.info"}},
		// The lookup already returned the whole user: no second call.
		{"@alice", []string{"users.list"}},
		{"alice@example.com", []string{"users.lookupByEmail"}},
	}
	for _, tt := range tests {
		rec := dir.recorder()
		got, err := GetUser(ctx, rec.client(), tt.input)
		if err != nil {
			t.Fatalf("GetUser(%q): %v", tt.input, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("GetUser(%q) = %+v", tt.input, got)
		}
		if !reflect.DeepEqual(rec.methods(), tt.wantMethods) {
			t.Errorf("GetUser(%q): methods = %v, want %v", tt.input, rec.methods(), tt.wantMethods)
		}
	}

	if _, err := GetUser(ctx, dir.recorder().client(), ""); err == nil || !strings.Contains(err.Error(), "user is empty") {
		t.Errorf("err = %v", err)
	}
	var nf *NotFoundError
	if _, err := GetUser(ctx, dir.recorder().client(), "@nobody"); !errors.As(err, &nf) {
		t.Errorf("err = %v", err)
	}
	if _, err := GetUser(ctx, dir.recorder().client(), "U0000GHOST"); !slackapi.IsErrorCode(err, "user_not_found") {
		t.Errorf("err = %v", err)
	}
	rec := &recorder{serve: func(string, map[string]any) (map[string]any, error) { return map[string]any{}, nil }}
	if _, err := GetUser(ctx, rec.client(), "U00000001A"); err == nil || !strings.Contains(err.Error(), "returned no user") {
		t.Errorf("err = %v", err)
	}
}

func TestGetDMChannelForUsers(t *testing.T) {
	ctx := context.Background()
	dir := directory{
		pages: [][]map[string]any{
			{member("U00000001A", "alice")},
			{member("U00000002A", "bob")},
		},
		byEmail: map[string]map[string]any{"carol@example.com": member("U00000003A", "carol")},
	}

	t.Run("single user", func(t *testing.T) {
		dir := dir
		dir.openChannel = "D00000001A"
		rec := dir.recorder()
		got, err := GetDMChannelForUsers(ctx, rec.client(), []string{"U00000001A"})
		if err != nil {
			t.Fatal(err)
		}
		want := DMChannel{UserIDs: []string{"U00000001A"}, DMChannelID: "D00000001A", ChannelType: "dm"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
		wantCalls := []call{{"conversations.open", map[string]any{"users": "U00000001A"}}}
		if !reflect.DeepEqual(rec.calls, wantCalls) {
			t.Errorf("calls = %v", rec.calls)
		}
	})

	t.Run("several users share one directory scan", func(t *testing.T) {
		dir := dir
		dir.openChannel = "G00000001A"
		rec := dir.recorder()
		got, err := GetDMChannelForUsers(ctx, rec.client(), []string{"@bob", " ", "U00000009A", "carol@example.com", "@alice", "bob", "U00000009A"})
		if err != nil {
			t.Fatal(err)
		}
		want := DMChannel{UserIDs: []string{"U00000002A", "U00000009A", "U00000003A", "U00000001A"}, DMChannelID: "G00000001A", ChannelType: "group_dm"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
		// The TypeScript scanned the whole directory once per handle.
		if n := len(rec.only("users.list")); n != 2 {
			t.Errorf("%d users.list calls, want 2 (one scan of two pages)", n)
		}
		if open := rec.only("conversations.open"); len(open) != 1 || open[0].params["users"] != "U00000002A,U00000009A,U00000003A,U00000001A" {
			t.Errorf("conversations.open calls = %v", open)
		}
	})

	t.Run("errors", func(t *testing.T) {
		for _, inputs := range [][]string{nil, {}, {"", "  "}} {
			if _, err := GetDMChannelForUsers(ctx, dir.recorder().client(), inputs); err == nil || !strings.Contains(err.Error(), "at least one user is required") {
				t.Errorf("%v: err = %v", inputs, err)
			}
		}

		nine := []string{"U00000001A", "U00000002A", "U00000003A", "U00000004A", "U00000005A", "U00000006A", "U00000007A", "U00000008A", "U00000009A"}
		rec := dir.recorder()
		if _, err := GetDMChannelForUsers(ctx, rec.client(), nine); err == nil || !strings.Contains(err.Error(), "maximum of 8 users") || len(rec.calls) != 0 {
			t.Errorf("err = %v after %v", err, rec.methods())
		}
		// Blank entries do not count towards the limit.
		dirOK := dir
		dirOK.openChannel = "G00000001A"
		if _, err := GetDMChannelForUsers(ctx, dirOK.recorder().client(), append([]string{" "}, nine[:8]...)); err != nil {
			t.Errorf("8 users + a blank: %v", err)
		}

		rec = dir.recorder()
		_, err := GetDMChannelForUsers(ctx, rec.client(), []string{"@alice", "@nobody"})
		var nf *NotFoundError
		if !errors.As(err, &nf) || nf.Input != "@nobody" || len(rec.only("conversations.open")) != 0 {
			t.Errorf("err = %v", err)
		}

		// conversations.open answering without a channel.
		if _, err := GetDMChannelForUsers(ctx, dir.recorder().client(), []string{"U00000001A"}); err == nil || !strings.Contains(err.Error(), "returned no channel") {
			t.Errorf("err = %v", err)
		}
	})
}

func names(page UsersPage) []string {
	var out []string
	for _, u := range page.Users {
		out = append(out, u.Name)
	}
	return out
}

func TestListUsers(t *testing.T) {
	ctx := context.Background()
	dir := directory{
		pages: [][]map[string]any{
			{member("U00000001A", "alice"), member("U00000002A", "buildbot", "is_bot", true), member("", "no-id")},
			{member("U00000003A", "carol"), member("U00000004A", "dave", "deleted", true)},
		},
		ims: []map[string]any{{"id": "D00000001A", "user": "U00000001A"}, {"id": "D00000004A", "user": "U00000004A"}, {"id": "D0000NOUSR"}},
	}

	t.Run("defaults", func(t *testing.T) {
		rec := dir.recorder()
		page, err := ListUsers(ctx, rec.client(), ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"alice", "carol", "dave"}; !reflect.DeepEqual(names(page), want) || page.NextCursor != "" {
			t.Errorf("page = %v, cursor %q", names(page), page.NextCursor)
		}
		if page.Users[0].DMID != "D00000001A" || page.Users[1].DMID != "" || page.Users[2].DMID != "D00000004A" {
			t.Errorf("dm ids = %+v", page.Users)
		}
		lists := rec.only("users.list")
		if len(lists) != 2 || !reflect.DeepEqual(lists[0].params, map[string]any{"limit": 200}) ||
			!reflect.DeepEqual(lists[1].params, map[string]any{"limit": 199, "cursor": "page1"}) {
			t.Errorf("users.list calls = %v", lists)
		}
		if ims := rec.only("conversations.list"); len(ims) != 1 || ims[0].params["types"] != "im" {
			t.Errorf("conversations.list calls = %v", ims)
		}
	})

	t.Run("bots included", func(t *testing.T) {
		page, err := ListUsers(ctx, dir.recorder().client(), ListOptions{IncludeBots: true})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"alice", "buildbot", "carol", "dave"}; !reflect.DeepEqual(names(page), want) {
			t.Errorf("page = %v", names(page))
		}
	})

	t.Run("limit reached with more to come", func(t *testing.T) {
		rec := dir.recorder()
		page, err := ListUsers(ctx, rec.client(), ListOptions{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(names(page), []string{"alice"}) || page.NextCursor != "page1" {
			t.Errorf("page = %v, cursor %q", names(page), page.NextCursor)
		}

		page, err = ListUsers(ctx, dir.recorder().client(), ListOptions{Limit: 5, Cursor: page.NextCursor})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(names(page), []string{"carol", "dave"}) || page.NextCursor != "" {
			t.Errorf("page 2 = %v, cursor %q", names(page), page.NextCursor)
		}
	})

	// The TypeScript kept the cursor of an earlier page when the directory
	// ran out before the limit, sending the caller back to a page it had
	// already been given.
	t.Run("no stale cursor once the directory is exhausted", func(t *testing.T) {
		three := directory{pages: [][]map[string]any{
			{member("U00000001A", "alice")},
			{member("U00000002A", "buildbot", "is_bot", true)},
			{member("U00000003A", "deploybot", "is_bot", true)},
		}}
		page, err := ListUsers(ctx, three.recorder().client(), ListOptions{Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(names(page), []string{"alice"}) || page.NextCursor != "" {
			t.Errorf("page = %v, cursor %q", names(page), page.NextCursor)
		}
	})

	t.Run("limit is clamped", func(t *testing.T) {
		rec := directory{}.recorder()
		if _, err := ListUsers(ctx, rec.client(), ListOptions{Limit: 5000}); err != nil {
			t.Fatal(err)
		}
		if got := rec.only("users.list")[0].params["limit"]; got != 200 {
			t.Errorf("page size = %v", got)
		}
	})

	t.Run("DMs that cannot be listed do not fail the listing", func(t *testing.T) {
		dir := dir
		dir.imsErr = "missing_scope"
		page, err := ListUsers(ctx, dir.recorder().client(), ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Users) != 3 || page.Users[0].DMID != "" {
			t.Errorf("page = %+v", page)
		}
	})

	t.Run("users.list errors propagate", func(t *testing.T) {
		rec := &recorder{serve: func(method string, _ map[string]any) (map[string]any, error) {
			if method == "users.list" {
				return apiError("invalid_auth"), nil
			}
			return map[string]any{}, nil
		}}
		if _, err := ListUsers(ctx, rec.client(), ListOptions{}); !slackapi.IsAuthError(err) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a cursor that does not advance ends the listing", func(t *testing.T) {
		rec := &recorder{serve: func(method string, _ map[string]any) (map[string]any, error) {
			if method == "users.list" {
				return map[string]any{"members": []any{member("U00000001A", "alice")}, "response_metadata": map[string]any{"next_cursor": "same"}}, nil
			}
			return map[string]any{}, nil
		}}
		page, err := ListUsers(ctx, rec.client(), ListOptions{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Users) != 2 || page.NextCursor != "" || len(rec.only("users.list")) != 2 {
			t.Errorf("page = %v, cursor %q", names(page), page.NextCursor)
		}
	})
}
