package messagecmd

import (
	"reflect"
	"strings"
	"testing"
)

func TestEdit(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantText string
	}{
		{"plain text", []string{testChannel, "hello", "--workspace", testWorkspace, "--ts", testTS}, "hello"},
		{"literal angle brackets are escaped", []string{testChannel, "please use <fix>", "--ts", testTS}, "please use &lt;fix&gt;"},
		{"message URL with a link label", []string{testURL, "Visit <https://example.com|Example>"}, "Visit <https://example.com|Example>"},
		{"mailto link", []string{testChannel, "Email <mailto:bob@example.com|Bob>", "--ts", testTS}, "Email <mailto:bob@example.com|Bob>"},
		{"usergroup mention", []string{testChannel, "Ping <!subteam^S12345678|@team>", "--ts", testTS}, "Ping <!subteam^S12345678|@team>"},
		{"inline formatting needs no blocks", []string{testChannel, "Update *now*", "--ts", testTS}, "Update *now*"},
		{"channel name", []string{"#general", "hello", "--ts", testTS}, "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			got := h.mustRun(append([]string{"edit"}, tt.args...)...)
			wantJSON(t, got, `{"ok": true}`)
			calls := h.callsTo("chat.update")
			want := map[string]any{"channel": testChannel, "ts": testTS, "text": tt.wantText}
			if len(calls) != 1 || !reflect.DeepEqual(calls[0].params, want) {
				t.Errorf("chat.update calls = %v\nwant params %#v", calls, want)
			}
		})
	}
}

func TestEditListBecomesRichText(t *testing.T) {
	h := newHarness(t)
	h.mustRun("edit", testChannel, "- Post in <#C87654321|general>\n- Update *now*", "--ts", testTS)
	want := `{"blocks": [{
		"type": "rich_text",
		"elements": [{
			"type": "rich_text_list",
			"style": "bullet",
			"elements": [
				{"type": "rich_text_section", "elements": [
					{"type": "text", "text": "Post in "},
					{"type": "channel", "channel_id": "C87654321"}
				]},
				{"type": "rich_text_section", "elements": [
					{"type": "text", "text": "Update "},
					{"type": "text", "text": "now", "style": {"bold": true}}
				]}
			]
		}]
	}]}`
	calls := h.callsTo("chat.update")
	if len(calls) != 1 {
		t.Fatalf("calls = %v", h.methods())
	}
	wantJSON(t, map[string]any{"blocks": jsonRoundTrip(t, calls[0].params["blocks"])}, want)
}

func TestEditWithBlocks(t *testing.T) {
	path, blocks := writeBlocks(t, []any{
		map[string]any{"type": "header", "text": map[string]any{"type": "plain_text", "text": "Edited"}},
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "Updated body"}},
	})

	h := newHarness(t)
	h.mustRun("edit", testURL, "fallback text", "--blocks", path)
	h.wantCalls(call{"chat.update", map[string]any{"channel": testChannel, "ts": testTS, "text": "fallback text", "blocks": blocks}})

	h.mustRun("edit", testChannel, "fallback text", "--workspace", testWorkspace, "--ts", testTS, "--blocks", path)
	h.wantCalls(call{"chat.update", map[string]any{"channel": testChannel, "ts": testTS, "text": "fallback text", "blocks": blocks}})

	// Blocks alone, from stdin.
	h.stdin = `[{"type": "divider"}]`
	h.mustRun("edit", testURL, "", "--blocks", "-")
	h.wantCalls(call{"chat.update", map[string]any{"channel": testChannel, "ts": testTS, "text": "", "blocks": []any{map[string]any{"type": "divider"}}}})
}

func TestEditValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"channel without ts", []string{testChannel, "hello"}, "--ts is required"},
		{"garbage ts", []string{testChannel, "hello", "--ts", "1"}, "invalid --ts"},
		{"empty text", []string{testURL, ""}, "<text> is empty"},
		{"missing text", []string{testURL}, "accepts 2 arg"},
		{"user target", []string{"U05BRPTKL6A", "hello"}, "does not support user ID targets"},
		{"bad blocks", []string{testURL, "x", "--blocks", writeFile(t, "b.json", `{}`)}, "expected a JSON array"},
		{"empty blocks path", []string{testURL, "x", "--blocks", ""}, "--blocks needs a file path"},
		{"unknown flag", []string{testURL, "x", "--thread-ts", testTS}, "unknown flag: --thread-ts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.mustFail(tt.want, append([]string{"edit"}, tt.args...)...)
			h.wantCalls()
		})
	}
}

func TestEditAndDeleteExplainRefusals(t *testing.T) {
	tests := []struct {
		args         []string
		method, code string
		want         string
	}{
		{[]string{"edit", testURL, "hi"}, "chat.update", "cant_update_message", "you can only edit your own messages"},
		{[]string{"edit", testURL, "hi"}, "chat.update", "edit_window_closed", "can no longer be edited"},
		{[]string{"edit", testURL, "hi"}, "chat.update", "message_not_found", "no message with ts " + testTS + " in " + testChannel},
		{[]string{"edit", testURL, "hi"}, "chat.update", "fatal_error", "edit of message " + testTS},
		{[]string{"delete", testURL}, "chat.delete", "cant_delete_message", "you can only delete your own messages"},
		{[]string{"delete", testURL}, "chat.delete", "message_not_found", "it may already be deleted"},
	}
	for _, tt := range tests {
		h := newHarness(t)
		h.fixtures[tt.method] = func(map[string]any) (map[string]any, error) { return nil, apiError(tt.method, tt.code) }
		err := h.mustFail(tt.want, tt.args...)
		// The Slack code stays visible and testable.
		if !strings.Contains(err.Error(), tt.code) {
			t.Errorf("%v: error %q does not name the Slack code %s", tt.args, err, tt.code)
		}
	}
}

func TestDelete(t *testing.T) {
	h := newHarness(t)
	wantJSON(t, h.mustRun("delete", testURL), `{"ok": true}`)
	h.wantCalls(call{"chat.delete", map[string]any{"channel": testChannel, "ts": testTS}})

	wantJSON(t, h.mustRun("delete", "#general", "--ts", testTS), `{"ok": true}`)
	if got := h.callsTo("chat.delete"); len(got) != 1 || !reflect.DeepEqual(got[0].params, map[string]any{"channel": testChannel, "ts": testTS}) {
		t.Errorf("chat.delete calls = %v", got)
	}

	h.mustFail("--ts is required", "delete", testChannel)
	h.mustFail("invalid --ts", "delete", testChannel, "--ts", "nope")
	h.mustFail("does not support user ID targets", "delete", "U05BRPTKL6A")
	h.wantCalls()
}

func TestReact(t *testing.T) {
	for _, tt := range []struct {
		args       []string
		method     string
		wantParams map[string]any
	}{
		{[]string{"react", "add", testURL, ":rocket:"}, "reactions.add", map[string]any{"channel": testChannel, "timestamp": testTS, "name": "rocket"}},
		{[]string{"react", "add", testURL, "🚀"}, "reactions.add", map[string]any{"channel": testChannel, "timestamp": testTS, "name": "rocket"}},
		{[]string{"react", "add", testChannel, "+1", "--ts", testTS}, "reactions.add", map[string]any{"channel": testChannel, "timestamp": testTS, "name": "+1"}},
		{[]string{"react", "remove", testURL, "eyes"}, "reactions.remove", map[string]any{"channel": testChannel, "timestamp": testTS, "name": "eyes"}},
		{[]string{"react", "remove", "#general", ":eyes:", "--ts", testTS}, "reactions.remove", map[string]any{"channel": testChannel, "timestamp": testTS, "name": "eyes"}},
	} {
		h := newHarness(t)
		wantJSON(t, h.mustRun(tt.args...), `{"ok": true}`)
		calls := h.callsTo(tt.method)
		if len(calls) != 1 || !reflect.DeepEqual(calls[0].params, tt.wantParams) {
			t.Errorf("%v: %s calls = %v", tt.args, tt.method, calls)
		}
	}
}

func TestReactValidation(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"react", "add", testChannel, "eyes"}, "--ts is required"},
		{[]string{"react", "add", testChannel, "eyes", "--ts", "x"}, "invalid --ts"},
		{[]string{"react", "add", "U05BRPTKL6A", "eyes"}, "does not support user ID targets"},
		{[]string{"react", "add", testURL, "not an emoji"}, "unsupported emoji"},
		{[]string{"react", "add", testURL, ""}, "emoji is empty"},
		{[]string{"react", "remove", testURL}, "accepts 2 arg"},
	} {
		h := newHarness(t)
		h.mustFail(tt.want, tt.args...)
		h.wantCalls()
	}
}

func TestReactExplainsSlackErrors(t *testing.T) {
	h := newHarness(t)
	h.fixtures["reactions.add"] = func(map[string]any) (map[string]any, error) { return nil, apiError("reactions.add", "already_reacted") }
	h.fixtures["reactions.remove"] = func(map[string]any) (map[string]any, error) { return nil, apiError("reactions.remove", "no_reaction") }
	h.mustFail("you already reacted with :eyes:", "react", "add", testURL, "eyes")
	h.mustFail("you have no :eyes: reaction", "react", "remove", testURL, "eyes")
}

// Message text starting with a dash is how lists are written; the flag
// parser must not take it for a flag.
func TestTextStartingWithDash(t *testing.T) {
	h := newHarness(t)
	const list = "- one\n- two"

	h.mustRun("send", testChannel, list, "--thread-ts", "1770160000.000001")
	p := h.callsTo("chat.postMessage")[0].params
	if p["text"] != list || p["thread_ts"] != "1770160000.000001" || p["blocks"] == nil {
		t.Errorf("send params = %#v", p)
	}

	h.mustRun("send", "--no-unfurl", testChannel, "-> see <https://example.com|x=y>")
	p = h.callsTo("chat.postMessage")[0].params
	if p["text"] != "-&gt; see <https://example.com|x=y>" || p["unfurl_links"] != false {
		t.Errorf("send params = %#v", p)
	}

	h.mustRun("edit", testURL, list)
	if p := h.callsTo("chat.update")[0].params; p["text"] != list {
		t.Errorf("edit params = %#v", p)
	}

	// "--" still works, and real flag mistakes are still reported.
	h.mustRun("send", "--", testChannel, "--not-a-flag")
	if p := h.callsTo("chat.postMessage")[0].params; p["text"] != "--not-a-flag" {
		t.Errorf("send params = %#v", p)
	}
	h.mustFail("unknown flag: --not-a-flag", "send", testChannel, "--not-a-flag")
	h.mustFail("unknown shorthand flag", "send", testChannel, "-x")
	h.mustFail("flag needs an argument: --thread-ts", "send", testChannel, "hi", "--thread-ts")
}
