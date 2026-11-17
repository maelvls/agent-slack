package workflowcmd

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

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const shortcutURL = "https://slack.com/shortcuts/Ft0123ABCDEF/0123456789abcdef"

type call struct {
	method string
	params map[string]any
}

// harness runs the workflow command against a fake Slack and a temporary
// credentials store holding one workspace.
type harness struct {
	t      *testing.T
	rt     *clictx.Context
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	// mu guards calls: "workflow list" makes its two calls concurrently.
	mu    sync.Mutex
	calls []call
	// responses maps an API method to its answer (default: ok).
	responses map[string]map[string]any
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	for _, env := range []string{"SLACK_TOKEN", "SLACK_COOKIE_D", "SLACK_COOKIE", "SLACK_WORKSPACE_URL", appinfo.EnvName("SAFE_MODE"), appinfo.LegacyEnvPrefix + "SAFE_MODE"} {
		t.Setenv(env, "")
	}
	h := &harness{t: t, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, responses: map[string]map[string]any{}}
	h.rt = &clictx.Context{
		Store:  &auth.Store{Path: filepath.Join(t.TempDir(), "credentials.json")},
		Stdout: h.stdout,
		Stderr: h.stderr,
		ClientOptions: []slackapi.Option{slackapi.WithHandler(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.calls = append(h.calls, call{method, params})
			return h.responses[method], nil
		})},
	}
	err := h.rt.Store.Upsert(auth.Workspace{URL: "https://acme.slack.com", Name: "Acme", Auth: slackapi.Auth{
		Type: slackapi.AuthBrowser, XoxcToken: "xoxc-acme", XoxdCookie: "xoxd-acme",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) run(args ...string) error {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	h.calls = nil
	cmd := New(h.rt)
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.ExecuteContext(context.Background())
}

// output runs the command and returns the JSON it printed.
func (h *harness) output(args ...string) map[string]any {
	h.t.Helper()
	if err := h.run(args...); err != nil {
		h.t.Fatalf("workflow %s: %v", strings.Join(args, " "), err)
	}
	var out map[string]any
	if err := json.Unmarshal(h.stdout.Bytes(), &out); err != nil {
		h.t.Fatalf("workflow %s printed invalid JSON: %v\n%s", strings.Join(args, " "), err, h.stdout)
	}
	return out
}

// fails runs the command and checks that it fails with an error
// containing substr without having printed a result.
func (h *harness) fails(substr string, args ...string) {
	h.t.Helper()
	err := h.run(args...)
	if err == nil || !strings.Contains(err.Error(), substr) {
		h.t.Errorf("workflow %s: error = %v, want it to contain %q", strings.Join(args, " "), err, substr)
	}
	if h.stdout.Len() != 0 {
		h.t.Errorf("workflow %s printed %q", strings.Join(args, " "), h.stdout)
	}
}

func (h *harness) methods() []string {
	out := make([]string, len(h.calls))
	for i, c := range h.calls {
		out[i] = c.method
	}
	return out
}

func (h *harness) wantMethods(want ...string) {
	h.t.Helper()
	if got := h.methods(); !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
		h.t.Errorf("API calls = %v, want %v", got, want)
	}
}

// find returns the parameters of the single call to method.
func (h *harness) find(method string) map[string]any {
	h.t.Helper()
	var found []map[string]any
	for _, c := range h.calls {
		if c.method == method {
			found = append(found, c.params)
		}
	}
	if len(found) != 1 {
		h.t.Fatalf("%d calls to %s, want 1 (calls: %v)", len(found), method, h.methods())
	}
	return found[0]
}

func wantJSON(t *testing.T, got, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("output = %v, want %v", got, want)
	}
}

func bookmarks() map[string]any {
	return map[string]any{"ok": true, "bookmarks": []any{
		map[string]any{"title": "Docs", "link": "https://example.com/docs"},
		map[string]any{"title": "Request help", "link": shortcutURL, "shortcut_id": "Ft0123ABCDEF", "app_id": "A0123ABCD"},
	}}
}

func preview() map[string]any {
	return map[string]any{"ok": true, "triggers": []any{map[string]any{
		"id":           "Ft0123ABCDEF",
		"type":         "shortcut",
		"name":         "Request help",
		"description":  "Ask the helpdesk",
		"shortcut_url": shortcutURL,
		"workflow": map[string]any{
			"workflow_id": "Wf0123ABCDEF",
			"title":       "Helpdesk request",
			"app_id":      "A0123ABCD",
			"app":         map[string]any{"name": "Helpdesk"},
		},
	}}}
}

func TestListPrintsBookmarkedAndFeaturedWorkflows(t *testing.T) {
	h := newHarness(t)
	h.responses["bookmarks.list"] = bookmarks()
	h.responses["workflows.featured.list"] = map[string]any{"ok": true, "featured_workflows": []any{map[string]any{
		"channel_id": "C12345678",
		"triggers": []any{
			map[string]any{"id": "Ft0123ABCDEF", "title": "Request help"},
			map[string]any{"id": "Ft0999ZZZZZZ", "title": "Report an outage"},
		},
	}}}

	out := h.output("list", "C12345678")

	wantJSON(t, out, map[string]any{
		"channel_id": "C12345678",
		"workflows": []any{
			map[string]any{"title": "Request help", "trigger_id": "Ft0123ABCDEF", "link": shortcutURL, "app_id": "A0123ABCD", "featured": true},
			map[string]any{"title": "Report an outage", "trigger_id": "Ft0999ZZZZZZ", "featured": true},
		},
	})
	if got := h.find("bookmarks.list"); !reflect.DeepEqual(got, map[string]any{"channel_id": "C12345678"}) {
		t.Errorf("bookmarks.list params = %v", got)
	}
	h.find("workflows.featured.list")
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q", h.stderr)
	}
}

// The TypeScript version silently listed fewer workflows when the
// featured lookup failed.
func TestListWarnsWhenFeaturedWorkflowsAreUnavailable(t *testing.T) {
	h := newHarness(t)
	h.responses["bookmarks.list"] = bookmarks()
	h.responses["workflows.featured.list"] = map[string]any{"ok": false, "error": "unknown_method"}

	out := h.output("list", "C12345678")

	wantJSON(t, out, map[string]any{
		"channel_id": "C12345678",
		"workflows": []any{
			map[string]any{"title": "Request help", "trigger_id": "Ft0123ABCDEF", "link": shortcutURL, "app_id": "A0123ABCD", "featured": false},
		},
	})
	if got := h.stderr.String(); !strings.Contains(got, "unknown_method") || !strings.Contains(got, "only bookmarked workflows") {
		t.Errorf("stderr = %q, want a warning naming the failure", got)
	}
}

func TestListFailsWhenBookmarksCannotBeListed(t *testing.T) {
	h := newHarness(t)
	h.responses["bookmarks.list"] = map[string]any{"ok": false, "error": "channel_not_found"}
	err := h.run("list", "C12345678")
	if !slackapi.IsErrorCode(err, "channel_not_found") {
		t.Errorf("error = %v, want channel_not_found", err)
	}
	if h.stdout.Len() != 0 {
		t.Errorf("printed %q despite the failure", h.stdout)
	}
}

func TestPreviewAcceptsATriggerIDOrShortcutURL(t *testing.T) {
	for _, arg := range []string{"Ft0123ABCDEF", shortcutURL} {
		h := newHarness(t)
		h.responses["workflows.triggers.preview"] = preview()

		out := h.output("preview", arg)

		h.wantMethods("workflows.triggers.preview")
		if got := h.find("workflows.triggers.preview"); !reflect.DeepEqual(got, map[string]any{"trigger_ids": "Ft0123ABCDEF"}) {
			t.Errorf("%s: params = %v", arg, got)
		}
		wantJSON(t, out, map[string]any{
			"trigger_id":   "Ft0123ABCDEF",
			"type":         "shortcut",
			"name":         "Request help",
			"description":  "Ask the helpdesk",
			"shortcut_url": shortcutURL,
			"workflow":     map[string]any{"id": "Wf0123ABCDEF", "title": "Helpdesk request", "app_id": "A0123ABCD", "app_name": "Helpdesk"},
		})
	}
}

func TestGetResolvesATriggerToItsWorkflow(t *testing.T) {
	h := newHarness(t)
	h.responses["workflows.triggers.preview"] = preview()
	h.responses["workflows.get"] = map[string]any{"ok": true, "workflow": map[string]any{
		"id": "Wf0123ABCDEF", "title": "Helpdesk request", "description": "Files a ticket",
	}}

	out := h.output("get", "Ft0123ABCDEF")

	h.wantMethods("workflows.triggers.preview", "workflows.get")
	if got := h.find("workflows.get"); !reflect.DeepEqual(got, map[string]any{"workflow_id": "Wf0123ABCDEF"}) {
		t.Errorf("workflows.get params = %v", got)
	}
	wantJSON(t, out, map[string]any{"workflow_id": "Wf0123ABCDEF", "title": "Helpdesk request", "description": "Files a ticket"})

	h.output("get", "Wf0123ABCDEF")
	h.wantMethods("workflows.get")
}

func tripped() map[string]any {
	return map[string]any{"ok": true, "function_execution_id": "Fx1", "trigger_execution_id": "Tx1", "is_slow_workflow": false}
}

func (h *harness) wantTrip(url string) {
	h.t.Helper()
	trip := h.find("workflows.triggers.trip")
	if trip["url"] != url {
		h.t.Errorf("trip url = %v, want %s", trip["url"], url)
	}
	want := map[string]any{"location": "bookmark", "channel_id": "C12345678", "trigger_id": "Ft0123ABCDEF"}
	if !reflect.DeepEqual(trip["context"], want) {
		h.t.Errorf("trip context = %v, want %v", trip["context"], want)
	}
}

func TestRunLooksUpTheShortcutURLOfATriggerID(t *testing.T) {
	h := newHarness(t)
	h.responses["bookmarks.list"] = bookmarks()
	h.responses["workflows.triggers.trip"] = tripped()

	out := h.output("run", "Ft0123ABCDEF", "--channel", "C12345678")

	h.wantMethods("bookmarks.list", "workflows.triggers.trip")
	h.wantTrip(shortcutURL)
	wantJSON(t, out, map[string]any{"function_execution_id": "Fx1", "trigger_execution_id": "Tx1", "is_slow_workflow": false})
}

func TestRunUsesAShortcutURLAsGiven(t *testing.T) {
	h := newHarness(t)
	h.responses["workflows.triggers.trip"] = tripped()

	h.output("run", shortcutURL, "--channel", "C12345678")

	h.wantMethods("workflows.triggers.trip")
	h.wantTrip(shortcutURL)
}

func TestRunDoesNotTripAnUnknownTrigger(t *testing.T) {
	h := newHarness(t)
	h.responses["bookmarks.list"] = map[string]any{"ok": true, "bookmarks": []any{}}
	h.responses["workflows.triggers.preview"] = map[string]any{"ok": true, "triggers": []any{}, "rejected_triggers": []any{"Ft0123ABCDEF"}}

	h.fails("rejected by Slack", "run", "Ft0123ABCDEF", "--channel", "C12345678")
	h.wantMethods("bookmarks.list", "workflows.triggers.preview")
}

func TestRunSurfacesSlackErrors(t *testing.T) {
	h := newHarness(t)
	h.responses["workflows.triggers.trip"] = map[string]any{"ok": false, "error": "trigger_not_found"}
	err := h.run("run", shortcutURL, "--channel", "C12345678")
	if !slackapi.IsErrorCode(err, "trigger_not_found") {
		t.Errorf("error = %v, want trigger_not_found", err)
	}
	if h.stdout.Len() != 0 {
		t.Errorf("printed %q despite the failure", h.stdout)
	}
}

func TestRunIsBlockedInSafeMode(t *testing.T) {
	h := newHarness(t)
	h.rt.SafeModeFlag = true
	h.fails(`"workflow run" is blocked`, "run", shortcutURL, "--channel", "C12345678")
	h.wantMethods()

	h.rt.SafeModeFlag = false
	t.Setenv(appinfo.EnvName("SAFE_MODE"), "1")
	h.fails("safe mode is active", "run", "Ft0123ABCDEF", "--channel", "C12345678")
	h.wantMethods()

	// Read-only commands still work.
	h.responses["workflows.triggers.preview"] = preview()
	h.output("preview", "Ft0123ABCDEF")
}

func TestBadArgumentsAreRejectedBeforeCallingSlack(t *testing.T) {
	h := newHarness(t)
	for _, tt := range []struct {
		want string
		args []string
	}{
		{`required flag(s) "channel" not set`, []string{"run", "Ft0123ABCDEF"}},
		{"the channel is empty", []string{"run", "Ft0123ABCDEF", "--channel", " "}},
		{"invalid workflow trigger", []string{"run", "Wf0123ABCDEF", "--channel", "C12345678"}},
		{"invalid workflow trigger", []string{"run", "https://evil.example/shortcuts/Ft0123ABCDEF/x", "--channel", "C12345678"}},
		{"invalid workflow trigger", []string{"preview", "nope"}},
		{"the workflow ID is empty", []string{"get", " "}},
		{"the channel is empty", []string{"list", "#"}},
		{`"U12345678" is not a channel`, []string{"list", "U12345678"}},
		{`--channel: "U12345678" is not a channel`, []string{"run", "Ft0123ABCDEF", "--channel", "U12345678"}},
		{"accepts 1 arg(s)", []string{"list"}},
		{"accepts 1 arg(s)", []string{"run", "--channel", "C12345678"}},
	} {
		h.fails(tt.want, tt.args...)
		h.wantMethods()
	}
}

func TestHelpContracts(t *testing.T) {
	root := New(newHarness(t).rt)
	commands := map[string]bool{}
	for _, c := range root.Commands() {
		commands[c.Name()] = true
		if f := c.Flags().Lookup("workspace"); f == nil || f.Usage != clictx.WorkspaceFlagUsage {
			t.Errorf("workflow %s: missing the standard --workspace flag", c.Name())
		}
		if c.Name() != "run" {
			continue
		}
		for _, s := range []string{"write action", "not idempotent", "post messages", "cannot be undone", "safe mode"} {
			if !strings.Contains(c.Long, s) {
				t.Errorf("workflow run help does not say %q", s)
			}
		}
		if !strings.Contains(c.Short, "WRITE ACTION") {
			t.Errorf("workflow run summary = %q, want it to flag the write action", c.Short)
		}
		if f := c.Flags().Lookup("channel"); f == nil || !strings.Contains(f.Usage, "required") {
			t.Error("workflow run: --channel must be documented as required")
		}
	}
	for _, name := range []string{"list", "preview", "get", "run"} {
		if !commands[name] {
			t.Errorf("missing command: workflow %s", name)
		}
	}
}
