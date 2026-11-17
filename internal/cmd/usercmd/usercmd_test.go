package usercmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strconv"
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
// credentials store holding the workspace acme.slack.com.
type harness struct {
	t      *testing.T
	rt     *clictx.Context
	stdout *bytes.Buffer

	mu    sync.Mutex
	calls []call
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	for _, env := range []string{"SLACK_TOKEN", "SLACK_COOKIE_D", "SLACK_COOKIE", "SLACK_WORKSPACE_URL"} {
		t.Setenv(env, "")
	}
	h := &harness{t: t, stdout: &bytes.Buffer{}}
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
			return serve(method, params), nil
		})},
	}
	return h
}

// run executes "user <args>" and returns the JSON it printed (nil if
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
		h.t.Fatalf("user %s printed invalid JSON: %v\n%s", strings.Join(args, " "), jsonErr, h.stdout)
	}
	return out, err
}

func (h *harness) only(method string) []call {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []call
	for _, c := range h.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

var members = []any{
	map[string]any{"id": "U000000001", "name": "alice", "real_name": "Alice A", "is_bot": false, "deleted": false,
		"profile": map[string]any{"display_name": "ali", "email": "alice@example.com", "title": "Engineer"}},
	map[string]any{"id": "U000000002", "name": "deploybot", "is_bot": true, "deleted": false},
	map[string]any{"id": "U000000003", "name": "bob", "real_name": "Bob B", "is_bot": false, "deleted": false},
}

// serve is a three-member workspace in which the caller has a DM with
// alice.
func serve(method string, params map[string]any) map[string]any {
	switch method {
	case "users.list":
		// The cursor is the offset of the next member.
		offset := 0
		if c, _ := params["cursor"].(string); c != "" {
			offset, _ = strconv.Atoi(c)
		}
		end := min(offset+params["limit"].(int), len(members))
		resp := map[string]any{"members": members[offset:end]}
		if end < len(members) {
			resp["response_metadata"] = map[string]any{"next_cursor": strconv.Itoa(end)}
		}
		return resp
	case "conversations.list":
		return map[string]any{"channels": []any{map[string]any{"id": "D000000001", "user": "U000000001"}}}
	case "users.info":
		for _, m := range members {
			if m.(map[string]any)["id"] == params["user"] {
				return map[string]any{"user": m}
			}
		}
		return map[string]any{"ok": false, "error": "user_not_found"}
	case "conversations.open":
		if strings.Contains(params["users"].(string), ",") {
			return map[string]any{"channel": map[string]any{"id": "G000000001"}}
		}
		// The DM with alice exists and is open.
		return map[string]any{"no_op": true, "already_open": true, "channel": map[string]any{"id": "D000000001"}}
	}
	return map[string]any{"ok": false, "error": "unknown_method"}
}

func userIDs(out map[string]any) []string {
	var ids []string
	for _, u := range out["users"].([]any) {
		ids = append(ids, u.(map[string]any)["id"].(string))
	}
	return ids
}

func TestList(t *testing.T) {
	h := newHarness(t)
	out, err := h.run("list")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(userIDs(out), []string{"U000000001", "U000000003"}) || out["next_cursor"] != nil {
		t.Errorf("printed %s", h.stdout)
	}
	alice := map[string]any{
		"id": "U000000001", "name": "alice", "real_name": "Alice A", "display_name": "ali", "email": "alice@example.com",
		"title": "Engineer", "is_bot": false, "deleted": false, "dm_id": "D000000001",
	}
	if got := out["users"].([]any)[0]; !reflect.DeepEqual(got, alice) {
		t.Errorf("first user = %v", got)
	}
	if got := h.only("users.list"); len(got) != 1 || !reflect.DeepEqual(got[0].params, map[string]any{"limit": 200}) {
		t.Errorf("users.list calls = %+v", got)
	}

	out, err = h.run("list", "--limit", "2", "--include-bots", "--workspace", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(userIDs(out), []string{"U000000001", "U000000002"}) || out["next_cursor"] != "2" {
		t.Errorf("--limit 2 --include-bots printed %s", h.stdout)
	}

	out, err = h.run("list", "--limit", "2", "--include-bots", "--cursor", "2")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(userIDs(out), []string{"U000000003"}) || out["next_cursor"] != nil {
		t.Errorf("--cursor 2 printed %s", h.stdout)
	}
	if got := h.only("users.list"); len(got) != 1 || !reflect.DeepEqual(got[0].params, map[string]any{"limit": 2, "cursor": "2"}) {
		t.Errorf("users.list calls = %+v", got)
	}
}

func TestListRejectsGarbageLimit(t *testing.T) {
	// The TypeScript passed NaN down for "--limit abc" and read "12abc"
	// as 12.
	for _, limit := range []string{"abc", "12abc", "0", "-1", "1001"} {
		h := newHarness(t)
		out, err := h.run("list", "--limit", limit)
		if err == nil || !strings.Contains(err.Error(), "--limit") {
			t.Errorf("--limit %s: err = %v", limit, err)
		}
		if out != nil || len(h.calls) != 0 {
			t.Errorf("--limit %s: printed %v, made calls %+v", limit, out, h.calls)
		}
	}
}

func TestGet(t *testing.T) {
	h := newHarness(t)
	want := map[string]any{"id": "U000000003", "name": "bob", "real_name": "Bob B", "is_bot": false, "deleted": false}

	out, err := h.run("get", "U000000003")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("printed %s", h.stdout)
	}
	if len(h.calls) != 1 || h.calls[0].method != "users.info" {
		t.Errorf("calls = %+v", h.calls)
	}

	out, err = h.run("get", "@bob")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("by handle: printed %s", h.stdout)
	}

	out, err = h.run("get", "@nobody")
	if err == nil || !strings.Contains(err.Error(), `could not resolve user "@nobody"`) || out != nil {
		t.Errorf("unknown handle: printed %v, err = %v", out, err)
	}
	if out, err := h.run("get", "U000000099"); !slackapi.IsErrorCode(err, "user_not_found") || out != nil {
		t.Errorf("unknown ID: printed %v, err = %v", out, err)
	}
	if _, err := h.run("get"); err == nil {
		t.Error("no error without a user")
	}
	if _, err := h.run("get", "U000000001", "U000000003"); err == nil {
		t.Error("no error with two users")
	}
}

func TestDMOpen(t *testing.T) {
	h := newHarness(t)
	out, err := h.run("dm-open", "U000000001")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"user_ids": []any{"U000000001"}, "dm_channel_id": "D000000001", "channel_type": "dm", "already_open": true}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("printed %s", h.stdout)
	}
	if !reflect.DeepEqual(h.calls, []call{{"conversations.open", map[string]any{"users": "U000000001"}}}) {
		t.Errorf("calls = %+v", h.calls)
	}

	out, err = h.run("dm-open", "@alice", "U000000003", "--workspace", "acme")
	if err != nil {
		t.Fatal(err)
	}
	want = map[string]any{"user_ids": []any{"U000000001", "U000000003"}, "dm_channel_id": "G000000001", "channel_type": "group_dm", "already_open": false}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("group: printed %s", h.stdout)
	}
	if got := h.only("conversations.open"); len(got) != 1 || got[0].params["users"] != "U000000001,U000000003" {
		t.Errorf("conversations.open calls = %+v", got)
	}

	// Nothing is opened when one of the users is unknown.
	out, err = h.run("dm-open", "U000000001", "@nobody")
	if err == nil || !strings.Contains(err.Error(), "@nobody") || out != nil {
		t.Errorf("unknown user: printed %v, err = %v", out, err)
	}
	if n := len(h.only("conversations.open")); n != 0 {
		t.Errorf("conversations.open calls = %d, want 0", n)
	}

	nine := []string{"dm-open"}
	for i := range 9 {
		nine = append(nine, "U00000010"+string(rune('0'+i)))
	}
	if _, err := h.run(nine...); err == nil || !strings.Contains(err.Error(), "maximum of 8") || len(h.calls) != 0 {
		t.Errorf("nine users: err = %v, calls = %+v", err, h.calls)
	}
	if _, err := h.run("dm-open"); err == nil || len(h.calls) != 0 {
		t.Errorf("no users: err = %v, calls = %+v", err, h.calls)
	}
}

// TestHelp ports the help contracts: --help is the documentation agents
// read.
func TestHelp(t *testing.T) {
	cmd := New(newHarness(t).rt)
	dmOpen, _, err := cmd.Find([]string{"dm-open"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"One to 8 other user", "caller is implicit", "already_open"} {
		if !strings.Contains(dmOpen.Long, want) {
			t.Errorf("dm-open help does not mention %q", want)
		}
	}
	for _, name := range []string{"list", "get", "dm-open"} {
		sub, _, err := cmd.Find([]string{name})
		if err != nil || sub.Name() != name {
			t.Errorf("missing sub-command %s", name)
		} else if sub.Flag("workspace") == nil {
			t.Errorf("%s lacks --workspace", name)
		}
	}
	list, _, _ := cmd.Find([]string{"list"})
	if got := list.Flag("limit").DefValue; got != "200" {
		t.Errorf("--limit default = %s, want 200", got)
	}
}
