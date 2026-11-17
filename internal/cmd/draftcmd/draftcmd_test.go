package draftcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/authextract"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const workspaceURL = "https://workspace.slack.com"

type call struct {
	method string
	params map[string]any
}

// harness runs the "draft" command against a fake Slack: a credentials
// store holding one browser-auth workspace, an API handler answering from
// fixtures, and a byte endpoint for uploads.
type harness struct {
	t      *testing.T
	rt     *clictx.Context
	stdout *bytes.Buffer
	stderr *bytes.Buffer

	// drafts is what drafts.list returns.
	drafts []any
	// channelInfo is what conversations.info returns, by channel ID.
	channelInfo map[string]map[string]any
	// failOnce makes the first call to that method fail with invalid_auth.
	failOnce string
	// failWith makes every call to a method fail with that error code.
	failWith map[string]string

	mu         sync.Mutex
	calls      []call
	failedOnce bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	for _, env := range []string{"SLACK_TOKEN", "SLACK_COOKIE_D", "SLACK_COOKIE", "SLACK_WORKSPACE_URL"} {
		t.Setenv(env, "")
	}
	h := &harness{t: t, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	t.Cleanup(srv.Close)

	handler := func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.calls = append(h.calls, call{method, params})
		if h.failOnce == method && !h.failedOnce {
			h.failedOnce = true
			return map[string]any{"ok": false, "error": "invalid_auth"}, nil
		}
		if code := h.failWith[method]; code != "" {
			return map[string]any{"ok": false, "error": code}, nil
		}
		switch method {
		case "drafts.list":
			return map[string]any{"ok": true, "drafts": h.drafts}, nil
		case "drafts.create":
			return map[string]any{"ok": true, "draft": map[string]any{"id": "DrNew", "destinations": params["destinations"]}}, nil
		case "drafts.update":
			return map[string]any{"ok": true, "draft": map[string]any{"id": params["draft_id"], "destinations": params["destinations"]}}, nil
		case "conversations.open":
			return map[string]any{"ok": true, "channel": map[string]any{"id": "D99999999"}}, nil
		case "conversations.info":
			if info := h.channelInfo[params["channel"].(string)]; info != nil {
				return map[string]any{"ok": true, "channel": info}, nil
			}
		case "conversations.history":
			return map[string]any{"ok": true, "messages": []any{
				map[string]any{"ts": params["latest"], "text": "root", "thread_ts": "1700000000.000001"},
			}}, nil
		case "files.getUploadURLExternal":
			return map[string]any{"ok": true, "upload_url": srv.URL + "/f", "file_id": "F-" + params["filename"].(string)}, nil
		}
		return map[string]any{"ok": true}, nil
	}

	store := &auth.Store{Path: filepath.Join(t.TempDir(), "credentials.json"), Service: "svc"}
	if err := store.Upsert(browserWorkspace()); err != nil {
		t.Fatal(err)
	}
	h.rt = &clictx.Context{
		Store:         store,
		Stdout:        h.stdout,
		Stderr:        h.stderr,
		ClientOptions: []slackapi.Option{slackapi.WithHandler(handler), slackapi.WithHTTPClient(srv.Client())},
	}
	return h
}

func browserWorkspace() auth.Workspace {
	return auth.Workspace{
		URL:  workspaceURL,
		Auth: slackapi.Auth{Type: slackapi.AuthBrowser, XoxcToken: "xoxc-test", XoxdCookie: "xoxd-test"},
	}
}

// run executes "draft <args>" and returns what it printed to stdout.
func (h *harness) run(args ...string) (string, error) {
	h.t.Helper()
	h.stdout.Reset()
	cmd := New(h.rt)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return h.stdout.String(), err
}

func (h *harness) mustRun(args ...string) string {
	h.t.Helper()
	out, err := h.run(args...)
	if err != nil {
		h.t.Fatalf("draft %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// mustFail runs the command and checks that it fails with an error
// containing want.
func (h *harness) mustFail(want string, args ...string) {
	h.t.Helper()
	out, err := h.run(args...)
	if err == nil || !strings.Contains(err.Error(), want) {
		h.t.Fatalf("draft %s: err = %v, want one containing %q", strings.Join(args, " "), err, want)
	}
	if out != "" {
		h.t.Errorf("a failed command printed a result: %s", out)
	}
}

func (h *harness) callsTo(method string) []call {
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

func (h *harness) methods() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.calls))
	for i, c := range h.calls {
		out[i] = c.method
	}
	return out
}

// only returns the single call made to method.
func (h *harness) only(method string) call {
	h.t.Helper()
	calls := h.callsTo(method)
	if len(calls) != 1 {
		h.t.Fatalf("%d calls to %s, want 1 (all calls: %v)", len(calls), method, h.methods())
	}
	return calls[0]
}

func (h *harness) wantNoCalls(methods ...string) {
	h.t.Helper()
	if len(methods) == 0 {
		if got := h.methods(); len(got) != 0 {
			h.t.Errorf("calls = %v, want none", got)
		}
		return
	}
	for _, m := range methods {
		if n := len(h.callsTo(m)); n != 0 {
			h.t.Errorf("%d calls to %s, want none", n, m)
		}
	}
}

func (h *harness) wantDestinations(c call, want ...map[string]any) {
	h.t.Helper()
	wantAny := make([]any, len(want))
	for i, w := range want {
		wantAny[i] = w
	}
	if !reflect.DeepEqual(c.params["destinations"], wantAny) {
		h.t.Errorf("destinations = %#v, want %#v", c.params["destinations"], wantAny)
	}
}

func (h *harness) wantFileIDs(c call, want ...string) {
	h.t.Helper()
	got, _ := c.params["file_ids"].([]string)
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		h.t.Errorf("file_ids = %#v, want %v", c.params["file_ids"], want)
	}
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func wantJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("output = %s\nwant %s", got, want)
	}
}

// desktopRefresh makes rt re-import credentials when a call fails with an
// auth error, as Slack Desktop being installed does.
func (h *harness) desktopRefresh() {
	h.rt.Desktop = func(context.Context) (*authextract.Extracted, error) {
		return &authextract.Extracted{
			CookieD: "xoxd-fresh",
			Teams:   []authextract.Team{{URL: workspaceURL + "/", Token: "xoxc-fresh"}},
		}, nil
	}
}

func draft(overrides map[string]any) map[string]any {
	d := map[string]any{
		"id":              "Dr123",
		"blocks":          []any{},
		"destinations":    []any{map[string]any{"channel_id": "C11111111"}},
		"last_updated_ts": "1700000000.5",
	}
	for k, v := range overrides {
		d[k] = v
	}
	return d
}

func threadedBroadcastDraft() map[string]any {
	return draft(map[string]any{
		"destinations": []any{map[string]any{
			"channel_id": "C11111111", "thread_ts": "1700000000.100000", "broadcast": true,
		}},
		"last_updated_ts": "1700000000.123",
		"file_ids":        []any{"F1", "F2"},
	})
}

const (
	threadTS      = "1700000000.100000"
	otherThreadTS = "1700000000.200000"
)

// --- create ---------------------------------------------------------------

func TestCreateForChannelID(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("create", "C11111111", "hello", "--workspace", workspaceURL)

	create := h.only("drafts.create")
	h.wantDestinations(create, map[string]any{"channel_id": "C11111111"})
	if blocks, _ := create.params["blocks"].([]any); len(blocks) != 1 {
		t.Errorf("blocks = %#v", create.params["blocks"])
	}
	wantJSON(t, out, `{"ok": true, "draft": {"id": "DrNew", "destinations": [{"channel_id": "C11111111"}]}}`)
}

func TestCreateForUserOpensDM(t *testing.T) {
	h := newHarness(t)
	h.mustRun("create", "U12345678", "hello", "--thread-ts", threadTS)

	if got := h.only("conversations.open").params["users"]; got != "U12345678" {
		t.Errorf("conversations.open users = %v", got)
	}
	h.wantDestinations(h.only("drafts.create"),
		map[string]any{"channel_id": "D99999999", "thread_ts": threadTS, "broadcast": false})
}

func TestCreateForMessageURLRepliesInItsThread(t *testing.T) {
	h := newHarness(t)
	url := workspaceURL + "/archives/C11111111/p1700000000100000"

	h.mustRun("create", url, "hello", "--broadcast")
	h.wantDestinations(h.only("drafts.create"),
		map[string]any{"channel_id": "C11111111", "thread_ts": "1700000000.000001", "broadcast": true})

	t.Run("--thread-ts overrides the thread of the URL", func(t *testing.T) {
		h := newHarness(t)
		h.mustRun("create", url, "hello", "--thread-ts", otherThreadTS)
		h.wantDestinations(h.only("drafts.create"),
			map[string]any{"channel_id": "C11111111", "thread_ts": otherThreadTS, "broadcast": false})
		h.wantNoCalls("conversations.history")
	})

	t.Run("warns about a URL the shell truncated", func(t *testing.T) {
		h := newHarness(t)
		h.mustRun("create", url+"?thread_ts=1700000000.000001", "hello")
		if !strings.Contains(h.stderr.String(), "truncated") {
			t.Errorf("stderr = %q, want a truncation hint", h.stderr.String())
		}
	})
}

func TestCreateRejectsBroadcast(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"without a thread", []string{"C11111111", "hello", "--broadcast"}, "--broadcast requires a thread"},
		{"for a DM (user) target", []string{"W12345678", "hello", "--thread-ts", threadTS, "--broadcast"}, "not supported for DM targets"},
		{"for a D… channel id", []string{"D12345678", "hello", "--thread-ts", threadTS, "--broadcast"}, "not supported for DM targets"},
		{"on a channel name with no thread", []string{"general", "x", "--broadcast"}, "--broadcast requires a thread"},
		{"on a DM message URL", []string{workspaceURL + "/archives/D12345678/p1700000000100000", "x", "--broadcast"}, "not supported for DM targets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.mustFail(tc.want, append([]string{"create"}, tc.args...)...)
			// The real problem is reported without any round-trip.
			h.wantNoCalls()
		})
	}
}

func TestCreateValidatesBeforeCallingSlack(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"empty text", []string{"C11111111", "  "}, "draft text is empty"},
		{"malformed --thread-ts", []string{"C11111111", "x", "--thread-ts", "yesterday"}, "invalid --thread-ts"},
		{"non-Slack URL", []string{"https://example.com/x", "x"}, "unsupported target URL"},
		{"missing attachment", []string{"C11111111", "nope", "--attach", filepath.Join(t.TempDir(), "missing.png")}, "missing.png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.mustFail(tc.want, append([]string{"create"}, tc.args...)...)
			h.wantNoCalls()
		})
	}
}

func TestCreateUploadsAttachments(t *testing.T) {
	h := newHarness(t)
	a, b := writeFile(t, "a.png", "x"), writeFile(t, "b.pdf", "y")

	// Repeated flags accumulate; a duplicate path is uploaded once.
	h.mustRun("create", "C11111111", "see attached", "--attach", a, "--attach", b, "--attach", a)

	// Every file is staged, then completed in one call, then the draft.
	want := []string{"files.getUploadURLExternal", "files.getUploadURLExternal", "files.completeUploadExternal", "drafts.create"}
	if got := h.methods(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	h.wantFileIDs(h.only("drafts.create"), "F-a.png", "F-b.pdf")
}

func TestCreateAllowsAttachmentsWithoutText(t *testing.T) {
	h := newHarness(t)
	h.mustRun("create", "C11111111", "", "--attach", writeFile(t, "a.png", "x"))
	create := h.only("drafts.create")
	h.wantFileIDs(create, "F-a.png")
	if blocks, ok := create.params["blocks"].([]any); !ok || len(blocks) != 0 {
		t.Errorf("blocks = %#v, want an empty list", create.params["blocks"])
	}
}

// --- update ---------------------------------------------------------------

func TestUpdateTextOnlyPreservesEverythingElse(t *testing.T) {
	h := newHarness(t)
	h.drafts = []any{threadedBroadcastDraft()}
	out := h.mustRun("update", "Dr123", "revised", "--workspace", workspaceURL)

	update := h.only("drafts.update")
	// The conflict-detection ts is looked up and padded to 7 digits.
	if got := update.params["client_last_updated_ts"]; got != "1700000000.1230000" {
		t.Errorf("client_last_updated_ts = %v", got)
	}
	h.wantDestinations(update, map[string]any{"channel_id": "C11111111", "thread_ts": threadTS, "broadcast": true})
	h.wantFileIDs(update, "F1", "F2")
	wantJSON(t, out, `{"ok": true, "draft": {"id": "Dr123", "destinations": [
		{"channel_id": "C11111111", "thread_ts": "1700000000.100000", "broadcast": true}]}}`)
}

func TestUpdateDestinationAndBroadcast(t *testing.T) {
	plainThread := draft(map[string]any{
		"destinations": []any{map[string]any{"channel_id": "C11111111", "thread_ts": threadTS}},
	})
	for _, tc := range []struct {
		name  string
		draft map[string]any
		args  []string
		want  map[string]any
	}{
		{
			// An inherited broadcast flag must not turn into a "broadcast
			// requires a thread" failure on a non-thread target.
			"re-addressing a broadcast thread draft to a channel clears broadcast",
			threadedBroadcastDraft(), []string{"--channel", "C99999999"},
			map[string]any{"channel_id": "C99999999"},
		},
		{
			"re-addressing to another thread does not inherit broadcast",
			threadedBroadcastDraft(), []string{"--channel", "C22222222", "--thread-ts", otherThreadTS},
			map[string]any{"channel_id": "C22222222", "thread_ts": otherThreadTS, "broadcast": false},
		},
		{
			"changing only --thread-ts does not inherit broadcast",
			threadedBroadcastDraft(), []string{"--thread-ts", otherThreadTS},
			map[string]any{"channel_id": "C11111111", "thread_ts": otherThreadTS, "broadcast": false},
		},
		{
			"repeating the same --thread-ts keeps broadcast",
			threadedBroadcastDraft(), []string{"--thread-ts", threadTS},
			map[string]any{"channel_id": "C11111111", "thread_ts": threadTS, "broadcast": true},
		},
		{
			"--no-broadcast clears an inherited flag",
			threadedBroadcastDraft(), []string{"--no-broadcast"},
			map[string]any{"channel_id": "C11111111", "thread_ts": threadTS, "broadcast": false},
		},
		{
			"--broadcast=false clears an inherited flag",
			threadedBroadcastDraft(), []string{"--broadcast=false"},
			map[string]any{"channel_id": "C11111111", "thread_ts": threadTS, "broadcast": false},
		},
		{
			// Defining --no-broadcast must not make broadcast default on.
			"no flag does not turn broadcast on",
			plainThread, nil,
			map[string]any{"channel_id": "C11111111", "thread_ts": threadTS, "broadcast": false},
		},
		{
			"--broadcast turns it on",
			plainThread, []string{"--broadcast"},
			map[string]any{"channel_id": "C11111111", "thread_ts": threadTS, "broadcast": true},
		},
		{
			"re-addressing to a user drafts a DM",
			threadedBroadcastDraft(), []string{"--channel", "U12345678"},
			map[string]any{"channel_id": "D99999999"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.drafts = []any{tc.draft}
			h.mustRun(append([]string{"update", "Dr123", "revised"}, tc.args...)...)
			h.wantDestinations(h.only("drafts.update"), tc.want)
		})
	}
}

func TestUpdateRefusals(t *testing.T) {
	dmThread := draft(map[string]any{
		"destinations": []any{map[string]any{"channel_id": "D12345678", "thread_ts": threadTS}},
	})
	multi := draft(map[string]any{
		"destinations": []any{map[string]any{"channel_id": "C11111111"}, map[string]any{"channel_id": "C22222222"}},
	})
	for _, tc := range []struct {
		name  string
		draft map[string]any
		args  []string
		want  string
		// calls are the only API methods allowed to have been called.
		calls []string
	}{
		{"--broadcast on a non-thread draft", draft(nil), []string{"--broadcast"}, "--broadcast requires a thread", []string{"drafts.list"}},
		{"a scheduled draft", draft(map[string]any{"date_scheduled": float64(1800000000)}), nil, "scheduled send time", []string{"drafts.list"}},
		{"text-only update of a multi-destination draft", multi, nil, "multiple destinations", []string{"drafts.list"}},
		{"--broadcast when re-addressing to a user", threadedBroadcastDraft(), []string{"--channel", "W12345678", "--broadcast"}, "not supported for DM targets", nil},
		{"--broadcast on a DM draft", dmThread, []string{"--broadcast"}, "not supported for DM targets", []string{"drafts.list"}},
		{"--broadcast when re-addressing to a channel name without a thread", draft(nil), []string{"--channel", "general", "--broadcast"}, "--broadcast requires a thread", nil},
		{"--broadcast together with --no-broadcast", threadedBroadcastDraft(), []string{"--broadcast", "--no-broadcast"}, "none of the others can be", nil},
		{"an unknown draft", draft(nil), []string{"--workspace", workspaceURL, "--thread-ts", threadTS}, "", nil},
		{"a draft without last_updated_ts", map[string]any{"id": "Dr123", "destinations": []any{map[string]any{"channel_id": "C11111111"}}}, nil, "pass --last-updated-ts", []string{"drafts.list"}},
		{"a draft without destination", draft(map[string]any{"destinations": []any{}}), nil, "pass --channel", []string{"drafts.list"}},
		{"a stale --last-updated-ts", draft(nil), []string{"--last-updated-ts", "1700000000.4"}, "changed since", []string{"drafts.list"}},
		{"a malformed --last-updated-ts", draft(nil), []string{"--last-updated-ts", "now"}, "--last-updated-ts", nil},
		{"a malformed --thread-ts", draft(nil), []string{"--thread-ts", "123"}, "invalid --thread-ts", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.drafts = []any{tc.draft}
			id := "Dr123"
			if tc.name == "an unknown draft" {
				id, tc.want, tc.calls = "DrMissing", "draft not found: DrMissing", []string{"drafts.list"}
			}
			h.mustFail(tc.want, append([]string{"update", id, "revised"}, tc.args...)...)
			if got := h.methods(); !slices.Equal(got, tc.calls) {
				t.Errorf("calls = %v, want %v", got, tc.calls)
			}
		})
	}
}

func TestUpdateMultiDestinationDraftCanBeReAddressed(t *testing.T) {
	h := newHarness(t)
	h.drafts = []any{draft(map[string]any{
		"destinations": []any{map[string]any{"channel_id": "C11111111"}, map[string]any{"channel_id": "C22222222"}},
	})}
	h.mustRun("update", "Dr123", "revised", "--channel", "C99999999")
	h.wantDestinations(h.only("drafts.update"), map[string]any{"channel_id": "C99999999"})
}

func TestUpdateWithMatchingLastUpdatedTS(t *testing.T) {
	h := newHarness(t)
	h.drafts = []any{draft(nil)}
	// Same instant as the draft's "1700000000.5", padded differently.
	h.mustRun("update", "Dr123", "revised", "--last-updated-ts", "1700000000.5000000")
	if got := h.only("drafts.update").params["client_last_updated_ts"]; got != "1700000000.5000000" {
		t.Errorf("client_last_updated_ts = %v", got)
	}
}

func TestUpdateAppendsAttachments(t *testing.T) {
	t.Run("new files are added to the existing ones", func(t *testing.T) {
		h := newHarness(t)
		h.drafts = []any{threadedBroadcastDraft()}
		h.mustRun("update", "Dr123", "revised", "--attach", writeFile(t, "c.txt", "z"))
		h.wantFileIDs(h.only("drafts.update"), "F1", "F2", "F-c.txt")
	})
	t.Run("an id already on the draft is not duplicated", func(t *testing.T) {
		h := newHarness(t)
		d := threadedBroadcastDraft()
		d["file_ids"] = []any{"F-c.txt"}
		h.drafts = []any{d}
		h.mustRun("update", "Dr123", "revised", "--attach", writeFile(t, "c.txt", "z"))
		h.wantFileIDs(h.only("drafts.update"), "F-c.txt")
	})
	t.Run("a refused draft uploads nothing", func(t *testing.T) {
		h := newHarness(t)
		h.drafts = []any{draft(map[string]any{"date_scheduled": float64(1800000000)})}
		h.mustFail("scheduled send time", "update", "Dr123", "revised", "--attach", writeFile(t, "c.txt", "z"))
		h.wantNoCalls("files.getUploadURLExternal", "files.delete")
	})
}

// --- delete, list -----------------------------------------------------------

func TestDelete(t *testing.T) {
	t.Run("looks up and pads last_updated_ts", func(t *testing.T) {
		h := newHarness(t)
		h.drafts = []any{draft(map[string]any{"last_updated_ts": "1700000000.42"})}
		out := h.mustRun("delete", "Dr123", "--workspace", workspaceURL)

		want := map[string]any{"draft_id": "Dr123", "client_last_updated_ts": "1700000000.4200000"}
		if got := h.only("drafts.delete").params; !reflect.DeepEqual(got, want) {
			t.Errorf("params = %#v", got)
		}
		wantJSON(t, out, `{"ok": true, "draft_id": "Dr123"}`)
	})
	t.Run("uses --last-updated-ts without a lookup", func(t *testing.T) {
		h := newHarness(t)
		h.mustRun("delete", "Dr123", "--last-updated-ts", "1700000000.1")
		if got := h.methods(); !reflect.DeepEqual(got, []string{"drafts.delete"}) {
			t.Errorf("calls = %v", got)
		}
	})
	t.Run("rejects what cannot be a draft id", func(t *testing.T) {
		for _, id := range []string{"C11111111", "#general", ""} {
			h := newHarness(t)
			h.mustFail("draft id", "delete", id)
			h.wantNoCalls()
		}
	})
}

func TestList(t *testing.T) {
	h := newHarness(t)
	h.drafts = []any{
		draft(map[string]any{"last_updated_ts": "1700000000.1", "blocks": []any{map[string]any{
			"type": "rich_text",
			"elements": []any{map[string]any{
				"type": "rich_text_section", "elements": []any{map[string]any{"type": "text", "text": "hi"}},
			}},
		}}}),
		draft(map[string]any{"id": "Dr456", "destinations": []any{
			map[string]any{"channel_id": "C11111111"}, map[string]any{"channel_id": "C00000000"},
		}}),
	}
	h.channelInfo = map[string]map[string]any{"C11111111": {"id": "C11111111", "name": "general"}}

	out := h.mustRun("list", "--workspace", workspaceURL)
	wantJSON(t, out, `{"ok": true, "count": 2, "drafts": [
		{"id": "Dr123", "text": "hi", "last_updated_ts": "1700000000.1",
		 "destinations": [{"channel_id": "C11111111", "channel_name": "general"}]},
		{"id": "Dr456", "last_updated_ts": "1700000000.5",
		 "destinations": [{"channel_id": "C11111111", "channel_name": "general"}, {"channel_id": "C00000000"}]}
	]}`)
	if got := h.only("drafts.list").params; !reflect.DeepEqual(got, map[string]any{"is_active": true}) {
		t.Errorf("drafts.list params = %#v", got)
	}
	// One lookup per distinct channel, not per destination.
	if n := len(h.callsTo("conversations.info")); n != 2 {
		t.Errorf("%d conversations.info calls, want 2", n)
	}
}

func TestListFlags(t *testing.T) {
	h := newHarness(t)
	h.drafts = []any{draft(nil), draft(map[string]any{"id": "Dr456"})}
	out := h.mustRun("list", "--all", "--limit", "1")
	if got := h.only("drafts.list").params; !reflect.DeepEqual(got, map[string]any{"limit": 1}) {
		t.Errorf("drafts.list params = %#v", got)
	}
	var parsed struct {
		Count  int              `json:"count"`
		Drafts []map[string]any `json:"drafts"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil || parsed.Count != 1 || len(parsed.Drafts) != 1 {
		t.Errorf("output = %s (err %v), want exactly one draft", out, err)
	}

	for _, bad := range []string{"abc", "0", "-3", "1.5"} {
		h := newHarness(t)
		h.mustFail("invalid --limit", "list", "--limit", bad)
		h.wantNoCalls()
	}
}

func TestListWithNoDrafts(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("list")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["ok"] != true || parsed["count"] != float64(0) {
		t.Errorf("output = %s, want ok and an explicit count of 0", out)
	}
}

// --- command surface --------------------------------------------------------

func TestOldEditorUsagePointsToCompose(t *testing.T) {
	h := newHarness(t)
	_, err := h.run("#general", "hi")
	if err == nil {
		t.Fatal("draft #general hi succeeded")
	}
	for _, want := range []string{"not a 'message draft' subcommand", `message compose "#general"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
	h.wantNoCalls()

	if _, err := h.run(); err != nil {
		t.Errorf("bare draft (help) failed: %v", err)
	}
}

func TestHelpContracts(t *testing.T) {
	cmd := New(newHarness(t).rt)
	find := func(name string) (long string, flag func(string) string) {
		for _, c := range cmd.Commands() {
			if c.Name() == name {
				return c.Long, func(f string) string { return c.Flags().Lookup(f).Usage }
			}
		}
		t.Fatalf("no %q subcommand", name)
		return "", nil
	}
	createLong, _ := find("create")
	if !strings.Contains(createLong, "user id") {
		t.Errorf("create help does not document DM targeting: %s", createLong)
	}
	updateLong, updateFlag := find("update")
	if !strings.Contains(updateFlag("no-broadcast"), "inherited") {
		t.Errorf("--no-broadcast help = %q", updateFlag("no-broadcast"))
	}
	if !strings.Contains(updateLong, "ADDS") || !strings.Contains(updateFlag("attach"), "in addition") {
		t.Errorf("update help does not say that --attach appends")
	}
	if !strings.Contains(cmd.Long, "message compose") || !strings.Contains(cmd.Short, "browser auth") {
		t.Errorf("draft help = %q / %q", cmd.Short, cmd.Long)
	}
}

// --- auth -------------------------------------------------------------------

func TestStandardTokenIsRefusedUpFront(t *testing.T) {
	for _, args := range [][]string{
		{"list"},
		{"create", "C11111111", "hello", "--attach", writeFile(t, "a.png", "x")},
		{"update", "Dr123", "revised"},
		{"delete", "Dr123"},
		{"delete", "Dr123", "--last-updated-ts", "1700000000.1"},
	} {
		t.Run(args[0], func(t *testing.T) {
			h := newHarness(t)
			t.Setenv("SLACK_TOKEN", "xoxb-not-a-browser-token")
			h.mustFail("require browser auth (xoxc/xoxd)", args...)
			// In particular nothing was uploaded.
			h.wantNoCalls()
		})
	}
}

// --- attachments: no orphans across auth retries and failed drafts ----------

func TestAuthRetryDoesNotUploadTwice(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		h := newHarness(t)
		h.failOnce = "drafts.create"
		h.desktopRefresh()
		h.mustRun("create", "C11111111", "see attached", "--attach", writeFile(t, "a.png", "x"))

		if n := len(h.callsTo("files.getUploadURLExternal")); n != 1 {
			t.Errorf("%d uploads staged, want 1", n)
		}
		if n := len(h.callsTo("files.completeUploadExternal")); n != 1 {
			t.Errorf("%d upload completions, want 1", n)
		}
		creates := h.callsTo("drafts.create")
		if len(creates) != 2 {
			t.Fatalf("%d drafts.create calls, want 2", len(creates))
		}
		for _, c := range creates {
			h.wantFileIDs(c, "F-a.png")
		}
		h.wantNoCalls("files.delete")
	})
	t.Run("update", func(t *testing.T) {
		h := newHarness(t)
		h.drafts = []any{draft(map[string]any{
			"destinations": []any{map[string]any{"channel_id": "C11111111", "thread_ts": threadTS}},
			"file_ids":     []any{"F1"},
		})}
		h.failOnce = "drafts.update"
		h.desktopRefresh()
		h.mustRun("update", "Dr123", "revised", "--attach", writeFile(t, "c.txt", "z"))

		if n := len(h.callsTo("files.getUploadURLExternal")); n != 1 {
			t.Errorf("%d uploads staged, want 1", n)
		}
		updates := h.callsTo("drafts.update")
		if len(updates) != 2 {
			t.Fatalf("%d drafts.update calls, want 2", len(updates))
		}
		for _, c := range updates {
			h.wantFileIDs(c, "F1", "F-c.txt")
		}
		h.wantNoCalls("files.delete")
	})
}

func TestFailedDraftDeletesItsUploads(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		h := newHarness(t)
		h.failWith = map[string]string{"drafts.create": "denied"}
		_, err := h.run("create", "C11111111", "see attached", "--attach", writeFile(t, "a.png", "x"))
		if !slackapi.IsErrorCode(err, "denied") {
			t.Fatalf("err = %v, want Slack's denied", err)
		}
		if got := h.only("files.delete").params["file"]; got != "F-a.png" {
			t.Errorf("deleted %v, want F-a.png", got)
		}
	})
	t.Run("update deletes only the new upload", func(t *testing.T) {
		h := newHarness(t)
		h.drafts = []any{draft(map[string]any{"file_ids": []any{"F1"}})}
		h.failWith = map[string]string{"drafts.update": "stale"}
		_, err := h.run("update", "Dr123", "revised", "--attach", writeFile(t, "c.txt", "z"))
		if !slackapi.IsErrorCode(err, "stale") {
			t.Fatalf("err = %v, want Slack's stale", err)
		}
		if got := h.only("files.delete").params["file"]; got != "F-c.txt" {
			t.Errorf("deleted %v, want F-c.txt", got)
		}
	})
	t.Run("nothing to delete without attachments", func(t *testing.T) {
		h := newHarness(t)
		h.failWith = map[string]string{"drafts.create": "denied"}
		if _, err := h.run("create", "C11111111", "no file"); !slackapi.IsErrorCode(err, "denied") {
			t.Fatalf("err = %v", err)
		}
		h.wantNoCalls("files.delete")
	})
	t.Run("a failed cleanup is a warning, not the error", func(t *testing.T) {
		h := newHarness(t)
		h.failWith = map[string]string{"drafts.create": "denied", "files.delete": "cant_delete_file"}
		_, err := h.run("create", "C11111111", "see attached", "--attach", writeFile(t, "a.png", "x"))
		if !slackapi.IsErrorCode(err, "denied") {
			t.Fatalf("err = %v, want Slack's denied", err)
		}
		if !strings.Contains(h.stderr.String(), "F-a.png") {
			t.Errorf("stderr = %q, want a warning naming the orphaned file", h.stderr.String())
		}
	})
}
