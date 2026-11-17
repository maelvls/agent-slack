package messagecmd

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSendPostsMessage(t *testing.T) {
	h := newHarness(t)
	got := h.mustRun("send", testChannel, "hello", "--workspace", testWorkspace)
	h.wantCalls(call{"chat.postMessage", map[string]any{"channel": testChannel, "text": "hello"}})
	wantJSON(t, got, `{
		"ok": true,
		"channel_id": "C12345678",
		"ts": "1770165109.628379",
		"permalink": "https://workspace.slack.com/archives/C12345678/p1770165109628379"
	}`)
}

func TestSendPermalinkWithImplicitWorkspace(t *testing.T) {
	h := newHarness(t)
	got := h.mustRun("send", testChannel, "hello")
	h.wantCalls(call{"chat.postMessage", map[string]any{"channel": testChannel, "text": "hello"}})
	if got["permalink"] != testURL {
		t.Errorf("permalink = %v, want %s", got["permalink"], testURL)
	}
}

func TestSendNoUnfurl(t *testing.T) {
	h := newHarness(t)
	h.mustRun("send", testChannel, "https://example.com", "--no-unfurl")
	h.wantCalls(call{"chat.postMessage", map[string]any{
		"channel":      testChannel,
		"text":         "https://example.com",
		"unfurl_links": false,
		"unfurl_media": false,
	}})
}

func TestSendToChannelName(t *testing.T) {
	h := newHarness(t)
	h.mustRun("send", "#general", "hello")
	if got := h.methods(); !reflect.DeepEqual(got, []string{"search.messages", "chat.postMessage"}) {
		t.Fatalf("calls = %v", got)
	}
	if ch := h.callsTo("chat.postMessage")[0].params["channel"]; ch != testChannel {
		t.Errorf("channel = %v", ch)
	}
}

func TestSendToUserOpensDM(t *testing.T) {
	for _, user := range []string{"W12345678", "U05BRPTKL6A"} {
		h := newHarness(t)
		got := h.mustRun("send", user, "hello")
		h.wantCalls(
			call{"conversations.open", map[string]any{"users": user}},
			call{"chat.postMessage", map[string]any{"channel": "D12345678", "text": "hello"}},
		)
		if got["channel_id"] != "D12345678" {
			t.Errorf("channel_id = %v", got["channel_id"])
		}
	}
}

// The TypeScript dropped --thread-ts for user targets and posted at the
// top level of the DM.
func TestSendToUserInThread(t *testing.T) {
	h := newHarness(t)
	h.mustRun("send", "U05BRPTKL6A", "hello", "--thread-ts", "1770160000.000001")
	post := h.callsTo("chat.postMessage")
	if len(post) != 1 || post[0].params["thread_ts"] != "1770160000.000001" {
		t.Fatalf("chat.postMessage = %v", post)
	}
}

func TestSendThreadedReply(t *testing.T) {
	h := newHarness(t)
	got := h.mustRun("send", testChannel, "reply", "--thread-ts", "1770160000.000001")
	h.wantCalls(call{"chat.postMessage", map[string]any{
		"channel": testChannel, "text": "reply", "thread_ts": "1770160000.000001",
	}})
	wantJSON(t, got, `{
		"ok": true,
		"channel_id": "C12345678",
		"ts": "1770165109.628379",
		"thread_ts": "1770160000.000001",
		"permalink": "https://workspace.slack.com/archives/C12345678/p1770165109628379?thread_ts=1770160000.000001&cid=C12345678"
	}`)
}

func TestSendReplyBroadcast(t *testing.T) {
	h := newHarness(t)
	h.mustRun("send", testChannel, "shipping today", "--thread-ts", "1770160000.000001", "--reply-broadcast")
	h.wantCalls(call{"chat.postMessage", map[string]any{
		"channel": testChannel, "text": "shipping today", "thread_ts": "1770160000.000001", "reply_broadcast": true,
	}})
}

func TestSendReplyBroadcastRequiresThread(t *testing.T) {
	h := newHarness(t)
	h.mustFail("--reply-broadcast requires --thread-ts", "send", testChannel, "oops", "--reply-broadcast")
	h.wantCalls()
}

func TestSendReplyBroadcastOnURLTargetUsesDerivedThread(t *testing.T) {
	h := newHarness(t)
	h.fixtures["conversations.history"] = func(map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true, "messages": []any{
			map[string]any{"ts": "1770160500.000100", "thread_ts": "1770160000.000001", "text": "reply"},
		}}, nil
	}
	got := h.mustRun("send", testWorkspace+"/archives/C12345678/p1770160500000100", "broadcast", "--reply-broadcast")
	post := h.callsTo("chat.postMessage")
	if len(post) != 1 {
		t.Fatalf("calls = %v", h.methods())
	}
	want := map[string]any{"channel": testChannel, "text": "broadcast", "thread_ts": "1770160000.000001", "reply_broadcast": true}
	if !reflect.DeepEqual(post[0].params, want) {
		t.Errorf("chat.postMessage params = %#v", post[0].params)
	}
	if got["thread_ts"] != "1770160000.000001" {
		t.Errorf("thread_ts = %v", got["thread_ts"])
	}
}

// A URL pointing at a message without a thread starts one under it.
func TestSendToURLOfTopLevelMessage(t *testing.T) {
	h := newHarness(t)
	h.fixtures["conversations.history"] = func(map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true, "messages": []any{map[string]any{"ts": "1770160500.000100", "text": "root"}}}, nil
	}
	h.mustRun("send", testWorkspace+"/archives/C12345678/p1770160500000100", "hi")
	if ts := h.callsTo("chat.postMessage")[0].params["thread_ts"]; ts != "1770160500.000100" {
		t.Errorf("thread_ts = %v", ts)
	}
}

// --thread-ts may repeat the thread a message URL designates, but not
// name another one ("message compose" applies the same rule).
func TestSendToURLWithThreadTS(t *testing.T) {
	const url = testWorkspace + "/archives/C12345678/p1770160500000100"
	h := newHarness(t)
	h.fixtures["conversations.history"] = func(map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true, "messages": []any{
			map[string]any{"ts": "1770160500.000100", "thread_ts": "1770160000.000001", "text": "reply"},
		}}, nil
	}
	got := h.mustRun("send", url, "hi", "--thread-ts", "1770160000.000001")
	if ts := h.callsTo("chat.postMessage")[0].params["thread_ts"]; ts != "1770160000.000001" || got["thread_ts"] != "1770160000.000001" {
		t.Errorf("thread_ts = %v, printed %v", ts, got)
	}

	for _, other := range []string{"1770150000.000001", "1770160500.000100"} {
		h.mustFail("--thread-ts "+other+" contradicts the message URL, whose thread is 1770160000.000001", "send", url, "hi", "--thread-ts", other)
		if len(h.callsTo("chat.postMessage")) != 0 {
			t.Errorf("--thread-ts %s: posted", other)
		}
	}
}

// An upload may outlast the command timeout: "send --attach" switches the
// watchdog off before uploading, and only then.
func TestSendAttachDisablesTheCommandTimeout(t *testing.T) {
	for _, attach := range []bool{true, false} {
		h := newHarness(t)
		uploadFixtures(h)
		disabled := false
		h.rt.SetWatchdogCanceller(func() {
			disabled = true
			if n := len(h.callsTo("files.getUploadURLExternal")); n != 0 {
				t.Error("the timeout was disabled after the upload had started")
			}
		})
		args := []string{"send", testChannel, "here's the report"}
		if attach {
			args = append(args, "--attach", writeFile(t, "report.md", "# report\n"))
		}
		h.mustRun(args...)
		if disabled != attach {
			t.Errorf("--attach given: %v; command timeout disabled: %v", attach, disabled)
		}
	}
}

func TestSendToMissingMessageURLDoesNotPost(t *testing.T) {
	h := newHarness(t)
	h.mustFail("no message with ts", "send", testURL, "hi")
	if len(h.callsTo("chat.postMessage")) != 0 {
		t.Error("posted although the linked message was not found")
	}
}

func TestSendReplyBroadcastOnDMTarget(t *testing.T) {
	h := newHarness(t)
	h.mustFail("--reply-broadcast is not supported for DM targets",
		"send", "U05BRPTKL6A", "hi", "--thread-ts", "1770160000.000001", "--reply-broadcast")
	h.wantCalls()
}

func TestSendReplyBroadcastWithBlocks(t *testing.T) {
	h := newHarness(t)
	path, blocks := writeBlocks(t, []any{map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "summary"}}})
	h.mustRun("send", testChannel, "fallback", "--thread-ts", "1770160000.000001", "--reply-broadcast", "--blocks", path)
	h.wantCalls(call{"chat.postMessage", map[string]any{
		"channel":         testChannel,
		"text":            "fallback",
		"thread_ts":       "1770160000.000001",
		"blocks":          blocks,
		"reply_broadcast": true,
	}})
}

func uploadFixtures(h *harness) {
	h.fixtures["files.getUploadURLExternal"] = func(p map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true, "upload_url": "https://upload.example/file", "file_id": "F-" + p["filename"].(string)}, nil
	}
}

func TestSendAttachment(t *testing.T) {
	h := newHarness(t)
	uploadFixtures(h)
	path := writeFile(t, "report.md", "# report\n")
	got := h.mustRun("send", testChannel, "here's the report", "--attach", path)

	if got := h.methods(); !reflect.DeepEqual(got, []string{"files.getUploadURLExternal", "files.completeUploadExternal"}) {
		t.Fatalf("calls = %v", got)
	}
	complete := h.callsTo("files.completeUploadExternal")[0].params
	if complete["initial_comment"] != "here's the report" || complete["channel_id"] != testChannel {
		t.Errorf("files.completeUploadExternal params = %#v", complete)
	}
	if len(h.requests) != 1 || h.requests[0].method != http.MethodPost || h.requests[0].body != "# report\n" {
		t.Errorf("upload requests = %+v", h.requests)
	}
	wantJSON(t, got, `{
		"ok": true,
		"channel_id": "C12345678",
		"files": [{"id": "F-report.md", "name": "report.md", "size": 9}]
	}`)
}

// The TypeScript posted one message per file; upload.ToChannel shares
// them all in one message carrying the comment.
func TestSendSeveralAttachmentsInOneMessage(t *testing.T) {
	h := newHarness(t)
	uploadFixtures(h)
	first, second := writeFile(t, "report.md", "# report\n"), writeFile(t, "log.txt", "ok\n")
	got := h.mustRun("send", testChannel, "see files", "--attach", first, "--attach", second, "--attach", " "+first, "--thread-ts", "1770160000.000001")

	completes := h.callsTo("files.completeUploadExternal")
	if len(completes) != 1 {
		t.Fatalf("files.completeUploadExternal called %d times", len(completes))
	}
	if p := completes[0].params; p["initial_comment"] != "see files" || p["thread_ts"] != "1770160000.000001" {
		t.Errorf("params = %#v", p)
	}
	if len(h.requests) != 2 {
		t.Errorf("%d upload requests, want 2 (duplicates removed)", len(h.requests))
	}
	if files, _ := got["files"].([]any); len(files) != 2 {
		t.Errorf("files = %v", got["files"])
	}
	if got["thread_ts"] != "1770160000.000001" {
		t.Errorf("thread_ts = %v", got["thread_ts"])
	}
}

func TestSendAttachmentWithoutText(t *testing.T) {
	h := newHarness(t)
	uploadFixtures(h)
	h.mustRun("send", testChannel, "--attach", writeFile(t, "data.csv", "a,b\n1,2\n"))
	complete := h.callsTo("files.completeUploadExternal")
	if len(complete) != 1 {
		t.Fatalf("calls = %v", h.methods())
	}
	if c, ok := complete[0].params["initial_comment"]; ok && c != "" {
		t.Errorf("initial_comment = %v", c)
	}
	if len(h.callsTo("chat.postMessage")) != 0 {
		t.Error("chat.postMessage called")
	}
}

func TestSendAttachmentWarnsThatListsAreNotConverted(t *testing.T) {
	h := newHarness(t)
	uploadFixtures(h)
	h.mustRun("send", testChannel, "- one\n- two", "--attach", writeFile(t, "a.txt", "a"))
	if !strings.Contains(h.stderr.String(), "rich text formatting is not supported with file attachments") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

// A bad path fails before the channel is resolved or anything is uploaded.
func TestSendAttachmentChecksPathsFirst(t *testing.T) {
	h := newHarness(t)
	h.mustFail("does-not-exist.md", "send", "#general", "hi", "--attach", writeFile(t, "ok.txt", "ok"), "--attach", "/does-not-exist.md")
	h.wantCalls()
}

func TestSendFlagConflicts(t *testing.T) {
	blocks, _ := writeBlocks(t, []any{map[string]any{"type": "divider"}})
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no text", []string{testChannel}, "<text> is required"},
		{"blank text", []string{testChannel, "  "}, "<text> is required"},
		{"no-unfurl with attach", []string{testChannel, "https://example.com", "--attach", "./report.md", "--no-unfurl"}, "--no-unfurl cannot be combined with --attach"},
		{"schedule with attach", []string{testChannel, "later with file", "--attach", "./report.md", "--schedule-in", "1h"}, "cannot be combined with --attach"},
		{"blocks with attach", []string{testChannel, "x", "--attach", "./report.md", "--blocks", blocks}, "--blocks cannot be combined with --attach"},
		{"broadcast with attach", []string{testChannel, "x", "--attach", "./report.md", "--thread-ts", "1770160000.000001", "--reply-broadcast"}, "--reply-broadcast cannot be combined with --attach"},
		{"both schedules", []string{testChannel, "x", "--schedule", "2030-01-01T00:00:00Z", "--schedule-in", "1h"}, "mutually exclusive"},
		{"empty schedule", []string{testChannel, "x", "--schedule", ""}, "--schedule needs a time"},
		{"empty schedule-in", []string{testChannel, "x", "--schedule-in", ""}, "--schedule-in needs a duration"},
		{"blank schedule-in", []string{testChannel, "x", "--schedule-in", "  "}, "--schedule-in needs a duration"},
		{"empty attach", []string{testChannel, "x", "--attach", ""}, "--attach needs a file path"},
		{"empty blocks", []string{testChannel, "x", "--blocks", ""}, "--blocks needs a file path"},
		{"bad thread ts", []string{testChannel, "x", "--thread-ts", "yesterday"}, "invalid --thread-ts"},
		{"permalink digits as thread ts", []string{testChannel, "x", "--thread-ts", "p1770165109628379"}, "invalid --thread-ts"},
		{"garbage schedule", []string{testChannel, "x", "--schedule", "soon"}, "soon"},
		{"schedule too far", []string{testChannel, "x", "--schedule-in", "200d"}, "120 days"},
		{"too many args", []string{testChannel, "one", "two"}, "accepts between 1 and 2 arg"},
		{"empty target", []string{"", "x"}, "missing target"},
		{"scheme-less URL", []string{"workspace.slack.com/archives/C12345678/p1770165109628379", "x"}, "unsupported target"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.mustFail(tt.want, append([]string{"send"}, tt.args...)...)
			h.wantCalls()
		})
	}
}

func TestSendBlocksFromFile(t *testing.T) {
	h := newHarness(t)
	path, blocks := writeBlocks(t, []any{
		map[string]any{"type": "header", "text": map[string]any{"type": "plain_text", "text": "Digest"}},
		map[string]any{"type": "table", "rows": []any{
			[]any{map[string]any{"type": "raw_text", "text": "Name"}, map[string]any{"type": "raw_text", "text": "Why"}},
			[]any{map[string]any{"type": "raw_text", "text": "Caveman"}, map[string]any{"type": "raw_text", "text": "token cut"}},
		}},
	})
	h.mustRun("send", testChannel, "fallback text", "--blocks", path)
	h.wantCalls(call{"chat.postMessage", map[string]any{"channel": testChannel, "text": "fallback text", "blocks": blocks}})
}

func TestSendBlocksWithoutText(t *testing.T) {
	h := newHarness(t)
	path, blocks := writeBlocks(t, []any{map[string]any{"type": "divider"}})
	h.mustRun("send", testChannel, "--blocks", path)
	h.wantCalls(call{"chat.postMessage", map[string]any{"channel": testChannel, "text": "", "blocks": blocks}})
}

func TestSendBlocksFromStdin(t *testing.T) {
	h := newHarness(t)
	h.stdin = `[{"type": "divider"}]`
	h.mustRun("send", testChannel, "fallback", "--blocks", "-")
	want := []any{map[string]any{"type": "divider"}}
	if got := h.callsTo("chat.postMessage")[0].params["blocks"]; !reflect.DeepEqual(got, want) {
		t.Errorf("blocks = %#v", got)
	}
}

func TestSendBlocksOverrideRichTextConversion(t *testing.T) {
	h := newHarness(t)
	path, blocks := writeBlocks(t, []any{map[string]any{"type": "divider"}})
	h.mustRun("send", testChannel, "- bullet one\n- bullet two", "--blocks", path)
	if got := h.callsTo("chat.postMessage")[0].params["blocks"]; !reflect.DeepEqual(got, blocks) {
		t.Errorf("blocks = %#v", got)
	}
}

func TestSendInvalidBlocks(t *testing.T) {
	var many []string
	for range maxBlocks + 1 {
		many = append(many, `{"type":"divider"}`)
	}
	tests := []struct {
		name, content, want string
	}{
		{"not an array", `{"type": "header"}`, "expected a JSON array of Block Kit blocks, got object"},
		{"malformed", `{not valid json`, "failed to parse JSON"},
		{"element not an object", `[{"type": "divider"}, "oops"]`, "element at index 1 is not a Block Kit block object (got string)"},
		{"null element", `[null]`, "element at index 0 is not a Block Kit block object (got null)"},
		{"element without type", `[{"text": "x"}]`, `element at index 0 has no "type"`},
		{"empty array", `[]`, "the blocks array is empty"},
		{"empty file", ` `, "is empty"},
		{"trailing data", `[{"type": "divider"}] []`, "unexpected data after the JSON value"},
		{"too many", "[" + strings.Join(many, ",") + "]", "at most 50"},
		{"too large", `["` + strings.Repeat("x", maxBlocksBytes) + `"]`, "larger than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.mustFail(tt.want, "send", testChannel, "text", "--blocks", writeFile(t, "blocks.json", tt.content))
			h.wantCalls()
		})
	}

	h := newHarness(t)
	h.mustFail("--blocks:", "send", testChannel, "text", "--blocks", "/does/not/exist.json")
	h.wantCalls()
}

func TestSendListAsRichText(t *testing.T) {
	h := newHarness(t)
	h.mustRun("send", testChannel, "- Review <https://example.com/pull/42|PR #42>")
	want := []any{map[string]any{
		"type": "rich_text",
		"elements": []any{map[string]any{
			"type":  "rich_text_list",
			"style": "bullet",
			"elements": []any{map[string]any{
				"type": "rich_text_section",
				"elements": []any{
					map[string]any{"type": "text", "text": "Review "},
					map[string]any{"type": "link", "url": "https://example.com/pull/42", "text": "PR #42"},
				},
			}},
		}},
	}}
	// The blocks are compared as Slack receives them, whatever Go types
	// the converter uses.
	wantJSON(t, map[string]any{"blocks": jsonRoundTrip(t, h.callsTo("chat.postMessage")[0].params["blocks"])}, mustMarshal(t, map[string]any{"blocks": want}))
}

func scheduleFixture(h *harness) {
	h.fixtures["chat.scheduleMessage"] = func(p map[string]any) (map[string]any, error) {
		// Slack returns post_at as a string here, as a number elsewhere.
		return map[string]any{"ok": true, "channel": p["channel"], "scheduled_message_id": "Q1234ABCD", "post_at": "1893456000"}, nil
	}
}

func TestSendSchedule(t *testing.T) {
	h := newHarness(t)
	scheduleFixture(h)
	now := time.Date(2029, 12, 31, 23, 0, 0, 0, time.UTC)
	h.c.now = func() time.Time { return now }
	got := h.mustRun("send", testChannel, "later", "--schedule", "2030-01-01T00:00:00Z")
	h.wantCalls(call{"chat.scheduleMessage", map[string]any{"channel": testChannel, "text": "later", "post_at": int64(1893456000)}})
	wantJSON(t, got, `{
		"ok": true,
		"channel_id": "C12345678",
		"scheduled_message_id": "Q1234ABCD",
		"post_at": 1893456000
	}`)
}

func TestSendScheduleIn(t *testing.T) {
	h := newHarness(t)
	h.fixtures["chat.scheduleMessage"] = func(p map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true, "channel": p["channel"], "scheduled_message_id": "Q1234ABCD"}, nil
	}
	now := time.Date(2029, 12, 31, 21, 0, 0, 0, time.UTC)
	h.c.now = func() time.Time { return now }
	got := h.mustRun("send", testChannel, "later", "--schedule-in", "3h")
	want := now.Add(3 * time.Hour).Unix()
	if got := h.callsTo("chat.scheduleMessage")[0].params["post_at"]; got != want {
		t.Errorf("post_at = %v, want %d", got, want)
	}
	// Without post_at in the response, the requested time is reported.
	if got["post_at"] != float64(want) {
		t.Errorf("printed post_at = %v, want %d", got["post_at"], want)
	}
	if len(h.callsTo("chat.postMessage")) != 0 {
		t.Error("message was also posted immediately")
	}
}

func TestSendScheduleNoUnfurl(t *testing.T) {
	h := newHarness(t)
	scheduleFixture(h)
	h.mustRun("send", testChannel, "https://example.com", "--schedule-in", "3h", "--no-unfurl")
	p := h.callsTo("chat.scheduleMessage")[0].params
	if p["unfurl_links"] != false || p["unfurl_media"] != false {
		t.Errorf("params = %#v", p)
	}
}

func TestSendScheduleWithBlocksThreadAndBroadcast(t *testing.T) {
	h := newHarness(t)
	scheduleFixture(h)
	path, blocks := writeBlocks(t, []any{map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "summary"}}})
	got := h.mustRun("send", testChannel, "fallback", "--blocks", path, "--thread-ts", "1770160000.000001", "--reply-broadcast", "--schedule-in", "30m")
	if len(h.calls) != 1 {
		t.Fatalf("calls = %v", h.methods())
	}
	p := h.calls[0].params
	delete(p, "post_at")
	want := map[string]any{"channel": testChannel, "text": "fallback", "thread_ts": "1770160000.000001", "blocks": blocks, "reply_broadcast": true}
	if h.calls[0].method != "chat.scheduleMessage" || !reflect.DeepEqual(p, want) {
		t.Errorf("call = %s %#v", h.calls[0].method, p)
	}
	if got["thread_ts"] != "1770160000.000001" {
		t.Errorf("thread_ts = %v", got["thread_ts"])
	}
}

func TestSendEscapesTextAndMentions(t *testing.T) {
	h := newHarness(t)
	h.mustRun("send", testChannel, "use <fix> & ping @U05BRPTKL6A")
	if got := h.callsTo("chat.postMessage")[0].params["text"]; got != "use &lt;fix&gt; &amp; ping <@U05BRPTKL6A>" {
		t.Errorf("text = %q", got)
	}
}

func TestSendExplainsSlackErrors(t *testing.T) {
	h := newHarness(t)
	h.fixtures["chat.postMessage"] = func(map[string]any) (map[string]any, error) {
		return nil, apiError("chat.postMessage", "not_in_channel")
	}
	h.mustFail("join the channel first", "send", testChannel, "hello")
}

func TestSendAppURLUsesDefaultWorkspace(t *testing.T) {
	h := newHarness(t)
	h.fixtures["conversations.history"] = func(map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true, "messages": []any{map[string]any{"ts": testTS, "text": "root"}}}, nil
	}
	got := h.mustRun("send", "https://app.slack.com/client/T12345678/C12345678/thread/C12345678-"+testTS, "hi")
	if p, _ := got["permalink"].(string); !strings.HasPrefix(p, testWorkspace+"/") {
		t.Errorf("permalink = %v, want it in the default workspace", got["permalink"])
	}
}
