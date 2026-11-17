package canvascmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/canvas"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const (
	acmeURL  = "https://acme.slack.com"  // standard token
	otherURL = "https://other.slack.com" // browser credentials
)

type call struct {
	method string
	params map[string]any
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// harness runs the canvas command against a fake Slack and a temporary
// credentials store.
type harness struct {
	t      *testing.T
	rt     *clictx.Context
	stdout *bytes.Buffer
	stderr *bytes.Buffer

	// calls are the API calls made; respond answers them (default: ok).
	calls   []call
	respond func(method string, params map[string]any) map[string]any
	// downloads are the raw HTTP requests made; html is their answer.
	downloads []*http.Request
	html      string
}

// newHarness stores the given workspaces; the first one is the default.
func newHarness(t *testing.T, workspaces ...auth.Workspace) *harness {
	t.Helper()
	for _, env := range []string{"SLACK_TOKEN", "SLACK_COOKIE_D", "SLACK_COOKIE", "SLACK_WORKSPACE_URL", appinfo.EnvName("SAFE_MODE"), appinfo.LegacyEnvPrefix + "SAFE_MODE"} {
		t.Setenv(env, "")
	}
	h := &harness{t: t, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	hc := &http.Client{Transport: roundTripper(func(req *http.Request) (*http.Response, error) {
		h.downloads = append(h.downloads, req)
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"text/html"}},
			Body:       io.NopCloser(strings.NewReader(h.html)),
		}, nil
	})}
	h.rt = &clictx.Context{
		Store:  &auth.Store{Path: filepath.Join(t.TempDir(), "credentials.json")},
		Stdout: h.stdout,
		Stderr: h.stderr,
		ClientOptions: []slackapi.Option{
			slackapi.WithHTTPClient(hc),
			slackapi.WithHandler(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
				h.calls = append(h.calls, call{method, params})
				if h.respond == nil {
					return nil, nil
				}
				return h.respond(method, params), nil
			}),
		},
	}
	if len(workspaces) > 0 {
		if err := h.rt.Store.Upsert(workspaces...); err != nil {
			t.Fatal(err)
		}
		if err := h.rt.Store.SetDefault(workspaces[0].URL); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func standardWorkspace() auth.Workspace {
	return auth.Workspace{URL: acmeURL, Name: "Acme", Auth: slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxb-acme"}}
}

func browserWorkspace() auth.Workspace {
	return auth.Workspace{URL: otherURL, Name: "Other", Auth: slackapi.Auth{
		Type: slackapi.AuthBrowser, XoxcToken: "xoxc-other", XoxdCookie: "xoxd-other",
	}}
}

func (h *harness) answer(resp map[string]any) {
	h.respond = func(string, map[string]any) map[string]any { return resp }
}

func (h *harness) run(stdin string, args ...string) error {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	cmd := New(h.rt)
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.ExecuteContext(context.Background())
}

// output runs the command and returns the JSON it printed.
func (h *harness) output(stdin string, args ...string) map[string]any {
	h.t.Helper()
	if err := h.run(stdin, args...); err != nil {
		h.t.Fatalf("canvas %s: %v", strings.Join(args, " "), err)
	}
	var out map[string]any
	if err := json.Unmarshal(h.stdout.Bytes(), &out); err != nil {
		h.t.Fatalf("canvas %s printed invalid JSON: %v\n%s", strings.Join(args, " "), err, h.stdout)
	}
	return out
}

// fails runs the command and checks that it fails with an error
// containing substr, without having called Slack or printed a result.
func (h *harness) fails(substr string, args ...string) error {
	h.t.Helper()
	h.calls = nil
	err := h.run("", args...)
	if err == nil || !strings.Contains(err.Error(), substr) {
		h.t.Errorf("canvas %s: error = %v, want it to contain %q", strings.Join(args, " "), err, substr)
	}
	if len(h.calls) != 0 {
		h.t.Errorf("canvas %s called Slack: %v", strings.Join(args, " "), h.calls)
	}
	if h.stdout.Len() != 0 {
		h.t.Errorf("canvas %s printed %q", strings.Join(args, " "), h.stdout)
	}
	return err
}

func (h *harness) wantCalls(want ...call) {
	h.t.Helper()
	if !reflect.DeepEqual(h.calls, want) {
		h.t.Errorf("calls = %v\nwant    %v", h.calls, want)
	}
}

func wantJSON(t *testing.T, got, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("output = %v, want %v", got, want)
	}
}

func markdownContent(md string) map[string]any {
	return map[string]any{"type": "markdown", "markdown": md}
}

func writeFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCreateFromInlineMarkdownInTheSelectedWorkspace(t *testing.T) {
	// The default workspace has browser credentials: reaching
	// canvases.create proves --workspace picked the other one.
	h := newHarness(t, browserWorkspace(), standardWorkspace())
	h.answer(map[string]any{"ok": true, "canvas_id": "F12345678"})

	out := h.output("", "create", "--markdown", "# Inline\n", "--title", "Inline", "--workspace", "acme")

	h.wantCalls(call{"canvases.create", map[string]any{"title": "Inline", "document_content": markdownContent("# Inline\n")}})
	wantJSON(t, out, map[string]any{"canvas": map[string]any{"id": "F12345678", "title": "Inline"}})
}

func TestCreateFromAFileAsAChannelTab(t *testing.T) {
	h := newHarness(t, standardWorkspace())
	h.answer(map[string]any{"ok": true, "canvas_id": "F12345678"})
	path := writeFile(t, "plan.md", []byte("# Plan\n"))

	out := h.output("", "create", "--file", path, "--channel", "C12345678")

	h.wantCalls(call{"canvases.create", map[string]any{"document_content": markdownContent("# Plan\n"), "channel_id": "C12345678"}})
	wantJSON(t, out, map[string]any{"canvas": map[string]any{"id": "F12345678", "channel_id": "C12345678"}})
}

func TestCreateFromStandardInput(t *testing.T) {
	h := newHarness(t, standardWorkspace())
	h.answer(map[string]any{"ok": true, "canvas_id": "F12345678"})

	h.output("# Piped\n\n- one\n", "create", "--file", "-")

	h.wantCalls(call{"canvases.create", map[string]any{"document_content": markdownContent("# Piped\n\n- one\n")}})
}

func TestCreateWithBrowserCredentials(t *testing.T) {
	h := newHarness(t, browserWorkspace())
	h.answer(map[string]any{"ok": true, "file_id": "F87654321"})

	out := h.output("", "create", "--markdown", "# Browser auth\n")

	h.wantCalls(call{"files.createCanvas", map[string]any{"title": "Untitled", "markdown": "# Browser auth\n", "loosenValidation": true}})
	wantJSON(t, out, map[string]any{"canvas": map[string]any{"id": "F87654321", "title": "Untitled"}})
}

func TestCreateRejectsBrowserAuthChannelTabsBeforeResolvingTheChannel(t *testing.T) {
	h := newHarness(t, browserWorkspace())
	err := h.fails("requires a standard Slack token", "create", "--markdown", "# Browser auth\n", "--channel", "project-launch")
	if !errors.Is(err, canvas.ErrBrowserChannel) {
		t.Errorf("error = %v, want ErrBrowserChannel", err)
	}
}

func TestCreateRejectsBadSourcesBeforeAuthenticating(t *testing.T) {
	// No credentials at all: an attempt to authenticate would fail with
	// a different error.
	h := newHarness(t)
	binary := writeFile(t, "plan.bin", []byte{0xff, 0xfe, 0x00, 0x01})
	huge := writeFile(t, "huge.md", bytes.Repeat([]byte("x"), maxMarkdownBytes+1))
	blank := writeFile(t, "blank.md", []byte(" \n\t\n"))

	h.fails("pass exactly one Markdown source", "create")
	h.fails("pass exactly one Markdown source", "create", "--file", "plan.md", "--markdown", "# Inline")
	h.fails("canvas Markdown is empty", "create", "--markdown", "\n \t")
	h.fails("canvas Markdown is empty", "create", "--markdown", "")
	h.fails("canvas Markdown is empty", "create", "--file", blank)
	h.fails("could not read Markdown file", "create", "--file", filepath.Join(t.TempDir(), "missing.md"))
	h.fails("not valid UTF-8", "create", "--file", binary)
	h.fails("is larger than 4 MiB", "create", "--file", huge)
	h.fails("--channel is empty", "create", "--markdown", "x", "--channel", " ")
	h.fails("unknown command", "create", "extra", "--markdown", "x")
}

func TestCreateFailsWithoutACanvasID(t *testing.T) {
	h := newHarness(t, standardWorkspace())
	h.answer(map[string]any{"ok": true})
	err := h.run("", "create", "--markdown", "Hello")
	if err == nil || !strings.Contains(err.Error(), "returned no canvas ID") {
		t.Errorf("error = %v", err)
	}
	if h.stdout.Len() != 0 {
		t.Errorf("printed %q despite the failure", h.stdout)
	}
}

func TestCreateSurfacesSlackErrors(t *testing.T) {
	h := newHarness(t, standardWorkspace())
	h.answer(map[string]any{"ok": false, "error": "missing_scope", "needed": "canvases:write", "provided": "chat:write"})
	err := h.run("", "create", "--markdown", "Hello")
	if !slackapi.IsErrorCode(err, "missing_scope") || !strings.Contains(err.Error(), "canvases:write") {
		t.Errorf("error = %v, want missing_scope naming the scope", err)
	}
}

func TestEditReplacesACanvasGivenByURL(t *testing.T) {
	// The URL's workspace wins over the default one, whose browser
	// credentials could not edit.
	h := newHarness(t, browserWorkspace(), standardWorkspace())

	out := h.output("", "edit", acmeURL+"/docs/T12345678/F12345678", "--markdown", "# Revised\n")

	h.wantCalls(call{"canvases.edit", map[string]any{
		"canvas_id": "F12345678",
		"changes":   []any{map[string]any{"operation": "replace", "document_content": markdownContent("# Revised\n")}},
	}})
	wantJSON(t, out, map[string]any{"ok": true, "canvas": map[string]any{"id": "F12345678", "operation": "replace"}})
}

func TestEditInsertsFromAFileWithASectionID(t *testing.T) {
	h := newHarness(t, standardWorkspace())
	path := writeFile(t, "addition.md", []byte("Added\n"))

	h.output("", "edit", "F12345678", "--operation", "insert_after", "--section-id", "temp:C:SECTION", "--file", path)

	h.wantCalls(call{"canvases.edit", map[string]any{
		"canvas_id": "F12345678",
		"changes": []any{map[string]any{
			"operation":        "insert_after",
			"section_id":       "temp:C:SECTION",
			"document_content": markdownContent("Added\n"),
		}},
	}})
}

func TestEditRenamesWithoutAMarkdownSource(t *testing.T) {
	h := newHarness(t, standardWorkspace())

	out := h.output("", "edit", "F12345678", "--operation", "rename", "--title", "Renamed")

	h.wantCalls(call{"canvases.edit", map[string]any{
		"canvas_id": "F12345678",
		"changes":   []any{map[string]any{"operation": "rename", "title_content": markdownContent("Renamed")}},
	}})
	wantJSON(t, out, map[string]any{"ok": true, "canvas": map[string]any{"id": "F12345678", "operation": "rename"}})
}

func TestEditDeletesASection(t *testing.T) {
	h := newHarness(t, standardWorkspace())

	h.output("", "edit", "F12345678", "--operation", "delete", "--section-id", "temp:C:SECTION")

	h.wantCalls(call{"canvases.edit", map[string]any{
		"canvas_id": "F12345678",
		"changes":   []any{map[string]any{"operation": "delete", "section_id": "temp:C:SECTION"}},
	}})
}

func TestEditResolvesAChannelCanvasURL(t *testing.T) {
	h := newHarness(t, standardWorkspace())
	h.respond = func(method string, _ map[string]any) map[string]any {
		if method == "conversations.info" {
			return map[string]any{"ok": true, "channel": map[string]any{
				"properties": map[string]any{"canvas": map[string]any{"file_id": "F0CHANNEL1"}},
			}}
		}
		return nil
	}

	out := h.output("", "edit", acmeURL+"/canvas/C12345678", "--operation", "insert_at_end", "--markdown", "More")

	h.wantCalls(
		call{"conversations.info", map[string]any{"channel": "C12345678"}},
		call{"canvases.edit", map[string]any{
			"canvas_id": "F0CHANNEL1",
			"changes":   []any{map[string]any{"operation": "insert_at_end", "document_content": markdownContent("More")}},
		}},
	)
	wantJSON(t, out, map[string]any{"ok": true, "canvas": map[string]any{"id": "F0CHANNEL1", "operation": "insert_at_end"}})
}

func TestEditRejectsMalformedInputBeforeAuthenticating(t *testing.T) {
	h := newHarness(t) // no credentials: authenticating would fail differently
	for _, tt := range []struct {
		want string
		args []string
	}{
		{"does not accept Markdown content", []string{"F12345678", "--operation", "delete", "--section-id", "x", "--markdown", "nope"}},
		{"does not accept Markdown content", []string{"F12345678", "--operation", "rename", "--title", "x", "--file", "plan.md"}},
		{"unsupported canvas edit operation", []string{"F12345678", "--operation", "not-real", "--markdown", "nope"}},
		{"pass exactly one Markdown source", []string{"F12345678"}},
		{"pass exactly one Markdown source", []string{"F12345678", "--file", "a.md", "--markdown", "b"}},
		{"canvas Markdown is empty", []string{"F12345678", "--markdown", " "}},
		{`"insert_after" requires --section-id`, []string{"F12345678", "--operation", "insert_after", "--markdown", "x"}},
		{`"insert_at_start" cannot use --section-id`, []string{"F12345678", "--operation", "insert_at_start", "--markdown", "x", "--section-id", "s"}},
		{`"delete" requires --section-id`, []string{"F12345678", "--operation", "delete"}},
		{`"rename" requires a non-empty --title`, []string{"F12345678", "--operation", "rename"}},
		{`"rename" cannot use --section-id`, []string{"F12345678", "--operation", "rename", "--title", "x", "--section-id", "s"}},
		{"--title is only valid with the rename operation", []string{"F12345678", "--markdown", "x", "--title", "Wrong"}},
		{"unsupported canvas", []string{"F456", "--markdown", "x"}},
		{"unsupported canvas", []string{"C12345678", "--markdown", "x"}},
		{"not a Slack workspace URL", []string{"https://evil.example/docs/T12345678/F12345678", "--markdown", "x"}},
		{"not a canvas URL", []string{acmeURL + "/archives/C12345678", "--markdown", "x"}},
		{"accepts 1 arg(s)", []string{"--markdown", "x"}},
	} {
		h.fails(tt.want, append([]string{"edit"}, tt.args...)...)
	}
}

func TestEditRejectsBrowserCredentialsBeforeCallingSlack(t *testing.T) {
	h := newHarness(t, browserWorkspace())
	err := h.fails("standard Slack token", "edit", "F12345678", "--markdown", "# Revised")
	if !errors.Is(err, canvas.ErrBrowserEdit) {
		t.Errorf("error = %v, want ErrBrowserEdit", err)
	}
	// Not even the channel canvas lookup is attempted.
	h.fails("standard Slack token", "edit", otherURL+"/canvas/C12345678", "--markdown", "# Revised")
}

func TestEditSurfacesSlackErrors(t *testing.T) {
	h := newHarness(t, standardWorkspace())
	h.answer(map[string]any{"ok": false, "error": "canvas_not_found"})
	err := h.run("", "edit", "F12345678", "--markdown", "x")
	if !slackapi.IsErrorCode(err, "canvas_not_found") {
		t.Errorf("error = %v, want canvas_not_found", err)
	}
	if h.stdout.Len() != 0 {
		t.Errorf("printed %q despite the failure", h.stdout)
	}
}

func TestSafeModeBlocksWritesButNotReads(t *testing.T) {
	h := newHarness(t, standardWorkspace())
	h.rt.SafeModeFlag = true
	h.fails("safe mode is active", "create", "--markdown", "x")
	h.fails("safe mode is active", "edit", "F12345678", "--markdown", "x")

	h.rt.SafeModeFlag = false
	t.Setenv(appinfo.EnvName("SAFE_MODE"), "1")
	h.fails(`"canvas edit" is blocked`, "edit", "F12345678", "--operation", "delete", "--section-id", "s")

	h.answer(fileInfo())
	h.html = "<p>Hello</p>"
	h.output("", "get", "F12345678")
}

func fileInfo() map[string]any {
	return map[string]any{"ok": true, "file": map[string]any{
		"title":                "Launch plan",
		"url_private_download": "https://files.slack.com/files-pri/T1-F12345678/download/plan",
	}}
}

func TestGetFetchesACanvasGivenByURL(t *testing.T) {
	h := newHarness(t, browserWorkspace(), standardWorkspace())
	h.answer(fileInfo())
	h.html = "<html><body><h1>Plan</h1><p>Ship it.</p></body></html>"

	out := h.output("", "get", acmeURL+"/docs/T12345678/F12345678")

	h.wantCalls(call{"files.info", map[string]any{"file": "F12345678"}})
	wantJSON(t, out, map[string]any{"canvas": map[string]any{"id": "F12345678", "title": "Launch plan", "markdown": "# Plan\n\nShip it."}})
	if len(h.downloads) != 1 || h.downloads[0].URL.String() != "https://files.slack.com/files-pri/T1-F12345678/download/plan" {
		t.Fatalf("downloads = %v", h.downloads)
	}
	// The credentials of the URL's workspace, not of the default one.
	if got := h.downloads[0].Header.Get("Authorization"); got != "Bearer xoxb-acme" {
		t.Errorf("Authorization = %q, want the acme token", got)
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q", h.stderr)
	}
}

func TestGetByIDHonoursWorkspaceSelection(t *testing.T) {
	h := newHarness(t, browserWorkspace(), standardWorkspace())
	h.answer(fileInfo())
	h.html = "<p>Hello</p>"
	token := func() string {
		t.Helper()
		if len(h.downloads) == 0 {
			t.Fatal("no download")
		}
		return h.downloads[len(h.downloads)-1].Header.Get("Authorization")
	}

	h.output("", "get", "F12345678")
	if got := token(); got != "Bearer xoxc-other" {
		t.Errorf("default workspace: Authorization = %q", got)
	}
	h.output("", "get", "F12345678", "--workspace", "acme")
	if got := token(); got != "Bearer xoxb-acme" {
		t.Errorf("--workspace: Authorization = %q", got)
	}
	// The TypeScript version ignored SLACK_WORKSPACE_URL for canvas IDs.
	t.Setenv("SLACK_WORKSPACE_URL", acmeURL)
	h.output("", "get", "F12345678")
	if got := token(); got != "Bearer xoxb-acme" {
		t.Errorf("SLACK_WORKSPACE_URL: Authorization = %q", got)
	}
}

func TestGetResolvesAChannelCanvasURL(t *testing.T) {
	h := newHarness(t, standardWorkspace())
	h.respond = func(method string, _ map[string]any) map[string]any {
		if method == "conversations.info" {
			return map[string]any{"ok": true, "channel": map[string]any{
				"properties": map[string]any{"canvas": map[string]any{"file_id": "F0CHANNEL1"}},
			}}
		}
		return fileInfo()
	}
	h.html = "<p>Hello</p>"

	out := h.output("", "get", acmeURL+"/canvas/C12345678")

	h.wantCalls(
		call{"conversations.info", map[string]any{"channel": "C12345678"}},
		call{"files.info", map[string]any{"file": "F0CHANNEL1"}},
	)
	wantJSON(t, out, map[string]any{"canvas": map[string]any{"id": "F0CHANNEL1", "title": "Launch plan", "markdown": "Hello"}})
}

func TestGetReportsTruncation(t *testing.T) {
	h := newHarness(t, standardWorkspace())
	h.answer(fileInfo())
	h.html = "<p>0123456789</p>"

	out := h.output("", "get", "F12345678", "--max-chars", "4")
	wantJSON(t, out, map[string]any{"canvas": map[string]any{
		"id": "F12345678", "title": "Launch plan", "markdown": "0123\n…", "truncated": true, "total_chars": float64(10),
	}})
	if !strings.Contains(h.stderr.String(), "--max-chars -1") {
		t.Errorf("stderr = %q, want a hint about --max-chars", h.stderr)
	}

	out = h.output("", "get", "F12345678", "--max-chars", "-1")
	wantJSON(t, out, map[string]any{"canvas": map[string]any{"id": "F12345678", "title": "Launch plan", "markdown": "0123456789"}})
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q", h.stderr)
	}
}

func TestGetRejectsBadInputBeforeAuthenticating(t *testing.T) {
	h := newHarness(t)
	// The TypeScript version parsed "abc" to NaN, which meant "no limit".
	h.fails("invalid argument", "get", "F12345678", "--max-chars", "abc")
	h.fails("invalid argument", "get", "F12345678", "--max-chars", "10k")
	h.fails("invalid --max-chars -5", "get", "F12345678", "--max-chars", "-5")
	h.fails("unsupported canvas", "get", "launch-plan")
	h.fails("not a Slack workspace URL", "get", "http://acme.slack.com/docs/T12345678/F12345678")
	h.fails("no canvas ID", "get", acmeURL+"/docs/T12345678")
	h.fails("accepts 1 arg(s)", "get")
}

func TestGetSurfacesAnExpiredSession(t *testing.T) {
	h := newHarness(t, browserWorkspace())
	h.answer(fileInfo())
	h.html = `<html><title>Sign in | Slack</title><form action="/signin"></form></html>`
	err := h.run("", "get", "F12345678")
	if err == nil || !strings.Contains(err.Error(), "sign-in page") {
		t.Errorf("error = %v, want the sign-in page to be reported", err)
	}
	if h.stdout.Len() != 0 {
		t.Errorf("printed %q despite the failure", h.stdout)
	}
}

func findCommand(t *testing.T, root *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
	}
	t.Fatalf("missing command: canvas %s", name)
	return nil
}

func flagUsage(t *testing.T, cmd *cobra.Command, name string) string {
	t.Helper()
	f := cmd.Flags().Lookup(name)
	if f == nil {
		t.Fatalf("missing flag --%s on %s", name, cmd.Name())
	}
	return f.Usage
}

func wantContains(t *testing.T, what, got string, substrs ...string) {
	t.Helper()
	for _, s := range substrs {
		if !strings.Contains(got, s) {
			t.Errorf("%s = %q, want it to contain %q", what, got, s)
		}
	}
}

func TestHelpContracts(t *testing.T) {
	root := New(newHarness(t).rt)

	create := findCommand(t, root, "create")
	wantContains(t, "create description", create.Short, "exactly one Markdown source")
	wantContains(t, "create --file", flagUsage(t, create, "file"), "mutually exclusive", `"-"`)
	wantContains(t, "create --markdown", flagUsage(t, create, "markdown"), "mutually exclusive")
	wantContains(t, "create --channel", flagUsage(t, create, "channel"), "canvases:write", "browser auth")
	wantContains(t, "create help", create.Long, "safe mode")
	flagUsage(t, create, "title")
	flagUsage(t, create, "workspace")

	edit := findCommand(t, root, "edit")
	wantContains(t, "edit description", edit.Short, "one documented operation")
	wantContains(t, "edit --operation", flagUsage(t, edit, "operation"), canvas.Operations...)
	wantContains(t, "edit --file", flagUsage(t, edit, "file"), "mutually exclusive")
	wantContains(t, "edit --section-id", flagUsage(t, edit, "section-id"), "required")
	wantContains(t, "edit --title", flagUsage(t, edit, "title"), "rename")
	wantContains(t, "edit help", edit.Long, append([]string{"standard Slack token", "canvases:write", "safe mode", "WHOLE canvas"}, canvas.Operations...)...)
	if got := edit.Flags().Lookup("operation").DefValue; got != "replace" {
		t.Errorf("default --operation = %q, want replace", got)
	}
	flagUsage(t, edit, "workspace")

	get := findCommand(t, root, "get")
	wantContains(t, "get --max-chars", flagUsage(t, get, "max-chars"), "-1")
	if got := get.Flags().Lookup("max-chars").DefValue; got != "20000" {
		t.Errorf("default --max-chars = %q, want 20000", got)
	}
	wantContains(t, "get help", get.Long, "truncated", "/docs/", "/canvas/C")
	flagUsage(t, get, "workspace")
}
