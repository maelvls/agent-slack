package search

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// Noon UTC on 2026-01-01, and one day.
const (
	jan1 = 1767268800
	day  = 86400
)

func ts(sec int) string { return fmt.Sprintf("%d.000100", sec) }

type call struct {
	method string
	params map[string]any
}

// fake is a Slack workspace: API methods are served by handlers, and file
// downloads by the files map (URL → body; a missing URL is a 404).
type fake struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []call
	handlers map[string]func(params map[string]any) (map[string]any, error)
	files    map[string]string
	gets     []string
}

func newFake(t *testing.T) *fake {
	return &fake{t: t, handlers: map[string]func(map[string]any) (map[string]any, error){}, files: map[string]string{}}
}

func (f *fake) on(method string, h func(params map[string]any) (map[string]any, error)) {
	f.handlers[method] = h
}

func (f *fake) handle(_ context.Context, method string, params map[string]any) (map[string]any, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{method, params})
	h := f.handlers[method]
	f.mu.Unlock()
	if h == nil {
		f.t.Errorf("unexpected API call %s %v", method, params)
		return map[string]any{"ok": false, "error": "unexpected_call"}, nil
	}
	return h(params)
}

func (f *fake) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.gets = append(f.gets, req.URL.String())
	body, ok := f.files[req.URL.String()]
	f.mu.Unlock()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}
	if !ok {
		resp.StatusCode = http.StatusNotFound
	}
	return resp, nil
}

// client returns a browser-auth client for https://test.slack.com.
func (f *fake) client() *slackapi.Client {
	return slackapi.NewFake(f.handle, slackapi.WithHTTPClient(&http.Client{Transport: f}))
}

func (f *fake) callsTo(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c.params)
		}
	}
	return out
}

func apiError(code string) (map[string]any, error) {
	return map[string]any{"ok": false, "error": code}, nil
}

// history serves conversations.history from msgs (newest first), both for
// scans and for the single-message lookups of messages.FetchMessage.
func (f *fake) history(msgs ...map[string]any) {
	f.on("conversations.history", func(p map[string]any) (map[string]any, error) {
		if limit, _ := jsonx.Int(p["limit"]); limit == 1 {
			for _, m := range msgs {
				if m["ts"] == p["latest"] {
					return map[string]any{"messages": []any{m}}, nil
				}
			}
			return map[string]any{"messages": []any{}}, nil
		}
		out := make([]any, len(msgs))
		for i, m := range msgs {
			out[i] = m
		}
		return map[string]any{"messages": out}, nil
	})
}

// searchPages serves search.messages or search.files page by page.
func (f *fake) searchPages(method string, total int, pages ...[]any) {
	key := strings.TrimPrefix(method, "search.")
	f.on(method, func(p map[string]any) (map[string]any, error) {
		page, _ := jsonx.Int(p["page"])
		var matches []any
		if page >= 1 && page <= len(pages) {
			matches = pages[page-1]
		}
		return map[string]any{key: map[string]any{
			"matches": matches,
			"paging":  map[string]any{"pages": len(pages), "total": total},
		}}, nil
	})
}

func match(channel string, sec int, text string) map[string]any {
	return map[string]any{
		"ts":        ts(sec),
		"text":      text,
		"user":      "U11111111",
		"channel":   map[string]any{"id": channel, "name": "general"},
		"permalink": fmt.Sprintf("https://test.slack.com/archives/%s/p%d000100", channel, sec),
	}
}

func message(sec int, text string) map[string]any {
	return map[string]any{"ts": ts(sec), "text": text, "user": "U11111111"}
}

func withFile(m map[string]any, file map[string]any) map[string]any {
	m["files"] = []any{file}
	return m
}

func slackFile(id, mimetype string) map[string]any {
	return map[string]any{
		"id":                   id,
		"name":                 id + "-name",
		"title":                id + " title",
		"mimetype":             mimetype,
		"url_private_download": "https://files.slack.com/files/" + id,
		"permalink":            "https://test.slack.com/files/U1/" + id,
	}
}

// options returns search options that keep downloads and caches inside
// the test's temporary directories, with days in UTC.
func options(t *testing.T, kind Kind, query string) Options {
	return Options{Kind: kind, Query: query, DownloadsDir: t.TempDir(), CacheDir: t.TempDir(), Location: time.UTC}
}

func mustSearch(t *testing.T, f *fake, opts Options) *Result {
	t.Helper()
	res, err := Search(t.Context(), f.client(), opts)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	return res
}

func messageTS(res *Result) []string {
	var out []string
	for _, m := range res.Messages {
		out = append(out, m.TS)
	}
	return out
}
