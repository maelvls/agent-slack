package unreadscmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

type call struct {
	method string
	params map[string]any
}

// harness runs the command against a fake Slack and a temporary
// credentials store holding the browser-auth workspace acme.slack.com.
type harness struct {
	t      *testing.T
	rt     *clictx.Context
	stdout *bytes.Buffer

	mu    sync.Mutex
	calls []call
	serve func(method string, params map[string]any) map[string]any
}

func newHarness(t *testing.T, serve func(method string, params map[string]any) map[string]any) *harness {
	t.Helper()
	for _, env := range []string{"SLACK_TOKEN", "SLACK_COOKIE_D", "SLACK_COOKIE", "SLACK_WORKSPACE_URL"} {
		t.Setenv(env, "")
	}
	h := &harness{t: t, stdout: &bytes.Buffer{}, serve: serve}
	store := &auth.Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
	if err := store.Upsert(auth.Workspace{
		URL:  "https://acme.slack.com",
		Auth: slackapi.Auth{Type: slackapi.AuthBrowser, XoxcToken: "xoxc-test", XoxdCookie: "xoxd-test"},
	}); err != nil {
		t.Fatal(err)
	}
	h.rt = &clictx.Context{
		Store:  store,
		Stdout: h.stdout,
		Stderr: io.Discard,
		ClientOptions: []slackapi.Option{slackapi.WithHandler(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
			h.mu.Lock()
			h.calls = append(h.calls, call{method, params})
			h.mu.Unlock()
			return h.serve(method, params), nil
		})},
	}
	return h
}

// run executes "unreads <args>" and returns the JSON it printed (nil if
// none).
func (h *harness) run(args ...string) (map[string]any, error) {
	h.t.Helper()
	h.stdout.Reset()
	h.calls = nil
	cmd := New(h.rt)
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.ExecuteContext(context.Background())
	if h.stdout.Len() == 0 {
		return nil, err
	}
	var out map[string]any
	if jsonErr := json.Unmarshal(h.stdout.Bytes(), &out); jsonErr != nil {
		h.t.Fatalf("unreads %s printed invalid JSON: %v\n%s", strings.Join(args, " "), jsonErr, h.stdout)
	}
	return out, err
}

func (h *harness) only(method string) []call {
	var out []call
	for _, c := range h.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func serveUnreads(method string, params map[string]any) map[string]any {
	switch method {
	case "client.counts":
		return map[string]any{
			"channels": []any{
				map[string]any{"id": "C000000001", "has_unreads": true, "mention_count": 1.0, "last_read": "1700000000.000100"},
				map[string]any{"id": "C000000002", "has_unreads": false},
			},
			"ims":     []any{map[string]any{"id": "D000000001", "has_unreads": true, "mention_count": 0.0, "last_read": "1700000000.000200"}},
			"threads": map[string]any{"has_unreads": true, "mention_count": 2.0},
		}
	case "conversations.info":
		if params["channel"] == "D000000001" {
			return map[string]any{"channel": map[string]any{"is_im": true, "user": "U000000009"}}
		}
		return map[string]any{"channel": map[string]any{"name": "general"}}
	case "users.info":
		return map[string]any{"user": map[string]any{"profile": map[string]any{"display_name": "alice"}}}
	case "conversations.history":
		if params["channel"] == "D000000001" {
			return map[string]any{"messages": []any{map[string]any{"ts": "1700000009.000100", "text": "ping", "user": "U000000009"}}}
		}
		return map[string]any{"has_more": true, "messages": []any{
			map[string]any{"ts": "1700000005.000100", "text": "second message", "user": "U000000002", "thread_ts": "1700000005.000100", "reply_count": 2.0},
			map[string]any{"ts": "1700000004.000100", "text": "joined", "subtype": "channel_join", "user": "U000000003"},
			map[string]any{"ts": "1700000003.000100", "text": "first message", "bot_id": "B000000001"},
		}}
	}
	return map[string]any{"ok": false, "error": "unknown_method"}
}

func TestUnreads(t *testing.T) {
	h := newHarness(t, serveUnreads)
	out, err := h.run()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"channels": []any{
			map[string]any{
				"channel_id": "C000000001", "channel_name": "general", "channel_type": "channel",
				"unread_count": 2.0, "mention_count": 1.0,
				"messages": []any{
					map[string]any{"ts": "1700000003.000100", "author": map[string]any{"bot_id": "B000000001"}, "content": "first message"},
					map[string]any{"ts": "1700000005.000100", "author": map[string]any{"user_id": "U000000002"}, "content": "second message", "thread_ts": "1700000005.000100", "reply_count": 2.0},
				},
				"messages_truncated": true,
			},
			map[string]any{
				"channel_id": "D000000001", "channel_name": "alice", "channel_type": "dm",
				"unread_count": 1.0, "mention_count": 0.0,
				"messages": []any{map[string]any{"ts": "1700000009.000100", "author": map[string]any{"user_id": "U000000009"}, "content": "ping"}},
			},
		},
		"threads": map[string]any{"has_unreads": true, "mention_count": 2.0},
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("printed %s", h.stdout)
	}
	for _, c := range h.only("conversations.history") {
		if c.params["limit"] != 10 || c.params["inclusive"] != false || c.params["oldest"] == nil {
			t.Errorf("history params = %v", c.params)
		}
	}
	// Reading unreads must not mark anything as read.
	if n := len(h.only("conversations.mark")); n != 0 {
		t.Errorf("conversations.mark calls = %d", n)
	}
}

func TestUnreadsFlags(t *testing.T) {
	h := newHarness(t, serveUnreads)

	out, err := h.run("--counts-only", "--workspace", "acme")
	if err != nil {
		t.Fatal(err)
	}
	want := []any{
		map[string]any{"channel_id": "C000000001", "channel_name": "general", "channel_type": "channel", "mention_count": 1.0},
		map[string]any{"channel_id": "D000000001", "channel_name": "alice", "channel_type": "dm", "mention_count": 0.0},
	}
	if !reflect.DeepEqual(out["channels"], want) {
		t.Errorf("--counts-only printed %s", h.stdout)
	}
	if n := len(h.only("conversations.history")); n != 0 {
		t.Errorf("--counts-only made %d conversations.history calls", n)
	}

	out, err = h.run("--max-messages", "5", "--max-body-chars", "6", "--include-system")
	if err != nil {
		t.Fatal(err)
	}
	general := out["channels"].([]any)[0].(map[string]any)
	msgs := general["messages"].([]any)
	if len(msgs) != 3 || general["unread_count"] != 3.0 {
		t.Fatalf("--include-system: messages = %v", msgs)
	}
	if got := msgs[0].(map[string]any)["content"]; got != "first \n..." {
		t.Errorf("content = %q", got)
	}
	for _, c := range h.only("conversations.history") {
		if c.params["limit"] != 5 {
			t.Errorf("history params = %v", c.params)
		}
	}
}

func TestUnreadsWithNothingUnread(t *testing.T) {
	h := newHarness(t, func(string, map[string]any) map[string]any {
		return map[string]any{"channels": []any{map[string]any{"id": "C000000001", "has_unreads": false}}}
	})
	if _, err := h.run(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(h.stdout.String()); got != "{}" {
		t.Errorf("printed %q", got)
	}
}

func TestUnreadsRejectsGarbageFlags(t *testing.T) {
	// The TypeScript sent "limit=NaN" to Slack for --max-messages abc and
	// disabled truncation for --max-body-chars abc.
	tests := []struct {
		args    []string
		wantErr string
	}{
		{[]string{"--max-messages", "abc"}, "--max-messages"},
		{[]string{"--max-messages", "5x"}, "--max-messages"},
		{[]string{"--max-messages", "0"}, "--max-messages"},
		{[]string{"--max-messages", "1000"}, "--max-messages"},
		{[]string{"--max-body-chars", "abc"}, "--max-body-chars"},
		{[]string{"--max-body-chars", "0"}, "--max-body-chars"},
		{[]string{"--max-body-chars", "-5"}, "--max-body-chars"},
		{[]string{"extra"}, "unknown command"},
	}
	for _, tt := range tests {
		h := newHarness(t, serveUnreads)
		out, err := h.run(tt.args...)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("unreads %v: err = %v, want one containing %q", tt.args, err, tt.wantErr)
		}
		if out != nil || len(h.calls) != 0 {
			t.Errorf("unreads %v: printed %v, made calls %+v", tt.args, out, h.calls)
		}
	}
}

func TestUnreadsErrors(t *testing.T) {
	h := newHarness(t, func(string, map[string]any) map[string]any {
		return map[string]any{"ok": false, "error": "ratelimited"}
	})
	if out, err := h.run(); !slackapi.IsErrorCode(err, "ratelimited") || out != nil {
		t.Errorf("printed %v, err = %v", out, err)
	}

	h = newHarness(t, serveUnreads)
	t.Setenv("SLACK_TOKEN", "xoxb-test")
	out, err := h.run()
	if err == nil || !strings.Contains(err.Error(), "browser session credentials") {
		t.Errorf("standard token: err = %v", err)
	}
	if out != nil || len(h.calls) != 0 {
		t.Errorf("standard token: printed %v, made calls %+v", out, h.calls)
	}
}
