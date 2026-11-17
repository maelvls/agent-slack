package users

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// hugeWorkspace is a workspace whose directory cannot be scanned:
// users.list is rate limited, as on kubernetes.slack.com.
func hugeWorkspace(d directory) *recorder {
	rec := d.recorder()
	serve := rec.serve
	rec.serve = func(method string, params map[string]any) (map[string]any, error) {
		if method == "users.list" {
			return nil, &slackapi.RateLimitError{Method: method, RetryAfter: 30 * time.Second}
		}
		return serve(method, params)
	}
	return rec
}

func TestResolveUserIDBySearch(t *testing.T) {
	ctx := context.Background()
	alice := member("U00000001A", "alice")

	t.Run("one search.messages call, no directory scan", func(t *testing.T) {
		for _, in := range []string{"alice", "@alice", " @Alice "} {
			rec := hugeWorkspace(directory{posted: map[string]string{"alice": "U00000001A"}})
			got, err := ResolveUserID(ctx, rec.client(), in)
			if err != nil || got != "U00000001A" {
				t.Fatalf("ResolveUserID(%q) = %q, %v", in, got, err)
			}
			want := []call{{"search.messages", map[string]any{"query": "from:@alice", "count": searchResults, "sort": "timestamp", "sort_dir": "desc"}}}
			if !reflect.DeepEqual(rec.calls, want) {
				t.Errorf("ResolveUserID(%q): calls = %v", in, rec.calls)
			}
		}
	})

	t.Run("a fuzzy hit is never trusted", func(t *testing.T) {
		nearMisses := []map[string]any{
			{"user": "U0000WRONG", "username": "alice2"},
			{"user": "U0000WRONG", "username": "malice"},
			{"username": "alice"},                      // no author ID
			{"user": "not-an-id", "username": "alice"}, // garbage
		}
		// The directory is the next strategy: it has the real alice.
		rec := directory{nearMisses: nearMisses, pages: [][]map[string]any{{alice}}}.recorder()
		got, err := ResolveUserID(ctx, rec.client(), "@alice")
		if err != nil || got != "U00000001A" {
			t.Errorf("got %q, %v", got, err)
		}
		if !reflect.DeepEqual(rec.methods(), []string{"search.messages", "users.list"}) {
			t.Errorf("methods = %v", rec.methods())
		}

		// The exact handle behind near misses is found.
		rec = hugeWorkspace(directory{nearMisses: nearMisses[:2], posted: map[string]string{"alice": "U00000001A"}})
		if got, err := ResolveUserID(ctx, rec.client(), "@alice"); err != nil || got != "U00000001A" {
			t.Errorf("behind near misses: got %q, %v", got, err)
		}
	})

	t.Run("a member who never posted is found in the directory, not reported missing", func(t *testing.T) {
		rec := directory{posted: map[string]string{"bob": "U00000002A"}, pages: [][]map[string]any{{alice}}}.recorder()
		got, err := ResolveUserID(ctx, rec.client(), "@alice")
		if err != nil || got != "U00000001A" {
			t.Errorf("got %q, %v", got, err)
		}
		if !reflect.DeepEqual(rec.methods(), []string{"search.messages", "users.list"}) {
			t.Errorf("methods = %v", rec.methods())
		}
	})

	t.Run("a failing search falls through to the directory", func(t *testing.T) {
		for _, code := range []string{"missing_scope", "ratelimited", "not_allowed_token_type"} {
			rec := directory{searchErr: code, pages: [][]map[string]any{{alice}}}.recorder()
			if got, err := ResolveUserID(ctx, rec.client(), "@alice"); err != nil || got != "U00000001A" {
				t.Errorf("search failing with %s: got %q, %v", code, got, err)
			}
		}
		// So does a rate limited search.
		rec := directory{pages: [][]map[string]any{{alice}}}.recorder()
		serve := rec.serve
		rec.serve = func(method string, params map[string]any) (map[string]any, error) {
			if method == "search.messages" {
				return nil, &slackapi.RateLimitError{Method: method, RetryAfter: time.Minute}
			}
			return serve(method, params)
		}
		if got, err := ResolveUserID(ctx, rec.client(), "@alice"); err != nil || got != "U00000001A" {
			t.Errorf("rate limited search: got %q, %v", got, err)
		}
	})

	t.Run("dead credentials and cancellation propagate", func(t *testing.T) {
		rec := directory{searchErr: "invalid_auth", pages: [][]map[string]any{{alice}}}.recorder()
		_, err := ResolveUserID(ctx, rec.client(), "@alice")
		if !slackapi.IsAuthError(err) || len(rec.only("users.list")) != 0 {
			t.Errorf("err = %v after %v", err, rec.methods())
		}

		cancelled, cancel := context.WithCancel(ctx)
		rec = directory{pages: [][]map[string]any{{alice}}}.recorder()
		rec.serve = func(string, map[string]any) (map[string]any, error) {
			cancel()
			return nil, cancelled.Err()
		}
		_, err = ResolveUserID(cancelled, rec.client(), "@alice")
		if !errors.Is(err, context.Canceled) || len(rec.calls) != 1 {
			t.Errorf("err = %v after %v", err, rec.methods())
		}
	})

	t.Run("no search for what search cannot answer", func(t *testing.T) {
		// Bot tokens cannot call search.*.
		rec := directory{posted: map[string]string{"alice": "U0000WRONG"}, pages: [][]map[string]any{{alice}}}.recorder()
		bot := slackapi.New(slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxb-test"}, "https://test.slack.com",
			slackapi.WithHandler(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
				rec.calls = append(rec.calls, call{method, params})
				return rec.serve(method, params)
			}))
		if got, err := ResolveUserID(ctx, bot, "@alice"); err != nil || got != "U00000001A" {
			t.Errorf("bot token: got %q, %v", got, err)
		}
		if !reflect.DeepEqual(rec.methods(), []string{"users.list"}) {
			t.Errorf("bot token: methods = %v", rec.methods())
		}

		// A display name is not a handle, and cannot go in a query.
		named := member("U00000003A", "ada", "profile", profile("display_name", "Ada L"))
		rec = directory{pages: [][]map[string]any{{named}}}.recorder()
		if got, err := ResolveUserID(ctx, rec.client(), "Ada L"); err != nil || got != "U00000003A" {
			t.Errorf("display name: got %q, %v", got, err)
		}
		if !reflect.DeepEqual(rec.methods(), []string{"users.list"}) {
			t.Errorf("display name: methods = %v", rec.methods())
		}

		// Emails have their own lookup.
		rec = directory{lookupErr: "missing_scope", pages: [][]map[string]any{{member("U00000004A", "al", "profile", profile("email", "al@example.com"))}}}.recorder()
		if got, err := ResolveUserID(ctx, rec.client(), "al@example.com"); err != nil || got != "U00000004A" {
			t.Errorf("email: got %q, %v", got, err)
		}
		if len(rec.only("search.messages")) != 0 {
			t.Errorf("email: methods = %v", rec.methods())
		}
	})

	t.Run("a directory too large to scan fails with what to do instead", func(t *testing.T) {
		rec := hugeWorkspace(directory{})
		_, err := ResolveUserID(ctx, rec.client(), "@ghost")
		var rl *slackapi.RateLimitError
		var nf *NotFoundError
		if !errors.As(err, &rl) || errors.As(err, &nf) {
			t.Fatalf("err = %v, want a rate limit error and not a NotFoundError", err)
		}
		for _, want := range []string{`"@ghost"`, "too large to scan", "user ID (U…)", "email", appinfo.EnvName("RATE_LIMIT_MAX_WAIT_MS")} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error does not mention %q: %v", want, err)
			}
		}
		if n := len(rec.only("users.list")); n != 1 {
			t.Errorf("users.list calls = %d, want 1", n)
		}
	})

	t.Run("GetUser fetches the profile of a user found by search", func(t *testing.T) {
		full := member("U00000001A", "alice", "real_name", "Alice A")
		rec := directory{posted: map[string]string{"alice": "U00000001A"}, pages: [][]map[string]any{{full}}}.recorder()
		got, err := GetUser(ctx, rec.client(), "@alice")
		if err != nil || got.RealName != "Alice A" {
			t.Errorf("got %+v, %v", got, err)
		}
		if !reflect.DeepEqual(rec.methods(), []string{"search.messages", "users.info"}) {
			t.Errorf("methods = %v", rec.methods())
		}
	})
}

func TestResolveUserIDs(t *testing.T) {
	ctx := context.Background()
	dir := directory{
		posted: map[string]string{"alice": "U00000001A"},
		pages: [][]map[string]any{
			{member("U00000001A", "alice"), member("U00000002A", "bob")},
			{member("U00000003A", "carol"), member("U00000005A", "sam1", "real_name", "Sam"), member("U00000006A", "sam2", "real_name", "Sam")},
		},
		byEmail: map[string]map[string]any{"dave@example.com": member("U00000004A", "dave")},
	}
	inputs := []string{"U00000009A", "@alice", "@bob", "carol", "@ghost", "dave@example.com", "nobody@example.com", " ", "Sam", "BOB"}
	rec := dir.recorder()
	got, err := ResolveUserIDs(ctx, rec.client(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(inputs) {
		t.Fatalf("%d resolutions for %d inputs", len(got), len(inputs))
	}
	wantIDs := []string{"U00000009A", "U00000001A", "U00000002A", "U00000003A", "", "U00000004A", "", "", "", "U00000002A"}
	for i, r := range got {
		if r.Input != inputs[i] || r.ID != wantIDs[i] || (r.ID == "") != (r.Err != nil) {
			t.Errorf("resolution %d = %+v, want ID %q", i, r, wantIDs[i])
		}
	}
	var nf *NotFoundError
	var amb *AmbiguousError
	if !errors.As(got[4].Err, &nf) || nf.Input != "@ghost" {
		t.Errorf("@ghost: err = %v", got[4].Err)
	}
	if !errors.As(got[6].Err, &nf) || nf.Input != "nobody@example.com" {
		t.Errorf("unknown email: err = %v", got[6].Err)
	}
	if got[7].Err == nil || !strings.Contains(got[7].Err.Error(), "user is empty") {
		t.Errorf("blank: err = %v", got[7].Err)
	}
	if !errors.As(got[8].Err, &amb) || len(amb.Matches) != 2 {
		t.Errorf("Sam: err = %v", got[8].Err)
	}

	// One scan of the two pages for the five inputs that needed it, and
	// one search per distinct handle.
	if n := len(rec.only("users.list")); n != 2 {
		t.Errorf("users.list calls = %d, want 2 (a single scan)", n)
	}
	var queries []string
	for _, c := range rec.only("search.messages") {
		queries = append(queries, c.params["query"].(string))
	}
	if want := []string{"from:@alice", "from:@bob", "from:@carol", "from:@ghost", "from:@sam"}; !reflect.DeepEqual(queries, want) {
		t.Errorf("searches = %v, want %v", queries, want)
	}

	// Nothing needs the directory: it is not scanned.
	rec = hugeWorkspace(dir)
	got, err = ResolveUserIDs(ctx, rec.client(), []string{"@alice", "U00000009A", "alice"})
	if err != nil || got[0].ID != "U00000001A" || got[2].ID != "U00000001A" || len(rec.only("search.messages")) != 1 {
		t.Errorf("got %+v, %v after %v", got, err, rec.methods())
	}

	// An operational failure is the error, not a per-input outcome.
	got, err = ResolveUserIDs(ctx, hugeWorkspace(dir).client(), []string{"@alice", "@bob"})
	var rl *slackapi.RateLimitError
	if !errors.As(err, &rl) || got != nil || !strings.Contains(err.Error(), `"@bob"`) || strings.Contains(err.Error(), `"@alice"`) {
		t.Errorf("got %+v, err = %v", got, err)
	}

	if got, err := ResolveUserIDs(ctx, dir.recorder().client(), nil); err != nil || len(got) != 0 {
		t.Errorf("no inputs: got %+v, %v", got, err)
	}
}

func TestGetDMChannelForUsersReportsAlreadyOpen(t *testing.T) {
	for _, alreadyOpen := range []bool{true, false} {
		rec := directory{openChannel: "D00000001A", alreadyOpen: alreadyOpen}.recorder()
		got, err := GetDMChannelForUsers(context.Background(), rec.client(), []string{"U00000001A"})
		if err != nil || got.AlreadyOpen != alreadyOpen {
			t.Errorf("already_open %v: got %+v, %v", alreadyOpen, got, err)
		}
	}
}
