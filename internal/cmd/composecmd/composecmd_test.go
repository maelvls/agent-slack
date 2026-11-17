package composecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/authextract"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const workspaceURL = "https://workspace.slack.com"

type apiCall struct {
	method string
	params map[string]any
}

// harness runs "compose" against a fake Slack and a scripted human.
type harness struct {
	t      *testing.T
	rt     *clictx.Context
	c      *composer
	stdout *bytes.Buffer
	stderr *bytes.Buffer

	// human is what the person at the browser does once the editor is
	// open. nil means the browser must not be opened at all.
	human func(b *browser)
	// openErr makes opening the browser fail.
	openErr error
	// failOnce makes the first chat.postMessage fail with that code.
	failOnce string

	mu       sync.Mutex
	calls    []apiCall
	opened   []string // URLs given to the browser opener
	humans   sync.WaitGroup
	failedAt bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	for _, env := range []string{"SLACK_TOKEN", "SLACK_COOKIE_D", "SLACK_COOKIE", "SLACK_WORKSPACE_URL", "CI",
		appinfo.EnvName("SAFE_MODE"), "AGENT_SLACK_SAFE_MODE"} {
		t.Setenv(env, "")
	}
	h := &harness{t: t, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	store := &auth.Store{Path: filepath.Join(t.TempDir(), "credentials.json"), Service: "svc"}
	if err := store.Upsert(auth.Workspace{
		URL:  workspaceURL,
		Auth: slackapi.Auth{Type: slackapi.AuthBrowser, XoxcToken: "xoxc-test", XoxdCookie: "xoxd-test"},
	}); err != nil {
		t.Fatal(err)
	}
	h.rt = &clictx.Context{
		Store:         store,
		Stdout:        h.stdout,
		Stderr:        h.stderr,
		ClientOptions: []slackapi.Option{slackapi.WithHandler(h.slack)},
	}
	h.c = &composer{
		rt:            h.rt,
		openBrowser:   h.openBrowser,
		humanAtStderr: func() bool { return false },
		timeouts:      patient,
	}
	t.Cleanup(h.humans.Wait)
	return h
}

func (h *harness) slack(_ context.Context, method string, params map[string]any) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, apiCall{method, params})
	switch method {
	case "search.messages":
		return map[string]any{"ok": true, "messages": map[string]any{"matches": []any{
			map[string]any{"channel": map[string]any{"id": "C12345678", "name": "general"}},
		}}}, nil
	case "conversations.info":
		if strings.HasPrefix(params["channel"].(string), "D") {
			return map[string]any{"ok": true, "channel": map[string]any{"is_im": true, "user": "U12345678"}}, nil
		}
		return map[string]any{"ok": true, "channel": map[string]any{"name": "general"}}, nil
	case "users.info":
		return map[string]any{"ok": true, "user": map[string]any{"profile": map[string]any{"display_name": "alice"}}}, nil
	case "conversations.open":
		return map[string]any{"ok": true, "channel": map[string]any{"id": "D99999999"}}, nil
	case "conversations.history":
		return map[string]any{"ok": true, "messages": []any{
			map[string]any{"ts": params["latest"], "text": "root"},
		}}, nil
	case "chat.postMessage":
		if h.failOnce != "" && !h.failedAt {
			h.failedAt = true
			return map[string]any{"ok": false, "error": h.failOnce}, nil
		}
		return map[string]any{"ok": true, "channel": params["channel"], "ts": "1770170000.000200"}, nil
	}
	return map[string]any{"ok": true}, nil
}

// openBrowser stands in for the real browser launcher: it lets the
// scripted human loose on the editor.
func (h *harness) openBrowser(_ context.Context, pageURL string) error {
	h.mu.Lock()
	h.opened = append(h.opened, pageURL)
	h.mu.Unlock()
	if h.openErr != nil {
		return h.openErr
	}
	if h.human == nil {
		h.t.Error("the browser was opened")
		return nil
	}
	h.visit(pageURL)
	return nil
}

// visit has the scripted human open pageURL.
func (h *harness) visit(pageURL string) {
	u, err := url.Parse(pageURL)
	if err != nil {
		h.t.Errorf("editor URL %q: %v", pageURL, err)
		return
	}
	h.humans.Go(func() {
		b := newBrowser(h.t, "http://"+u.Host)
		b.open(pageURL)
		h.human(b)
	})
}

// sends is a human who replaces the text and presses Send.
func sends(text string) func(*browser) {
	return func(b *browser) {
		if status, resp := b.post("/send", map[string]any{"text": text}); status != http.StatusOK {
			b.t.Errorf("send: %d %+v", status, resp)
		}
	}
}

// sendsAsIs is a human who presses Send without touching the text.
func sendsAsIs(b *browser) {
	sends(b.config["initialText"].(string))(b)
}

func cancels(b *browser) {
	if status, resp := b.post("/cancel", nil); status != http.StatusOK {
		b.t.Errorf("cancel: %d %+v", status, resp)
	}
}

// run executes "compose <args>" and returns the decoded result.
func (h *harness) run(args ...string) (map[string]any, error) {
	h.t.Helper()
	h.stdout.Reset()
	cmd := newCommand(h.c)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); err != nil {
		if h.stdout.Len() != 0 {
			h.t.Errorf("a failed command printed a result: %s", h.stdout)
		}
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(h.stdout.Bytes(), &out); err != nil {
		h.t.Fatalf("output is not JSON: %v\n%s", err, h.stdout)
	}
	return out, nil
}

func (h *harness) mustRun(args ...string) map[string]any {
	h.t.Helper()
	out, err := h.run(args...)
	if err != nil {
		h.t.Fatalf("compose %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func (h *harness) callsTo(method string) []apiCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []apiCall
	for _, c := range h.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

// posted returns the parameters of the one chat.postMessage call.
func (h *harness) posted() map[string]any {
	h.t.Helper()
	calls := h.callsTo("chat.postMessage")
	if len(calls) != 1 {
		h.t.Fatalf("%d chat.postMessage calls, want 1", len(calls))
	}
	return calls[0].params
}

func (h *harness) wantNothingPosted() {
	h.t.Helper()
	if n := len(h.callsTo("chat.postMessage")); n != 0 {
		h.t.Errorf("%d chat.postMessage calls, want none", n)
	}
}

func (h *harness) wantNoSlackCalls() {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.calls) != 0 {
		h.t.Errorf("Slack was called: %+v", h.calls)
	}
}

// wantNoSecretsPrinted checks that neither output stream carries the
// editor's key-bearing URL.
func (h *harness) wantNoSecretsPrinted() {
	h.t.Helper()
	h.mu.Lock()
	opened := append([]string(nil), h.opened...)
	h.mu.Unlock()
	for _, stream := range []string{h.stdout.String(), h.stderr.String()} {
		if strings.Contains(stream, "?k=") {
			h.t.Errorf("the editor key was printed: %s", stream)
		}
		for _, u := range opened {
			if key := u[strings.Index(u, "?k=")+3:]; strings.Contains(stream, key) {
				h.t.Errorf("the editor key was printed: %s", stream)
			}
		}
	}
}

func wantParams(t *testing.T, got, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("params = %#v\nwant     %#v", got, want)
	}
}

func TestComposeSendsWhatTheHumanSent(t *testing.T) {
	h := newHarness(t)
	h.human = sends("edited by a human")

	out := h.mustRun("#general", "written by an agent")

	wantParams(t, h.posted(), map[string]any{"channel": "C12345678", "text": "edited by a human"})
	want := map[string]any{
		"ok": true, "sent": true,
		"channel_id": "C12345678",
		"ts":         "1770170000.000200",
		"permalink":  workspaceURL + "/archives/C12345678/p1770170000000200",
		"text":       "edited by a human",
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("output = %v\nwant     %v", out, want)
	}
	// stderr says where the editor is, without the key that opens it.
	if !regexp.MustCompile(`#general is open in the browser \(http://127\.0\.0\.1:\d+\)`).MatchString(h.stderr.String()) {
		t.Errorf("stderr = %q", h.stderr.String())
	}
	h.wantNoSecretsPrinted()
	if len(h.opened) != 1 || !strings.HasPrefix(h.opened[0], "http://127.0.0.1:") {
		t.Errorf("opened %q", h.opened)
	}
}

func TestComposeShowsTheHumanWhereTheMessageGoes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		config map[string]any
	}{
		{"channel name", []string{"general", "hi"}, map[string]any{
			"channelName": "general", "isDM": false, "workspaceName": "workspace", "threadTs": "", "threadUrl": "", "initialText": "hi",
		}},
		{"channel id", []string{"C12345678"}, map[string]any{
			"channelName": "general", "isDM": false, "initialText": "",
		}},
		{"user id", []string{"U12345678", "hi"}, map[string]any{
			"channelName": "alice", "isDM": true,
		}},
		{"message URL", []string{workspaceURL + "/archives/C12345678/p1770160500000100", "hi"}, map[string]any{
			"channelName": "general", "threadTs": "1770160500.000100",
			"threadUrl": workspaceURL + "/archives/C12345678/p1770160500000100",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			var seen map[string]any
			h.human = func(b *browser) {
				seen = b.config
				cancels(b)
			}
			h.mustRun(tc.args...)
			h.humans.Wait()
			for key, want := range tc.config {
				if seen[key] != want {
					t.Errorf("page config %s = %#v, want %#v", key, seen[key], want)
				}
			}
			if seen["appName"] != appinfo.Name {
				t.Errorf("appName = %v", seen["appName"])
			}
		})
	}
}

func TestComposeTargets(t *testing.T) {
	const url = workspaceURL + "/archives/C12345678/p1770160500000100"
	for _, tc := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{"channel id", []string{"C12345678", "hi"},
			map[string]any{"channel": "C12345678", "text": "hi"}},
		{"channel with --thread-ts", []string{"#general", "hi", "--thread-ts", "1770160500.000100"},
			map[string]any{"channel": "C12345678", "text": "hi", "thread_ts": "1770160500.000100"}},
		{"message URL replies in its thread", []string{url, "hi"},
			map[string]any{"channel": "C12345678", "text": "hi", "thread_ts": "1770160500.000100"}},
		{"--thread-ts may repeat the thread of the URL", []string{url, "hi", "--thread-ts", "1770160500.000100"},
			map[string]any{"channel": "C12345678", "text": "hi", "thread_ts": "1770160500.000100"}},
		{"user id is a DM", []string{"U12345678", "hi"},
			map[string]any{"channel": "D99999999", "text": "hi"}},
		{"--no-unfurl", []string{"#general", "https://example.com", "--no-unfurl"},
			map[string]any{"channel": "C12345678", "text": "https://example.com", "unfurl_links": false, "unfurl_media": false}},
		{"--no-unfurl with a URL target", []string{url, "https://example.com", "--no-unfurl"},
			map[string]any{"channel": "C12345678", "text": "https://example.com", "thread_ts": "1770160500.000100", "unfurl_links": false, "unfurl_media": false}},
		{"text after --", []string{"#general", "--", "-1 from me"},
			map[string]any{"channel": "C12345678", "text": "-1 from me"}},
		// A list starts with a dash: it is text, not a flag.
		{"text starting with a dash", []string{"#general", "-- sent from the CLI", "--no-unfurl"},
			map[string]any{"channel": "C12345678", "text": "-- sent from the CLI", "unfurl_links": false, "unfurl_media": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.human = sendsAsIs
			out := h.mustRun(tc.args...)
			wantParams(t, h.posted(), tc.want)
			wantThread, _ := tc.want["thread_ts"].(string)
			if got, _ := out["thread_ts"].(string); got != wantThread {
				t.Errorf("output thread_ts = %q, want %q", got, wantThread)
			}
			if out["sent"] != true || out["channel_id"] != tc.want["channel"] {
				t.Errorf("output = %v", out)
			}
		})
	}
}

func TestComposeFormatsLikeMessageSend(t *testing.T) {
	h := newHarness(t)
	h.human = sends("hey @U12345678, is 1 < 2 & <https://example.com|this> ok?\n- one\n- two")
	out := h.mustRun("#general")

	posted := h.posted()
	// Mentions notify and special characters are escaped, as `message
	// send` does; Slack link tokens are kept.
	if got, want := posted["text"], "hey <@U12345678>, is 1 &lt; 2 &amp; <https://example.com|this> ok?\n- one\n- two"; got != want {
		t.Errorf("text = %q\nwant   %q", got, want)
	}
	// Lists only render as lists from rich text blocks.
	blocks, _ := posted["blocks"].([]any)
	if len(blocks) != 1 || !strings.Contains(mustJSON(t, blocks), "rich_text_list") {
		t.Errorf("blocks = %s", mustJSON(t, posted["blocks"]))
	}
	// The result reports the text as the human wrote it.
	if out["text"] != "hey @U12345678, is 1 < 2 & <https://example.com|this> ok?\n- one\n- two" {
		t.Errorf("output text = %q", out["text"])
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestComposeCancelled(t *testing.T) {
	h := newHarness(t)
	h.human = cancels
	out := h.mustRun("#general", "hello")
	want := map[string]any{"ok": true, "cancelled": true, "reason": "cancelled in the editor"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("output = %v, want %v", out, want)
	}
	h.wantNothingPosted()
	h.wantNoSecretsPrinted()
}

func TestComposeTabClosed(t *testing.T) {
	h := newHarness(t)
	h.c.timeouts.Closing = 20 * time.Millisecond
	h.human = func(b *browser) { b.post("/bye", nil) }
	out := h.mustRun("#general", "hello")
	if out["cancelled"] != true || out["reason"] != string(endTabClosed) {
		t.Errorf("output = %v", out)
	}
	if !strings.Contains(h.stderr.String(), "Nothing was sent") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
	h.wantNothingPosted()
}

func TestComposeNobodyOpensTheEditor(t *testing.T) {
	h := newHarness(t)
	h.c.timeouts.Unopened = 30 * time.Millisecond
	h.human = func(*browser) {}
	h.c.openBrowser = func(context.Context, string) error { return nil } // a browser that shows nothing
	out := h.mustRun("#general", "hello")
	if out["cancelled"] != true || out["reason"] != string(endNeverOpened) {
		t.Errorf("output = %v", out)
	}
	h.wantNothingPosted()
}

func TestComposeInterrupted(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.human = func(*browser) { cancel() } // Ctrl-C while the editor is open
	out, err := h.c.compose(ctx, "#general", "hello", Options{})
	if !errors.Is(err, context.Canceled) || out != nil {
		t.Errorf("out = %v, err = %v", out, err)
	}
	h.wantNothingPosted()
	h.humans.Wait()
	// The editor is gone.
	u, _ := url.Parse(h.opened[0])
	if conn, err := net.DialTimeout("tcp", u.Host, time.Second); err == nil {
		conn.Close()
		t.Error("the editor still listens after the interruption")
	}
}

func TestComposeRefreshesCredentialsWhenSending(t *testing.T) {
	h := newHarness(t)
	// The credentials expired while the human was reviewing.
	h.failOnce = "invalid_auth"
	h.rt.Desktop = func(context.Context) (*authextract.Extracted, error) {
		return &authextract.Extracted{
			CookieD: "xoxd-fresh",
			Teams:   []authextract.Team{{URL: workspaceURL + "/", Token: "xoxc-fresh"}},
		}, nil
	}
	h.human = sendsAsIs
	out := h.mustRun("#general", "hello")
	if out["sent"] != true {
		t.Errorf("output = %v", out)
	}
	if n := len(h.callsTo("chat.postMessage")); n != 2 {
		t.Errorf("%d chat.postMessage calls, want the failed one and its retry", n)
	}
}

func TestComposeSendFailureIsShownToTheHuman(t *testing.T) {
	h := newHarness(t)
	h.failOnce = "msg_too_long"
	h.human = func(b *browser) {
		status, resp := b.post("/send", map[string]any{"text": "too long"})
		if status != http.StatusBadGateway || !strings.Contains(resp.Error, "msg_too_long") {
			b.t.Errorf("failed send: %d %+v", status, resp)
		}
		sends("shorter")(b)
	}
	out := h.mustRun("#general")
	if out["sent"] != true || out["text"] != "shorter" {
		t.Errorf("output = %v", out)
	}
}

func TestComposeRejectsBadInputBeforeAnything(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"malformed --thread-ts", []string{"#general", "hi", "--thread-ts", "yesterday"}, "invalid --thread-ts"},
		{"non-Slack URL", []string{"https://example.com/x", "hi"}, "unsupported target URL"},
		{"no target", nil, "accepts between 1 and 2 arg(s)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if _, err := h.run(tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
			h.wantNoSlackCalls()
			if len(h.opened) != 0 {
				t.Error("the browser was opened")
			}
		})
	}
}

// --thread-ts may not send the reply to another thread than the one the
// message URL designates ("message send" applies the same rule), whether
// compose is run directly or by a safe-mode "message send".
func TestThreadTSContradictingTheURLIsRefused(t *testing.T) {
	const url = workspaceURL + "/archives/C12345678/p1770160500000100"
	for _, safeMode := range []bool{false, true} {
		h := newHarness(t)
		h.rt.SafeModeFlag = safeMode
		_, err := Compose(context.Background(), h.rt, url, "hi", Options{ThreadTS: "1770160000.000001"})
		if err == nil || !strings.Contains(err.Error(), "--thread-ts 1770160000.000001 contradicts the message URL, whose thread is 1770160500.000100") {
			t.Errorf("safe mode %v: err = %v", safeMode, err)
		}
		h.wantNothingPosted()
		if len(h.opened) != 0 {
			t.Errorf("safe mode %v: the browser was opened", safeMode)
		}
	}
}

// --- no browser -------------------------------------------------------------

// Under safe mode the editor link is never printed, not even at a
// terminal: an agent may be running the command under a pseudo-terminal,
// and whoever holds the link can send.
func TestNoBrowserInSafeModeFailsClosedEvenAtATerminal(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(h *harness) error
	}{
		{"message compose", func(h *harness) error { _, err := h.run("#general", "hello"); return err }},
		{"message send redirected", func(h *harness) error {
			_, err := h.c.compose(context.Background(), "#general", "hello", Options{})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.rt.SafeModeFlag = true
			h.openErr = errors.New("no graphical session")
			h.c.humanAtStderr = func() bool { return true }

			err := tc.run(h)
			if err == nil {
				t.Fatal("compose went on without a browser")
			}
			for _, want := range []string{"safe mode is active", "no graphical session", "nothing was sent", "message compose <target> [text]", "graphical session"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			h.wantNoSecretsPrinted()
			if strings.Contains(err.Error(), "?k=") || strings.Contains(err.Error(), "127.0.0.1") {
				t.Errorf("the error carries the editor address: %v", err)
			}
			if strings.Contains(h.stderr.String(), "127.0.0.1") {
				t.Errorf("stderr carries the editor address: %s", h.stderr)
			}
			h.wantNothingPosted()
			u, _ := url.Parse(h.opened[0])
			if conn, err := net.DialTimeout("tcp", u.Host, time.Second); err == nil {
				conn.Close()
				t.Error("the editor still listens")
			}
		})
	}
}

func TestNoBrowserAndNoTerminalFailsClosed(t *testing.T) {
	h := newHarness(t)
	h.openErr = errors.New("exec: \"xdg-open\": executable file not found in $PATH")

	_, err := h.run("#general", "hello")
	if err == nil || !strings.Contains(err.Error(), "could not open a browser") || !strings.Contains(err.Error(), "xdg-open") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "interactive terminal") {
		t.Errorf("the error does not say what a human can do: %v", err)
	}
	// The link is the capability to press Send: it must not reach a
	// stream that an agent reads.
	h.wantNoSecretsPrinted()
	if strings.Contains(err.Error(), "?k=") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("the error carries the editor address: %v", err)
	}
	h.wantNothingPosted()
	// And the editor is not left running.
	u, _ := url.Parse(h.opened[0])
	if conn, err := net.DialTimeout("tcp", u.Host, time.Second); err == nil {
		conn.Close()
		t.Error("the editor still listens")
	}
}

func TestNoBrowserAtATerminalPrintsTheLinkForTheHuman(t *testing.T) {
	h := newHarness(t)
	h.openErr = errors.New("no graphical session")
	h.c.humanAtStderr = func() bool { return true }
	h.human = sends("typed the link by hand")
	// The human reads the link off the terminal and opens it.
	terminal := &terminalReader{h: h}
	h.rt.Stderr = terminal

	out := h.mustRun("#general", "hello")
	if out["sent"] != true || out["text"] != "typed the link by hand" {
		t.Errorf("output = %v", out)
	}
	if !strings.Contains(terminal.String(), "no graphical session") {
		t.Errorf("stderr does not say why: %q", terminal.String())
	}
	if strings.Contains(h.stdout.String(), "?k=") {
		t.Errorf("the link was printed to stdout: %s", h.stdout)
	}
}

// terminalReader is a stderr at which a human sits: they open the editor
// link as soon as it is printed.
type terminalReader struct {
	h *harness

	mu      sync.Mutex
	buf     bytes.Buffer
	visited bool
}

var editorLinkRe = regexp.MustCompile(`http://127\.0\.0\.1:\d+/\?k=[A-Z0-9]+`)

func (r *terminalReader) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf.Write(p)
	if link := editorLinkRe.FindString(r.buf.String()); link != "" && !r.visited {
		r.visited = true
		r.h.visit(link)
	}
	return len(p), nil
}

func (r *terminalReader) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func TestIsTerminal(t *testing.T) {
	if isTerminal(&bytes.Buffer{}) {
		t.Error("a buffer is not a terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("a regular file is not a terminal")
	}
	// A character device, but nobody reads it: with 2>/dev/null and no
	// browser, compose must fail rather than wait for a link to be opened.
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if isTerminal(null) {
		t.Errorf("%s is not a terminal", os.DevNull)
	}
}

// --- CI ---------------------------------------------------------------------

func TestCISkipsTheEditorAndSends(t *testing.T) {
	const url = workspaceURL + "/archives/C12345678/p1770160500000100"
	for _, tc := range []struct {
		name string
		args []string
		want map[string]any
	}{
		{"keeps Slack's default previews", []string{"#general", "https://example.com"},
			map[string]any{"channel": "C12345678", "text": "https://example.com"}},
		{"--no-unfurl disables previews", []string{"#general", "https://example.com", "--no-unfurl"},
			map[string]any{"channel": "C12345678", "text": "https://example.com", "unfurl_links": false, "unfurl_media": false}},
		{"--no-unfurl for a URL target", []string{url, "https://example.com", "--no-unfurl"},
			map[string]any{"channel": "C12345678", "text": "https://example.com", "thread_ts": "1770160500.000100", "unfurl_links": false, "unfurl_media": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			t.Setenv("CI", "1")
			out := h.mustRun(tc.args...)
			wantParams(t, h.posted(), tc.want)
			if out["ok"] != true || out["sent"] != true || out["editor"] != "skipped" || out["ts"] != "1770170000.000200" {
				t.Errorf("output = %v", out)
			}
			if len(h.opened) != 0 {
				t.Error("the browser was opened in CI")
			}
			// No display name is looked up for an editor that is not shown.
			if n := len(h.callsTo("conversations.info")); n != 0 {
				t.Errorf("%d conversations.info calls", n)
			}
		})
	}

	t.Run("requires text", func(t *testing.T) {
		for _, args := range [][]string{{"#general"}, {"#general", "  "}} {
			h := newHarness(t)
			t.Setenv("CI", "true")
			if _, err := h.run(args...); err == nil || !strings.Contains(err.Error(), "CI") {
				t.Errorf("err = %v", err)
			}
			h.wantNoSlackCalls()
		}
	})
	t.Run("CI=false and CI=0 are not CI", func(t *testing.T) {
		for _, v := range []string{"false", "0", "FALSE", " "} {
			h := newHarness(t)
			t.Setenv("CI", v)
			h.human = cancels
			if out := h.mustRun("#general", "hello"); out["cancelled"] != true {
				t.Errorf("CI=%q: output = %v", v, out)
			}
			h.wantNothingPosted()
		}
	})
}

func TestSafeModeNeverSendsWithoutAHuman(t *testing.T) {
	safeModes := map[string]func(*harness){
		"--safe-mode flag": func(h *harness) { h.rt.SafeModeFlag = true },
		"environment":      func(h *harness) { h.t.Setenv(appinfo.EnvName("SAFE_MODE"), "1") },
	}
	for name, enable := range safeModes {
		t.Run("CI with "+name, func(t *testing.T) {
			h := newHarness(t)
			t.Setenv("CI", "1")
			enable(h)
			// Both the command and the exported entry point that
			// `message send` is redirected to.
			_, err := h.run("#general", "hello")
			if err == nil || !strings.Contains(err.Error(), "safe mode") || !strings.Contains(err.Error(), "CI") {
				t.Errorf("command: err = %v", err)
			}
			out, err := Compose(context.Background(), h.rt, "#general", "hello", Options{})
			if err == nil || out != nil || !strings.Contains(err.Error(), "refusing to send without human review") {
				t.Errorf("Compose: out = %v, err = %v", out, err)
			}
			h.wantNoSlackCalls()
		})
		t.Run("no browser with "+name, func(t *testing.T) {
			h := newHarness(t)
			enable(h)
			h.openErr = errors.New("no browser")
			if _, err := h.run("#general", "hello"); err == nil {
				t.Error("compose succeeded without a browser")
			}
			h.wantNothingPosted()
			h.wantNoSecretsPrinted()
		})
		t.Run("the editor with "+name, func(t *testing.T) {
			h := newHarness(t)
			enable(h)
			h.human = sendsAsIs
			if out := h.mustRun("#general", "hello"); out["sent"] != true {
				t.Errorf("output = %v", out)
			}
			h.wantNoSecretsPrinted()
		})
	}
}

func TestComposeExportedEntryPointInCI(t *testing.T) {
	h := newHarness(t)
	t.Setenv("CI", "1")
	out, err := Compose(context.Background(), h.rt, "C12345678", "hello", Options{ThreadTS: "1770160500.000100", NoUnfurl: true})
	if err != nil {
		t.Fatal(err)
	}
	wantParams(t, h.posted(), map[string]any{
		"channel": "C12345678", "text": "hello", "thread_ts": "1770160500.000100", "unfurl_links": false, "unfurl_media": false,
	})
	if out["editor"] != "skipped" || out["thread_ts"] != "1770160500.000100" {
		t.Errorf("output = %v", out)
	}
}

// --- command surface --------------------------------------------------------

func TestCommandContract(t *testing.T) {
	cmd := New(newHarness(t).rt)
	if cmd.Name() != "compose" {
		t.Errorf("name = %q", cmd.Name())
	}
	if !clictx.HasNoTimeout(cmd) {
		t.Error("compose waits on a human: it must be exempt from the command timeout")
	}
	if strings.Contains(cmd.Short, "CI") || len(cmd.Short) > 90 {
		t.Errorf("Short = %q, want a one-liner (the CI caveat belongs in Long)", cmd.Short)
	}
	for _, line := range strings.Split(cmd.Long, "\n") {
		if len([]rune(line)) > 80 {
			t.Errorf("Long is not wrapped: %q", line)
		}
	}
	for _, want := range []string{"sent immediately", "safe mode", "user ID", "{ok, cancelled, reason}", "CI environment variable", "never printed"} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("Long does not mention %q", want)
		}
	}
	for flag, want := range map[string]string{
		"thread-ts": "must not contradict",
		"no-unfurl": "link and media previews",
		"workspace": "Workspace selector",
	} {
		f := cmd.Flags().Lookup(flag)
		if f == nil || !strings.Contains(f.Usage, want) {
			t.Errorf("--%s: %+v, want usage containing %q", flag, f, want)
		}
	}
	if f := cmd.Flags().Lookup("no-unfurl"); f.DefValue != "false" {
		t.Errorf("--no-unfurl defaults to %s", f.DefValue)
	}
}
