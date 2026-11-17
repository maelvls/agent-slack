package messagecmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maelvls/slackcli/internal/slack/files"
	"github.com/maelvls/slackcli/internal/slack/messages"
)

func (h *harness) download(ctx context.Context, msgs ...messages.Message) (map[string]files.DownloadResult, error) {
	h.t.Helper()
	r, err := h.rt.Client(ctx, "")
	if err != nil {
		h.t.Fatal(err)
	}
	return h.c.downloadMessageFiles(ctx, r.Client, msgs)
}

func messageWith(fs ...messages.FileSummary) messages.Message {
	return messages.Message{ChannelID: "C123", TS: "1234567890.000001", Text: "hello", Markdown: "hello", Files: fs}
}

func TestDownloadCanvasLoginPageIsRecorded(t *testing.T) {
	h := newHarness(t)
	h.serve = func(*http.Request) *http.Response {
		return httpResponse(http.StatusOK, "text/html", "<html><head><title>Sign in</title></head><body>Sign in</body></html>")
	}
	got, err := h.download(context.Background(), messageWith(messages.FileSummary{
		ID: "F1", Name: "canvas", Mode: "canvas", URLPrivate: "https://files.slack.com/files/canvas",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := files.DownloadResult{
		Error:      "Downloaded auth/login page instead of canvas content (token may be expired)",
		HTTPStatus: http.StatusOK,
		Path:       filepath.Join(h.c.downloadsDir, "F1.download-error.txt"),
	}
	if got["F1"] != want {
		t.Errorf("result = %+v\nwant %+v", got["F1"], want)
	}
	if b, _ := os.ReadFile(want.Path); !strings.Contains(string(b), "auth/login page") {
		t.Errorf("error file = %q", b)
	}
	if _, err := os.Stat(filepath.Join(h.c.downloadsDir, "F1.md")); err == nil {
		t.Error("a login page was saved as the canvas")
	}
}

func TestDownloadFailureIsRecorded(t *testing.T) {
	h := newHarness(t)
	h.serve = func(*http.Request) *http.Response { return httpResponse(http.StatusNotFound, "", "") }
	got, err := h.download(context.Background(), messageWith(messages.FileSummary{
		ID: "F2", Name: "report.txt", Mimetype: "text/plain", URLPrivate: "https://files.slack.com/files/report",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := files.DownloadResult{
		Error:      "Failed to download file (404)",
		HTTPStatus: http.StatusNotFound,
		Path:       filepath.Join(h.c.downloadsDir, "F2.download-error.txt"),
	}
	if got["F2"] != want {
		t.Errorf("result = %+v\nwant %+v", got["F2"], want)
	}
	// The file is not skipped: it stays in the output, with the error.
	if !strings.Contains(h.stderr.String(), "Warning: could not download file F2: Failed to download file (404)") || strings.Contains(h.stderr.String(), "skipping") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestDownloadCanvasAsMarkdown(t *testing.T) {
	h := newHarness(t)
	h.serve = func(*http.Request) *http.Response {
		return httpResponse(http.StatusOK, "text/html", "<html><body><h1>Plan</h1><p>Ship <b>it</b>.</p></body></html>")
	}
	canvas := messages.FileSummary{
		ID: "F3", Name: "plan", Mode: "quip",
		URLPrivate:         "https://files.slack.com/files-pri/T1-F3/plan",
		URLPrivateDownload: "https://files.slack.com/files-pri/T1-F3/download/plan",
	}
	got, err := h.download(context.Background(), messageWith(canvas))
	if err != nil {
		t.Fatal(err)
	}
	want := files.DownloadResult{OK: true, Path: filepath.Join(h.c.downloadsDir, "F3.md")}
	if got["F3"] != want {
		t.Fatalf("result = %+v, want %+v", got["F3"], want)
	}
	md, _ := os.ReadFile(want.Path)
	if !strings.Contains(string(md), "# Plan") || !strings.Contains(string(md), "**it**") {
		t.Errorf("markdown = %q", md)
	}
	// Canvases are read from url_private, not the download URL.
	if len(h.requests) != 1 || h.requests[0].url != canvas.URLPrivate {
		t.Errorf("requests = %+v", h.requests)
	}
	if _, err := os.Stat(filepath.Join(h.c.downloadsDir, "F3.html")); err == nil {
		t.Error("the intermediate HTML was left behind")
	}
	if info, _ := os.Stat(want.Path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}

	// A canvas changes: it is fetched again, not served from disk.
	h.serve = func(*http.Request) *http.Response {
		return httpResponse(http.StatusOK, "text/html", "<html><body><h1>Plan v2</h1></body></html>")
	}
	if _, err := h.download(context.Background(), messageWith(canvas)); err != nil {
		t.Fatal(err)
	}
	if md, _ := os.ReadFile(want.Path); !strings.Contains(string(md), "Plan v2") {
		t.Errorf("markdown after the second download = %q", md)
	}
}

func TestDownloadNamesAndDeduplicates(t *testing.T) {
	h := newHarness(t)
	h.serve = func(req *http.Request) *http.Response {
		return httpResponse(http.StatusOK, "application/octet-stream", "data")
	}
	png := messages.FileSummary{ID: "F10", Name: "shot", Mimetype: "image/png", URLPrivateDownload: "https://files.slack.com/files-pri/T1-F10/download/shot", URLPrivate: "https://files.slack.com/files-pri/T1-F10/shot"}
	zip := messages.FileSummary{ID: "F11", Name: "Logs.ZIP", Filetype: "zip", URLPrivate: "https://files.slack.com/files-pri/T1-F11/logs.zip"}
	bare := messages.FileSummary{ID: "F12", Name: "README", URLPrivateDownload: "https://files.slack.com/files-pri/T1-F12/download/README"}

	// The same file shared in two messages is downloaded once.
	got, err := h.download(context.Background(), messageWith(png, zip), messageWith(png, bare))
	if err != nil {
		t.Fatal(err)
	}
	for id, name := range map[string]string{"F10": "F10.png", "F11": "F11.zip", "F12": "F12"} {
		if want := (files.DownloadResult{OK: true, Path: filepath.Join(h.c.downloadsDir, name)}); got[id] != want {
			t.Errorf("%s: result = %+v, want %+v", id, got[id], want)
		}
	}
	if len(h.requests) != 3 {
		t.Errorf("%d requests, want 3", len(h.requests))
	}
	// The download URL is preferred over url_private.
	for _, r := range h.requests {
		if strings.Contains(r.url, "F10") && r.url != png.URLPrivateDownload {
			t.Errorf("F10 fetched from %s", r.url)
		}
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestDownloadProblemsNeverFailTheCommand(t *testing.T) {
	h := newHarness(t)
	got, err := h.download(context.Background(), messageWith(
		messages.FileSummary{ID: "F20", Name: "deleted.txt"},
		messages.FileSummary{ID: "F21", Name: "doc", URLPrivate: "https://docs.google.com/document/d/abc"},
		messages.FileSummary{ID: "F22", Name: "canvas", Mode: "canvas"},
	))
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"F20": "no download URL", "F21": "", "F22": "no download URL"} {
		res := got[id]
		if res.OK || res.Error == "" || !strings.Contains(res.Error, want) {
			t.Errorf("%s: result = %+v", id, res)
		}
		if res.Path != filepath.Join(h.c.downloadsDir, id+".download-error.txt") {
			t.Errorf("%s: path = %q", id, res.Path)
		}
	}
	// Credentials never leave Slack: nothing was requested.
	if len(h.requests) != 0 {
		t.Errorf("requests = %+v", h.requests)
	}

	// An unusable downloads directory (a local failure) is recorded too.
	h.c.downloadsDir = writeFile(t, "not-a-directory", "x")
	got, err = h.download(context.Background(), messageWith(messages.FileSummary{
		ID: "F23", Name: "a.txt", URLPrivateDownload: "https://files.slack.com/files-pri/T1-F23/download/a.txt",
	}))
	if err != nil {
		t.Fatalf("a local failure aborted the command: %v", err)
	}
	if res := got["F23"]; res.OK || res.Error == "" || res.Path != "" {
		t.Errorf("result = %+v", res)
	}
}

func TestDownloadsAreConcurrentButBounded(t *testing.T) {
	h := newHarness(t)
	var running, peak atomic.Int32
	var once sync.Once
	full := make(chan struct{})
	h.serve = func(*http.Request) *http.Response {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if n == downloadConcurrency {
			once.Do(func() { close(full) })
		}
		// Hold the slot until the pool is full (or give up waiting), so
		// that the downloads overlap.
		select {
		case <-full:
		case <-time.After(5 * time.Second):
		}
		return httpResponse(http.StatusOK, "application/octet-stream", "data")
	}
	var fs []messages.FileSummary
	for i := range 3 * downloadConcurrency {
		id := fmt.Sprintf("F%d", 100+i)
		fs = append(fs, messages.FileSummary{ID: id, URLPrivateDownload: "https://files.slack.com/files-pri/T1-" + id + "/download/x.bin"})
	}
	got, err := h.download(context.Background(), messageWith(fs...))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(fs) {
		t.Errorf("%d results, want %d", len(got), len(fs))
	}
	for id, res := range got {
		if !res.OK {
			t.Errorf("%s: %+v", id, res)
		}
	}
	if p := peak.Load(); p != downloadConcurrency {
		t.Errorf("peak concurrency = %d, want %d", p, downloadConcurrency)
	}
}

func TestDownloadCancelled(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.download(ctx, messageWith(messages.FileSummary{
		ID: "F30", URLPrivateDownload: "https://files.slack.com/files-pri/T1-F30/download/a.txt",
	}))
	if err == nil {
		t.Error("a cancelled download reported success")
	}
}

func TestInferFileExtension(t *testing.T) {
	tests := []struct {
		file messages.FileSummary
		want string
	}{
		{messages.FileSummary{Mimetype: "image/png"}, "png"},
		{messages.FileSummary{Filetype: "PNG"}, "png"},
		{messages.FileSummary{Mimetype: "image/jpeg"}, "jpg"},
		{messages.FileSummary{Mimetype: "image/jpg"}, "jpg"},
		{messages.FileSummary{Filetype: "jpeg"}, "jpg"},
		{messages.FileSummary{Mimetype: "image/webp"}, "webp"},
		{messages.FileSummary{Filetype: "gif"}, "gif"},
		{messages.FileSummary{Mimetype: "text/plain", Name: "notes.log"}, "txt"},
		{messages.FileSummary{Filetype: "text"}, "txt"},
		{messages.FileSummary{Filetype: "markdown"}, "md"},
		{messages.FileSummary{Mimetype: "text/markdown"}, "md"},
		{messages.FileSummary{Mimetype: "application/json"}, "json"},
		{messages.FileSummary{Name: "archive.TAR"}, "tar"},
		{messages.FileSummary{Title: "report.pdf"}, "pdf"},
		{messages.FileSummary{Name: "data.csv", Title: "ignored.pdf"}, "csv"},
		{messages.FileSummary{Name: "README"}, ""},
		{messages.FileSummary{Name: "weird.ext-with-dash"}, ""},
		{messages.FileSummary{Name: "../../etc/passwd"}, ""},
		{messages.FileSummary{}, ""},
	}
	for _, tt := range tests {
		if got := inferFileExtension(tt.file); got != tt.want {
			t.Errorf("inferFileExtension(%+v) = %q, want %q", tt.file, got, tt.want)
		}
	}
}
