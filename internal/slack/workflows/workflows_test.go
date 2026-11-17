package workflows

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/maelvls/slackcli/internal/output"
	"github.com/maelvls/slackcli/internal/slackapi"
)

type call struct {
	method string
	params map[string]any
}

type fake struct {
	mu       sync.Mutex
	calls    []call
	fixtures map[string]any // map[string]any response or error
}

func (f *fake) client() *slackapi.Client {
	return slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, call{method, params})
		switch fx := f.fixtures[method].(type) {
		case error:
			return nil, fx
		case map[string]any:
			return fx, nil
		}
		return nil, nil
	})
}

func (f *fake) callsTo(method string) []call {
	var out []call
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func apiError(code string) map[string]any { return map[string]any{"ok": false, "error": code} }

var bookmarks = map[string]any{"ok": true, "bookmarks": []any{
	map[string]any{"title": "Request access", "shortcut_id": "Ft0AAA", "link": "https://slack.com/shortcuts/Ft0AAA/hash1", "app_id": "A1"},
	map[string]any{"title": "Link only", "link": "https://acme.slack.com/shortcuts/Ft0BBB/hash2"},
	map[string]any{"title": "Docs", "link": "https://example.com/docs"},
	// Not Slack: a substring match on "slack.com/shortcuts/" used to
	// list this as a workflow.
	map[string]any{"title": "Phish", "link": "https://evil.example/slack.com/shortcuts/Ft0EEE/x"},
}}

func TestParseTrigger(t *testing.T) {
	ok := map[string]Trigger{
		"Ft0123ABC":   {ID: "Ft0123ABC"},
		" Ft0123ABC ": {ID: "Ft0123ABC"},
		"https://slack.com/shortcuts/Ft0123ABC/0a1b2c": {ID: "Ft0123ABC", URL: "https://slack.com/shortcuts/Ft0123ABC/0a1b2c"},
		"https://acme.slack.com/shortcuts/Ft0123ABC":   {ID: "Ft0123ABC", URL: "https://acme.slack.com/shortcuts/Ft0123ABC"},
	}
	for in, want := range ok {
		if got, err := ParseTrigger(in); err != nil || got != want {
			t.Errorf("ParseTrigger(%q) = %+v, %v, want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "Wf0123ABC", "Ft", "Ft01-23", "ft0123abc",
		"https://example.com/shortcuts/Ft0123ABC/x",
		"https://notslack.com/shortcuts/Ft0123ABC/x",
		"https://slack.com/archives/C0123ABC",
		"https://slack.com/shortcuts/Wf0123/x",
	} {
		if got, err := ParseTrigger(in); err == nil || !strings.Contains(err.Error(), "Ft…") {
			t.Errorf("ParseTrigger(%q) = %+v, %v, want an error", in, got, err)
		}
	}
}

func TestListMergesBookmarkedAndFeatured(t *testing.T) {
	f := &fake{fixtures: map[string]any{
		"bookmarks.list": bookmarks,
		"workflows.featured.list": map[string]any{"ok": true, "featured_workflows": []any{
			map[string]any{"channel_id": "C0OTHER", "triggers": []any{map[string]any{"id": "Ft0ZZZ", "title": "Elsewhere"}}},
			map[string]any{"channel_id": "C1", "triggers": []any{
				map[string]any{"id": "Ft0AAA", "title": "Request access"},
				map[string]any{"id": "Ft0CCC", "title": "Featured only"},
				map[string]any{"title": "No id"},
			}},
		}},
	}}
	got, err := List(context.Background(), f.client(), "C1")
	if err != nil {
		t.Fatal(err)
	}
	want := ChannelWorkflows{ChannelID: "C1", Workflows: []Workflow{
		{Title: "Request access", TriggerID: "Ft0AAA", Link: "https://slack.com/shortcuts/Ft0AAA/hash1", AppID: "A1", Featured: true},
		{Title: "Link only", TriggerID: "Ft0BBB", Link: "https://acme.slack.com/shortcuts/Ft0BBB/hash2"},
		{Title: "Featured only", TriggerID: "Ft0CCC", Featured: true},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List =\n%+v, want\n%+v", got, want)
	}
	if p := f.callsTo("workflows.featured.list")[0].params; !reflect.DeepEqual(p["channel_ids"], []string{"C1"}) {
		t.Errorf("featured params = %v", p)
	}

	// featured:false must survive output pruning.
	b, err := output.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"featured": false`) || strings.Contains(string(b), "FeaturedErr") {
		t.Errorf("printed output:\n%s", b)
	}
}

func TestListFeaturedFailures(t *testing.T) {
	t.Run("method unavailable is tolerated and reported", func(t *testing.T) {
		f := &fake{fixtures: map[string]any{"bookmarks.list": bookmarks, "workflows.featured.list": apiError("unknown_method")}}
		got, err := List(context.Background(), f.client(), "C1")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Workflows) != 2 || !slackapi.IsErrorCode(got.FeaturedErr, "unknown_method") {
			t.Errorf("List = %+v", got)
		}
	})
	// Both used to be swallowed, printing a plausible but incomplete list.
	t.Run("expired credentials fail", func(t *testing.T) {
		f := &fake{fixtures: map[string]any{"bookmarks.list": bookmarks, "workflows.featured.list": apiError("invalid_auth")}}
		if _, err := List(context.Background(), f.client(), "C1"); !slackapi.IsAuthError(err) {
			t.Errorf("err = %v, want an auth error", err)
		}
	})
	t.Run("transport errors fail", func(t *testing.T) {
		boom := errors.New("connection reset")
		f := &fake{fixtures: map[string]any{"bookmarks.list": bookmarks, "workflows.featured.list": boom}}
		if _, err := List(context.Background(), f.client(), "C1"); !errors.Is(err, boom) {
			t.Errorf("err = %v, want %v", err, boom)
		}
	})
	t.Run("bookmarks errors fail", func(t *testing.T) {
		f := &fake{fixtures: map[string]any{"bookmarks.list": apiError("channel_not_found")}}
		if _, err := List(context.Background(), f.client(), "C1"); !slackapi.IsErrorCode(err, "channel_not_found") {
			t.Errorf("err = %v", err)
		}
	})
}

var previewResponse = map[string]any{"ok": true, "triggers": []any{map[string]any{
	"id": "Ft0AAA", "type": "shortcut", "name": "Request access", "description": "Ask for access",
	"shortcut_url": "https://slack.com/shortcuts/Ft0AAA/hash1",
	"workflow": map[string]any{
		"workflow_id": "Wf0AAA", "title": "Access", "description": "Grants access",
		"app": map[string]any{"id": "A1", "name": "Access Bot"},
	},
	"workflow_details": map[string]any{"collaborators": []any{"U1", 7.0, "", "U2"}},
}}}

func TestPreviewTrigger(t *testing.T) {
	f := &fake{fixtures: map[string]any{"workflows.triggers.preview": previewResponse}}
	got, err := PreviewTrigger(context.Background(), f.client(), "Ft0AAA")
	if err != nil {
		t.Fatal(err)
	}
	want := Preview{
		TriggerID: "Ft0AAA", Type: "shortcut", Name: "Request access", Description: "Ask for access",
		ShortcutURL:   "https://slack.com/shortcuts/Ft0AAA/hash1",
		Workflow:      PreviewWorkflow{ID: "Wf0AAA", Title: "Access", Description: "Grants access", AppID: "A1", AppName: "Access Bot"},
		Collaborators: []string{"U1", "U2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PreviewTrigger =\n%+v, want\n%+v", got, want)
	}
	if p := f.calls[0].params; p["trigger_ids"] != "Ft0AAA" {
		t.Errorf("params = %v", p)
	}
}

func TestPreviewTriggerFailures(t *testing.T) {
	f := &fake{fixtures: map[string]any{"workflows.triggers.preview": map[string]any{"ok": true, "rejected_triggers": []any{"Ft0AAA"}}}}
	if _, err := PreviewTrigger(context.Background(), f.client(), "Ft0AAA"); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("err = %v", err)
	}
	f = &fake{}
	if _, err := PreviewTrigger(context.Background(), f.client(), "Ft0AAA"); err == nil || !strings.Contains(err.Error(), "no preview data") {
		t.Errorf("err = %v", err)
	}
}

func TestResolveShortcutURL(t *testing.T) {
	f := &fake{fixtures: map[string]any{"bookmarks.list": bookmarks, "workflows.triggers.preview": previewResponse}}
	ctx := context.Background()

	if got, err := ResolveShortcutURL(ctx, f.client(), "C1", "Ft0AAA"); err != nil || got != "https://slack.com/shortcuts/Ft0AAA/hash1" {
		t.Errorf("by shortcut_id = %q, %v", got, err)
	}
	// List reports this bookmark as a workflow, so run must find it too.
	if got, err := ResolveShortcutURL(ctx, f.client(), "C1", "Ft0BBB"); err != nil || got != "https://acme.slack.com/shortcuts/Ft0BBB/hash2" {
		t.Errorf("by link = %q, %v", got, err)
	}
	if n := len(f.callsTo("workflows.triggers.preview")); n != 0 {
		t.Errorf("preview calls = %d, want 0 for bookmarked triggers", n)
	}
	// Featured-only workflows have no bookmark: fall back to the preview.
	if got, err := ResolveShortcutURL(ctx, f.client(), "C1", "Ft0CCC"); err != nil || got != "https://slack.com/shortcuts/Ft0AAA/hash1" {
		t.Errorf("via preview = %q, %v", got, err)
	}

	f = &fake{fixtures: map[string]any{"bookmarks.list": bookmarks, "workflows.triggers.preview": map[string]any{"ok": true, "triggers": []any{map[string]any{"id": "Ft0CCC"}}}}}
	if _, err := ResolveShortcutURL(ctx, f.client(), "C1", "Ft0CCC"); err == nil || !strings.Contains(err.Error(), "pass the full shortcut URL") {
		t.Errorf("err = %v", err)
	}
	f = &fake{fixtures: map[string]any{"bookmarks.list": apiError("channel_not_found")}}
	if _, err := ResolveShortcutURL(ctx, f.client(), "C1", "Ft0AAA"); !slackapi.IsErrorCode(err, "channel_not_found") {
		t.Errorf("err = %v", err)
	}
}

func TestRun(t *testing.T) {
	f := &fake{fixtures: map[string]any{"workflows.triggers.trip": map[string]any{
		"ok": true, "function_execution_id": "Fx1", "trigger_execution_id": "Tx1", "is_slow_workflow": true,
	}}}
	in := RunInput{ShortcutURL: "https://slack.com/shortcuts/Ft0AAA/hash1", ChannelID: "C1", TriggerID: "Ft0AAA"}
	got, err := Run(context.Background(), f.client(), in)
	if err != nil {
		t.Fatal(err)
	}
	if want := (RunResult{FunctionExecutionID: "Fx1", TriggerExecutionID: "Tx1", IsSlowWorkflow: true}); got != want {
		t.Errorf("Run = %+v, want %+v", got, want)
	}
	p := f.calls[0].params
	wantCtx := map[string]any{"location": "bookmark", "channel_id": "C1", "trigger_id": "Ft0AAA"}
	if p["url"] != in.ShortcutURL || p["run_precheck"] != true || !reflect.DeepEqual(p["context"], wantCtx) {
		t.Errorf("params = %v", p)
	}
	first := p["client_token"].(string)
	if _, err := Run(context.Background(), f.client(), in); err != nil {
		t.Fatal(err)
	}
	if second := f.calls[1].params["client_token"].(string); !strings.HasPrefix(first, "cli-") || first == second {
		t.Errorf("client tokens %q and %q must be distinct", first, second)
	}

	if _, err := Run(context.Background(), f.client(), RunInput{ChannelID: "C1", TriggerID: "Ft0AAA"}); err == nil {
		t.Error("want an error without a shortcut URL")
	}
	f = &fake{fixtures: map[string]any{"workflows.triggers.trip": apiError("trigger_not_found")}}
	if _, err := Run(context.Background(), f.client(), in); !slackapi.IsErrorCode(err, "trigger_not_found") {
		t.Errorf("err = %v", err)
	}
}

func form(title string, names ...string) map[string]any {
	var elements []any
	for _, n := range names {
		elements = append(elements, map[string]any{"name": n, "title": strings.ToUpper(n), "type": "string"})
	}
	return map[string]any{
		"function": map[string]any{"callback_id": "open_form", "title": "Collect info in a form"},
		"inputs": map[string]any{
			"title":  map[string]any{"value": title},
			"fields": map[string]any{"value": map[string]any{"elements": elements, "required": []any{names[0]}}},
		},
	}
}

func TestGetSchema(t *testing.T) {
	first := form("Request", "reason", "details")
	elements := first["inputs"].(map[string]any)["fields"].(map[string]any)["value"].(map[string]any)["elements"].([]any)
	elements[1] = map[string]any{"name": "details", "title": "Details", "description": "Anything else", "long": true}
	f := &fake{fixtures: map[string]any{
		"workflows.triggers.preview": previewResponse,
		"workflows.get": map[string]any{"ok": true, "workflow": map[string]any{
			"id": "Wf0AAA", "title": "Access", "description": "Grants access",
			"steps": []any{
				first,
				map[string]any{"function": map[string]any{"callback_id": "send_message"}},
				form("Follow-up", "rating"),
			},
		}},
	}}
	want := Schema{
		WorkflowID: "Wf0AAA", Title: "Access", Description: "Grants access",
		// The first form is the one shown when starting the workflow; a
		// later form used to overwrite it.
		FormTitle: "Request",
		Fields: []FormField{
			{Name: "reason", Title: "REASON", Type: "string", Required: true},
			{Name: "details", Title: "Details", Type: "string", Description: "Anything else", Long: true},
		},
		Steps: []string{"Collect info in a form", "send_message", "Collect info in a form"},
	}

	for _, id := range []string{"Wf0AAA", "Ft0AAA", "https://slack.com/shortcuts/Ft0AAA/hash1"} {
		f.calls = nil
		got, err := GetSchema(context.Background(), f.client(), id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("GetSchema(%q) =\n%+v, want\n%+v", id, got, want)
		}
		if p := f.callsTo("workflows.get")[0].params; p["workflow_id"] != "Wf0AAA" {
			t.Errorf("GetSchema(%q): workflows.get params = %v", id, p)
		}
		if previews := len(f.callsTo("workflows.triggers.preview")); (previews == 1) != (id != "Wf0AAA") {
			t.Errorf("GetSchema(%q): %d preview calls", id, previews)
		}
	}
}

func TestGetSchemaFailures(t *testing.T) {
	ctx := context.Background()
	f := &fake{}
	if _, err := GetSchema(ctx, f.client(), "Wf0AAA"); err == nil || !strings.Contains(err.Error(), "no workflow found") {
		t.Errorf("err = %v", err)
	}
	if _, err := GetSchema(ctx, f.client(), " "); err == nil || len(f.calls) != 1 {
		t.Errorf("blank ID: err = %v, calls = %d", err, len(f.calls))
	}
	// A preview without a workflow ID used to call workflows.get with "".
	f = &fake{fixtures: map[string]any{"workflows.triggers.preview": map[string]any{"ok": true, "triggers": []any{map[string]any{"id": "Ft0AAA"}}}}}
	if _, err := GetSchema(ctx, f.client(), "Ft0AAA"); err == nil || !strings.Contains(err.Error(), "pass the workflow ID") {
		t.Errorf("err = %v", err)
	}
	if n := len(f.callsTo("workflows.get")); n != 0 {
		t.Errorf("workflows.get calls = %d, want 0", n)
	}
	f = &fake{fixtures: map[string]any{"workflows.get": apiError("workflow_not_found")}}
	if _, err := GetSchema(ctx, f.client(), "Wf0AAA"); !slackapi.IsErrorCode(err, "workflow_not_found") {
		t.Errorf("err = %v", err)
	}
}
