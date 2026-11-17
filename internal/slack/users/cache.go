package users

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slackapi"
	"github.com/maelvls/slackcli/internal/slackid"
)

const (
	cacheVersion = 1
	// CacheTTL is how long a cached user is served before being fetched
	// again.
	CacheTTL = 24 * time.Hour
	// fetchConcurrency bounds the users.info calls of ResolveByID.
	fetchConcurrency = 5
)

// ResolveOptions tunes ResolveByID.
type ResolveOptions struct {
	// WorkspaceURL keys the cache; "" means client.WorkspaceURL(). When
	// neither is known (a standard token used without a workspace), users
	// are fetched without being cached.
	WorkspaceURL string
	// ForceRefresh fetches every user again, ignoring cached entries.
	ForceRefresh bool
	// CacheDir is where cache files live; "" means appinfo.CacheDir().
	CacheDir string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

type cacheEntry struct {
	// FetchedAt is in Unix milliseconds.
	FetchedAt int64       `json:"fetched_at"`
	User      CompactUser `json:"user"`
}

type cacheFile struct {
	Version int                   `json:"version"`
	Entries map[string]cacheEntry `json:"entries"`
}

// fresh reports whether e may still be served at now. An entry dated in
// the future (clock moved backwards) is not trusted: it would otherwise
// never expire.
func (e cacheEntry) fresh(now time.Time) bool {
	age := now.Sub(time.UnixMilli(e.FetchedAt))
	return age >= 0 && age < CacheTTL
}

// ResolveByID returns the users with the given IDs, keyed by ID. Values
// that are not user IDs are ignored, as are users that cannot be fetched
// (deleted, from another workspace …): callers show the bare ID for
// those.
//
// Users are cached on disk per workspace for CacheTTL, so that reading
// messages does not cost one users.info call per author each time. The
// cache is safe to share between concurrent invocations; failing to read
// or write it is never an error.
//
// The error is non-nil only when the lookup must be abandoned: cancelled
// context, expired credentials, rate limit.
func ResolveByID(ctx context.Context, client *slackapi.Client, userIDs []string, opts ResolveOptions) (map[string]CompactUser, error) {
	ids := dedupeUserIDs(userIDs)
	out := make(map[string]CompactUser, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	now := time.Now()
	if opts.Now != nil {
		now = opts.Now()
	}
	path := cachePath(cmp.Or(opts.CacheDir, appinfo.CacheDir()), cmp.Or(opts.WorkspaceURL, client.WorkspaceURL()))
	cached := loadCache(path)

	var missing []string
	for _, id := range ids {
		if e, ok := cached[id]; ok && !opts.ForceRefresh && e.fresh(now) {
			out[id] = e.User
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) == 0 {
		return out, nil
	}

	fetched, err := fetchUsers(ctx, client, missing)
	for id, user := range fetched {
		out[id] = user
	}
	// Keep what was fetched even when the batch was cut short.
	storeCache(path, fetched, now)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// fetchUsers looks users up with users.info, a few at a time. Users that
// cannot be fetched are left out; the error reports a fatal failure only.
func fetchUsers(ctx context.Context, client *slackapi.Client, ids []string) (map[string]CompactUser, error) {
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		fatal error
		out   = make(map[string]CompactUser, len(ids))
		sem   = make(chan struct{}, fetchConcurrency)
	)
	for _, id := range ids {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			mu.Lock()
			abort := fatal != nil
			mu.Unlock()
			if abort {
				return
			}
			resp, err := client.API(ctx, "users.info", map[string]any{"user": id})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				if u := jsonx.Map(resp["user"]); jsonx.Str(u["id"]) != "" {
					out[id] = ToCompactUser(u)
				}
			case isFatal(ctx, err):
				fatal = cmp.Or(fatal, err)
			}
		})
	}
	wg.Wait()
	return out, fatal
}

// cachePath returns the cache file of a workspace, or "" when the
// workspace is unknown (nothing is cached then).
func cachePath(dir, workspaceURL string) string {
	key := workspaceKey(workspaceURL)
	if key == "" {
		return ""
	}
	return filepath.Join(dir, "users-cache-"+key+".json")
}

func workspaceKey(workspaceURL string) string {
	source := strings.ToLower(strings.TrimSpace(workspaceURL))
	if u, err := url.Parse(source); err == nil && u.Hostname() != "" {
		source = u.Hostname()
	}
	if source == "" || source == "unknown" {
		return ""
	}
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])[:16]
}

// loadCache reads a cache file. A missing, unreadable, corrupt or
// foreign-version file is an empty cache.
func loadCache(path string) map[string]cacheEntry {
	entries := map[string]cacheEntry{}
	if path == "" {
		return entries
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return entries
	}
	var file cacheFile
	if err := json.Unmarshal(data, &file); err != nil || file.Version != cacheVersion {
		return entries
	}
	for id, e := range file.Entries {
		if slackid.IsUserID(id) && e.FetchedAt > 0 && e.User.ID != "" {
			entries[id] = e
		}
	}
	return entries
}

// storeCache adds fetched users to the cache file and drops expired
// entries. It is best effort and safe against concurrent invocations: the
// file is re-read just before being rewritten, so that entries another
// process added meanwhile are kept, and it is replaced atomically, so
// that a reader never sees a half-written file.
func storeCache(path string, fetched map[string]CompactUser, now time.Time) {
	if path == "" || len(fetched) == 0 {
		return
	}
	entries := loadCache(path)
	for id, e := range entries {
		if !e.fresh(now) {
			delete(entries, id)
		}
	}
	for id, user := range fetched {
		entries[id] = cacheEntry{FetchedAt: now.UnixMilli(), User: user}
	}
	data, err := json.MarshalIndent(cacheFile{Version: cacheVersion, Entries: entries}, "", "  ")
	if err != nil {
		return
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".users-cache-*.tmp") // created 0600
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp.Name(), path)
}

func dedupeUserIDs(ids []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if slackid.IsUserID(id) && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
