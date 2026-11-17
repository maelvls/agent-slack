package canvas

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/slack/files"
	"github.com/maelvls/slackcli/internal/slackapi"
)

type call struct {
	method string
	params map[string]any
}

// recorder is a fake Slack API that answers every call with resp.
type recorder struct {
	calls []call
	resp  map[string]any
}

func (r *recorder) handle(_ context.Context, method string, params map[string]any) (map[string]any, error) {
	r.calls = append(r.calls, call{method, params})
	return r.resp, nil
}

func (r *recorder) standard(opts ...slackapi.Option) *slackapi.Client {
	auth := slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxb-test"}
	return slackapi.New(auth, "", append([]slackapi.Option{slackapi.WithHandler(r.handle)}, opts...)...)
}

func (r *recorder) browser() *slackapi.Client { return slackapi.NewFake(r.handle) }

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func httpResponse(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Fatalf("error = %v, want it to contain %q", err, substr)
	}
}

func markdownContent(md string) map[string]any {
	return map[string]any{"type": "markdown", "markdown": md}
}

func TestParseURL(t *testing.T) {
	tests := []struct {
		in      string
		want    Ref
		wantErr string
	}{
		{in: "https://acme.slack.com/docs/T12345678/F12345678", want: Ref{WorkspaceURL: "https://acme.slack.com", CanvasID: "F12345678"}},
		{in: " https://Acme.Enterprise.slack.com/docs/E123/F12345678?focus_section_id=temp:C:abc ", want: Ref{WorkspaceURL: "https://acme.enterprise.slack.com", CanvasID: "F12345678"}},
		{in: "https://acme.slack.com/canvas/C12345678", want: Ref{WorkspaceURL: "https://acme.slack.com", ChannelID: "C12345678"}},
		{in: "https://app.slack.com/docs/T12345678/F12345678", want: Ref{CanvasID: "F12345678"}},
		{in: "https://acme.slack.com/docs/T12345678", wantErr: "no canvas ID"},
		{in: "https://acme.slack.com/archives/C12345678/p1770165109628379", wantErr: "not a canvas URL"},
		{in: "https://acme.slack.com/canvas/nope", wantErr: "not a canvas URL"},
		{in: "http://acme.slack.com/docs/T12345678/F12345678", wantErr: "not a Slack workspace URL"},
		{in: "https://evil.example/docs/T12345678/F12345678", wantErr: "not a Slack workspace URL"},
		{in: "https://acme.slack.com.evil.example/docs/T12345678/F12345678", wantErr: "not a Slack workspace URL"},
		{in: "https://user@acme.slack.com/docs/T12345678/F12345678", wantErr: "not a Slack workspace URL"},
		{in: "F12345678", wantErr: "invalid canvas URL"},
	}
	for _, tt := range tests {
		got, err := ParseURL(tt.in)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ParseURL(%q) error = %v, want %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("ParseURL(%q) = %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
	}
}

func TestIsID(t *testing.T) {
	for in, want := range map[string]bool{"F12345678": true, "F080JDE025R": true, "F456": false, "C12345678": false, "f12345678": false, "": false} {
		if got := IsID(in); got != want {
			t.Errorf("IsID(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestChannelCanvasID(t *testing.T) {
	r := &recorder{resp: map[string]any{"ok": true, "channel": map[string]any{
		"properties": map[string]any{"canvas": map[string]any{"file_id": "F12345678"}},
	}}}
	id, err := ChannelCanvasID(context.Background(), r.standard(), "C12345678")
	if err != nil || id != "F12345678" {
		t.Fatalf("ChannelCanvasID = %q, %v", id, err)
	}
	want := []call{{"conversations.info", map[string]any{"channel": "C12345678"}}}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}

	r = &recorder{resp: map[string]any{"ok": true, "channel": map[string]any{"id": "C12345678"}}}
	_, err = ChannelCanvasID(context.Background(), r.standard(), "C12345678")
	wantErr(t, err, "has no channel canvas")

	r = &recorder{resp: map[string]any{"ok": false, "error": "channel_not_found"}}
	_, err = ChannelCanvasID(context.Background(), r.standard(), "C12345678")
	if !slackapi.IsErrorCode(err, "channel_not_found") {
		t.Errorf("error = %v, want channel_not_found", err)
	}
}

func TestCreateSendsMarkdownTitleAndChannel(t *testing.T) {
	r := &recorder{resp: map[string]any{"ok": true, "canvas_id": "F12345678"}}
	got, err := Create(context.Background(), r.standard(), CreateInput{
		Markdown:  "# Launch plan\n\n- Ship it\n",
		Title:     "  Launch plan  ",
		ChannelID: "C12345678",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []call{{"canvases.create", map[string]any{
		"title":            "Launch plan",
		"document_content": markdownContent("# Launch plan\n\n- Ship it\n"),
		"channel_id":       "C12345678",
	}}}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}
	if want := (CreateResult{Canvas: Created{ID: "F12345678", Title: "Launch plan", ChannelID: "C12345678"}}); got != want {
		t.Errorf("result = %+v, want %+v", got, want)
	}
}

func TestCreateOmitsUnsetTitleAndChannel(t *testing.T) {
	r := &recorder{resp: map[string]any{"ok": true, "canvas_id": "F12345678"}}
	got, err := Create(context.Background(), r.standard(), CreateInput{Markdown: "Hello", Title: "  "})
	if err != nil {
		t.Fatal(err)
	}
	want := []call{{"canvases.create", map[string]any{"document_content": markdownContent("Hello")}}}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}
	if want := (CreateResult{Canvas: Created{ID: "F12345678"}}); got != want {
		t.Errorf("result = %+v, want %+v", got, want)
	}
}

func TestCreateRejectsEmptyMarkdownBeforeCallingSlack(t *testing.T) {
	r := &recorder{}
	_, err := Create(context.Background(), r.standard(), CreateInput{Markdown: "  \n"})
	if !errors.Is(err, ErrEmptyMarkdown) {
		t.Errorf("error = %v, want ErrEmptyMarkdown", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("calls = %v, want none", r.calls)
	}
}

func TestCreateRejectsSuccessWithoutCanvasID(t *testing.T) {
	r := &recorder{resp: map[string]any{"ok": true}}
	_, err := Create(context.Background(), r.standard(), CreateInput{Markdown: "Hello"})
	wantErr(t, err, "returned no canvas ID")
}

func TestCreateSurfacesSlackErrors(t *testing.T) {
	r := &recorder{resp: map[string]any{"ok": false, "error": "free_teams_cannot_create_non_tabbed_canvases"}}
	_, err := Create(context.Background(), r.standard(), CreateInput{Markdown: "Hello"})
	if !slackapi.IsErrorCode(err, "free_teams_cannot_create_non_tabbed_canvases") {
		t.Fatalf("error = %v, want the Slack error code to be kept", err)
	}
	wantErr(t, err, "pass --channel")
}

func TestCreateWithBrowserCredentialsUsesFilesCreateCanvas(t *testing.T) {
	r := &recorder{resp: map[string]any{"ok": true, "file_id": "F87654321"}}
	got, err := Create(context.Background(), r.browser(), CreateInput{Markdown: "# Browser auth\n"})
	if err != nil {
		t.Fatal(err)
	}
	want := []call{{"files.createCanvas", map[string]any{
		"title":            "Untitled",
		"markdown":         "# Browser auth\n",
		"loosenValidation": true,
	}}}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}
	if want := (CreateResult{Canvas: Created{ID: "F87654321", Title: "Untitled"}}); got != want {
		t.Errorf("result = %+v, want %+v", got, want)
	}
}

// files.createCanvas ignores form-encoded bodies, so the request has to go
// out as multipart/form-data.
func TestCreateWithBrowserCredentialsIsMultipart(t *testing.T) {
	var form map[string][]string
	hc := &http.Client{Transport: roundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://test.slack.com/api/files.createCanvas" {
			t.Errorf("unexpected request to %s", req.URL)
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("request is not multipart: %v (Content-Type %q)", err, req.Header.Get("Content-Type"))
		} else {
			form = req.MultipartForm.Value
		}
		return httpResponse(200, "application/json", `{"ok":true,"file_id":"F87654321"}`), nil
	})}
	auth := slackapi.Auth{Type: slackapi.AuthBrowser, XoxcToken: "xoxc-test", XoxdCookie: "xoxd-test"}
	client := slackapi.New(auth, "https://test.slack.com", slackapi.WithHTTPClient(hc))

	got, err := Create(context.Background(), client, CreateInput{Markdown: "# Plan\n", Title: "Plan"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Canvas.ID != "F87654321" {
		t.Errorf("canvas ID = %q", got.Canvas.ID)
	}
	for field, want := range map[string]string{"title": "Plan", "markdown": "# Plan\n", "loosenValidation": "true"} {
		if v := form[field]; len(v) != 1 || v[0] != want {
			t.Errorf("multipart field %s = %q, want %q", field, v, want)
		}
	}
}

func TestCreateRejectsChannelTabsWithBrowserCredentials(t *testing.T) {
	r := &recorder{resp: map[string]any{"ok": true, "file_id": "F87654321"}}
	_, err := Create(context.Background(), r.browser(), CreateInput{Markdown: "# Browser auth\n", ChannelID: "C12345678"})
	if !errors.Is(err, ErrBrowserChannel) {
		t.Errorf("error = %v, want ErrBrowserChannel", err)
	}
	wantErr(t, err, "requires a standard Slack token")
	if len(r.calls) != 0 {
		t.Errorf("calls = %v, want none", r.calls)
	}
}

func TestApplySendsTheDocumentedChange(t *testing.T) {
	tests := []struct {
		name string
		edit Edit
		want map[string]any
	}{
		{"replace", Edit{Operation: "replace", Markdown: "# Revised\n"},
			map[string]any{"operation": "replace", "document_content": markdownContent("# Revised\n")}},
		{"replace section", Edit{Operation: "replace", SectionID: "temp:C:SECTION", Markdown: "Updated section"},
			map[string]any{"operation": "replace", "section_id": "temp:C:SECTION", "document_content": markdownContent("Updated section")}},
		{"insert before", Edit{Operation: "insert_before", SectionID: "temp:C:SECTION", Markdown: "Before"},
			map[string]any{"operation": "insert_before", "section_id": "temp:C:SECTION", "document_content": markdownContent("Before")}},
		{"insert after", Edit{Operation: "insert_after", SectionID: " temp:C:SECTION ", Markdown: "After"},
			map[string]any{"operation": "insert_after", "section_id": "temp:C:SECTION", "document_content": markdownContent("After")}},
		{"insert at start", Edit{Operation: "insert_at_start", Markdown: "Start"},
			map[string]any{"operation": "insert_at_start", "document_content": markdownContent("Start")}},
		{"insert at end", Edit{Operation: "insert_at_end", Markdown: "End"},
			map[string]any{"operation": "insert_at_end", "document_content": markdownContent("End")}},
		{"delete section", Edit{Operation: "delete", SectionID: "temp:C:SECTION"},
			map[string]any{"operation": "delete", "section_id": "temp:C:SECTION"}},
		{"rename", Edit{Operation: "rename", Title: " Project status "},
			map[string]any{"operation": "rename", "title_content": markdownContent("Project status")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &recorder{resp: map[string]any{"ok": true}}
			tt.edit.CanvasID = " F12345678 "
			got, err := Apply(context.Background(), r.standard(), tt.edit)
			if err != nil {
				t.Fatal(err)
			}
			want := []call{{"canvases.edit", map[string]any{"canvas_id": "F12345678", "changes": []any{tt.want}}}}
			if !reflect.DeepEqual(r.calls, want) {
				t.Errorf("calls = %v, want %v", r.calls, want)
			}
			if want := (EditResult{OK: true, Canvas: EditedCanvas{ID: "F12345678", Operation: tt.edit.Operation}}); got != want {
				t.Errorf("result = %+v, want %+v", got, want)
			}
		})
	}
}

func TestApplyRejectsInvalidCombinationsBeforeCallingSlack(t *testing.T) {
	long := strings.Repeat("x", MaxEditChars+1)
	tests := []struct {
		edit Edit
		want string
	}{
		{Edit{Operation: "insert_after", Markdown: "Body"}, `operation "insert_after" requires --section-id`},
		{Edit{Operation: "insert_before", Markdown: "Body"}, `operation "insert_before" requires --section-id`},
		{Edit{Operation: "insert_at_end", Markdown: "Body", SectionID: "temp:C:SECTION"}, `operation "insert_at_end" cannot use --section-id`},
		{Edit{Operation: "insert_at_start", Markdown: "Body", SectionID: "temp:C:SECTION"}, `operation "insert_at_start" cannot use --section-id`},
		{Edit{Operation: "delete"}, `operation "delete" requires --section-id`},
		{Edit{Operation: "delete", SectionID: "temp:C:SECTION", Markdown: "Body"}, `operation "delete" does not accept Markdown content`},
		{Edit{Operation: "delete", SectionID: "temp:C:SECTION", Title: "Wrong"}, "--title is only valid with the rename operation"},
		{Edit{Operation: "rename"}, `operation "rename" requires a non-empty --title`},
		{Edit{Operation: "rename", Title: "  "}, `operation "rename" requires a non-empty --title`},
		{Edit{Operation: "rename", Title: "New", Markdown: "Body"}, `operation "rename" does not accept Markdown content`},
		{Edit{Operation: "rename", Title: "New", SectionID: "temp:C:SECTION"}, `operation "rename" cannot use --section-id`},
		{Edit{Operation: "rename", Title: long}, "character limit"},
		{Edit{Operation: "replace", Markdown: "Body", Title: "Wrong"}, "--title is only valid with the rename operation"},
		{Edit{Operation: "replace", Markdown: " \n"}, "requires non-empty Markdown"},
		{Edit{Operation: "replace"}, "requires non-empty Markdown"},
		{Edit{Operation: "replace", Markdown: long}, "character limit"},
		{Edit{Operation: "nope", Markdown: "Body"}, `unsupported canvas edit operation "nope"`},
		{Edit{Operation: "", Markdown: "Body"}, "unsupported canvas edit operation"},
	}
	for _, tt := range tests {
		r := &recorder{resp: map[string]any{"ok": true}}
		if err := tt.edit.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Validate(%+.60v) = %v, want %q", tt.edit, err, tt.want)
		}
		tt.edit.CanvasID = "F12345678"
		if _, err := Apply(context.Background(), r.standard(), tt.edit); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Apply(%+.60v) = %v, want %q", tt.edit, err, tt.want)
		}
		if len(r.calls) != 0 {
			t.Errorf("Apply(%+.60v) called Slack: %v", tt.edit, r.calls)
		}
	}
}

func TestApplyRequiresACanvasID(t *testing.T) {
	r := &recorder{resp: map[string]any{"ok": true}}
	_, err := Apply(context.Background(), r.standard(), Edit{CanvasID: " ", Operation: "delete", SectionID: "temp:C:SECTION"})
	wantErr(t, err, "canvas ID is required")
	if len(r.calls) != 0 {
		t.Errorf("calls = %v, want none", r.calls)
	}
}

// The limit is in characters, not bytes: a document of multi-byte
// characters within the limit is accepted.
func TestApplyCountsCharactersNotBytes(t *testing.T) {
	r := &recorder{resp: map[string]any{"ok": true}}
	edit := Edit{CanvasID: "F12345678", Operation: "replace", Markdown: strings.Repeat("é", MaxEditChars)}
	if _, err := Apply(context.Background(), r.standard(), edit); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRejectsBrowserCredentialsBeforeCallingSlack(t *testing.T) {
	r := &recorder{resp: map[string]any{"ok": true}}
	_, err := Apply(context.Background(), r.browser(), Edit{CanvasID: "F12345678", Operation: "replace", Markdown: "# Revised"})
	if !errors.Is(err, ErrBrowserEdit) {
		t.Errorf("error = %v, want ErrBrowserEdit", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("calls = %v, want none", r.calls)
	}
}

func TestApplySurfacesSlackErrors(t *testing.T) {
	r := &recorder{resp: map[string]any{"ok": false, "error": "canvas_not_found"}}
	_, err := Apply(context.Background(), r.standard(), Edit{CanvasID: "F12345678", Operation: "replace", Markdown: "x"})
	if !slackapi.IsErrorCode(err, "canvas_not_found") {
		t.Fatalf("error = %v, want canvas_not_found", err)
	}
	wantErr(t, err, "editing canvas F12345678")
}

func TestNeedsMarkdown(t *testing.T) {
	for _, op := range Operations {
		if got, want := NeedsMarkdown(op), op != "delete" && op != "rename"; got != want {
			t.Errorf("NeedsMarkdown(%q) = %v, want %v", op, got, want)
		}
	}
	if NeedsMarkdown("nope") {
		t.Error(`NeedsMarkdown("nope") = true`)
	}
}

const canvasHTML = `<html><head><title>Plan</title></head><body><h1>Plan</h1><p>Ship <b>it</b>.</p></body></html>`

// fetchClient serves files.info from info and the download from respond.
func fetchClient(t *testing.T, r *recorder, respond func(*http.Request) *http.Response) *slackapi.Client {
	t.Helper()
	hc := &http.Client{Transport: roundTripper(func(req *http.Request) (*http.Response, error) {
		return respond(req), nil
	})}
	return r.standard(slackapi.WithHTTPClient(hc))
}

func fileInfo(fields map[string]any) map[string]any {
	return map[string]any{"ok": true, "file": fields}
}

func TestFetchConvertsTheCanvasToMarkdown(t *testing.T) {
	r := &recorder{resp: fileInfo(map[string]any{
		"title":                "  Launch plan ",
		"name":                 "launch_plan",
		"url_private":          "https://files.slack.com/files-pri/T1-F12345678/plan",
		"url_private_download": "https://files.slack.com/files-pri/T1-F12345678/download/plan",
	})}
	var requests []*http.Request
	client := fetchClient(t, r, func(req *http.Request) *http.Response {
		requests = append(requests, req)
		return httpResponse(200, "text/html; charset=utf-8", canvasHTML)
	})

	got, err := Fetch(context.Background(), client, "F12345678", DefaultMaxChars)
	if err != nil {
		t.Fatal(err)
	}
	want := FetchResult{Canvas: Fetched{ID: "F12345678", Title: "Launch plan", Markdown: "# Plan\n\nShip **it**."}}
	if got != want {
		t.Errorf("result = %+v, want %+v", got, want)
	}
	if want := []call{{"files.info", map[string]any{"file": "F12345678"}}}; !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %v, want %v", r.calls, want)
	}
	if len(requests) != 1 || requests[0].URL.String() != "https://files.slack.com/files-pri/T1-F12345678/download/plan" {
		t.Fatalf("download requests = %v", requests)
	}
	if got := requests[0].Header.Get("Authorization"); got != "Bearer xoxb-test" {
		t.Errorf("Authorization = %q", got)
	}
}

// The TypeScript version kept <id>.html in the downloads directory and
// reused it forever, so an edited canvas was read back stale.
func TestFetchNeverServesAStaleCopy(t *testing.T) {
	r := &recorder{resp: fileInfo(map[string]any{"name": "plan", "url_private": "https://files.slack.com/files-pri/T1-F12345678/plan"})}
	body := "<p>first</p>"
	client := fetchClient(t, r, func(*http.Request) *http.Response { return httpResponse(200, "text/html", body) })

	for _, want := range []string{"first", "second"} {
		body = "<p>" + want + "</p>"
		got, err := Fetch(context.Background(), client, "F12345678", -1)
		if err != nil {
			t.Fatal(err)
		}
		if got.Canvas.Markdown != want || got.Canvas.Title != "plan" {
			t.Errorf("canvas = %+v, want Markdown %q", got.Canvas, want)
		}
	}
}

func TestFetchTruncatesVisibly(t *testing.T) {
	r := &recorder{resp: fileInfo(map[string]any{"url_private": "https://files.slack.com/f"})}
	client := fetchClient(t, r, func(*http.Request) *http.Response {
		return httpResponse(200, "text/html", "<p>héllo wörld</p>")
	})

	got, err := Fetch(context.Background(), client, "F12345678", 4)
	if err != nil {
		t.Fatal(err)
	}
	// Cut on a character boundary, not in the middle of "é".
	if want := (Fetched{ID: "F12345678", Markdown: "héll\n…", Truncated: true, TotalChars: 11}); got.Canvas != want {
		t.Errorf("canvas = %+v, want %+v", got.Canvas, want)
	}

	for _, limit := range []int{-1, 11, 12} {
		got, err := Fetch(context.Background(), client, "F12345678", limit)
		if err != nil {
			t.Fatal(err)
		}
		if want := (Fetched{ID: "F12345678", Markdown: "héllo wörld"}); got.Canvas != want {
			t.Errorf("limit %d: canvas = %+v, want %+v", limit, got.Canvas, want)
		}
	}
}

func TestFetchErrors(t *testing.T) {
	ok := func(*http.Request) *http.Response { return httpResponse(200, "text/html", canvasHTML) }
	info := fileInfo(map[string]any{"filetype": "quip", "url_private": "https://files.slack.com/f"})
	tests := []struct {
		name    string
		info    map[string]any
		respond func(*http.Request) *http.Response
		want    string
		check   func(error) bool
	}{
		{name: "unknown file", info: map[string]any{"ok": false, "error": "file_not_found"}, respond: ok,
			want: "check the canvas ID", check: func(err error) bool { return slackapi.IsErrorCode(err, "file_not_found") }},
		{name: "no file in the response", info: map[string]any{"ok": true}, respond: ok, want: "files.info returned no file"},
		{name: "no download URL", info: fileInfo(map[string]any{"title": "x"}), respond: ok, want: "has no download URL"},
		{name: "external URL", info: fileInfo(map[string]any{"url_private": "https://evil.example/f"}), respond: ok,
			want: "Refusing to send Slack credentials", check: func(err error) bool { return errors.Is(err, files.ErrNotSlackHost) }},
		{name: "HTTP error", info: info,
			respond: func(*http.Request) *http.Response { return httpResponse(403, "text/html", "nope") },
			want:    "HTTP 403"},
		{name: "sign-in page", info: info,
			respond: func(*http.Request) *http.Response {
				return httpResponse(200, "text/html", `<html><title>Sign in | Slack</title><form action="/signin"></form></html>`)
			},
			want: "auth import-desktop", check: func(err error) bool { return errors.Is(err, files.ErrLoginPage) }},
		{name: "binary file", info: fileInfo(map[string]any{"filetype": "pdf", "url_private": "https://files.slack.com/f"}),
			respond: func(*http.Request) *http.Response { return httpResponse(200, "application/pdf", "%PDF\xff\xfe\x00") },
			want:    `is not a canvas (file type "pdf")`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &recorder{resp: tt.info}
			requested := false
			client := fetchClient(t, r, func(req *http.Request) *http.Response {
				requested = true
				return tt.respond(req)
			})
			_, err := Fetch(context.Background(), client, "F12345678", DefaultMaxChars)
			wantErr(t, err, tt.want)
			if tt.check != nil && !tt.check(err) {
				t.Errorf("error %v does not carry its cause", err)
			}
			if tt.name == "external URL" && requested {
				t.Error("a request was sent to a non-Slack host")
			}
		})
	}
}
