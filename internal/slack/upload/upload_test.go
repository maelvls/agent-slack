package upload

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/maelvls/slackcli/internal/slackapi"
)

type call struct {
	method string
	params map[string]any
}

type received struct {
	body          string
	contentLength int64
	header        http.Header
}

// harness fakes both halves of an upload: the Slack API (fixtures by
// method name) and the pre-signed byte endpoint (a TLS server).
type harness struct {
	t        *testing.T
	client   *slackapi.Client
	fixtures map[string]func(params map[string]any) map[string]any
	status   int // HTTP status of the byte endpoint

	mu     sync.Mutex
	calls  []call
	posted []received
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, status: http.StatusOK, fixtures: map[string]func(map[string]any) map[string]any{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.posted = append(h.posted, received{string(body), r.ContentLength, r.Header.Clone()})
		h.mu.Unlock()
		w.WriteHeader(h.status)
		if h.status != http.StatusOK {
			io.WriteString(w, "err")
		}
	}))
	t.Cleanup(srv.Close)

	// Default: reserve "F-<filename>" at the fake byte endpoint.
	h.fixtures["files.getUploadURLExternal"] = func(p map[string]any) map[string]any {
		return map[string]any{"ok": true, "upload_url": srv.URL + "/f", "file_id": "F-" + p["filename"].(string)}
	}
	h.client = slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		h.mu.Lock()
		h.calls = append(h.calls, call{method, params})
		h.mu.Unlock()
		if fx := h.fixtures[method]; fx != nil {
			return fx(params), nil
		}
		return map[string]any{"ok": true}, nil
	}, slackapi.WithHTTPClient(srv.Client()))
	return h
}

func (h *harness) callsTo(method string) []call {
	var out []call
	for _, c := range h.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func completedIDs(c call) []string {
	var ids []string
	for _, f := range c.params["files"].([]map[string]any) {
		ids = append(ids, f["id"].(string))
	}
	return ids
}

func TestForDraftNeverShiftsIDsWhenCompletionOmitsOne(t *testing.T) {
	h := newHarness(t)
	// Slack returns a null id for the FIRST file: b's id must not slide
	// into a's slot.
	h.fixtures["files.completeUploadExternal"] = func(map[string]any) map[string]any {
		return map[string]any{"ok": true, "files": []any{map[string]any{"id": nil}, map[string]any{"id": "F-final-b"}}}
	}
	files, err := ForDraft(context.Background(), h.client, []string{writeFile(t, "a.png", "x"), writeFile(t, "b.pdf", "y")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := IDs(files), []string{"F-a.png", "F-final-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

func TestForDraftFallsBackToStagedIDsWhenCompletionLengthDisagrees(t *testing.T) {
	h := newHarness(t)
	h.fixtures["files.completeUploadExternal"] = func(map[string]any) map[string]any {
		return map[string]any{"ok": true, "files": []any{map[string]any{"id": "F-only-one"}}}
	}
	files, err := ForDraft(context.Background(), h.client, []string{writeFile(t, "a.png", "x"), writeFile(t, "b.pdf", "y")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := IDs(files), []string{"F-a.png", "F-b.pdf"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

func TestForDraftStagesEveryFileThenCompletesOnceWithoutChannel(t *testing.T) {
	h := newHarness(t)
	files, err := ForDraft(context.Background(), h.client, []string{writeFile(t, "a.png", "xx"), writeFile(t, "b.pdf", "yyy")})
	if err != nil {
		t.Fatal(err)
	}
	want := []File{{ID: "F-a.png", Name: "a.png", Size: 2}, {ID: "F-b.pdf", Name: "b.pdf", Size: 3}}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("files = %+v, want %+v", files, want)
	}
	if len(h.posted) != 2 {
		t.Fatalf("byte POSTs = %d, want 2", len(h.posted))
	}
	completes := h.callsTo("files.completeUploadExternal")
	if len(completes) != 1 {
		t.Fatalf("completions = %d, want 1", len(completes))
	}
	if got := completedIDs(completes[0]); !reflect.DeepEqual(got, []string{"F-a.png", "F-b.pdf"}) {
		t.Errorf("completed ids = %v", got)
	}
	for _, k := range []string{"channel_id", "thread_ts", "initial_comment"} {
		if _, ok := completes[0].params[k]; ok {
			t.Errorf("draft completion must not carry %s", k)
		}
	}
}

func TestLaterStagingFailureCompletesNothing(t *testing.T) {
	h := newHarness(t)
	reserve := h.fixtures["files.getUploadURLExternal"]
	n := 0
	h.fixtures["files.getUploadURLExternal"] = func(p map[string]any) map[string]any {
		n++
		if n == 1 {
			return reserve(p)
		}
		return map[string]any{"ok": false, "error": "ratelimited"}
	}
	_, err := ForDraft(context.Background(), h.client, []string{writeFile(t, "a.png", "x"), writeFile(t, "b.pdf", "y")})
	if !slackapi.IsErrorCode(err, "ratelimited") {
		t.Fatalf("err = %v, want ratelimited API error", err)
	}
	if n := len(h.callsTo("files.completeUploadExternal")); n != 0 {
		t.Errorf("completions = %d, want 0", n)
	}
}

func TestMissingPathFailsBeforeAnyAPICall(t *testing.T) {
	h := newHarness(t)
	// The bad path is LAST: validation must still run before the first
	// file is staged.
	_, err := ForDraft(context.Background(), h.client, []string{writeFile(t, "a.png", "x"), filepath.Join(t.TempDir(), "missing.png")})
	if err == nil || !strings.Contains(err.Error(), "attachment not found") {
		t.Fatalf("err = %v", err)
	}
	if len(h.calls) != 0 || len(h.posted) != 0 {
		t.Errorf("API calls = %d, byte POSTs = %d, want none", len(h.calls), len(h.posted))
	}
}

func TestBytePostFailureCompletesNothing(t *testing.T) {
	h := newHarness(t)
	h.status = http.StatusInternalServerError
	_, err := ForDraft(context.Background(), h.client, []string{writeFile(t, "x.txt", "hi")})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500: err") {
		t.Fatalf("err = %v", err)
	}
	if n := len(h.callsTo("files.completeUploadExternal")); n != 0 {
		t.Errorf("completions = %d, want 0", n)
	}
}

func TestCompletionFailureIsReturned(t *testing.T) {
	h := newHarness(t)
	h.fixtures["files.completeUploadExternal"] = func(map[string]any) map[string]any {
		return map[string]any{"ok": false, "error": "denied"}
	}
	_, err := ForDraft(context.Background(), h.client, []string{writeFile(t, "x.txt", "hi")})
	if !slackapi.IsErrorCode(err, "denied") {
		t.Fatalf("err = %v, want denied API error", err)
	}
}

func TestCheckRejectsBadPaths(t *testing.T) {
	dir := t.TempDir()
	huge := filepath.Join(dir, "huge.bin")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	// Sparse: no gigabyte is actually written.
	if err := f.Truncate(MaxFileSize + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	tests := []struct {
		name, path, want string
	}{
		{"directory", dir, "is a directory"},
		{"missing", filepath.Join(dir, "nope.txt"), "attachment not found"},
		{"empty file", writeFile(t, "empty.txt", ""), "is empty (0 bytes)"},
		{"too large", huge, "too large"},
		{"blank path", "  ", "path is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Check([]string{tt.path})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Check(%q) = %v, want error containing %q", tt.path, err, tt.want)
			}
		})
	}
	if err := Check([]string{writeFile(t, "ok.txt", "ok")}); err != nil {
		t.Errorf("Check(valid) = %v", err)
	}
}

func TestToChannelSharesAllFilesAsOneMessage(t *testing.T) {
	h := newHarness(t)
	dest := Destination{ChannelID: "C1", ThreadTS: "123.000100", InitialComment: " hi "}
	files, err := ToChannel(context.Background(), h.client, dest, []string{writeFile(t, "r.md", "# r\n"), writeFile(t, "s.md", "# s\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got := IDs(files); !reflect.DeepEqual(got, []string{"F-r.md", "F-s.md"}) {
		t.Errorf("ids = %v", got)
	}
	completes := h.callsTo("files.completeUploadExternal")
	if len(completes) != 1 {
		t.Fatalf("completions = %d, want 1 (a second one would post a second message)", len(completes))
	}
	p := completes[0].params
	if p["channel_id"] != "C1" || p["thread_ts"] != "123.000100" || p["initial_comment"] != "hi" {
		t.Errorf("completion params = %v", p)
	}
	if got := completedIDs(completes[0]); !reflect.DeepEqual(got, []string{"F-r.md", "F-s.md"}) {
		t.Errorf("completed ids = %v", got)
	}
}

func TestToChannelOmitsBlankOptionalParams(t *testing.T) {
	h := newHarness(t)
	if _, err := ToChannel(context.Background(), h.client, Destination{ChannelID: "C1", InitialComment: "  "}, []string{writeFile(t, "r.md", "r")}); err != nil {
		t.Fatal(err)
	}
	p := h.callsTo("files.completeUploadExternal")[0].params
	for _, k := range []string{"thread_ts", "initial_comment"} {
		if _, ok := p[k]; ok {
			t.Errorf("unexpected %s in %v", k, p)
		}
	}
	if _, err := ToChannel(context.Background(), h.client, Destination{}, []string{writeFile(t, "r.md", "r")}); err == nil {
		t.Error("want an error without a channel ID")
	}
}

func TestBytesAreStreamedWithoutCredentials(t *testing.T) {
	h := newHarness(t)
	content := strings.Repeat("slack", 50_000)
	if _, err := ForDraft(context.Background(), h.client, []string{writeFile(t, "big.txt", content)}); err != nil {
		t.Fatal(err)
	}
	got := h.posted[0]
	if got.body != content || got.contentLength != int64(len(content)) {
		t.Errorf("posted %d bytes (Content-Length %d), want %d", len(got.body), got.contentLength, len(content))
	}
	// The fake client carries an xoxc token and an xoxd cookie; neither
	// may reach the pre-signed upload host.
	for _, k := range []string{"Authorization", "Cookie"} {
		if v := got.header.Get(k); v != "" {
			t.Errorf("%s header sent to the upload host: %q", k, v)
		}
	}
	if ct := got.header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if n := h.callsTo("files.getUploadURLExternal")[0].params["length"]; n != int64(len(content)) {
		t.Errorf("announced length = %v", n)
	}
}

func TestNonHTTPSUploadURLIsRefused(t *testing.T) {
	h := newHarness(t)
	h.fixtures["files.getUploadURLExternal"] = func(map[string]any) map[string]any {
		return map[string]any{"ok": true, "upload_url": "http://files.example/f", "file_id": "F1"}
	}
	_, err := ForDraft(context.Background(), h.client, []string{writeFile(t, "x.txt", "hi")})
	if err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("err = %v", err)
	}
	if len(h.posted) != 0 {
		t.Error("bytes were sent")
	}
}

func TestSymlinkUploadsUnderTargetName(t *testing.T) {
	h := newHarness(t)
	target := writeFile(t, "real.txt", "hi")
	link := filepath.Join(t.TempDir(), "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	files, err := ForDraft(context.Background(), h.client, []string{link})
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Name != "real.txt" {
		t.Errorf("name = %q, want real.txt", files[0].Name)
	}
}

func TestDeleteFiles(t *testing.T) {
	h := newHarness(t)
	h.fixtures["files.delete"] = func(p map[string]any) map[string]any {
		switch p["file"] {
		case "F-gone":
			return map[string]any{"ok": false, "error": "file_not_found"}
		case "F-locked":
			return map[string]any{"ok": false, "error": "cant_delete_file"}
		}
		return map[string]any{"ok": true}
	}
	// A cancelled context is the typical situation: the draft call that
	// should have used the files just timed out.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := DeleteFiles(ctx, h.client, []string{"F1", "F-gone", "F-locked"})
	if !slackapi.IsErrorCode(err, "cant_delete_file") || !strings.Contains(err.Error(), "F-locked") {
		t.Errorf("err = %v, want cant_delete_file for F-locked", err)
	}
	if strings.Contains(err.Error(), "F-gone") || errors.Is(err, context.Canceled) {
		t.Errorf("err = %v: already-deleted files and the caller's cancellation must not surface", err)
	}
	if n := len(h.callsTo("files.delete")); n != 3 {
		t.Errorf("files.delete calls = %d, want 3", n)
	}
	if err := DeleteFiles(ctx, h.client, nil); err != nil {
		t.Errorf("DeleteFiles(nil) = %v", err)
	}
}
