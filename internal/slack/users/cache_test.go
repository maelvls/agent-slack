package users

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/slackapi"
)

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func at(t time.Time) func() time.Time { return func() time.Time { return t } }

// infoServer serves users.info for any user ID, except those in missing.
func infoServer(missing ...string) *recorder {
	return &recorder{serve: func(method string, params map[string]any) (map[string]any, error) {
		if method != "users.info" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		id := params["user"].(string)
		for _, m := range missing {
			if m == id {
				return apiError("user_not_found"), nil
			}
		}
		return map[string]any{"user": member(id, "handle-"+id, "real_name", "Real "+id,
			"profile", profile("display_name", "Display "+id, "email", id+"@example.com", "title", "Engineer"))}, nil
	}}
}

func cacheFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestResolveByIDCachesUsers(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "cache")
	opts := ResolveOptions{CacheDir: dir, Now: at(t0)}

	rec := infoServer()
	got, err := ResolveByID(ctx, rec.client(), []string{"U11111111", " U22222222 ", "U11111111", "not-a-user", "B33333333"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["U11111111"].Name != "handle-U11111111" || got["U22222222"].DisplayName != "Display U22222222" {
		t.Errorf("users = %+v", got)
	}
	if n := len(rec.calls); n != 2 {
		t.Errorf("%d users.info calls, want 2", n)
	}

	// Private file in a private directory, keyed by workspace.
	files := cacheFiles(t, dir)
	if len(files) != 1 || !strings.HasPrefix(files[0], "users-cache-") || !strings.HasSuffix(files[0], ".json") {
		t.Fatalf("cache dir = %v", files)
	}
	if info, _ := os.Stat(filepath.Join(dir, files[0])); info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", info.Mode().Perm())
	}

	// Served from the cache, with every field intact. (The TypeScript
	// re-parsed cached users as raw Slack users, which dropped
	// display_name, email and title.)
	rec2 := infoServer()
	again, err := ResolveByID(ctx, rec2.client(), []string{"U11111111", "U22222222"}, ResolveOptions{CacheDir: dir, Now: at(t0.Add(23 * time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec2.calls) != 0 {
		t.Errorf("%d API calls on a warm cache", len(rec2.calls))
	}
	if !reflect.DeepEqual(again, got) {
		t.Errorf("cached = %+v\nfetched = %+v", again, got)
	}
	if u := again["U11111111"]; u.DisplayName != "Display U11111111" || u.Email != "U11111111@example.com" || u.Title != "Engineer" {
		t.Errorf("cached user lost fields: %+v", u)
	}

	// Only the missing user is fetched.
	rec3 := infoServer()
	if _, err := ResolveByID(ctx, rec3.client(), []string{"U11111111", "U44444444"}, opts); err != nil {
		t.Fatal(err)
	}
	if calls := rec3.only("users.info"); len(calls) != 1 || calls[0].params["user"] != "U44444444" {
		t.Errorf("calls = %v", calls)
	}
}

func TestResolveByIDExpiry(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := ResolveByID(ctx, infoServer().client(), []string{"U11111111", "U22222222"}, ResolveOptions{CacheDir: dir, Now: at(t0)}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		opts      ResolveOptions
		wantCalls int
	}{
		{"just before the TTL", ResolveOptions{Now: at(t0.Add(CacheTTL - time.Second))}, 0},
		{"at the TTL", ResolveOptions{Now: at(t0.Add(CacheTTL))}, 1},
		{"forced refresh", ResolveOptions{Now: at(t0.Add(time.Minute)), ForceRefresh: true}, 1},
		// A clock that went backwards must not make entries immortal.
		{"entry dated in the future", ResolveOptions{Now: at(t0.Add(-time.Hour))}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each case works on its own copy of the cache.
			copyDir := t.TempDir()
			for _, name := range cacheFiles(t, dir) {
				data, _ := os.ReadFile(filepath.Join(dir, name))
				if err := os.WriteFile(filepath.Join(copyDir, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			tt.opts.CacheDir = copyDir
			rec := infoServer()
			got, err := ResolveByID(ctx, rec.client(), []string{"U11111111"}, tt.opts)
			if err != nil || got["U11111111"].ID != "U11111111" {
				t.Fatalf("got %+v, %v", got, err)
			}
			if len(rec.calls) != tt.wantCalls {
				t.Errorf("%d API calls, want %d", len(rec.calls), tt.wantCalls)
			}
		})
	}

	// Writing prunes entries that have expired.
	late := t0.Add(CacheTTL + time.Hour)
	if _, err := ResolveByID(ctx, infoServer().client(), []string{"U33333333"}, ResolveOptions{CacheDir: dir, Now: at(late)}); err != nil {
		t.Fatal(err)
	}
	entries := loadCache(filepath.Join(dir, cacheFiles(t, dir)[0]))
	if len(entries) != 1 || entries["U33333333"].FetchedAt != late.UnixMilli() {
		t.Errorf("entries after pruning = %+v", entries)
	}
}

func TestResolveByIDKeysCacheByWorkspace(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	for _, ws := range []string{"https://acme.slack.com", "https://other.slack.com"} {
		rec := infoServer()
		if _, err := ResolveByID(ctx, rec.client(), []string{"U11111111"}, ResolveOptions{CacheDir: dir, Now: at(t0), WorkspaceURL: ws}); err != nil {
			t.Fatal(err)
		}
		if len(rec.calls) != 1 {
			t.Errorf("%s: %d calls, want 1 (no cross-workspace hit)", ws, len(rec.calls))
		}
	}
	if n := len(cacheFiles(t, dir)); n != 2 {
		t.Errorf("%d cache files, want 2", n)
	}

	// Same workspace, however its URL is written.
	for _, ws := range []string{"https://ACME.slack.com/", "acme.slack.com", " https://acme.slack.com/archives/C1 "} {
		rec := infoServer()
		if _, err := ResolveByID(ctx, rec.client(), []string{"U11111111"}, ResolveOptions{CacheDir: dir, Now: at(t0), WorkspaceURL: ws}); err != nil {
			t.Fatal(err)
		}
		if len(rec.calls) != 0 {
			t.Errorf("%q: cache miss", ws)
		}
	}

	// By default the client's workspace is the key (NewFake: test.slack.com).
	if workspaceKey("https://test.slack.com") == "" || cachePath(dir, "") != "" {
		t.Error("unexpected workspace keys")
	}
	rec := infoServer()
	for range 2 {
		if _, err := ResolveByID(ctx, rec.client(), []string{"U11111111"}, ResolveOptions{CacheDir: dir, Now: at(t0)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.calls) != 1 || len(cacheFiles(t, dir)) != 3 {
		t.Errorf("%d calls, files %v", len(rec.calls), cacheFiles(t, dir))
	}
}

func TestResolveByIDWithoutWorkspaceDoesNotCache(t *testing.T) {
	dir := t.TempDir()
	rec := infoServer()
	client := slackapi.New(slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxb-test"}, "", slackapi.WithHandler(
		func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
			rec.calls = append(rec.calls, call{method, params})
			return rec.serve(method, params)
		}))
	for range 2 {
		got, err := ResolveByID(context.Background(), client, []string{"U11111111"}, ResolveOptions{CacheDir: dir, Now: at(t0)})
		if err != nil || len(got) != 1 {
			t.Fatalf("got %+v, %v", got, err)
		}
	}
	if len(rec.calls) != 2 || len(cacheFiles(t, dir)) != 0 {
		t.Errorf("%d calls, files %v", len(rec.calls), cacheFiles(t, dir))
	}
}

func TestResolveByIDDefaultsToTheAppCacheDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	if _, err := ResolveByID(context.Background(), infoServer().client(), []string{"U11111111"}, ResolveOptions{Now: at(t0)}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(appinfo.CacheDir(), home) || len(cacheFiles(t, appinfo.CacheDir())) != 1 {
		t.Errorf("cache dir %s holds %v", appinfo.CacheDir(), cacheFiles(t, appinfo.CacheDir()))
	}
}

func TestResolveByIDUnresolvableUsers(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	opts := ResolveOptions{CacheDir: dir, Now: at(t0)}

	got, err := ResolveByID(ctx, infoServer("U0000GHOST").client(), []string{"U11111111", "U0000GHOST"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["U0000GHOST"]; ok || len(got) != 1 {
		t.Errorf("users = %+v", got)
	}
	// Failures are not cached: the user is asked for again next time.
	rec := infoServer()
	if _, err := ResolveByID(ctx, rec.client(), []string{"U11111111", "U0000GHOST"}, opts); err != nil {
		t.Fatal(err)
	}
	if calls := rec.only("users.info"); len(calls) != 1 || calls[0].params["user"] != "U0000GHOST" {
		t.Errorf("calls = %v", calls)
	}

	// Nothing to resolve: no API call, no file.
	rec = infoServer()
	empty := t.TempDir()
	got, err = ResolveByID(ctx, rec.client(), []string{"", "general", "B12345678"}, ResolveOptions{CacheDir: empty})
	if err != nil || len(got) != 0 || len(rec.calls) != 0 || len(cacheFiles(t, empty)) != 0 {
		t.Errorf("got %+v, %v, calls %v", got, err, rec.methods())
	}
}

func TestResolveByIDFatalErrors(t *testing.T) {
	ctx := context.Background()

	rec := &recorder{serve: func(string, map[string]any) (map[string]any, error) { return apiError("token_expired"), nil }}
	if _, err := ResolveByID(ctx, rec.client(), []string{"U11111111"}, ResolveOptions{CacheDir: t.TempDir()}); !slackapi.IsAuthError(err) {
		t.Errorf("err = %v, want an auth error", err)
	}

	rec = &recorder{serve: func(method string, _ map[string]any) (map[string]any, error) {
		return nil, &slackapi.RateLimitError{Method: method, RetryAfter: time.Minute}
	}}
	var ids []string
	for i := range 40 {
		ids = append(ids, fmt.Sprintf("U%08d", i))
	}
	_, err := ResolveByID(ctx, rec.client(), ids, ResolveOptions{CacheDir: t.TempDir()})
	var rl *slackapi.RateLimitError
	if !errors.As(err, &rl) {
		t.Errorf("err = %v, want a rate limit error", err)
	}
	// The remaining lookups are not attempted once Slack says to back off.
	if n := len(rec.calls); n > fetchConcurrency {
		t.Errorf("%d calls after being rate limited", n)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	rec = &recorder{serve: func(string, map[string]any) (map[string]any, error) { return nil, cancelled.Err() }}
	if _, err := ResolveByID(cancelled, rec.client(), []string{"U11111111"}, ResolveOptions{CacheDir: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestResolveByIDBoundsConcurrency(t *testing.T) {
	var mu sync.Mutex
	inFlight, peak := 0, 0
	inner := infoServer()
	rec := &recorder{serve: func(method string, params map[string]any) (map[string]any, error) {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return inner.serve(method, params)
	}}
	var ids []string
	for i := range 40 {
		ids = append(ids, fmt.Sprintf("U%08d", i))
	}
	got, err := ResolveByID(context.Background(), rec.client(), ids, ResolveOptions{CacheDir: t.TempDir()})
	if err != nil || len(got) != 40 {
		t.Fatalf("%d users, %v", len(got), err)
	}
	if peak > fetchConcurrency {
		t.Errorf("peak concurrency = %d, want <= %d", peak, fetchConcurrency)
	}
}

func TestCacheFileTolerance(t *testing.T) {
	ctx := context.Background()
	fresh := t0.UnixMilli()
	tests := map[string]string{
		"corrupt JSON":    `{"version": 1, "entries": {"U11111111": `,
		"not an object":   `[]`,
		"other version":   fmt.Sprintf(`{"version": 2, "entries": {"U11111111": {"fetched_at": %d, "user": {"id": "U11111111", "name": "stale"}}}}`, fresh),
		"no fetched_at":   `{"version": 1, "entries": {"U11111111": {"user": {"id": "U11111111", "name": "stale"}}}}`,
		"entry sans user": fmt.Sprintf(`{"version": 1, "entries": {"U11111111": {"fetched_at": %d}}}`, fresh),
		"wrong types":     `{"version": 1, "entries": {"U11111111": "nope"}}`,
		"empty file":      ``,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := cachePath(dir, "https://test.slack.com")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			rec := infoServer()
			got, err := ResolveByID(ctx, rec.client(), []string{"U11111111"}, ResolveOptions{CacheDir: dir, Now: at(t0)})
			if err != nil || got["U11111111"].Name != "handle-U11111111" || len(rec.calls) != 1 {
				t.Fatalf("got %+v, %v after %d calls", got, err, len(rec.calls))
			}
			// The bad file was replaced by a good one.
			if entries := loadCache(path); len(entries) != 1 {
				t.Errorf("entries = %+v", entries)
			}
		})
	}

	t.Run("entries under keys that are not user IDs are ignored", func(t *testing.T) {
		dir := t.TempDir()
		path := cachePath(dir, "https://test.slack.com")
		content := fmt.Sprintf(`{"version": 1, "entries": {"../x": {"fetched_at": %d, "user": {"id": "U1"}}, "U11111111": {"fetched_at": %d, "user": {"id": "U11111111", "name": "cached"}}}}`, fresh, fresh)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if entries := loadCache(path); len(entries) != 1 || entries["U11111111"].User.Name != "cached" {
			t.Errorf("entries = %+v", entries)
		}
	})

	t.Run("an unwritable cache directory is not an error", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, []byte("a file, not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ResolveByID(ctx, infoServer().client(), []string{"U11111111"}, ResolveOptions{CacheDir: filepath.Join(blocker, "cache")})
		if err != nil || len(got) != 1 {
			t.Errorf("got %+v, %v", got, err)
		}
	})
}

func TestCacheFileFormat(t *testing.T) {
	dir := t.TempDir()
	if _, err := ResolveByID(context.Background(), infoServer().client(), []string{"U11111111"}, ResolveOptions{CacheDir: dir, Now: at(t0)}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cachePath(dir, "https://test.slack.com"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"version": float64(1),
		"entries": map[string]any{"U11111111": map[string]any{
			"fetched_at": float64(t0.UnixMilli()),
			"user": map[string]any{
				"id": "U11111111", "name": "handle-U11111111", "real_name": "Real U11111111",
				"display_name": "Display U11111111", "email": "U11111111@example.com", "title": "Engineer",
			},
		}},
	}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("file = %s", data)
	}
}

// Many invocations of the CLI may resolve users at the same time.
func TestCacheConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := cachePath(dir, "https://test.slack.com")

	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			// A reader must never see a partially written file.
			if data, err := os.ReadFile(path); err == nil {
				var file cacheFile
				if err := json.Unmarshal(data, &file); err != nil {
					t.Errorf("torn cache file: %v", err)
					return
				}
			}
		}
	})

	var writers sync.WaitGroup
	for w := range 8 {
		writers.Go(func() {
			for i := range 10 {
				id := fmt.Sprintf("U%04d%04d", w, i)
				got, err := ResolveByID(ctx, infoServer().client(), []string{id, "U0SHARED00"}, ResolveOptions{CacheDir: dir, Now: at(t0)})
				if err != nil || got[id].ID != id {
					t.Errorf("writer %d: got %+v, %v", w, got, err)
				}
			}
		})
	}
	writers.Wait()
	close(stop)
	readers.Wait()

	entries := loadCache(path)
	if entries["U0SHARED00"].User.ID != "U0SHARED00" {
		t.Errorf("shared user missing from the cache (%d entries)", len(entries))
	}
	if left := cacheFiles(t, dir); len(left) != 1 {
		t.Errorf("temporary files left behind: %v", left)
	}
}

// A writer keeps what another invocation stored after it started.
func TestCacheWriteMergesWithConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := cachePath(dir, "https://test.slack.com")

	inner := infoServer()
	rec := &recorder{}
	rec.serve = func(method string, params map[string]any) (map[string]any, error) {
		// While this invocation is fetching, another one finishes and
		// writes the cache.
		if _, err := ResolveByID(ctx, infoServer().client(), []string{"U22222222"}, ResolveOptions{CacheDir: dir, Now: at(t0)}); err != nil {
			t.Error(err)
		}
		return inner.serve(method, params)
	}
	if _, err := ResolveByID(ctx, rec.client(), []string{"U11111111"}, ResolveOptions{CacheDir: dir, Now: at(t0)}); err != nil {
		t.Fatal(err)
	}
	entries := loadCache(path)
	if len(entries) != 2 || entries["U11111111"].User.ID == "" || entries["U22222222"].User.ID == "" {
		t.Errorf("entries = %+v", entries)
	}
}
