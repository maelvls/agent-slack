package search

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/maelvls/slackcli/internal/slack/messages"
	"github.com/maelvls/slackcli/internal/slack/users"
)

// sentQuery runs a message search and returns the query Slack received.
func sentQuery(t *testing.T, f *fake, opts Options) string {
	t.Helper()
	f.searchPages("search.messages", 0)
	mustSearch(t, f, opts)
	return f.callsTo("search.messages")[0]["query"].(string)
}

// Port of "resolves a W-prefixed user ID to a from filter".
func TestQueryResolvesUserIDToHandle(t *testing.T) {
	f := newFake(t)
	f.on("users.info", func(p map[string]any) (map[string]any, error) {
		if !reflect.DeepEqual(p, map[string]any{"user": "W12345678"}) {
			t.Errorf("users.info params = %v", p)
		}
		return map[string]any{"user": map[string]any{"id": "W12345678", "name": "alice"}}, nil
	})
	opts := options(t, KindMessages, "deployment")
	opts.User = "W12345678"
	if got := sentQuery(t, f, opts); got != "deployment from:@alice" {
		t.Errorf("query = %q", got)
	}
}

func TestQueryKeepsUserFilterWhenHandleIsUnknown(t *testing.T) {
	f := newFake(t)
	f.on("users.info", func(map[string]any) (map[string]any, error) { return apiError("user_not_found") })
	opts := options(t, KindMessages, "deployment")
	opts.User = "W12345678"
	// The TS dropped the filter and returned everybody's messages.
	if got := sentQuery(t, f, opts); got != "deployment from:<@W12345678>" {
		t.Errorf("query = %q", got)
	}
}

func TestQueryUserForms(t *testing.T) {
	directory := func(f *fake) {
		f.on("users.list", func(map[string]any) (map[string]any, error) {
			return map[string]any{"members": []any{
				map[string]any{"id": "U11111111", "name": "alice", "profile": map[string]any{"display_name": "Alice Smith"}},
			}}, nil
		})
		f.on("users.info", func(map[string]any) (map[string]any, error) {
			return map[string]any{"user": map[string]any{"id": "U11111111", "name": "alice"}}, nil
		})
	}

	for user, want := range map[string]string{
		"@bob":        "x from:@bob",
		"bob.b-1_2":   "x from:@bob.b-1_2",
		"Alice Smith": "x from:@alice", // resolved, never spliced into the query
	} {
		f := newFake(t)
		directory(f)
		opts := options(t, KindMessages, "x")
		opts.User = user
		if got := sentQuery(t, f, opts); got != want {
			t.Errorf("user %q: query = %q, want %q", user, got, want)
		}
		if strings.HasPrefix(want, "x from:@bob") && len(f.callsTo("users.list"))+len(f.callsTo("users.info")) != 0 {
			t.Errorf("user %q: a handle needs no lookup", user)
		}
	}

	f := newFake(t)
	directory(f)
	opts := options(t, KindMessages, "x")
	opts.User = "bob in:#secret"
	_, err := Search(t.Context(), f.client(), opts)
	var notFound *users.NotFoundError
	if !errors.As(err, &notFound) {
		t.Errorf("err = %v, want an unknown user", err)
	}
}

func TestQueryDatesAndEmptiness(t *testing.T) {
	f := newFake(t)
	opts := options(t, KindMessages, "  smoke tests in:#alerts ")
	opts.After, opts.Before = " 2026-01-01", "2026-02-01"
	if got, want := sentQuery(t, f, opts), "smoke tests in:#alerts after:2026-01-01 before:2026-02-01"; got != want {
		t.Errorf("query = %q, want %q", got, want)
	}

	// Filters alone make a query.
	opts = options(t, KindMessages, "")
	opts.After = "2026-01-01"
	if got := sentQuery(t, newFake(t), opts); got != "after:2026-01-01" {
		t.Errorf("query = %q", got)
	}

	f = newFake(t)
	_, err := Search(t.Context(), f.client(), options(t, KindAll, "   "))
	if err == nil || !strings.Contains(err.Error(), "query is empty") || len(f.calls) != 0 {
		t.Errorf("err = %v, calls = %v", err, f.calls)
	}
}

func TestParseDayRange(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip(err)
	}
	r, err := parseDayRange("2026-01-01", "2026-01-03", paris)
	if err != nil {
		t.Fatal(err)
	}
	// Local midnights, not UTC ones: 2026-01-02 00:00 and 2026-01-03 00:00 CET.
	if r.oldest.Unix() != 1767308400 || r.latest.Unix() != 1767394800 {
		t.Errorf("window = %d..%d", r.oldest.Unix(), r.latest.Unix())
	}

	for _, tc := range []struct{ after, before, want string }{
		{"2026-13-01", "", "--after"},
		{"", "2026-02-30", "--before"},
		{"", "01/02/2026", "YYYY-MM-DD"},
		{"", "2026-1-2", "YYYY-MM-DD"},
		{"2026-01-01", "2026-01-02", "no day in between"},
		{"2026-01-02", "2026-01-01", "no day in between"},
	} {
		if _, err := parseDayRange(tc.after, tc.before, time.UTC); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("after %q before %q: err = %v, want %q", tc.after, tc.before, err, tc.want)
		}
	}
	if r, err := parseDayRange("", "", time.UTC); err != nil || !r.oldest.IsZero() || !r.latest.IsZero() {
		t.Errorf("no dates: %+v, %v", r, err)
	}
}

func TestMatcher(t *testing.T) {
	m, err := newMatcher(`  Deploy "smoke  tests" "from:prod" `)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"deploy", "smoke  tests", "from:prod"}; !reflect.DeepEqual(m.terms, want) {
		t.Errorf("terms = %q, want %q", m.terms, want)
	}
	if !m.matches("the smoke  tests FROM:PROD after DEPLOYment") || m.matches("deploy smoke tests from:prod") {
		t.Error("every term must match, phrases verbatim")
	}

	for _, q := range []string{"in:#general x", "x from:@bob", "-in:#general", "has:link", "IS:thread", "before:2026-01-01", "during:january"} {
		if _, err := newMatcher(q); err == nil {
			t.Errorf("%q: operator accepted", q)
		}
	}
	for _, q := range []string{"error: timeout", "http://example.com", "12:30", "in: x"} {
		if _, err := newMatcher(q); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
}

func TestParseContentType(t *testing.T) {
	for in, want := range map[string]ContentType{"": ContentAny, "any": ContentAny, " Image ": ContentImage, "TEXT": ContentText, "snippet": ContentSnippet, "file": ContentFile} {
		if got, err := ParseContentType(in); err != nil || got != want {
			t.Errorf("ParseContentType(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// The TS silently searched everything.
	if _, err := ParseContentType("images"); err == nil || !strings.Contains(err.Error(), "any, text, image, snippet or file") {
		t.Errorf("err = %v", err)
	}
}

// Port of "passesContentTypeFilter with failed downloads". The filter now
// looks at the message rather than at what was downloaded, so a failed
// (or skipped) download cannot change the answer.
func TestPassesMessage(t *testing.T) {
	msg := func(files ...messages.FileSummary) messages.Message {
		return messages.Message{ChannelID: "C123", TS: "1234567890.000001", Text: "hello", Files: files}
	}
	image := msg(messages.FileSummary{ID: "F1", Mimetype: "image/png"})
	snippet := msg(messages.FileSummary{ID: "F1", Mimetype: "text/plain", Mode: "snippet"})
	plain := msg()

	for _, tc := range []struct {
		name string
		msg  messages.Message
		ct   ContentType
		want bool
	}{
		{"image passes image", image, ContentImage, true},
		{"image passes file", image, ContentFile, true},
		{"image is not text", image, ContentText, false},
		{"image is not a snippet", image, ContentSnippet, false},
		{"image passes any", image, ContentAny, true},
		{"upper-case media type", msg(messages.FileSummary{ID: "F1", Mimetype: "IMAGE/PNG"}), ContentImage, true},
		{"snippet passes snippet", snippet, ContentSnippet, true},
		{"snippet passes file", snippet, ContentFile, true},
		{"snippet is not an image", snippet, ContentImage, false},
		{"no file passes text", plain, ContentText, true},
		{"no file is not an image", plain, ContentImage, false},
		{"no file is not a file", plain, ContentFile, false},
		{"no file passes any", plain, ContentAny, true},
	} {
		if got := passesMessage(tc.msg, tc.ct); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

func TestPassesFile(t *testing.T) {
	png := fileHit{mimetype: "Image/PNG"}
	snippet := fileHit{mimetype: "text/plain", mode: "snippet"}
	pdf := fileHit{mimetype: "application/pdf"}
	for _, tc := range []struct {
		f    fileHit
		ct   ContentType
		want bool
	}{
		{png, ContentImage, true}, {png, ContentText, false}, {png, ContentSnippet, false},
		{snippet, ContentSnippet, true}, {snippet, ContentText, true}, {snippet, ContentImage, false},
		{pdf, ContentFile, true}, {pdf, ContentAny, true}, {pdf, ContentText, false},
	} {
		if got := passesFile(tc.f, tc.ct); got != tc.want {
			t.Errorf("%+v as %s: got %v", tc.f, tc.ct, got)
		}
	}
}

func TestInferExt(t *testing.T) {
	for _, tc := range []struct{ mimetype, filetype, name, title, want string }{
		{"image/png", "", "", "", "png"},
		{"", "PNG", "", "", "png"},
		{"IMAGE/JPEG", "", "", "", "jpg"},
		{"image/jpg", "", "", "", "jpg"},
		{"", "jpeg", "", "", "jpg"},
		{"image/webp", "", "", "", "webp"},
		{"", "gif", "", "", "gif"},
		{"text/plain", "", "notes.log", "", "txt"},
		{"", "text", "", "", "txt"},
		{"text/markdown", "", "", "", "md"},
		{"", "md", "", "", "md"},
		{"application/json", "", "", "", "json"},
		{"application/pdf", "pdf", "Report.PDF", "", "pdf"},
		{"application/zip", "", "", "archive.tar.gz", "gz"},
		{"application/octet-stream", "", "name.bin", "title.dat", "bin"},
		{"application/octet-stream", "", "noext", "", ""},
		{"", "", "bad.ext-with-dash", "", ""},
	} {
		if got := inferExt(tc.mimetype, tc.filetype, tc.name, tc.title); got != tc.want {
			t.Errorf("inferExt(%q, %q, %q, %q) = %q, want %q", tc.mimetype, tc.filetype, tc.name, tc.title, got, tc.want)
		}
	}
	if got := downloadName("F1", "", "", "", ""); got != "F1" {
		t.Errorf("downloadName without extension = %q", got)
	}
}

func TestMore(t *testing.T) {
	newS := func(limit int, before string) *searcher {
		return &searcher{limit: limit, opts: Options{Location: time.UTC}, days: dayRange{before: before}}
	}
	// Results from Jan 5 down to Jan 2: continue before Jan 3.
	m := newS(20, "").more("messages", 532, ts(jan1+4*day), ts(jan1+day), "")
	if m.Total != 532 || m.Before != "2026-01-03" || !strings.Contains(m.Hint, "--before 2026-01-03") || !strings.Contains(m.Hint, "2026-01-02 will come back") {
		t.Errorf("more = %+v", m)
	}
	// Everything on one day: --before cannot make progress.
	m = newS(20, "").more("messages", 0, ts(jan1+60), ts(jan1), "")
	if m.Before != "" || !strings.Contains(m.Hint, "Every result returned is dated 2026-01-01") || !strings.Contains(m.Hint, "--limit (now 20, max 200)") {
		t.Errorf("same day: %+v", m)
	}
	// Already bounded by --before 2026-01-02: asking again changes nothing.
	m = newS(MaxLimit, "2026-01-02").more("files", 0, ts(jan1+60), ts(jan1), "")
	if m.Before != "" || strings.Contains(m.Hint, "--limit") {
		t.Errorf("at --before, max limit: %+v", m)
	}
	// Nothing to date the results with.
	m = newS(MaxLimit, "").more("files", 0, "", "", "")
	if m.Hint == "" {
		t.Error("empty hint")
	}
	m = newS(20, "").more("messages", 0, "", "", "It stopped.")
	if !strings.HasPrefix(m.Hint, "It stopped. ") {
		t.Errorf("reason: %q", m.Hint)
	}
}
