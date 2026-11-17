package messagecmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// history makes conversations.history answer with msgs, whatever is asked.
func (h *harness) history(msgs ...map[string]any) {
	h.fixtures["conversations.history"] = func(map[string]any) (map[string]any, error) {
		out := make([]any, len(msgs))
		for i, m := range msgs {
			out[i] = m
		}
		return map[string]any{"ok": true, "messages": out}, nil
	}
}

// replies makes conversations.replies answer with msgs.
func (h *harness) replies(msgs ...map[string]any) {
	h.fixtures["conversations.replies"] = func(map[string]any) (map[string]any, error) {
		out := make([]any, len(msgs))
		for i, m := range msgs {
			out[i] = m
		}
		return map[string]any{"ok": true, "messages": out}, nil
	}
}

func TestGetByURL(t *testing.T) {
	h := newHarness(t)
	h.history(map[string]any{"ts": testTS, "user": "U05BRPTKL6A", "text": "hello *world*"})
	got := h.mustRun("get", testURL)
	wantJSON(t, got, `{"message": {
		"channel_id": "C12345678",
		"ts": "1770165109.628379",
		"author": {"user_id": "U05BRPTKL6A"},
		"content": "hello *world*"
	}}`)
	h.wantCalls(call{"conversations.history", map[string]any{"channel": testChannel, "latest": testTS, "inclusive": true, "limit": 1}})
}

func TestDefaultSubcommandIsGet(t *testing.T) {
	h := newHarness(t)
	h.history(map[string]any{"ts": testTS, "user": "U05BRPTKL6A", "text": "0123456789"})

	want := h.mustRun("get", testURL, "--max-body-chars", "4")
	for _, args := range [][]string{
		{testURL, "--max-body-chars", "4"},
		{"--max-body-chars", "4", testURL},
		{testChannel, "--ts", testTS, "--max-body-chars=4"},
		{"general", "--ts", testTS, "--max-body-chars", "4"},
	} {
		if got := h.mustRun(args...); !reflect.DeepEqual(got, want) {
			t.Errorf("message %v = %v, want the output of get: %v", args, got, want)
		}
	}
	content, _ := want["message"].(map[string]any)["content"].(string)
	if !strings.HasPrefix(content, "0123") || strings.Contains(content, "456789") {
		t.Errorf("content = %q, want it cut after 4 characters", content)
	}
}

func TestUnknownSubcommand(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"gett", testURL}, `unknown command "gett" for "message"`},
		{[]string{"sned", testChannel, "hello"}, `did you mean send?`},
		{[]string{"lisst"}, `did you mean list?`},
		{[]string{"sennd"}, `did you mean send?`},
		{[]string{"frobnicate", "x", "y"}, `unknown command "frobnicate"`},
		{[]string{testURL, "extra"}, "accepts exactly one <target>"},
		// A single unknown word is a channel name, which needs --ts.
		{[]string{"announcements"}, "--ts is required"},
		{[]string{"react", "ad", testURL, "eyes"}, `unknown command "ad" for "message react"`},
		{[]string{"scheduled", "cancle", "Q1"}, `unknown command "cancle" for "message scheduled"`},
	}
	for _, tt := range tests {
		h := newHarness(t)
		h.mustFail(tt.want, tt.args...)
		h.wantCalls()
	}
}

func TestMessageWithoutArgsPrintsHelp(t *testing.T) {
	h := newHarness(t)
	if out, err := h.run(); err != nil || out != "" {
		t.Errorf("out = %q, err = %v", out, err)
	}
	h.wantCalls()
}

func TestGetValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"channel without ts", []string{"get", testChannel}, `--ts is required when the target is a channel: pass --ts "<seconds>.<micros>"`},
		{"blank ts", []string{"get", testChannel, "--ts", "   "}, "--ts is required"},
		{"garbage ts", []string{"get", testChannel, "--ts", "abc"}, "invalid --ts"},
		{"truncated ts", []string{"get", testChannel, "--ts", "1770165109"}, "invalid --ts"},
		{"ts contradicting URL", []string{"get", testURL, "--ts", "1770160000.000001"}, "contradicts the message URL"},
		{"garbage thread ts", []string{"get", testURL, "--thread-ts", "x"}, "invalid --thread-ts"},
		{"user target", []string{"get", "U05BRPTKL6A"}, "does not support user ID targets"},
		{"non-numeric max-body-chars", []string{"get", testURL, "--max-body-chars", "abc"}, `invalid argument "abc" for "--max-body-chars"`},
		{"fractional max-body-chars", []string{"get", testURL, "--max-body-chars", "3.5"}, "--max-body-chars"},
		{"zero max-body-chars", []string{"get", testURL, "--max-body-chars", "0"}, "must be a positive integer, or -1 for unlimited"},
		{"negative max-body-chars", []string{"get", testURL, "--max-body-chars", "-2"}, "must be a positive integer, or -1 for unlimited"},
		{"non-Slack URL", []string{"get", "https://example.com/archives/C12345678/p1770165109628379"}, "unsupported target URL"},
		{"no target", []string{"get"}, "accepts 1 arg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.mustFail(tt.want, tt.args...)
			h.wantCalls()
		})
	}
}

func TestMessageTS(t *testing.T) {
	h := newHarness(t)
	h.history(map[string]any{"ts": testTS, "text": "x"})
	// Surrounding blanks are trimmed; a --ts equal to the URL's is fine.
	h.mustRun("get", testChannel, "--ts", " "+testTS+" ")
	if ts := h.callsTo("conversations.history")[0].params["latest"]; ts != testTS {
		t.Errorf("latest = %q", ts)
	}
	h.mustRun("get", testURL, "--ts", testTS)
}

func TestGetUnlimitedBody(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("x", 9000)
	h.history(map[string]any{"ts": testTS, "text": long})

	content := func(args ...string) string {
		s, _ := h.mustRun(args...)["message"].(map[string]any)["content"].(string)
		return s
	}
	if got := content("get", testURL); len(got) >= 9000 || !strings.HasPrefix(got, strings.Repeat("x", 8000)) {
		t.Errorf("default: content has %d characters, want it cut at 8000", len(got))
	}
	if got := content("get", testURL, "--max-body-chars", "-1"); got != long {
		t.Errorf("-1: content has %d characters, want all 9000", len(got))
	}
}

func TestGetMessageNotFound(t *testing.T) {
	h := newHarness(t)
	h.mustFail("no message with ts "+testTS, "get", testURL)
}

func TestGetThreadSummary(t *testing.T) {
	root := map[string]any{"ts": "1770160000.000001", "thread_ts": "1770160000.000001", "reply_count": 3, "text": "root"}
	reply := map[string]any{"ts": "1770160500.000100", "thread_ts": "1770160000.000001", "text": "reply"}

	t.Run("root: no extra call", func(t *testing.T) {
		h := newHarness(t)
		h.history(root)
		got := h.mustRun("get", testChannel, "--ts", "1770160000.000001")
		wantJSON(t, map[string]any{"thread": got["thread"]}, `{"thread": {"ts": "1770160000.000001", "length": 4}}`)
		if got := h.methods(); !reflect.DeepEqual(got, []string{"conversations.history"}) {
			t.Errorf("calls = %v", got)
		}
	})

	t.Run("reply: length comes from the root", func(t *testing.T) {
		h := newHarness(t)
		h.fixtures["conversations.replies"] = func(p map[string]any) (map[string]any, error) {
			if p["ts"] == "1770160000.000001" && p["limit"] == 1 {
				return map[string]any{"ok": true, "messages": []any{root}}, nil
			}
			return map[string]any{"ok": true, "messages": []any{root, reply}}, nil
		}
		// --thread-ts tells where to look for the reply (the TypeScript
		// ignored it for URL targets).
		got := h.mustRun("get", testWorkspace+"/archives/C12345678/p1770160500000100", "--thread-ts", "1770160000.000001")
		wantJSON(t, map[string]any{"thread": got["thread"]}, `{"thread": {"ts": "1770160000.000001", "length": 4}}`)
		if got["message"].(map[string]any)["ts"] != "1770160500.000100" {
			t.Errorf("message = %v", got["message"])
		}
		first := h.callsTo("conversations.replies")[0].params
		if first["ts"] != "1770160000.000001" || first["oldest"] != "1770160500.000100" {
			t.Errorf("first conversations.replies call = %#v, want the hinted thread", first)
		}
	})

	t.Run("no thread", func(t *testing.T) {
		h := newHarness(t)
		h.history(map[string]any{"ts": testTS, "text": "alone"})
		if got := h.mustRun("get", testURL); got["thread"] != nil {
			t.Errorf("thread = %v", got["thread"])
		}
	})
}

func TestGetIncludeReactionsAndResolveUsers(t *testing.T) {
	h := newHarness(t)
	h.history(map[string]any{
		"ts": testTS, "user": "U05BRPTKL6A", "text": "ping <@U0AAAAAAAAA>",
		"reactions": []any{map[string]any{"name": "eyes", "users": []any{"U0BBBBBBBBB"}, "count": 1}},
	})
	h.fixtures["users.info"] = func(p map[string]any) (map[string]any, error) {
		id := p["user"].(string)
		return map[string]any{"ok": true, "user": map[string]any{"id": id, "name": "name-" + id}}, nil
	}

	got := h.mustRun("get", testURL)
	if got["referenced_users"] != nil || got["message"].(map[string]any)["reactions"] != nil {
		t.Errorf("without flags: %v", got)
	}
	if len(h.callsTo("users.info")) != 0 {
		t.Error("users.info called without --resolve-users")
	}

	got = h.mustRun("get", testURL, "--include-reactions", "--resolve-users")
	users, _ := got["referenced_users"].(map[string]any)
	for _, id := range []string{"U05BRPTKL6A", "U0AAAAAAAAA", "U0BBBBBBBBB"} {
		if u, _ := users[id].(map[string]any); u["name"] != "name-"+id {
			t.Errorf("referenced_users[%s] = %v", id, users[id])
		}
	}
	reactions, _ := got["message"].(map[string]any)["reactions"].([]any)
	if len(reactions) != 1 || reactions[0].(map[string]any)["name"] != "eyes" {
		t.Errorf("reactions = %v", reactions)
	}
	if h.callsTo("conversations.history")[0].params["include_all_metadata"] != true {
		t.Error("include_all_metadata not requested")
	}

	// Cached now; --refresh-users implies --resolve-users and refetches.
	h.mustRun("get", testURL, "--resolve-users")
	if n := len(h.callsTo("users.info")); n != 0 {
		t.Errorf("%d users.info calls with a warm cache", n)
	}
	got = h.mustRun("get", testURL, "--refresh-users")
	if n := len(h.callsTo("users.info")); n != 2 {
		t.Errorf("%d users.info calls with --refresh-users, want 2", n)
	}
	if got["referenced_users"] == nil {
		t.Error("--refresh-users did not print referenced_users")
	}
}

func TestGetTruncatedURLHint(t *testing.T) {
	h := newHarness(t)
	h.history(map[string]any{"ts": testTS, "text": "x"})
	h.mustRun("get", testURL+"?thread_ts=1770160000.000001")
	if !strings.Contains(h.stderr.String(), "URL may have been truncated by shell") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
	h.mustRun("get", testURL)
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestGetDownloadsFiles(t *testing.T) {
	h := newHarness(t)
	h.history(map[string]any{"ts": testTS, "text": "see attached", "files": []any{
		map[string]any{"id": "F1", "name": "notes.txt", "mimetype": "text/plain", "url_private_download": "https://files.slack.com/files-pri/T1-F1/download/notes.txt"},
		map[string]any{"id": "F2", "name": "gone.png", "mimetype": "image/png", "url_private_download": "https://files.slack.com/files-pri/T1-F2/download/gone.png"},
	}})
	h.serve = func(req *http.Request) *http.Response {
		if strings.Contains(req.URL.Path, "F2") {
			return httpResponse(http.StatusNotFound, "", "")
		}
		return httpResponse(http.StatusOK, "text/plain", "my notes")
	}

	// One failed download does not hide the message or the other file.
	got := h.mustRun("get", testURL)
	wantJSON(t, got, fmt.Sprintf(`{"message": {
		"channel_id": "C12345678",
		"ts": "1770165109.628379",
		"content": "see attached",
		"files": [
			{"name": "notes.txt", "mimetype": "text/plain", "path": %q},
			{"name": "gone.png", "mimetype": "image/png", "path": %q, "error": "Failed to download file (404)"}
		]
	}}`, filepath.Join(h.c.downloadsDir, "F1.txt"), filepath.Join(h.c.downloadsDir, "F2.download-error.txt")))
	if b, _ := os.ReadFile(filepath.Join(h.c.downloadsDir, "F1.txt")); string(b) != "my notes" {
		t.Errorf("downloaded content = %q", b)
	}
	if !strings.Contains(h.stderr.String(), "Warning: could not download file F2: Failed to download file (404)") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func threadFixture(h *harness) {
	h.replies(
		map[string]any{"ts": "1770160000.000001", "thread_ts": "1770160000.000001", "reply_count": 2, "user": "U05BRPTKL6A", "text": "root"},
		map[string]any{"ts": "1770160500.000100", "thread_ts": "1770160000.000001", "user": "U0AAAAAAAAA", "text": "first"},
		map[string]any{"ts": "1770160600.000200", "thread_ts": "1770160000.000001", "user": "U05BRPTKL6A", "text": "second"},
	)
}

const wantThread = `{"messages": [
	{"ts": "1770160000.000001", "author": {"user_id": "U05BRPTKL6A"}, "content": "root"},
	{"ts": "1770160500.000100", "author": {"user_id": "U0AAAAAAAAA"}, "content": "first"},
	{"ts": "1770160600.000200", "author": {"user_id": "U05BRPTKL6A"}, "content": "second"}
]}`

func TestListThread(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		wantTS string
	}{
		{"message URL", []string{testWorkspace + "/archives/C12345678/p1770160000000001"}, "1770160000.000001"},
		{"reply URL with thread_ts", []string{testWorkspace + "/archives/C12345678/p1770160500000100?thread_ts=1770160000.000001&cid=C12345678"}, "1770160000.000001"},
		{"channel and --thread-ts", []string{testChannel, "--thread-ts", "1770160000.000001"}, "1770160000.000001"},
		{"channel and --ts", []string{testChannel, "--ts", "1770160500.000100"}, "1770160500.000100"},
		{"channel name and --ts", []string{"#general", "--ts", "1770160000.000001"}, "1770160000.000001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			threadFixture(h)
			// channel_id and thread_ts are not repeated on every message.
			wantJSON(t, h.mustRun(append([]string{"list"}, tt.args...)...), wantThread)
			calls := h.callsTo("conversations.replies")
			// Asked about a reply, FetchThread asks again about the root
			// that the answer names.
			wantCalls := 1
			if tt.wantTS != "1770160000.000001" {
				wantCalls = 2
			}
			if len(calls) != wantCalls || calls[0].params["ts"] != tt.wantTS || calls[0].params["channel"] != testChannel ||
				calls[len(calls)-1].params["ts"] != "1770160000.000001" {
				t.Errorf("conversations.replies calls = %v", calls)
			}
			if len(h.callsTo("conversations.history")) != 0 {
				t.Error("the message was fetched before its thread: FetchThread alone is enough")
			}
		})
	}
}

func TestListThreadNotFound(t *testing.T) {
	h := newHarness(t)
	h.fixtures["conversations.replies"] = func(map[string]any) (map[string]any, error) {
		return nil, apiError("conversations.replies", "thread_not_found")
	}
	h.mustFail("no message with ts "+testTS+" in channel "+testChannel, "list", testURL)
}

func TestListThreadRejectsHistoryFlags(t *testing.T) {
	for _, flag := range [][]string{
		{"--limit", "5"}, {"--oldest", "1770160000.000001"}, {"--latest", "1770160000.000001"},
		{"--with-reaction", "eyes"}, {"--without-reaction", "eyes"},
	} {
		for _, target := range [][]string{{testURL}, {testChannel, "--ts", testTS}, {testChannel, "--thread-ts", testTS}} {
			h := newHarness(t)
			args := append(append([]string{"list"}, target...), flag...)
			h.mustFail(flag[0]+" only applies to channel history", args...)
			h.wantCalls()
		}
	}
}

func TestListHistory(t *testing.T) {
	h := newHarness(t)
	// Slack answers newest first.
	h.history(
		map[string]any{"ts": "1770160600.000200", "user": "U05BRPTKL6A", "text": "newer"},
		map[string]any{"ts": "1770160000.000001", "thread_ts": "1770160000.000001", "reply_count": 2, "user": "U0AAAAAAAAA", "text": "older"},
	)
	got := h.mustRun("list", "general", "--limit", "50", "--oldest", " 1770100000 ", "--latest", "1770170000.000000")
	wantJSON(t, got, `{
		"channel_id": "C12345678",
		"messages": [
			{"channel_id": "C12345678", "ts": "1770160000.000001", "thread_ts": "1770160000.000001", "author": {"user_id": "U0AAAAAAAAA"}, "content": "older"},
			{"channel_id": "C12345678", "ts": "1770160600.000200", "author": {"user_id": "U05BRPTKL6A"}, "content": "newer"}
		]
	}`)
	want := map[string]any{"channel": testChannel, "limit": 50, "oldest": "1770100000", "latest": "1770170000.000000"}
	if got := h.callsTo("conversations.history")[0].params; !reflect.DeepEqual(got, want) {
		t.Errorf("conversations.history params = %#v\nwant %#v", got, want)
	}
}

func TestListHistoryDefaultLimitAndEmptyResult(t *testing.T) {
	h := newHarness(t)
	got := h.mustRun("list", testChannel)
	wantJSON(t, got, `{"channel_id": "C12345678", "messages": []}`)
	// "messages" is there although empty: jq '.messages[]' must not fail.
	if want := "{\n  \"channel_id\": \"C12345678\",\n  \"messages\": []\n}\n"; h.stdout.String() != want {
		t.Errorf("printed %s, want %s", h.stdout, want)
	}
	if limit := h.callsTo("conversations.history")[0].params["limit"]; limit != 25 {
		t.Errorf("limit = %v, want 25", limit)
	}
	if !strings.Contains(h.stderr.String(), "No messages matched") {
		t.Errorf("stderr = %q, want a hint that nothing matched", h.stderr.String())
	}
}

func TestListHistoryByChannelURL(t *testing.T) {
	h := newHarness(t)
	h.history(map[string]any{"ts": testTS, "text": "x"})
	got := h.mustRun("list", testWorkspace+"/archives/"+testChannel)
	if got["channel_id"] != testChannel || len(h.callsTo("conversations.history")) != 1 {
		t.Errorf("got %v, calls %v", got, h.methods())
	}
}

func TestListHistoryReactionFilters(t *testing.T) {
	h := newHarness(t)
	h.history(
		map[string]any{"ts": "1770160600.000200", "text": "done", "reactions": []any{map[string]any{"name": "eyes", "users": []any{"U05BRPTKL6A"}, "count": 1}, map[string]any{"name": "white_check_mark", "users": []any{"U05BRPTKL6A"}, "count": 1}}},
		map[string]any{"ts": "1770160500.000100", "text": "todo", "reactions": []any{map[string]any{"name": "eyes", "users": []any{"U05BRPTKL6A"}, "count": 1}}},
		map[string]any{"ts": "1770160000.000001", "text": "unseen"},
	)
	got := h.mustRun("list", testChannel, "--oldest", "1770100000.000000",
		"--with-reaction", ":eyes:", "--with-reaction", "👀", "--without-reaction", "white_check_mark")
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "todo" {
		t.Fatalf("messages = %v", msgs)
	}
	// Reactions are fetched for filtering but only printed on request.
	if msgs[0].(map[string]any)["reactions"] != nil {
		t.Errorf("reactions printed without --include-reactions: %v", msgs[0])
	}
}

func TestListValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"limit not a number", []string{testChannel, "--limit", "abc"}, `invalid argument "abc" for "--limit"`},
		{"limit zero", []string{testChannel, "--limit", "0"}, "must be between 1 and 1000"},
		{"limit negative", []string{testChannel, "--limit", "-5"}, "must be between 1 and 1000"},
		{"limit fractional", []string{testChannel, "--limit", "3.5"}, `invalid argument "3.5" for "--limit"`},
		{"limit above the maximum", []string{testChannel, "--limit", "1001"}, "must be between 1 and 1000"},
		{"oldest garbage", []string{testChannel, "--oldest", "yesterday"}, "invalid --oldest"},
		{"latest garbage", []string{testChannel, "--latest", "now"}, "invalid --latest"},
		{"reaction filter without oldest", []string{testChannel, "--with-reaction", "dart"}, `reaction filters require --oldest "<seconds>.<micros>" to bound scan size.`},
		{"negative filter without oldest", []string{testChannel, "--without-reaction", "dart"}, "reaction filters require --oldest"},
		{"reaction filter with blank oldest", []string{testChannel, "--with-reaction", "dart", "--oldest", " "}, "reaction filters require --oldest"},
		{"unknown emoji", []string{testChannel, "--oldest", "1770100000", "--with-reaction", "not an emoji"}, "--with-reaction: unsupported emoji"},
		{"contradictory filters", []string{testChannel, "--oldest", "1770100000", "--with-reaction", "dart", "--without-reaction", "🎯"}, "both --with-reaction and --without-reaction"},
		{"user target", []string{"U05BRPTKL6A"}, "does not support user ID targets"},
		{"bad thread ts", []string{testChannel, "--thread-ts", "abc"}, "invalid --thread-ts"},
		{"bad ts", []string{testChannel, "--ts", "abc"}, "invalid --ts"},
		{"zero max-body-chars", []string{testChannel, "--max-body-chars", "0"}, "--max-body-chars"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.mustFail(tt.want, append([]string{"list"}, tt.args...)...)
			h.wantCalls()
		})
	}
}

func TestParseReactionFilters(t *testing.T) {
	got, err := parseReactionFilters("--with-reaction", []string{":dart:", "dart", "🎯"})
	if err != nil || !reflect.DeepEqual(got, []string{"dart"}) {
		t.Errorf("got %v, %v; want [dart]", got, err)
	}
	if got, err := parseReactionFilters("--with-reaction", nil); err != nil || len(got) != 0 {
		t.Errorf("unset: got %v, %v", got, err)
	}
	if _, err := parseReactionFilters("--with-reaction", []string{""}); err == nil {
		t.Error("an empty emoji was accepted")
	}
}

func TestRequireOldestForReactionFilters(t *testing.T) {
	if err := requireOldestForReactionFilters("", false); err != nil {
		t.Errorf("no filters: %v", err)
	}
	if err := requireOldestForReactionFilters("1770165109.628379", true); err != nil {
		t.Errorf("filters with oldest: %v", err)
	}
	if err := requireOldestForReactionFilters("", true); err == nil {
		t.Error("filters without oldest accepted")
	}
}

func TestWorkspaceSelection(t *testing.T) {
	h := newHarness(t)
	h.addWorkspace("https://other.slack.com")
	permalink := func(args ...string) string {
		p, _ := h.mustRun(args...)["permalink"].(string)
		return p
	}
	// The default workspace, then --workspace, then SLACK_WORKSPACE_URL.
	if p := permalink("send", testChannel, "hi"); !strings.HasPrefix(p, testWorkspace+"/") {
		t.Errorf("default: permalink = %s", p)
	}
	if p := permalink("send", testChannel, "hi", "--workspace", "other"); !strings.HasPrefix(p, "https://other.slack.com/") {
		t.Errorf("--workspace: permalink = %s", p)
	}
	t.Setenv("SLACK_WORKSPACE_URL", "https://other.slack.com")
	if p := permalink("send", "#general", "hi"); !strings.HasPrefix(p, "https://other.slack.com/") {
		t.Errorf("SLACK_WORKSPACE_URL: permalink = %s", p)
	}
	// A URL names its workspace, whatever the default.
	h.history(map[string]any{"ts": testTS, "text": "x"})
	if p := permalink("send", testURL, "hi"); !strings.HasPrefix(p, testWorkspace+"/") {
		t.Errorf("URL target: permalink = %s", p)
	}
	h.mustFail("no-such-workspace", "send", testChannel, "hi", "--workspace", "no-such-workspace")
}
