package messagecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/cmd/composecmd"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const (
	testWorkspace = "https://workspace.slack.com"
	testChannel   = "C12345678"
	testTS        = "1770165109.628379"
	testURL       = testWorkspace + "/archives/" + testChannel + "/p1770165109628379"
)

type call struct {
	method string
	params map[string]any
}

type composeCall struct {
	target, text string
	opts         composecmd.Options
}

type httpRequest struct {
	method, url, body string
}

// harness runs the "message" command against a temporary credentials
// store holding one workspace, a fake Slack API and a fake HTTP transport.
type harness struct {
	t      *testing.T
	rt     *clictx.Context
	c      *command
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	stdin  string

	// fixtures answer API methods; methods without one answer {ok: true}.
	fixtures map[string]func(params map[string]any) (map[string]any, error)
	// serve answers non-API HTTP requests (downloads, upload bytes).
	serve func(req *http.Request) *http.Response
	// composeResult is what the fake compose editor returns.
	composeResult map[string]any

	mu       sync.Mutex
	calls    []call
	requests []httpRequest
	composed []composeCall
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	// Nothing may leak in from, or out to, the developer's environment.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	for _, env := range []string{
		"SLACK_TOKEN", "SLACK_COOKIE_D", "SLACK_COOKIE", "SLACK_WORKSPACE_URL", "CI",
		"SLACKCLI_SAFE_MODE", "AGENT_SLACK_SAFE_MODE",
	} {
		t.Setenv(env, "")
	}

	h := &harness{
		t:        t,
		stdout:   &bytes.Buffer{},
		stderr:   &bytes.Buffer{},
		fixtures: map[string]func(map[string]any) (map[string]any, error){},
	}
	h.fixtures["chat.postMessage"] = func(p map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true, "channel": p["channel"], "ts": testTS}, nil
	}
	h.fixtures["search.messages"] = func(map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true, "messages": map[string]any{"matches": []any{
			map[string]any{"channel": map[string]any{"id": testChannel, "name": "general"}},
		}}}, nil
	}
	h.fixtures["conversations.open"] = func(map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true, "channel": map[string]any{"id": "D12345678"}}, nil
	}
	h.serve = func(*http.Request) *http.Response { return httpResponse(http.StatusOK, "", "") }

	store := &auth.Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
	if err := store.Upsert(auth.Workspace{
		URL:  testWorkspace,
		Auth: slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxp-test"},
	}); err != nil {
		t.Fatal(err)
	}
	h.rt = &clictx.Context{
		Store:  store,
		Stdout: h.stdout,
		Stderr: h.stderr,
		ClientOptions: []slackapi.Option{
			slackapi.WithHandler(h.handle),
			slackapi.WithHTTPClient(&http.Client{Transport: roundTripper(h.roundTrip)}),
		},
	}
	h.c = &command{
		rt:           h.rt,
		now:          time.Now,
		downloadsDir: filepath.Join(t.TempDir(), "downloads"),
		compose: func(_ context.Context, _ *clictx.Context, target, text string, opts composecmd.Options) (map[string]any, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.composed = append(h.composed, composeCall{target, text, opts})
			return h.composeResult, nil
		},
	}
	return h
}

func (h *harness) handle(_ context.Context, method string, params map[string]any) (map[string]any, error) {
	h.mu.Lock()
	h.calls = append(h.calls, call{method, params})
	fx := h.fixtures[method]
	h.mu.Unlock()
	if fx != nil {
		return fx(params)
	}
	return map[string]any{"ok": true}, nil
}

type roundTripper func(req *http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func (h *harness) roundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		req.Body.Close()
		body = string(b)
	}
	h.mu.Lock()
	h.requests = append(h.requests, httpRequest{req.Method, req.URL.String(), body})
	serve := h.serve
	h.mu.Unlock()
	resp := serve(req)
	resp.Request = req
	return resp, nil
}

func httpResponse(status int, contentType, body string) *http.Response {
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

// apiError is what the fake API returns for a Slack ok:false response.
func apiError(method, code string) error {
	return &slackapi.APIError{Method: method, Code: code, Response: map[string]any{"ok": false, "error": code}}
}

// run executes "message <args>" and returns what it printed on stdout.
func (h *harness) run(args ...string) (string, error) {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	h.mu.Lock()
	h.calls, h.requests, h.composed = nil, nil, nil
	h.mu.Unlock()
	cmd := h.command()
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return h.stdout.String(), err
}

// command builds the command the way the root does.
func (h *harness) command() *cobra.Command {
	cmd := newCommand(h.c)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetIn(strings.NewReader(h.stdin))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd
}

// mustRun is run for commands expected to succeed; it decodes the output.
func (h *harness) mustRun(args ...string) map[string]any {
	h.t.Helper()
	out, err := h.run(args...)
	if err != nil {
		h.t.Fatalf("message %s: %v", strings.Join(args, " "), err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		h.t.Fatalf("message %s: output is not a JSON object: %v\n%s", strings.Join(args, " "), err, out)
	}
	return v
}

// mustFail is run for commands expected to fail with an error containing
// want.
func (h *harness) mustFail(want string, args ...string) error {
	h.t.Helper()
	out, err := h.run(args...)
	if err == nil {
		h.t.Fatalf("message %s: expected an error containing %q, got output:\n%s", strings.Join(args, " "), want, out)
	}
	if !strings.Contains(err.Error(), want) {
		h.t.Fatalf("message %s: error = %q, want it to contain %q", strings.Join(args, " "), err, want)
	}
	if out != "" {
		h.t.Errorf("message %s: failed but printed:\n%s", strings.Join(args, " "), out)
	}
	return err
}

func (h *harness) callsTo(method string) []call {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []call
	for _, c := range h.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func (h *harness) methods() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.calls))
	for i, c := range h.calls {
		out[i] = c.method
	}
	return out
}

// wantCalls asserts the exact API calls made, in order.
func (h *harness) wantCalls(want ...call) {
	h.t.Helper()
	h.mu.Lock()
	got := append([]call(nil), h.calls...)
	h.mu.Unlock()
	if len(got) != len(want) {
		h.t.Fatalf("API calls = %v, want %d calls %v", got, len(want), want)
	}
	for i := range want {
		if got[i].method != want[i].method || !reflect.DeepEqual(got[i].params, want[i].params) {
			h.t.Errorf("API call %d = %s %#v\nwant %s %#v", i, got[i].method, got[i].params, want[i].method, want[i].params)
		}
	}
}

// wantJSON asserts that got equals the JSON document want.
func wantJSON(t *testing.T, got map[string]any, want string) {
	t.Helper()
	var w map[string]any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expectation: %v", err)
	}
	if !reflect.DeepEqual(got, w) {
		g, _ := json.MarshalIndent(got, "", "  ")
		t.Errorf("output =\n%s\nwant\n%s", g, want)
	}
}

// writeFile creates a file in a fresh temporary directory.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeBlocks writes v as a --blocks file and returns its path along with
// the blocks as the API will receive them.
func writeBlocks(t *testing.T, v any) (string, []any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatal(err)
	}
	return writeFile(t, "blocks.json", string(raw)), blocks
}

// addWorkspace configures a second workspace.
func (h *harness) addWorkspace(url string) {
	h.t.Helper()
	if err := h.rt.Store.Upsert(auth.Workspace{URL: url, Auth: slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxp-other"}}); err != nil {
		h.t.Fatal(err)
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// jsonRoundTrip returns v as Slack would decode it.
func jsonRoundTrip(t *testing.T, v any) any {
	t.Helper()
	var out any
	if err := json.Unmarshal([]byte(mustMarshal(t, v)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
