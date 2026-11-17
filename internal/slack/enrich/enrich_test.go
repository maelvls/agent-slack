package enrich

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/slackapi"
)

func rateLimit(method string) error {
	return &slackapi.RateLimitError{Method: method, RetryAfter: time.Minute}
}

func authError() error {
	_, err := slackapi.NewFake(func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{"ok": false, "error": "invalid_auth"}, nil
	}).API(context.Background(), "auth.test", nil)
	return err
}

func TestMustAbort(t *testing.T) {
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	if !MustAbort(ctx, authError()) || !MustAbort(ctx, fmt.Errorf("wrapped: %w", authError())) {
		t.Error("dead credentials do not abort")
	}
	if !MustAbort(cancelled, errors.New("anything")) {
		t.Error("an error under a cancelled context does not abort")
	}
	// A rate limit is not a reason to lose the result.
	for _, err := range []error{nil, rateLimit("users.info"), errors.New("connection reset"), context.DeadlineExceeded} {
		if MustAbort(ctx, err) {
			t.Errorf("MustAbort(%v) = true", err)
		}
	}
	if MustAbort(cancelled, nil) {
		t.Error("no error, yet abort")
	}

	if !IsRateLimit(rateLimit("x")) || !IsRateLimit(fmt.Errorf("wrapped: %w", rateLimit("x"))) || IsRateLimit(authError()) || IsRateLimit(nil) {
		t.Error("IsRateLimit is wrong")
	}
}

func TestGate(t *testing.T) {
	var g Gate
	if g.Closed() {
		t.Fatal("the zero gate is closed")
	}
	if g.Failed(nil) || g.Failed(errors.New("user_not_found")) || g.Closed() {
		t.Fatal("an ordinary failure closed the gate")
	}
	if n, err := g.Skipped(); n != 0 || err != nil {
		t.Fatalf("Skipped = %d, %v", n, err)
	}

	first := rateLimit("users.info")
	if !g.Failed(first) || !g.Failed(rateLimit("users.info")) {
		t.Fatal("a rate limit was not recognised")
	}
	if !g.Closed() || !g.Closed() {
		t.Fatal("the gate is still open after a rate limit")
	}
	// Two refused, two not even tried; the first rate limit is the cause.
	if n, err := g.Skipped(); n != 4 || err != first {
		t.Errorf("Skipped = %d, %v", n, err)
	}

	// Safe for concurrent use.
	var wg sync.WaitGroup
	var shared Gate
	for range 50 {
		wg.Go(func() {
			if !shared.Closed() {
				shared.Failed(rateLimit("files.info"))
			}
		})
	}
	wg.Wait()
	if n, _ := shared.Skipped(); n != 50 {
		t.Errorf("Skipped = %d, want 50", n)
	}
}

func TestWarnings(t *testing.T) {
	// Without a collector, a warning is dropped.
	Warnf(context.Background(), "nobody listens")

	ctx, outer := Collect(context.Background())
	Warnf(ctx, "%d users missing", 3)
	Warnf(ctx, "%d users missing", 3) // a retried command repeats itself
	Warnf(ctx, "files incomplete")

	// A function with its own Warnings field collects for itself.
	nested, inner := Collect(ctx)
	Warnf(nested, "inner only")

	if got, want := outer.List(), []string{"3 users missing", "files incomplete"}; !reflect.DeepEqual(got, want) {
		t.Errorf("outer = %q, want %q", got, want)
	}
	if got, want := inner.List(), []string{"inner only"}; !reflect.DeepEqual(got, want) {
		t.Errorf("inner = %q, want %q", got, want)
	}

	if hint := Hint(); !strings.Contains(hint, appinfo.EnvName("RATE_LIMIT_MAX_WAIT_MS")) {
		t.Errorf("hint = %q", hint)
	}
}
