package channelcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const messageURL = "https://acme.slack.com/archives/C12345678/p1234567890123456"

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

	mu    sync.Mutex
	calls []call
	// fail maps "method" or "method:<user, email or channel>" to the
	// Slack error code to answer.
	fail map[string]string
	// failOnce is like fail, for the first matching call only.
	failOnce map[string]string
	// empty makes the workspace have no conversation at all.
	empty bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	for _, env := range []string{"SLACK_TOKEN", "SLACK_COOKIE_D", "SLACK_COOKIE", "SLACK_WORKSPACE_URL"} {
		t.Setenv(env, "")
	}
	h := &harness{t: t, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, fail: map[string]string{}}
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
			return h.serve(method, params), nil
		})},
	}
	return h
}

// serve is a workspace with one member (alice) and one channel (general).
func (h *harness) serve(method string, params map[string]any) map[string]any {
	subject := ""
	switch v := params["users"].(type) {
	case string:
		subject = v
	}
	if emails, ok := params["emails"].([]string); ok && len(emails) == 1 {
		subject = emails[0]
	}
	for _, key := range []string{method + ":" + subject, method} {
		if code := h.failOnce[key]; code != "" {
			delete(h.failOnce, key)
			return map[string]any{"ok": false, "error": code}
		}
		if code := h.fail[key]; code != "" {
			return map[string]any{"ok": false, "error": code}
		}
	}
	if h.empty {
		return map[string]any{}
	}
	switch method {
	case "users.list":
		return map[string]any{"members": []any{map[string]any{"id": "U000000123", "name": "alice"}}}
	case "users.lookupByEmail":
		return map[string]any{"user": map[string]any{"id": "U000000456", "name": "carol"}}
	case "search.messages":
		return map[string]any{"messages": map[string]any{"matches": []any{map[string]any{"channel": map[string]any{"id": "C000000001", "name": "general"}}}}}
	case "conversations.create":
		return map[string]any{"channel": map[string]any{"id": "C000000002", "name": params["name"], "is_private": params["is_private"]}}
	case "client.counts":
		return map[string]any{
			"channels": []any{map[string]any{"id": "C000000001"}},
			"ims":      []any{map[string]any{"id": "D000000001"}},
		}
	case "conversations.info":
		return map[string]any{"channel": map[string]any{"id": params["channel"], "name": "name-of-" + params["channel"].(string)}}
	case "conversations.mark", "conversations.invite", "conversations.inviteShared":
		return nil
	}
	return map[string]any{
		"channels":          []any{map[string]any{"id": "C000000001", "name": "general"}},
		"response_metadata": map[string]any{"next_cursor": "n1"},
	}
}

// run executes "channel <args>" and returns the JSON it printed (nil if
// none).
func (h *harness) run(args ...string) (map[string]any, error) {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
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
		h.t.Fatalf("channel %s printed invalid JSON: %v\n%s", strings.Join(args, " "), jsonErr, h.stdout)
	}
	return out, err
}

func (h *harness) methods() []string {
	var out []string
	for _, c := range h.calls {
		out = append(out, c.method)
	}
	return out
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

func TestListDefaultsToUsersConversations(t *testing.T) {
	h := newHarness(t)
	out, err := h.run("list")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 1 || h.calls[0].method != "users.conversations" {
		t.Fatalf("calls = %+v", h.calls)
	}
	if p := h.calls[0].params; p["limit"] != 100 || p["exclude_archived"] != true || p["user"] != nil || p["cursor"] != nil {
		t.Errorf("params = %v", p)
	}
	want := map[string]any{"channels": []any{map[string]any{"id": "C000000001", "name": "general"}}, "next_cursor": "n1"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("printed %s", h.stdout)
	}
}

func TestListUser(t *testing.T) {
	h := newHarness(t)
	if _, err := h.run("list", "--user", "U12345678"); err != nil {
		t.Fatal(err)
	}
	if got := h.methods(); !reflect.DeepEqual(got, []string{"users.conversations"}) || h.calls[0].params["user"] != "U12345678" {
		t.Errorf("--user <id>: calls = %+v", h.calls)
	}

	// A handle is resolved first: one search for a message the user posted,
	// then (nothing found) the directory.
	if _, err := h.run("list", "--user", "@alice"); err != nil {
		t.Fatal(err)
	}
	if got := h.methods(); !reflect.DeepEqual(got, []string{"search.messages", "users.list", "users.conversations"}) || h.calls[2].params["user"] != "U000000123" {
		t.Errorf("--user @alice: calls = %+v", h.calls)
	}

	out, err := h.run("list", "--user", "@unknown")
	if err == nil || !strings.Contains(err.Error(), `could not resolve user "@unknown"`) || out != nil {
		t.Errorf("--user @unknown: printed %v, err = %v", out, err)
	}
	if n := len(h.only("users.conversations")); n != 0 {
		t.Errorf("--user @unknown listed conversations anyway")
	}
}

func TestListAll(t *testing.T) {
	h := newHarness(t)
	if _, err := h.run("list", "--all", "--workspace", "acme"); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 1 || h.calls[0].method != "conversations.list" || h.calls[0].params["exclude_archived"] != true {
		t.Errorf("calls = %+v", h.calls)
	}
}

func TestListForwardsLimitAndCursor(t *testing.T) {
	h := newHarness(t)
	if _, err := h.run("list", "--limit", "50", "--cursor", "cursor-1"); err != nil {
		t.Fatal(err)
	}
	if p := h.calls[0].params; p["limit"] != 50 || p["cursor"] != "cursor-1" {
		t.Errorf("params = %v", p)
	}
}

func TestListViaCounts(t *testing.T) {
	h := newHarness(t)
	out, err := h.run("list", "--via-counts", "--limit", "1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"channels":    []any{map[string]any{"id": "C000000001", "name": "name-of-C000000001"}},
		"next_cursor": "1",
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("printed %s", h.stdout)
	}
	// The TypeScript ignored --cursor here, so the conversations beyond
	// --limit could not be listed at all.
	out, err = h.run("list", "--via-counts", "--limit", "1", "--cursor", "1")
	if err != nil {
		t.Fatal(err)
	}
	want = map[string]any{"channels": []any{map[string]any{"id": "D000000001", "name": "name-of-D000000001"}}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("second page printed %s", h.stdout)
	}
	if got := h.methods(); !reflect.DeepEqual(got, []string{"client.counts", "conversations.info"}) {
		t.Errorf("calls = %v", got)
	}

	// client.counts is an internal API: bot and user tokens cannot call it.
	t.Setenv("SLACK_TOKEN", "xoxb-test")
	out, err = h.run("list", "--via-counts")
	if err == nil || !strings.Contains(err.Error(), "browser session credentials") || out != nil || len(h.calls) != 0 {
		t.Errorf("standard token: printed %v, err = %v, calls = %+v", out, err, h.calls)
	}
}

func TestListRejectsBadFlags(t *testing.T) {
	tests := []struct {
		args    []string
		wantErr string
	}{
		{[]string{"list", "--all", "--user", "U12345678"}, "--all cannot be used with --user"},
		{[]string{"list", "--via-counts", "--all"}, "--via-counts cannot be used with --all"},
		{[]string{"list", "--via-counts", "--user", "U12345678"}, "--via-counts cannot be used with --user"},
		{[]string{"list", "--limit", "abc"}, "--limit"},
		// Number.parseInt read this as 12.
		{[]string{"list", "--limit", "12abc"}, "--limit"},
		{[]string{"list", "--limit", "0"}, "--limit"},
		{[]string{"list", "--limit", "-5"}, "--limit"},
		// Slack serves at most 1000 per page: asking for more must not
		// silently return fewer.
		{[]string{"list", "--limit", "1001"}, "--limit"},
		{[]string{"list", "--via-counts", "--cursor", "dXNlcjpVMQ=="}, "invalid cursor"},
		{[]string{"list", "extra"}, "unknown command"},
	}
	for _, tt := range tests {
		h := newHarness(t)
		out, err := h.run(tt.args...)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("channel %v: err = %v, want one containing %q", tt.args, err, tt.wantErr)
		}
		if out != nil || len(h.calls) != 0 {
			t.Errorf("channel %v: printed %v, made calls %+v", tt.args, out, h.calls)
		}
	}
}

func TestNew(t *testing.T) {
	h := newHarness(t)
	out, err := h.run("new", "--name", "#project-apollo", "--private")
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []call{{"conversations.create", map[string]any{"name": "project-apollo", "is_private": true}}}
	if !reflect.DeepEqual(h.calls, wantCalls) {
		t.Errorf("calls = %+v", h.calls)
	}
	want := map[string]any{"channel": map[string]any{"id": "C000000002", "name": "project-apollo", "is_private": true}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("printed %s", h.stdout)
	}

	out, err = h.run("new", "--name", "public-one")
	if err != nil {
		t.Fatal(err)
	}
	if h.calls[0].params["is_private"] != false || out["channel"].(map[string]any)["is_private"] != false {
		t.Errorf("public channel: calls = %+v, printed %s", h.calls, h.stdout)
	}

	for _, args := range [][]string{{"new"}, {"new", "--name", "Project Apollo"}, {"new", "--name", ""}, {"new", "--name", "ok", "extra"}} {
		out, err := h.run(args...)
		if err == nil || out != nil || len(h.calls) != 0 {
			t.Errorf("channel %v: printed %v, err = %v, calls = %+v", args, out, err, h.calls)
		}
	}

	h.fail["conversations.create"] = "name_taken"
	if out, err := h.run("new", "--name", "general"); !slackapi.IsErrorCode(err, "name_taken") || out != nil {
		t.Errorf("name_taken: printed %v, err = %v", out, err)
	}
}

func TestInvite(t *testing.T) {
	h := newHarness(t)
	out, err := h.run("invite", "--channel", "#general", "--users", "U000000001, @alice,carol@example.com,U000000001")
	if err != nil {
		t.Fatal(err)
	}
	var invited []string
	for _, c := range h.only("conversations.invite") {
		if c.params["channel"] != "C000000001" {
			t.Errorf("invite params = %v", c.params)
		}
		invited = append(invited, c.params["users"].(string))
	}
	if want := []string{"U000000001", "U000000123", "U000000456"}; !reflect.DeepEqual(invited, want) {
		t.Errorf("invited = %v, want %v", invited, want)
	}
	want := map[string]any{"channel_id": "C000000001", "invited_user_ids": []any{"U000000001", "U000000123", "U000000456"}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("printed %s", h.stdout)
	}
}

// TestInviteScansTheDirectoryOnce: each handle used to cost a whole scan
// of the user directory.
func TestInviteScansTheDirectoryOnce(t *testing.T) {
	h := newHarness(t)
	out, err := h.run("invite", "--channel", "C000000001", "--users", "@ghost,@alice,@nobody,alice")
	if err == nil || !strings.Contains(err.Error(), "could not resolve 2 of the users: @ghost, @nobody") {
		t.Errorf("err = %v", err)
	}
	want := map[string]any{
		"channel_id":       "C000000001",
		"invited_user_ids": []any{"U000000123"},
		"unresolved_users": []any{"@ghost", "@nobody"},
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("printed %s", h.stdout)
	}
	if n := len(h.only("users.list")); n != 1 {
		t.Errorf("users.list calls = %d, want 1 for the four handles", n)
	}
	for _, handle := range []string{"@ghost", "@nobody"} {
		if !strings.Contains(h.stderr.String(), `could not resolve user "`+handle+`"`) {
			t.Errorf("stderr = %q, want the reason %s is unresolved", h.stderr, handle)
		}
	}
}

func TestInviteReportsPartialFailure(t *testing.T) {
	h := newHarness(t)
	h.fail["conversations.invite:U000000002"] = "already_in_channel"
	h.fail["conversations.invite:U000000003"] = "user_is_restricted"
	out, err := h.run("invite", "--channel", "C000000001", "--users", "U000000001,U000000002,U000000003,@ghost")
	// The TypeScript stopped at the first refusal, or exited 0 with the
	// unknown handle tucked away in unresolved_users.
	if err == nil || !strings.Contains(err.Error(), "U000000003 (user_is_restricted)") || !strings.Contains(err.Error(), "@ghost") {
		t.Errorf("err = %v", err)
	}
	want := map[string]any{
		"channel_id":                  "C000000001",
		"invited_user_ids":            []any{"U000000001"},
		"already_in_channel_user_ids": []any{"U000000002"},
		"unresolved_users":            []any{"@ghost"},
		"failed":                      []any{map[string]any{"target": "U000000003", "error": "user_is_restricted"}},
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("printed %s", h.stdout)
	}
	if !strings.Contains(h.stderr.String(), `could not resolve user "@ghost"`) {
		t.Errorf("stderr = %q, want the reason @ghost is unresolved", h.stderr)
	}
}

func TestInviteAbortPrintsWhatWasDone(t *testing.T) {
	h := newHarness(t)
	h.fail["conversations.invite:U000000002"] = "not_in_channel"
	out, err := h.run("invite", "--channel", "C000000001", "--users", "U000000001,U000000002,U000000003")
	if !slackapi.IsErrorCode(err, "not_in_channel") {
		t.Errorf("err = %v", err)
	}
	want := map[string]any{"channel_id": "C000000001", "invited_user_ids": []any{"U000000001"}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("printed %s", h.stdout)
	}
	if n := len(h.only("conversations.invite")); n != 2 {
		t.Errorf("conversations.invite calls = %d, want 2 (abort)", n)
	}

	// A failure before any invite prints nothing.
	h.fail = map[string]string{"users.list": "ratelimited"}
	out, err = h.run("invite", "--channel", "C000000001", "--users", "U000000001,@alice")
	if !slackapi.IsErrorCode(err, "ratelimited") || out != nil || len(h.only("conversations.invite")) != 0 {
		t.Errorf("lookup failure: printed %v, err = %v, calls = %v", out, err, h.methods())
	}
}

func TestInviteExternal(t *testing.T) {
	h := newHarness(t)
	h.fail["conversations.inviteShared:dave@example.com"] = "already_invited"
	h.fail["conversations.inviteShared:erin@example.com"] = "invalid_email"
	out, err := h.run("invite", "--channel", "C000000001", "--external", "--users", "bob@example.com,dave@example.com,erin@example.com,Bob@Example.com")
	if err == nil || !strings.Contains(err.Error(), "erin@example.com (invalid_email)") {
		t.Errorf("err = %v", err)
	}
	want := map[string]any{
		"channel_id": "C000000001", "external": true, "external_limited": true,
		"invited_emails":         []any{"bob@example.com"},
		"already_invited_emails": []any{"dave@example.com"},
		"failed":                 []any{map[string]any{"target": "erin@example.com", "error": "invalid_email"}},
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("printed %s", h.stdout)
	}
	calls := h.only("conversations.inviteShared")
	if len(calls) != 3 || calls[0].params["external_limited"] != true || calls[0].params["channel"] != "C000000001" {
		t.Errorf("inviteShared calls = %+v", calls)
	}

	h.fail = map[string]string{}
	out, err = h.run("invite", "--channel", "C000000001", "--external", "--allow-external-user-invites", "--users", "bob@example.com")
	if err != nil {
		t.Fatal(err)
	}
	// external_limited must be printed even when false.
	if v, ok := out["external_limited"]; !ok || v != false {
		t.Errorf("printed %s", h.stdout)
	}
	if got := h.only("conversations.inviteShared"); len(got) != 1 || got[0].params["external_limited"] != false {
		t.Errorf("inviteShared calls = %+v", got)
	}
}

func TestInviteRejectsBadUsage(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"flag without --external", []string{"invite", "--channel", "C000000001", "--users", "U000000001", "--allow-external-user-invites"}, "requires --external"},
		{"no users", []string{"invite", "--channel", "C000000001", "--users", " , "}, "no users provided"},
		{"missing --users", []string{"invite", "--channel", "C000000001"}, `required flag(s) "users" not set`},
		{"missing --channel", []string{"invite", "--users", "U000000001"}, `required flag(s) "channel" not set`},
		// The TypeScript invited the email addresses and exited 0, with
		// the rest in invalid_external_targets.
		{"external with a handle", []string{"invite", "--channel", "C000000001", "--external", "--users", "bob@example.com,@alice"}, "not an email address: @alice"},
		{"external without emails", []string{"invite", "--channel", "C000000001", "--external", "--users", "U000000001"}, "only accepts email addresses"},
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

func TestMark(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantChannel string
		wantTS      string
	}{
		{"URL target", []string{"mark", messageURL}, "C12345678", "1234567890.123456"},
		{"URL target with a --ts override", []string{"mark", messageURL, "--ts", "9999999999.999999"}, "C12345678", "9999999999.999999"},
		{"URL target with its own --workspace", []string{"mark", messageURL, "--workspace", "acme"}, "C12345678", "1234567890.123456"},
		{"channel name with --ts", []string{"mark", "#general", "--ts", "1234567890.123456"}, "C000000001", "1234567890.123456"},
		{"channel ID with --ts and --workspace", []string{"mark", "C12345678", "--ts", "1234567890.123456", "--workspace", "acme"}, "C12345678", "1234567890.123456"},
		{"channel URL with --ts", []string{"mark", "https://acme.slack.com/archives/C12345678", "--ts", "1234567890.123456"}, "C12345678", "1234567890.123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			out, err := h.run(tt.args...)
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := []call{{"conversations.mark", map[string]any{"channel": tt.wantChannel, "ts": tt.wantTS}}}
			if got := h.only("conversations.mark"); !reflect.DeepEqual(got, wantCalls) {
				t.Errorf("conversations.mark calls = %+v", got)
			}
			want := map[string]any{"ok": true, "channel": tt.wantChannel, "ts": tt.wantTS}
			if !reflect.DeepEqual(out, want) {
				t.Errorf("printed %s", h.stdout)
			}
		})
	}
}

func TestMarkErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"URL target with --workspace", []string{"mark", messageURL, "--workspace", "https://other.slack.com"}, `--workspace "https://other.slack.com" contradicts the URL`},
		{"channel without --ts", []string{"mark", "#general"}, "--ts is required"},
		{"channel URL without --ts", []string{"mark", "https://acme.slack.com/archives/C12345678"}, "--ts is required"},
		{"user target", []string{"mark", "U12345678", "--ts", "1234567890.123456"}, `channel mark" does not support user ID targets`},
		// The TypeScript sent any --ts to Slack as it was.
		{"garbage --ts", []string{"mark", "#general", "--ts", "now"}, "invalid --ts"},
		{"garbage --ts override", []string{"mark", messageURL, "--ts", "12abc"}, "invalid --ts"},
		{"another workspace's URL", []string{"mark", "https://other.slack.com/archives/C12345678/p1234567890123456"}, `no Slack credentials for workspace "https://other.slack.com"`},
		{"no target", []string{"mark"}, "accepts 1 arg(s)"},
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

	h := newHarness(t)
	h.fail["conversations.mark"] = "channel_not_found"
	if out, err := h.run("mark", messageURL); !slackapi.IsErrorCode(err, "channel_not_found") || out != nil {
		t.Errorf("Slack error: printed %v, err = %v", out, err)
	}
}

func TestMarkTruncatedURLHint(t *testing.T) {
	h := newHarness(t)
	if _, err := h.run("mark", messageURL+"?thread_ts=1234567000.000100"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stderr.String(), "truncated") {
		t.Errorf("stderr = %q, want the truncated-URL hint", h.stderr)
	}
}

// TestHelp ports the help contracts: --help is the documentation agents
// read.
func TestHelp(t *testing.T) {
	cmd := New(newHarness(t).rt)
	find := func(name string) (flagUsage func(flag string) string) {
		sub, _, err := cmd.Find([]string{name})
		if err != nil || sub.Name() != name {
			t.Fatalf("missing sub-command %s", name)
		}
		return func(flag string) string {
			f := sub.Flag(flag)
			if f == nil {
				t.Fatalf("%s lacks --%s", name, flag)
			}
			return f.Usage
		}
	}
	contains := func(what, text string, wants ...string) {
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("%s help %q does not mention %q", what, text, want)
			}
		}
	}

	list := find("list")
	contains("list --limit", list("limit"), "one page")
	contains("list --cursor", list("cursor"), "next page", "--via-counts")
	contains("list --via-counts", list("via-counts"), "Enterprise Grid", "browser")
	for _, flag := range []string{"workspace", "user", "all"} {
		list(flag)
	}

	mark := find("mark")
	contains("mark --workspace", mark("workspace"), "may repeat but not contradict")
	contains("mark --ts", mark("ts"), "overrides a URL timestamp")

	invite := find("invite")
	for _, flag := range []string{"channel", "users", "external", "allow-external-user-invites", "workspace"} {
		invite(flag)
	}
	newCmd := find("new")
	for _, flag := range []string{"name", "private", "workspace"} {
		newCmd(flag)
	}

	var names []string
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}
	slices.Sort(names)
	if want := []string{"invite", "list", "mark", "new"}; !reflect.DeepEqual(names, want) {
		t.Errorf("sub-commands = %v, want %v", names, want)
	}
}
