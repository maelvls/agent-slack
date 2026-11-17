package latercmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const messageURL = "https://acme.slack.com/archives/C12345678/p1700000000000100"

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
	stderr *bytes.Buffer
	now    time.Time
	loc    *time.Location

	mu    sync.Mutex
	calls []call
	// serve answers API calls; nil answers {"ok": true}.
	serve func(method string, params map[string]any) map[string]any
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	for _, env := range []string{"SLACK_TOKEN", "SLACK_COOKIE_D", "SLACK_COOKIE", "SLACK_WORKSPACE_URL"} {
		t.Setenv(env, "")
	}
	h := &harness{t: t, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, loc: time.UTC}
	// A Friday.
	h.now = time.Date(2026, 3, 6, 14, 0, 0, 0, time.UTC)
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
		Stderr: h.stderr,
		ClientOptions: []slackapi.Option{slackapi.WithHandler(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
			h.mu.Lock()
			h.calls = append(h.calls, call{method, params})
			h.mu.Unlock()
			if h.serve == nil {
				return nil, nil
			}
			return h.serve(method, params), nil
		})},
	}
	return h
}

// run executes "later <args>" and returns the JSON it printed (nil if
// none).
func (h *harness) run(args ...string) (map[string]any, error) {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	h.calls = nil
	cmd := newCommand(h.rt, func() time.Time { return h.now }, h.loc)
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.ExecuteContext(context.Background())
	if h.stdout.Len() == 0 {
		return nil, err
	}
	var out map[string]any
	if jsonErr := json.Unmarshal(h.stdout.Bytes(), &out); jsonErr != nil {
		h.t.Fatalf("later %s printed invalid JSON: %v\n%s", strings.Join(args, " "), jsonErr, h.stdout)
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

func apiError(code string) map[string]any { return map[string]any{"ok": false, "error": code} }

func serveSaved(method string, params map[string]any) map[string]any {
	switch method {
	case "saved.list":
		return map[string]any{
			"saved_items": []any{
				map[string]any{"item_id": "C12345678", "item_type": "message", "ts": "1700000000.000100", "state": "in_progress", "date_created": 1700000500.0, "date_due": 1700009000.0},
				map[string]any{"item_id": "C12345678", "item_type": "message", "ts": "1700000001.000100", "state": "completed", "date_created": 1700000400.0, "date_completed": 1700000600.0},
				map[string]any{"item_id": "C12345678", "item_type": "message", "ts": "1700000002.000100", "state": "in_progress", "date_created": 1700000300.0},
			},
			"counts": map[string]any{"uncompleted_count": 2.0, "archived_count": 0.0, "completed_count": 1.0, "total_count": 3.0},
		}
	case "conversations.info":
		return map[string]any{"channel": map[string]any{"name": "general"}}
	case "conversations.history":
		return map[string]any{"messages": []any{map[string]any{"ts": params["latest"], "text": "saved " + params["latest"].(string), "user": "U1"}}}
	}
	return apiError("unknown_method")
}

func TestListIsTheDefaultCommand(t *testing.T) {
	want := map[string]any{
		"counts": map[string]any{"in_progress": 2.0, "archived": 0.0, "completed": 1.0, "total": 3.0},
		"items": []any{
			map[string]any{
				"channel_id": "C12345678", "channel_name": "general", "ts": "1700000000.000100", "state": "in_progress",
				"date_saved": 1700000500.0, "date_due": 1700009000.0,
				"message": map[string]any{"author": map[string]any{"user_id": "U1"}, "content": "saved 1700000000.000100"},
			},
			map[string]any{
				"channel_id": "C12345678", "channel_name": "general", "ts": "1700000002.000100", "state": "in_progress",
				"date_saved": 1700000300.0,
				"message":    map[string]any{"author": map[string]any{"user_id": "U1"}, "content": "saved 1700000002.000100"},
			},
		},
	}
	for _, args := range [][]string{nil, {"list"}, {"--state", "open"}, {"list", "--workspace", "acme"}} {
		h := newHarness(t)
		h.serve = serveSaved
		out, err := h.run(args...)
		if err != nil {
			t.Fatalf("later %v: %v", args, err)
		}
		if !reflect.DeepEqual(out, want) {
			t.Errorf("later %v printed\n%s", args, h.stdout)
		}
	}
}

func TestListFlags(t *testing.T) {
	h := newHarness(t)
	h.serve = serveSaved

	out, err := h.run("--state", "completed", "--max-body-chars", "5")
	if err != nil {
		t.Fatal(err)
	}
	items := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	item := items[0].(map[string]any)
	if item["state"] != "completed" || item["date_completed"] != 1700000600.0 {
		t.Errorf("item = %v", item)
	}
	if got := item["message"].(map[string]any)["content"]; got != "saved\n…" {
		t.Errorf("content = %q", got)
	}

	// --limit, then --cursor to continue where it stopped.
	out, err = h.run("list", "--limit", "1")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(out["items"].([]any)); n != 1 || out["next_cursor"] != "offset-1:" {
		t.Fatalf("items = %d, next_cursor = %v", n, out["next_cursor"])
	}
	out, err = h.run("list", "--limit", "1", "--cursor", out["next_cursor"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if ts := out["items"].([]any)[0].(map[string]any)["ts"]; ts != "1700000002.000100" || out["next_cursor"] != nil {
		t.Errorf("second page: ts = %v, next_cursor = %v", ts, out["next_cursor"])
	}

	out, err = h.run("--counts-only")
	if err != nil {
		t.Fatal(err)
	}
	if out["items"] != nil || out["counts"].(map[string]any)["total"] != 3.0 {
		t.Errorf("--counts-only printed %s", h.stdout)
	}
	if len(h.calls) != 1 || h.calls[0].method != "saved.list" {
		t.Errorf("--counts-only calls = %+v", h.calls)
	}
}

func TestListRejectsGarbageFlags(t *testing.T) {
	// The TypeScript turned these into NaN: "--limit abc" paged through
	// every saved item and printed none, "--max-body-chars abc" disabled
	// truncation, and an unknown --state listed in_progress items.
	tests := []struct {
		args    []string
		wantErr string
	}{
		{[]string{"list", "--limit", "abc"}, "--limit"},
		{[]string{"list", "--limit", "12abc"}, "--limit"},
		{[]string{"list", "--limit", "0"}, "--limit"},
		{[]string{"--limit", "-3"}, "--limit"},
		{[]string{"list", "--max-body-chars", "abc"}, "--max-body-chars"},
		{[]string{"list", "--max-body-chars", "0"}, "--max-body-chars"},
		{[]string{"list", "--max-body-chars", "-2"}, "--max-body-chars"},
		{[]string{"list", "--state", "finished"}, "--state"},
		{[]string{"list", "--cursor", "offset-x:abc"}, "invalid cursor"},
		{[]string{"list", "extra"}, "unknown command"},
		{[]string{"bogus"}, "unknown command"},
	}
	for _, tt := range tests {
		h := newHarness(t)
		h.serve = serveSaved
		out, err := h.run(tt.args...)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("later %v: err = %v, want one containing %q", tt.args, err, tt.wantErr)
		}
		if out != nil || len(h.calls) != 0 {
			t.Errorf("later %v: printed %v, made calls %+v", tt.args, out, h.calls)
		}
	}
}

func TestActions(t *testing.T) {
	item := func(extra map[string]any) map[string]any {
		params := map[string]any{"item_id": "C12345678", "item_type": "message", "ts": "1700000000.000100"}
		for k, v := range extra {
			params[k] = v
		}
		return params
	}
	tests := []struct {
		action string
		want   []call
	}{
		{"complete", []call{{"saved.update", item(map[string]any{"mark": "completed"})}}},
		{"archive", []call{{"saved.update", item(map[string]any{"mark": "archived"})}}},
		{"reopen", []call{{"saved.update", item(map[string]any{"mark": "uncompleted"})}, {"saved.update", item(map[string]any{"mark": "unarchived"})}}},
		{"save", []call{{"saved.add", item(nil)}}},
		{"remove", []call{{"saved.delete", item(nil)}}},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			h := newHarness(t)
			out, err := h.run(tt.action, messageURL)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(h.calls, tt.want) {
				t.Errorf("calls = %+v, want %+v", h.calls, tt.want)
			}
			// The TypeScript printed {"ok": true} whatever it had done.
			want := map[string]any{"ok": true, "action": tt.action, "channel_id": "C12345678", "ts": "1700000000.000100"}
			if !reflect.DeepEqual(out, want) {
				t.Errorf("printed %s", h.stdout)
			}

			// A Slack error is an error, not {"ok": true}.
			h.serve = func(string, map[string]any) map[string]any { return apiError("item_not_found") }
			out, err = h.run(tt.action, messageURL)
			if !slackapi.IsErrorCode(err, "item_not_found") || out != nil {
				t.Errorf("failing: printed %v, err = %v", out, err)
			}
		})
	}
}

func TestActionOnChannelTarget(t *testing.T) {
	h := newHarness(t)
	h.serve = func(method string, _ map[string]any) map[string]any {
		if method == "search.messages" {
			return map[string]any{"messages": map[string]any{"matches": []any{map[string]any{"channel": map[string]any{"id": "C000000001", "name": "general"}}}}}
		}
		return nil
	}
	out, err := h.run("save", "#general", "--ts", "1700000000.000100")
	if err != nil {
		t.Fatal(err)
	}
	want := []call{{"saved.add", map[string]any{"item_id": "C000000001", "item_type": "message", "ts": "1700000000.000100"}}}
	if got := h.only("saved.add"); !reflect.DeepEqual(got, want) {
		t.Errorf("saved.add calls = %+v", got)
	}
	if out["channel_id"] != "C000000001" || out["ts"] != "1700000000.000100" {
		t.Errorf("printed %s", h.stdout)
	}

	// IDs need no lookup; --workspace selects the workspace.
	if _, err := h.run("complete", "C12345678", "--ts", "1700000000.000100", "--workspace", "acme"); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 1 || h.calls[0].params["item_id"] != "C12345678" {
		t.Errorf("calls = %+v", h.calls)
	}

	// Flags that only repeat what the URL says are harmless.
	if _, err := h.run("complete", messageURL, "--ts", "1700000000.000100", "--workspace", "acme"); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 1 || h.calls[0].params["item_id"] != "C12345678" || h.calls[0].params["ts"] != "1700000000.000100" {
		t.Errorf("calls = %+v", h.calls)
	}
}

func TestActionTargetErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"channel without --ts", []string{"complete", "#general"}, "--ts is required"},
		{"channel ID without --ts", []string{"complete", "C12345678"}, "--ts is required"},
		// The TypeScript sent whatever --ts held to Slack.
		{"garbage --ts", []string{"complete", "C12345678", "--ts", "yesterday"}, "invalid --ts"},
		{"truncated --ts", []string{"complete", "C12345678", "--ts", "1700000000"}, "invalid --ts"},
		// The TypeScript silently ignored these two flags with a URL.
		{"URL and another --ts", []string{"complete", messageURL, "--ts", "1700000000.000200"}, "--ts 1700000000.000200 contradicts the message URL"},
		{"URL and another --workspace", []string{"complete", messageURL, "--workspace", "other"}, `--workspace "other" contradicts the URL`},
		// The TypeScript looked for a channel named after the user ID.
		{"user target", []string{"complete", "U12345678", "--ts", "1700000000.000100"}, "does not support user ID targets"},
		{"channel URL", []string{"complete", "https://acme.slack.com/archives/C12345678"}, "--ts is required"},
		{"not a Slack URL", []string{"complete", "https://example.com/archives/C12345678/p1700000000000100"}, "unsupported target URL"},
		{"another workspace's URL", []string{"complete", "https://other.slack.com/archives/C12345678/p1700000000000100"}, `no Slack credentials for workspace "https://other.slack.com"`},
		{"no target", []string{"complete"}, "accepts 1 arg(s)"},
		{"remind without --in", []string{"remind", messageURL}, `required flag(s) "in" not set`},
		{"remind with garbage", []string{"remind", messageURL, "--in", "soonish"}, "invalid --in value"},
		{"remind in the past", []string{"remind", messageURL, "--in", "1700000000"}, "future time"},
		{"remind today after 9:00", []string{"remind", messageURL, "--in", "today"}, "already passed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			out, err := h.run(tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want one containing %q", err, tt.wantErr)
			}
			if out != nil || len(h.calls) != 0 {
				t.Errorf("printed %v, made calls %+v", out, h.calls)
			}
		})
	}
}

func TestRemind(t *testing.T) {
	h := newHarness(t)
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no timezone database: %v", err)
	}
	h.loc = ny
	h.now = time.Date(2026, 3, 6, 14, 0, 0, 0, ny) // a Friday

	monday9 := time.Date(2026, 3, 9, 9, 0, 0, 0, ny)
	tests := []struct {
		in      string
		want    int64
		wantISO string
	}{
		{"monday", monday9.Unix(), "2026-03-09T09:00:00-04:00"},
		{"tomorrow", time.Date(2026, 3, 7, 9, 0, 0, 0, ny).Unix(), "2026-03-07T09:00:00-05:00"},
		{"90m", h.now.Unix() + 5400, "2026-03-06T15:30:00-05:00"},
		{"1800000000", 1800000000, "2027-01-15T03:00:00-05:00"},
	}
	for _, tt := range tests {
		out, err := h.run("remind", messageURL, "--in", tt.in)
		if err != nil {
			t.Fatalf("--in %s: %v", tt.in, err)
		}
		wantCalls := []call{{"saved.update", map[string]any{"item_id": "C12345678", "item_type": "message", "ts": "1700000000.000100", "date_due": tt.want}}}
		if !reflect.DeepEqual(h.calls, wantCalls) {
			t.Errorf("--in %s: calls = %+v, want %+v", tt.in, h.calls, wantCalls)
		}
		want := map[string]any{
			"ok": true, "action": "remind", "channel_id": "C12345678", "ts": "1700000000.000100",
			"remind_at": float64(tt.want), "remind_at_local": tt.wantISO,
		}
		if !reflect.DeepEqual(out, want) {
			t.Errorf("--in %s printed %s", tt.in, h.stdout)
		}
	}
}

func TestTruncatedURLHint(t *testing.T) {
	h := newHarness(t)
	if _, err := h.run("save", messageURL+"?thread_ts=1699999999.000100"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stderr.String(), "truncated") {
		t.Errorf("stderr = %q, want the truncated-URL hint", h.stderr)
	}
	if _, err := h.run("save", messageURL+"?thread_ts=1699999999.000100&cid=C12345678"); err != nil {
		t.Fatal(err)
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", h.stderr)
	}
}

func TestStandardTokenIsRefusedBeforeAnyCall(t *testing.T) {
	for _, args := range [][]string{{"list"}, {"save", messageURL}, {"reopen", messageURL}, {"remind", messageURL, "--in", "1h"}} {
		h := newHarness(t)
		t.Setenv("SLACK_TOKEN", "xoxb-test")
		out, err := h.run(args...)
		if err == nil || !strings.Contains(err.Error(), "browser session credentials") {
			t.Errorf("later %v: err = %v", args, err)
		}
		if out != nil || len(h.calls) != 0 {
			t.Errorf("later %v: printed %v, made calls %+v", args, out, h.calls)
		}
	}
}

func TestAmbiguousChannelNameAcrossWorkspaces(t *testing.T) {
	h := newHarness(t)
	// Two workspaces and no default: only a hand-written file has that.
	path := filepath.Join(t.TempDir(), "credentials.json")
	const creds = `{"version": 1, "workspaces": [
		{"workspace_url": "https://acme.slack.com", "auth": {"auth_type": "browser", "xoxc_token": "xoxc-1", "xoxd_cookie": "xoxd-1"}},
		{"workspace_url": "https://other.slack.com", "auth": {"auth_type": "browser", "xoxc_token": "xoxc-2", "xoxd_cookie": "xoxd-2"}}]}`
	if err := os.WriteFile(path, []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	h.rt.Store = &auth.Store{Path: path}

	// The TypeScript looked the name up in whichever workspace came first.
	out, err := h.run("save", "#general", "--ts", "1700000000.000100")
	if err == nil || !strings.Contains(err.Error(), "ambiguous channel name") {
		t.Errorf("err = %v, want a request for --workspace", err)
	}
	if out != nil || len(h.calls) != 0 {
		t.Errorf("printed %v, made calls %+v", out, h.calls)
	}
	// A channel ID does not say which workspace it is in either: the
	// client lookup refuses to guess one.
	out, err = h.run("save", "C12345678", "--ts", "1700000000.000100")
	if err == nil || !strings.Contains(err.Error(), "cannot tell which workspace to use") {
		t.Errorf("err = %v, want a request for --workspace", err)
	}
	if out != nil || len(h.calls) != 0 {
		t.Errorf("printed %v, made calls %+v", out, h.calls)
	}
	h.serve = func(method string, _ map[string]any) map[string]any {
		if method == "search.messages" {
			return map[string]any{"messages": map[string]any{"matches": []any{map[string]any{"channel": map[string]any{"id": "C000000001", "name": "general"}}}}}
		}
		return nil
	}
	if _, err := h.run("save", "#general", "--ts", "1700000000.000100", "--workspace", "other"); err != nil {
		t.Errorf("with --workspace: %v", err)
	}
}

// TestHelp ports the help contracts: --help is the documentation agents
// read.
func TestHelp(t *testing.T) {
	cmd := New(newHarness(t).rt)
	remind, _, err := cmd.Find([]string{"remind"})
	if err != nil {
		t.Fatal(err)
	}
	in := remind.Flag("in").Usage
	for _, want := range []string{"local timezone at 9:00", "Unix timestamp"} {
		if !strings.Contains(in, want) {
			t.Errorf("--in help %q does not mention %q", in, want)
		}
	}
	for _, name := range []string{"list", "complete", "archive", "reopen", "save", "remove", "remind"} {
		sub, _, err := cmd.Find([]string{name})
		if err != nil || sub.Name() != name {
			t.Errorf("missing sub-command %s", name)
			continue
		}
		if name != "list" && (sub.Flag("ts") == nil || sub.Flag("workspace") == nil) {
			t.Errorf("%s lacks --ts/--workspace", name)
		}
	}
	// `later` alone is `later list`: same flags.
	for _, flag := range []string{"workspace", "state", "limit", "cursor", "max-body-chars", "counts-only"} {
		if cmd.Flag(flag) == nil {
			t.Errorf("later lacks --%s", flag)
		}
	}
}
