// Package search finds Slack messages and files, either through Slack's
// search API (search.messages / search.files) or, when the search is
// scoped to channels, by scanning those channels and matching locally.
package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/maelvls/slackcli/internal/slack/channels"
	"github.com/maelvls/slackcli/internal/slack/enrich"
	"github.com/maelvls/slackcli/internal/slack/messages"
	"github.com/maelvls/slackcli/internal/slack/users"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const (
	// DefaultLimit is the number of results of each kind returned when
	// Options.Limit is zero.
	DefaultLimit = 20
	// MaxLimit is the largest Options.Limit accepted.
	MaxLimit = 200
	// DefaultMaxContentChars is the message content length used when
	// Options.MaxContentChars is zero.
	DefaultMaxContentChars = 4000

	// The concurrency bounds keep a search from tripping Slack's rate
	// limits by issuing all of its follow-up requests at once.
	fetchConcurrency    = 4
	downloadConcurrency = 4
	scanConcurrency     = 4
)

// Kind selects what to search for.
type Kind string

const (
	KindMessages Kind = "messages"
	KindFiles    Kind = "files"
	KindAll      Kind = "all"
)

// Options describes a search.
type Options struct {
	// Query is the search text. Without Channels it is a Slack search
	// query (operators such as in:#channel or has:link are allowed). With
	// Channels it is matched locally: every word and every "quoted
	// phrase" must appear, case-insensitively.
	Query string
	Kind  Kind
	// Channels ("#name", "name" or ID) scopes the search to these
	// channels, which are then scanned instead of calling the search API.
	Channels []string
	// User keeps only what this user posted: a user ID, "@handle",
	// handle, email address or display name.
	User string
	// After and Before are days (YYYY-MM-DD), both exclusive like Slack's
	// after: and before: operators: results are dated strictly after the
	// After day and strictly before the Before day.
	After  string
	Before string
	// ContentType filters results; "" means ContentAny.
	ContentType ContentType
	// Limit is the maximum number of results of each kind: 0 means
	// DefaultLimit, and it cannot exceed MaxLimit.
	Limit int
	// MaxContentChars truncates message content: 0 means
	// DefaultMaxContentChars, a negative value means no limit.
	MaxContentChars int
	// NoDownload skips downloading files: results then carry no local
	// path.
	NoDownload bool
	// ResolveUsers adds the profiles of the users the messages refer to;
	// RefreshUsers does too, bypassing the user cache.
	ResolveUsers bool
	RefreshUsers bool

	// DownloadsDir and CacheDir override where files are downloaded and
	// where the user cache lives; "" means the appinfo defaults.
	DownloadsDir string
	CacheDir     string
	// Location is the time zone After and Before are interpreted in when
	// scanning channels; nil means time.Local. (The search API uses the
	// time zone of the Slack account.)
	Location *time.Location
}

// Message is a message search result: the compact message shape without
// thread_ts, plus the permalink (which carries the thread).
type Message struct {
	ChannelID        string                     `json:"channel_id"`
	TS               string                     `json:"ts"`
	Author           *messages.Author           `json:"author,omitempty"`
	Content          string                     `json:"content,omitempty"`
	Files            []messages.CompactFile     `json:"files,omitempty"`
	ForwardedThreads []messages.ForwardedThread `json:"forwarded_threads,omitempty"`
	Permalink        string                     `json:"permalink,omitempty"`
}

// File is a file search result.
type File struct {
	Title    string `json:"title,omitempty"`
	Mimetype string `json:"mimetype,omitempty"`
	Mode     string `json:"mode,omitempty"`
	// Path is the downloaded file, or the error file when Error is set.
	// It is empty when downloads are off.
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
	// Permalink opens the file in Slack.
	Permalink string `json:"permalink,omitempty"`
}

// More says that a result list is incomplete and how to get the rest.
type More struct {
	// Total is Slack's count of matches for the query, before any
	// content-type filtering (search API only).
	Total int `json:"total,omitempty"`
	// Before is the --before value that continues the search with older
	// results; empty when that would not make progress.
	Before string `json:"before,omitempty"`
	// Hint tells the reader what to do.
	Hint string `json:"hint"`
}

// Result is what a search prints. Lists are sorted newest first.
type Result struct {
	Messages        []Message                    `json:"messages,omitempty"`
	Files           []File                       `json:"files,omitempty"`
	ReferencedUsers map[string]users.CompactUser `json:"referenced_users,omitempty"`
	// MoreMessages and MoreFiles are set when the corresponding list was
	// cut short.
	MoreMessages *More `json:"more_messages,omitempty"`
	MoreFiles    *More `json:"more_files,omitempty"`
	// Warnings are non-fatal problems (failed downloads, a scan cut short
	// by a rate limit) for the caller to show on stderr.
	Warnings []string `json:"-"`
}

// searcher is the state of one Search call.
type searcher struct {
	client      *slackapi.Client
	opts        Options
	limit       int
	contentType ContentType
	days        dayRange

	// Channel-scoped scan.
	channelIDs []string
	userID     string
	match      matcher
	// Search API.
	query string

	mu          sync.Mutex
	warnings    []string
	degraded    int
	degradedErr error
	// fetchLimited stops the fetching of matched messages once Slack has
	// rate limited it: the rest are rendered from the search index.
	fetchLimited enrich.Gate
}

// Search runs the search described by opts. For KindAll, messages and
// files are searched concurrently.
func Search(ctx context.Context, client *slackapi.Client, opts Options) (*Result, error) {
	s, err := newSearcher(client, opts)
	if err != nil {
		return nil, err
	}
	if err := s.prepare(ctx); err != nil {
		return nil, err
	}
	// What the lookups below could not do for being rate limited (file
	// details, user profiles) ends up in Result.Warnings.
	ctx, incomplete := enrich.Collect(ctx)

	var (
		res     = &Result{}
		matched []messages.Message
		scoped  = len(s.channelIDs) > 0
		parts   []func(ctx context.Context) error
	)
	if opts.Kind != KindFiles {
		parts = append(parts, func(ctx context.Context) error {
			find := s.searchMessages
			if scoped {
				find = s.scanMessages
			}
			hits, more, err := find(ctx)
			if err != nil {
				return err
			}
			for _, h := range hits {
				matched = append(matched, h.msg)
			}
			res.MoreMessages = more
			res.Messages, err = s.compactMessages(ctx, hits)
			return err
		})
	}
	if opts.Kind != KindMessages {
		parts = append(parts, func(ctx context.Context) error {
			find := s.searchFiles
			if scoped {
				find = s.scanFiles
			}
			hits, more, err := find(ctx)
			if err != nil {
				return err
			}
			res.MoreFiles = more
			res.Files, err = s.downloadFiles(ctx, hits)
			return err
		})
	}
	err = forEach(ctx, len(parts), len(parts), func(ctx context.Context, i int) error {
		return parts[i](ctx)
	})
	if err != nil {
		return nil, err
	}

	if opts.ResolveUsers || opts.RefreshUsers {
		ids := users.CollectReferencedUserIDs(matched, false)
		// An enrichment: a rate limit leaves some users out and a warning,
		// the error is for dead credentials and cancellation.
		byID, err := users.ResolveByID(ctx, client, ids, users.ResolveOptions{ForceRefresh: opts.RefreshUsers, CacheDir: opts.CacheDir})
		if err != nil {
			return nil, fmt.Errorf("resolving the users referenced by the results: %w", err)
		}
		res.ReferencedUsers = users.ToReferencedUsers(ids, byID)
	}

	if s.degraded > 0 {
		s.warnf("%d message(s) could not be fetched in full (%v): they are shown as the search index has them, possibly without attachments",
			s.degraded, s.degradedErr)
	}
	res.Warnings = append(s.warnings, incomplete.List()...)
	return res, nil
}

func newSearcher(client *slackapi.Client, opts Options) (*searcher, error) {
	switch opts.Kind {
	case KindMessages, KindFiles, KindAll:
	default:
		return nil, fmt.Errorf("unknown search kind %q: expected messages, files or all", opts.Kind)
	}
	s := &searcher{client: client, opts: opts, limit: opts.Limit, contentType: opts.ContentType}
	if s.limit == 0 {
		s.limit = DefaultLimit
	}
	if s.limit < 1 || s.limit > MaxLimit {
		return nil, fmt.Errorf("invalid limit %d: it must be between 1 and %d (to go further back, repeat the search with an earlier --before date)", opts.Limit, MaxLimit)
	}
	if s.contentType == "" {
		s.contentType = ContentAny
	}
	if _, err := ParseContentType(string(s.contentType)); err != nil {
		return nil, err
	}
	if s.opts.Location == nil {
		s.opts.Location = time.Local
	}
	var err error
	s.days, err = parseDayRange(opts.After, opts.Before, s.opts.Location)
	return s, err
}

// prepare resolves what both the message and the file search need, so
// that KindAll does it once.
func (s *searcher) prepare(ctx context.Context) error {
	if len(s.opts.Channels) == 0 {
		// Bot tokens get not_allowed_token_type from search.*: say what
		// works instead, before making any call.
		if strings.HasPrefix(s.client.Auth().BearerToken(), "xoxb-") {
			return errSearchNotAllowed
		}
		var err error
		s.query, err = s.buildQuery(ctx)
		return err
	}

	var err error
	if s.match, err = newMatcher(s.opts.Query); err != nil {
		return err
	}
	ids := make([]string, len(s.opts.Channels))
	err = forEach(ctx, len(ids), scanConcurrency, func(ctx context.Context, i int) error {
		var err error
		ids[i], err = channels.ResolveID(ctx, s.client, s.opts.Channels[i])
		return err
	})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			s.channelIDs = append(s.channelIDs, id)
		}
	}
	if strings.TrimSpace(s.opts.User) != "" {
		if s.userID, err = users.ResolveUserID(ctx, s.client, s.opts.User); err != nil {
			return err
		}
	}
	return nil
}

func (s *searcher) warnf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warnings = append(s.warnings, fmt.Sprintf(format, args...))
}

// isFatal reports whether err must abort the search rather than cost one
// result: the command was cancelled, or the credentials are dead (the
// caller refreshes them and retries).
func isFatal(ctx context.Context, err error) bool {
	return err != nil && (ctx.Err() != nil || slackapi.IsAuthError(err))
}

func isRateLimit(err error) bool {
	var rl *slackapi.RateLimitError
	return errors.As(err, &rl)
}

// forEach calls fn(i) for i in [0, n) on at most workers goroutines and
// returns the first error, cancelling the calls still running.
func forEach(ctx context.Context, n, workers int, fn func(ctx context.Context, i int) error) error {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
		sem   = make(chan struct{}, max(workers, 1))
	)
	for i := range n {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			if err := fn(ctx, i); err != nil {
				mu.Lock()
				if first == nil {
					first = err
				}
				mu.Unlock()
				cancel()
			}
		})
	}
	wg.Wait()
	if first == nil {
		// Cancelled from outside before every call ran.
		return parent.Err()
	}
	return first
}
