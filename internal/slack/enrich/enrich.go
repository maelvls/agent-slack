// Package enrich holds the rule that every optional lookup follows.
//
// A command makes one primary call (the history, the search, the listing
// the user asked for) and then enriches the result: the profiles of the
// users it references, the names of its channels, the details of its files.
// A failed primary call is an error. A failed enrichment is not: the result
// is printed without it. Two failures still abort an enrichment and the
// command with it, since no further call can succeed: a cancelled context
// and dead credentials (which the caller refreshes before retrying), see
// [MustAbort].
//
// Being rate limited does not abort. Slack limits each method per minute,
// and the default policy is to fail fast rather than wait, so on a busy
// workspace a burst of lookups is cut short. The lookups already answered
// are kept, no further call of that kind is made for the invocation (see
// [Gate]), and the caller is told that the result is incomplete: through
// the per-item error fields and `Warnings` of the result when it has them,
// and otherwise through the [Warnings] collector carried by the context.
package enrich

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// RateLimitedCode is what per-item error fields (`messages_error`,
// `message_error`) hold for a lookup skipped because of a rate limit. It
// is the error code Slack itself uses.
const RateLimitedCode = "ratelimited"

// MustAbort reports whether err, returned by an optional lookup, must fail
// the whole command: the command was cancelled, or the credentials are no
// longer valid.
func MustAbort(ctx context.Context, err error) bool {
	return err != nil && (ctx.Err() != nil || slackapi.IsAuthError(err))
}

// IsRateLimit reports whether err is Slack refusing a call for now.
func IsRateLimit(err error) bool {
	var rl *slackapi.RateLimitError
	return errors.As(err, &rl)
}

// Gate stops one kind of optional lookup for the rest of an invocation
// once Slack has rate limited it: the calls that follow would be refused
// too, and each one pushes the end of the limit further away. The zero
// value is an open gate; a Gate is safe for concurrent use.
type Gate struct {
	mu      sync.Mutex
	err     error
	skipped int
}

// Closed reports whether a lookup must be skipped, and counts it as
// skipped when so.
func (g *Gate) Closed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err == nil {
		return false
	}
	g.skipped++
	return true
}

// Failed records the error of a lookup. It reports whether the error is a
// rate limit, in which case the gate closes and the lookup counts as
// skipped.
func (g *Gate) Failed(err error) bool {
	if !IsRateLimit(err) {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err == nil {
		g.err = err
	}
	g.skipped++
	return true
}

// Skipped returns the number of lookups that got no answer because of the
// rate limit, the one that hit it included, and the rate limit error (nil
// while the gate is open).
func (g *Gate) Skipped() (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.skipped, g.err
}

// Hint is the end of a warning about a rate-limited enrichment: what the
// reader can do about it.
func Hint() string {
	return fmt.Sprintf("run the command again in a minute, or set %s to wait and retry automatically", appinfo.EnvName("RATE_LIMIT_MAX_WAIT_MS"))
}

// Warnings collects what a command's optional lookups could not do, for
// the command to show on stderr. It is safe for concurrent use.
type Warnings struct {
	mu   sync.Mutex
	list []string
}

type warningsKey struct{}

// Collect returns a context whose optional lookups report to the returned
// collector. Functions that have a result with a `Warnings` field collect
// their own and put them there: only what they do not catch reaches the
// caller's collector.
func Collect(ctx context.Context) (context.Context, *Warnings) {
	w := &Warnings{}
	return context.WithValue(ctx, warningsKey{}, w), w
}

// Warnf reports an incomplete enrichment to the collector of ctx. Without
// a collector the warning is dropped: the result is still usable, only
// less complete.
func Warnf(ctx context.Context, format string, args ...any) {
	w, _ := ctx.Value(warningsKey{}).(*Warnings)
	if w == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	w.mu.Lock()
	defer w.mu.Unlock()
	// A command retried after a credentials refresh says things twice.
	if !slices.Contains(w.list, msg) {
		w.list = append(w.list, msg)
	}
}

// List returns the warnings collected so far, in order.
func (w *Warnings) List() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.list)
}
