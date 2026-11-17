package composecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// patient are timeouts that never fire during a test.
var patient = timeouts{
	Unopened: time.Minute,
	Silent:   time.Minute,
	Closing:  time.Minute,
	Ping:     10 * time.Second,
	Shutdown: 5 * time.Second,
}

// sendRecorder is a sendFunc that records what it is asked to send.
type sendRecorder struct {
	mu    sync.Mutex
	texts []string
	// fail, when set, is returned (and cleared) by the next call.
	fail error
	// gate, when set, blocks every call until it is closed.
	gate    chan struct{}
	started chan struct{} // receives one value per call, if set
}

func (r *sendRecorder) send(_ context.Context, text string) (sentMessage, error) {
	if r.started != nil {
		r.started <- struct{}{}
	}
	if r.gate != nil {
		<-r.gate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	if err := r.fail; err != nil {
		r.fail = nil
		return sentMessage{}, err
	}
	return sentMessage{ChannelID: "C1", TS: "1700000000.000100", Permalink: "https://acme.slack.com/archives/C1/p1700000000000100"}, nil
}

func (r *sendRecorder) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.texts...)
}

func startTestSession(t *testing.T, page pageConfig, send sendFunc, to timeouts) *session {
	t.Helper()
	s, err := startSession(context.Background(), page, send, to)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.interrupt()
		s.stop()
	})
	return s
}

// browser plays the part of the user's browser tab.
type browser struct {
	t      *testing.T
	client *http.Client
	origin string
	// token is the session token found in the page, once opened.
	token string
	// config is the page's configuration block, once opened.
	config map[string]any
}

func newBrowser(t *testing.T, origin string) *browser {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, client: &http.Client{Jar: jar}, origin: origin}
}

var configBlockRe = regexp.MustCompile(`(?s)<script type="application/json" id="composeConfig">(.*?)</script>`)

// get fetches a URL and returns the response with its body read.
func (b *browser) get(rawURL string, mutate ...func(*http.Request)) (*http.Response, string) {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	return b.do(req, mutate...)
}

func (b *browser) do(req *http.Request, mutate ...func(*http.Request)) (*http.Response, string) {
	b.t.Helper()
	for _, m := range mutate {
		m(req)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return resp, string(body)
}

// open loads the editor page as the browser launched by the CLI does, and
// reads the session token out of it, as the page's script does.
func (b *browser) open(pageURL string) (*http.Response, string) {
	b.t.Helper()
	resp, body := b.get(pageURL)
	if resp.StatusCode != http.StatusOK {
		b.t.Fatalf("GET %s: %s\n%s", pageURL, resp.Status, body)
	}
	m := configBlockRe.FindStringSubmatch(body)
	if m == nil {
		b.t.Fatalf("no configuration block in the page")
	}
	if err := json.Unmarshal([]byte(m[1]), &b.config); err != nil {
		b.t.Fatalf("configuration block is not JSON: %v\n%s", err, m[1])
	}
	b.token, _ = b.config["token"].(string)
	if b.token == "" {
		b.t.Fatal("the page carries no session token")
	}
	return resp, body
}

// post sends an action the way the page's script does. mutate can then
// make the request look different.
func (b *browser) post(path string, payload map[string]any, mutate ...func(*http.Request)) (int, actionResponse) {
	b.t.Helper()
	body := map[string]any{"token": b.token}
	for k, v := range payload {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	return b.postRaw(path, raw, mutate...)
}

func (b *browser) postRaw(path string, raw []byte, mutate ...func(*http.Request)) (int, actionResponse) {
	b.t.Helper()
	req, err := http.NewRequest(http.MethodPost, b.origin+path, bytes.NewReader(raw))
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", b.origin)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, respBody := b.do(req, mutate...)
	var decoded actionResponse
	_ = json.Unmarshal([]byte(respBody), &decoded)
	return resp.StatusCode, decoded
}

// waitResult runs s.wait in the background.
type waitResult struct {
	out outcome
	err error
}

func waitAsync(ctx context.Context, s *session) <-chan waitResult {
	ch := make(chan waitResult, 1)
	go func() {
		out, err := s.wait(ctx)
		ch <- waitResult{out, err}
	}()
	return ch
}

func mustEnd(t *testing.T, ch <-chan waitResult) waitResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end")
		return waitResult{}
	}
}

func mustNotEnd(t *testing.T, ch <-chan waitResult, d time.Duration) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("the session ended early: %+v", r)
	case <-time.After(d):
	}
}

// mustBeStopped checks that nothing listens on the session's port any
// more and that its server goroutine is gone.
func mustBeStopped(t *testing.T, s *session) {
	t.Helper()
	select {
	case <-s.served:
	default:
		t.Error("the server goroutine is still running")
	}
	conn, err := net.DialTimeout("tcp", s.hosts[0], time.Second)
	if err == nil {
		conn.Close()
		t.Error("the server still accepts connections")
	}
}

func TestSessionListensOnLoopbackOnly(t *testing.T) {
	s := startTestSession(t, pageConfig{}, (&sendRecorder{}).send, patient)
	addr := s.listener.Addr().(*net.TCPAddr)
	if !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) || addr.Port == 0 {
		t.Errorf("listening on %v, want 127.0.0.1 and an ephemeral port", addr)
	}
	if got := s.origin(); got != fmt.Sprintf("http://127.0.0.1:%d", addr.Port) {
		t.Errorf("origin = %q", got)
	}
	// Each secret is distinct, long and from crypto/rand.
	secrets := map[string]bool{s.key: true, s.token: true, s.cookieSecret: true}
	if len(secrets) != 3 {
		t.Error("the key, the token and the cookie secret must differ")
	}
	for secret := range secrets {
		if len(secret) < 26 {
			t.Errorf("secret %q is too short", secret)
		}
		if strings.Contains(s.origin(), secret) {
			t.Error("origin() must not carry a secret")
		}
	}
	other := startTestSession(t, pageConfig{}, (&sendRecorder{}).send, patient)
	if other.key == s.key || other.token == s.token {
		t.Error("secrets are reused across sessions")
	}
}

func TestPageIsServedOnceForTheKey(t *testing.T) {
	s := startTestSession(t, pageConfig{AppName: "app", ChannelName: "general", InitialText: "hello"}, (&sendRecorder{}).send, patient)
	b := newBrowser(t, s.origin())

	resp, body := b.open(s.url())

	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	for header, want := range map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"Cache-Control":                "no-store",
		"Referrer-Policy":              "no-referrer",
		"X-Frame-Options":              "DENY",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	csp := resp.Header.Get("Content-Security-Policy")
	nonce := regexp.MustCompile(`script-src 'nonce-([A-Z0-9]+)'`).FindStringSubmatch(csp)
	if nonce == nil {
		t.Fatalf("CSP has no script nonce: %q", csp)
	}
	for _, directive := range []string{"default-src 'none'", "connect-src 'self'", "base-uri 'none'", "form-action 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP lacks %q: %q", directive, csp)
		}
	}
	for _, forbidden := range []string{"unsafe-eval", "script-src 'unsafe-inline'", "http:", "https:", "*"} {
		if strings.Contains(csp, forbidden) {
			t.Errorf("CSP allows %q: %q", forbidden, csp)
		}
	}
	if !strings.Contains(body, `<script nonce="`+nonce[1]+`">`) {
		t.Error("the page's script does not carry the CSP nonce")
	}

	if b.config["channelName"] != "general" || b.config["initialText"] != "hello" || b.config["appName"] != "app" {
		t.Errorf("config = %v", b.config)
	}
	if b.token != s.token {
		t.Error("the page does not carry the session token")
	}
	if strings.Contains(body, s.key) || strings.Contains(body, s.cookieSecret) {
		t.Error("the page leaks the one-time key or the cookie secret")
	}

	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == s.cookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Value != s.cookieSecret {
		t.Errorf("reload cookie = %+v, want HttpOnly, SameSite=Strict", cookie)
	}

	t.Run("a nonce is not reused", func(t *testing.T) {
		resp, _ := b.get(s.origin() + "/")
		if again := resp.Header.Get("Content-Security-Policy"); again == csp || resp.StatusCode != http.StatusOK {
			t.Errorf("reload: status %s, same CSP: %v", resp.Status, again == csp)
		}
	})
	t.Run("the same browser can reload", func(t *testing.T) {
		for _, u := range []string{s.origin() + "/", s.url()} {
			if resp, _ := b.get(u); resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s with the cookie: %s", u, resp.Status)
			}
		}
	})
	t.Run("nobody else can use the key again", func(t *testing.T) {
		stranger := newBrowser(t, s.origin())
		resp, body := stranger.get(s.url())
		if resp.StatusCode != http.StatusForbidden || strings.Contains(body, s.token) {
			t.Errorf("second use of the key: %s", resp.Status)
		}
		if !strings.Contains(body, "Ctrl-C") {
			t.Errorf("the refusal does not tell the human what to do: %q", body)
		}
	})
}

func TestPageNeedsTheKey(t *testing.T) {
	s := startTestSession(t, pageConfig{}, (&sendRecorder{}).send, patient)
	for _, u := range []string{
		s.origin() + "/",
		s.origin() + "/?k=",
		s.origin() + "/?k=" + s.key + "x",
		s.origin() + "/?k=" + s.token, // the token is not the key
		s.origin() + "/?token=" + s.key,
	} {
		resp, body := newBrowser(t, s.origin()).get(u)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s: %s, want 403", u, resp.Status)
		}
		if strings.Contains(body, s.token) {
			t.Errorf("GET %s leaks the session token", u)
		}
	}
	// None of those attempts spent the key.
	newBrowser(t, s.origin()).open(s.url())

	t.Run("a forged cookie does not help", func(t *testing.T) {
		resp, _ := newBrowser(t, s.origin()).get(s.origin()+"/", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: s.cookie, Value: s.token})
		})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %s, want 403", resp.Status)
		}
	})
}

func TestPageCannotBeInjected(t *testing.T) {
	hostile := pageConfig{
		AppName:       "app",
		ChannelName:   `</script><script>alert(1)</script>`,
		WorkspaceName: `"><img src=x onerror=alert(2)>`,
		ThreadTS:      "1700000000.000100",
		ThreadURL:     "javascript:alert(3)",
		InitialText: "</script><script>fetch('/send')</script> <!-- <script> ]]>    & ' \" \\ " +
			noncePlaceholder + " " + configPlaceholder,
	}
	s := startTestSession(t, hostile, (&sendRecorder{}).send, patient)
	b := newBrowser(t, s.origin())
	_, body := b.open(s.url())

	// The page has exactly its two script elements: the configuration
	// (inert JSON) and the editor.
	if n := strings.Count(strings.ToLower(body), "<script"); n != 2 {
		t.Errorf("%d <script elements in the page, want 2", n)
	}
	if n := strings.Count(strings.ToLower(body), "</script"); n != 2 {
		t.Errorf("%d </script closers in the page, want 2", n)
	}
	block := configBlockRe.FindStringSubmatch(body)[1]
	for _, raw := range []string{"<", ">", "&", " ", " "} {
		if strings.Contains(block, raw) {
			t.Errorf("the configuration block contains a raw %q", raw)
		}
	}
	// And yet the values arrive intact, placeholders included.
	for key, want := range map[string]string{
		"channelName":   hostile.ChannelName,
		"workspaceName": hostile.WorkspaceName,
		"threadUrl":     hostile.ThreadURL,
		"initialText":   hostile.InitialText,
	} {
		if b.config[key] != want {
			t.Errorf("config[%s] = %q, want %q", key, b.config[key], want)
		}
	}
	if strings.Contains(body, noncePlaceholder+`"`) || strings.Count(body, configPlaceholder) != 1 {
		t.Error("placeholders were not substituted exactly once")
	}
}

func TestEditorPageHasNoInlineHandlersOrRemoteResources(t *testing.T) {
	lower := strings.ToLower(editorHTML)
	if m := regexp.MustCompile(`\son[a-z]+\s*=\s*"`).FindString(lower); m != "" {
		t.Errorf("inline event handler in editor.html (%q): the CSP would block it", m)
	}
	for _, remote := range []string{`src="http`, `<link`, `@import`, `url(http`, "googleapis", "eval(", "new function"} {
		if strings.Contains(lower, remote) {
			t.Errorf("editor.html contains %q", remote)
		}
	}
	if strings.Count(editorHTML, noncePlaceholder) != 1 || strings.Count(editorHTML, configPlaceholder) != 1 {
		t.Error("editor.html must hold each placeholder exactly once")
	}
	// The mrkdwn renderer escapes everything it is given.
	for _, want := range []string{`t = escapeHtml(t);`, `.replace(/"/g, "&quot;")`, `msg.textContent = title;`} {
		if !strings.Contains(editorHTML, want) {
			t.Errorf("editor.html no longer contains %q", want)
		}
	}
}

func TestActionsAreRefusedUnlessFromThePage(t *testing.T) {
	rec := &sendRecorder{}
	s := startTestSession(t, pageConfig{}, rec.send, patient)
	b := newBrowser(t, s.origin())
	b.open(s.url())
	port := strings.TrimPrefix(s.hosts[0], "127.0.0.1:")
	send := map[string]any{"text": "hello"}

	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{"DNS rebinding: foreign Host", func(r *http.Request) { r.Host = "evil.example:" + port }, http.StatusForbidden},
		{"foreign Host without port", func(r *http.Request) { r.Host = "127.0.0.1" }, http.StatusForbidden},
		{"another port as Host", func(r *http.Request) { r.Host = "127.0.0.1:1" }, http.StatusForbidden},
		{"no Origin", func(r *http.Request) { r.Header.Del("Origin") }, http.StatusForbidden},
		{"Origin null", func(r *http.Request) { r.Header.Set("Origin", "null") }, http.StatusForbidden},
		{"Origin of another site", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusForbidden},
		{"Origin of another local port", func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:1") }, http.StatusForbidden},
		{"Origin that is not the Host", func(r *http.Request) { r.Header.Set("Origin", "http://localhost:"+port) }, http.StatusForbidden},
		{"https Origin", func(r *http.Request) { r.Header.Set("Origin", "https://127.0.0.1:"+port) }, http.StatusForbidden},
		{"Sec-Fetch-Site: cross-site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden},
		{"Sec-Fetch-Site: same-site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }, http.StatusForbidden},
		{"Sec-Fetch-Site: none", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "none") }, http.StatusForbidden},
		{"form Content-Type", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, http.StatusUnsupportedMediaType},
		{"text/plain Content-Type", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType},
		{"no Content-Type", func(r *http.Request) { r.Header.Del("Content-Type") }, http.StatusUnsupportedMediaType},
		{"GET", func(r *http.Request) { r.Method = http.MethodGet }, http.StatusMethodNotAllowed},
		{"PUT", func(r *http.Request) { r.Method = http.MethodPut }, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/send", "/cancel", "/ping", "/bye"} {
				if status, resp := b.post(path, send, tc.mutate); status != tc.want || resp.OK {
					t.Errorf("POST %s: status %d (ok=%v), want %d", path, status, resp.OK, tc.want)
				}
			}
		})
	}

	t.Run("wrong or missing token", func(t *testing.T) {
		for _, token := range []any{"", "wrong", s.key, s.cookieSecret, s.token + "x", nil, 42} {
			for _, path := range []string{"/send", "/cancel", "/ping", "/bye"} {
				status, _ := b.post(path, map[string]any{"text": "hello", "token": token})
				// A token of the wrong JSON type is a malformed body.
				if status != http.StatusForbidden && status != http.StatusBadRequest {
					t.Errorf("POST %s with token %v: status %d, want 403", path, token, status)
				}
			}
		}
	})
	t.Run("the token in the URL or a header is not enough", func(t *testing.T) {
		status, _ := b.postRaw("/send?token="+s.token, []byte(`{"text":"hello"}`), func(r *http.Request) {
			r.Header.Set("X-Compose-Token", s.token)
			r.Header.Set("Authorization", "Bearer "+s.token)
		})
		if status != http.StatusForbidden {
			t.Errorf("status = %d, want 403", status)
		}
	})
	t.Run("malformed body", func(t *testing.T) {
		for _, raw := range []string{"", "{", "[]", `"text"`, "token=" + s.token} {
			if status, _ := b.postRaw("/send", []byte(raw)); status != http.StatusBadRequest {
				t.Errorf("body %q: status %d, want 400", raw, status)
			}
		}
	})
	t.Run("oversized body", func(t *testing.T) {
		huge, _ := json.Marshal(map[string]any{"token": s.token, "text": strings.Repeat("a", maxBodyBytes)})
		if status, _ := b.postRaw("/send", huge); status != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", status)
		}
	})
	t.Run("blank text", func(t *testing.T) {
		for _, text := range []any{"", " \n\t", nil} {
			if status, resp := b.post("/send", map[string]any{"text": text}); status != http.StatusBadRequest || resp.Error != "text is required" {
				t.Errorf("text %#v: status %d %q, want 400", text, status, resp.Error)
			}
		}
	})
	t.Run("other paths and methods", func(t *testing.T) {
		for _, path := range []string{"/index.html", "/favicon.ico", "/send/", "/../send", "/api"} {
			if status, _ := b.post(path, send); status != http.StatusNotFound {
				t.Errorf("POST %s: status %d, want 404", path, status)
			}
		}
		if status, _ := b.post("/", send); status != http.StatusMethodNotAllowed {
			t.Errorf("POST /: status %d, want 405", status)
		}
		resp, _ := b.get(s.origin()+"/", func(r *http.Request) { r.Host = "evil.example:" + port })
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET / with a foreign Host: %s, want 403", resp.Status)
		}
	})

	// After all of that, nothing was sent and the session is still open.
	if got := rec.sent(); len(got) != 0 {
		t.Fatalf("sent %q", got)
	}
	if status, resp := b.post("/ping", nil); status != http.StatusOK || !resp.OK {
		t.Errorf("ping: %d %+v", status, resp)
	}
	t.Run("localhost is the same server", func(t *testing.T) {
		status, resp := b.post("/ping", nil, func(r *http.Request) {
			r.Host = "localhost:" + port
			r.Header.Set("Origin", "http://localhost:"+port)
		})
		if status != http.StatusOK || !resp.OK {
			t.Errorf("ping: %d %+v", status, resp)
		}
	})
	t.Run("a charset on the Content-Type is fine", func(t *testing.T) {
		status, resp := b.post("/ping", nil, func(r *http.Request) {
			r.Header.Set("Content-Type", "application/json; charset=utf-8")
		})
		if status != http.StatusOK || !resp.OK {
			t.Errorf("ping: %d %+v", status, resp)
		}
	})
}

func TestSendEndsTheSession(t *testing.T) {
	rec := &sendRecorder{}
	s := startTestSession(t, pageConfig{}, rec.send, patient)
	b := newBrowser(t, s.origin())
	b.open(s.url())
	ended := waitAsync(context.Background(), s)

	status, resp := b.post("/send", map[string]any{"text": "edited *text*"})
	if status != http.StatusOK || !resp.OK || resp.TS != "1700000000.000100" || !strings.HasPrefix(resp.Permalink, "https://") {
		t.Fatalf("send: %d %+v", status, resp)
	}
	r := mustEnd(t, ended)
	if r.err != nil || r.out.Sent == nil || r.out.Sent.TS != "1700000000.000100" || r.out.Text != "edited *text*" || r.out.Reason != "" {
		t.Errorf("outcome = %+v, err = %v", r.out, r.err)
	}
	if got := rec.sent(); len(got) != 1 || got[0] != "edited *text*" {
		t.Errorf("sent %q", got)
	}
	mustBeStopped(t, s)
}

func TestDoubleSubmitSendsOnce(t *testing.T) {
	rec := &sendRecorder{gate: make(chan struct{}), started: make(chan struct{}, 8)}
	s := startTestSession(t, pageConfig{}, rec.send, patient)
	b := newBrowser(t, s.origin())
	b.open(s.url())
	ended := waitAsync(context.Background(), s)

	type result struct {
		status int
		resp   actionResponse
	}
	first := make(chan result, 1)
	go func() {
		status, resp := b.post("/send", map[string]any{"text": "once"})
		first <- result{status, resp}
	}()
	<-rec.started // the first send is in flight

	for range 3 {
		if status, resp := b.post("/send", map[string]any{"text": "twice"}); status != http.StatusConflict || resp.OK {
			t.Errorf("second send while the first is in flight: %d %+v, want 409", status, resp)
		}
	}
	if status, resp := b.post("/cancel", nil); status != http.StatusConflict || resp.OK {
		t.Errorf("cancel during a send: %d %+v, want 409", status, resp)
	}
	// Signs of life do not disturb a send in flight.
	if status, _ := b.post("/bye", nil); status != http.StatusOK {
		t.Errorf("bye during a send: %d", status)
	}
	mustNotEnd(t, ended, 50*time.Millisecond)

	close(rec.gate)
	if r := <-first; r.status != http.StatusOK || !r.resp.OK {
		t.Errorf("first send: %d %+v", r.status, r.resp)
	}
	r := mustEnd(t, ended)
	if r.err != nil || r.out.Sent == nil || r.out.Text != "once" {
		t.Errorf("outcome = %+v, err = %v", r.out, r.err)
	}
	if got := rec.sent(); len(got) != 1 || got[0] != "once" {
		t.Errorf("sent %q, want exactly one message", got)
	}
}

func TestConcurrentSendsSendOnce(t *testing.T) {
	var calls atomic.Int32
	send := func(context.Context, string) (sentMessage, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return sentMessage{TS: "1.000001"}, nil
	}
	s := startTestSession(t, pageConfig{}, send, patient)
	b := newBrowser(t, s.origin())
	b.open(s.url())

	var wg sync.WaitGroup
	var ok atomic.Int32
	for range 20 {
		wg.Go(func() {
			if status, _ := b.post("/send", map[string]any{"text": "x"}); status == http.StatusOK {
				ok.Add(1)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 || ok.Load() != 1 {
		t.Errorf("%d sends, %d successes; want 1 and 1", calls.Load(), ok.Load())
	}
	// Once sent, the session takes nothing more.
	if status, resp := b.post("/send", map[string]any{"text": "again"}); status != http.StatusConflict || resp.Error != "the message was already sent" {
		t.Errorf("send after sent: %d %+v", status, resp)
	}
	if status, _ := b.post("/cancel", nil); status != http.StatusConflict {
		t.Errorf("cancel after sent: %d, want 409", status)
	}
	if status, _ := b.post("/ping", nil); status != http.StatusGone {
		t.Errorf("ping after sent: %d, want 410", status)
	}
	if resp, _ := b.get(s.origin() + "/"); resp.StatusCode != http.StatusGone {
		t.Errorf("page after sent: %s, want 410", resp.Status)
	}
	if r := mustEnd(t, waitAsync(context.Background(), s)); r.err != nil || r.out.Sent == nil {
		t.Errorf("outcome = %+v, err = %v", r.out, r.err)
	}
	if calls.Load() != 1 {
		t.Errorf("%d sends", calls.Load())
	}
}

func TestFailedSendCanBeRetried(t *testing.T) {
	rec := &sendRecorder{fail: errors.New("Slack API error calling chat.postMessage: channel_not_found (token xoxc-123-abc, cookie xoxd-AbC%2Bdef)")}
	s := startTestSession(t, pageConfig{}, rec.send, patient)
	b := newBrowser(t, s.origin())
	b.open(s.url())
	ended := waitAsync(context.Background(), s)

	status, resp := b.post("/send", map[string]any{"text": "first"})
	if status != http.StatusBadGateway || resp.OK || !strings.Contains(resp.Error, "channel_not_found") {
		t.Fatalf("failed send: %d %+v", status, resp)
	}
	if strings.Contains(resp.Error, "xoxc-123") || strings.Contains(resp.Error, "AbC") || strings.Count(resp.Error, "[REDACTED]") != 2 {
		t.Errorf("credentials are not redacted: %q", resp.Error)
	}
	mustNotEnd(t, ended, 50*time.Millisecond)

	if status, resp := b.post("/send", map[string]any{"text": "second"}); status != http.StatusOK || !resp.OK {
		t.Fatalf("retry: %d %+v", status, resp)
	}
	if r := mustEnd(t, ended); r.err != nil || r.out.Sent == nil || r.out.Text != "second" {
		t.Errorf("outcome = %+v, err = %v", r.out, r.err)
	}
	if got := rec.sent(); len(got) != 2 {
		t.Errorf("send attempts = %q", got)
	}
}

func TestCancelEndsTheSession(t *testing.T) {
	rec := &sendRecorder{}
	s := startTestSession(t, pageConfig{}, rec.send, patient)
	b := newBrowser(t, s.origin())
	b.open(s.url())

	for range 2 { // cancelling twice is harmless
		if status, resp := b.post("/cancel", nil); status != http.StatusOK || !resp.OK {
			t.Fatalf("cancel: %d %+v", status, resp)
		}
	}
	if status, resp := b.post("/send", map[string]any{"text": "too late"}); status != http.StatusConflict || resp.OK {
		t.Errorf("send after cancel: %d %+v, want 409", status, resp)
	}
	r := mustEnd(t, waitAsync(context.Background(), s))
	if r.err != nil || r.out.Sent != nil || r.out.Reason != endCancelled {
		t.Errorf("outcome = %+v, err = %v", r.out, r.err)
	}
	if got := rec.sent(); len(got) != 0 {
		t.Errorf("sent %q", got)
	}
	mustBeStopped(t, s)
}

func TestContextCancellationStopsTheSession(t *testing.T) {
	rec := &sendRecorder{}
	s := startTestSession(t, pageConfig{}, rec.send, patient)
	b := newBrowser(t, s.origin())
	b.open(s.url())
	ctx, cancel := context.WithCancel(context.Background())
	ended := waitAsync(ctx, s)
	mustNotEnd(t, ended, 30*time.Millisecond)

	cancel() // Ctrl-C
	r := mustEnd(t, ended)
	if !errors.Is(r.err, context.Canceled) || !strings.Contains(r.err.Error(), "before anything was sent") {
		t.Errorf("err = %v, want an interruption wrapping context.Canceled", r.err)
	}
	if got := rec.sent(); len(got) != 0 {
		t.Errorf("sent %q", got)
	}
	mustBeStopped(t, s)
}

func TestSendInFlightCompletesBeforeShutdown(t *testing.T) {
	rec := &sendRecorder{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	s := startTestSession(t, pageConfig{}, rec.send, patient)
	b := newBrowser(t, s.origin())
	b.open(s.url())
	ctx, cancel := context.WithCancel(context.Background())
	ended := waitAsync(ctx, s)

	type result struct {
		status int
		resp   actionResponse
	}
	posted := make(chan result, 1)
	go func() {
		status, resp := b.post("/send", map[string]any{"text": "in flight"})
		posted <- result{status, resp}
	}()
	<-rec.started

	cancel() // Ctrl-C while Slack is being called
	mustNotEnd(t, ended, 100*time.Millisecond)

	close(rec.gate)
	r := mustEnd(t, ended)
	if r.err != nil || r.out.Sent == nil || r.out.Text != "in flight" {
		t.Errorf("outcome = %+v, err = %v; want the message reported as sent", r.out, r.err)
	}
	// The browser still got its answer before the server went away.
	if p := <-posted; p.status != http.StatusOK || !p.resp.OK {
		t.Errorf("send response: %d %+v", p.status, p.resp)
	}
	mustBeStopped(t, s)
}

func TestSendContextSurvivesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	seen := make(chan error, 1)
	send := func(sendCtx context.Context, _ string) (sentMessage, error) {
		cancel()
		seen <- sendCtx.Err()
		return sentMessage{TS: "1.000001"}, nil
	}
	s, err := startSession(ctx, pageConfig{}, send, patient)
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t, s.origin())
	b.open(s.url())
	ended := waitAsync(ctx, s)
	b.post("/send", map[string]any{"text": "x"})
	if err := <-seen; err != nil {
		t.Errorf("the send's context was cancelled with the command's: %v", err)
	}
	if r := mustEnd(t, ended); r.err != nil || r.out.Sent == nil {
		t.Errorf("outcome = %+v, err = %v", r.out, r.err)
	}
}

func TestFailedSendInFlightAtCancellationIsAnInterruption(t *testing.T) {
	rec := &sendRecorder{gate: make(chan struct{}), started: make(chan struct{}, 1), fail: errors.New("boom")}
	s := startTestSession(t, pageConfig{}, rec.send, patient)
	b := newBrowser(t, s.origin())
	b.open(s.url())
	ctx, cancel := context.WithCancel(context.Background())
	ended := waitAsync(ctx, s)
	go b.post("/send", map[string]any{"text": "x"})
	<-rec.started
	cancel()
	close(rec.gate)
	if r := mustEnd(t, ended); !errors.Is(r.err, context.Canceled) || r.out.Sent != nil {
		t.Errorf("outcome = %+v, err = %v", r.out, r.err)
	}
}

func TestUnopenedEditorTimesOut(t *testing.T) {
	to := patient
	to.Unopened = 60 * time.Millisecond
	s := startTestSession(t, pageConfig{}, (&sendRecorder{}).send, to)
	start := time.Now()
	r := mustEnd(t, waitAsync(context.Background(), s))
	if r.err != nil || r.out.Sent != nil || r.out.Reason != endNeverOpened {
		t.Errorf("outcome = %+v, err = %v", r.out, r.err)
	}
	if time.Since(start) < to.Unopened {
		t.Error("timed out early")
	}
	mustBeStopped(t, s)
}

func TestClosedTabEndsTheSession(t *testing.T) {
	t.Run("a tab that stops pinging", func(t *testing.T) {
		to := patient
		to.Unopened = 40 * time.Millisecond // opening the page lifts this one
		to.Silent = 250 * time.Millisecond
		s := startTestSession(t, pageConfig{}, (&sendRecorder{}).send, to)
		b := newBrowser(t, s.origin())
		b.open(s.url())
		ended := waitAsync(context.Background(), s)

		// Pings keep the session alive well past Silent.
		for range 8 {
			time.Sleep(60 * time.Millisecond)
			if status, _ := b.post("/ping", nil); status != http.StatusOK {
				t.Fatalf("ping: %d", status)
			}
		}
		mustNotEnd(t, ended, 0)

		// Then the tab goes away without a word (crash, kill).
		r := mustEnd(t, ended)
		if r.err != nil || r.out.Sent != nil || r.out.Reason != endTabClosed {
			t.Errorf("outcome = %+v, err = %v", r.out, r.err)
		}
		mustBeStopped(t, s)
	})

	t.Run("a tab that says goodbye", func(t *testing.T) {
		to := patient
		to.Closing = 60 * time.Millisecond
		s := startTestSession(t, pageConfig{}, (&sendRecorder{}).send, to)
		b := newBrowser(t, s.origin())
		b.open(s.url())
		ended := waitAsync(context.Background(), s)

		if status, _ := b.post("/bye", nil); status != http.StatusOK {
			t.Fatalf("bye: %d", status)
		}
		r := mustEnd(t, ended)
		if r.err != nil || r.out.Reason != endTabClosed {
			t.Errorf("outcome = %+v, err = %v", r.out, r.err)
		}
	})

	t.Run("a reload is not a close", func(t *testing.T) {
		to := patient
		to.Closing = 150 * time.Millisecond
		s := startTestSession(t, pageConfig{}, (&sendRecorder{}).send, to)
		b := newBrowser(t, s.origin())
		b.open(s.url())
		ended := waitAsync(context.Background(), s)

		b.post("/bye", nil)
		b.open(s.origin() + "/") // the reloaded page, authorised by its cookie
		mustNotEnd(t, ended, 2*to.Closing)

		if status, resp := b.post("/send", map[string]any{"text": "after reload"}); status != http.StatusOK || !resp.OK {
			t.Fatalf("send after reload: %d %+v", status, resp)
		}
		if r := mustEnd(t, ended); r.out.Sent == nil || r.out.Text != "after reload" {
			t.Errorf("outcome = %+v, err = %v", r.out, r.err)
		}
	})
}

func TestRedactSecrets(t *testing.T) {
	got := redactSecrets("invalid_auth for xoxc-1234-abcd and d=xoxd-AbC%2Fdef%3D; xoxb-1-2 too. Not a token: xox, boxes-1")
	want := "invalid_auth for [REDACTED] and d=[REDACTED]; [REDACTED] too. Not a token: xox, boxes-1"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestBrowserCommand(t *testing.T) {
	// A URL full of shell metacharacters stays one argument.
	u := "http://127.0.0.1:1234/?k=A&b=$(rm -rf ~);`x`|y %PATH% ^ \"q\""
	for goos, want := range map[string][]string{
		"darwin":  {"open", u},
		"linux":   {"xdg-open", u},
		"freebsd": {"xdg-open", u},
		"windows": {"rundll32", "url.dll,FileProtocolHandler", u},
	} {
		name, args := browserCommand(goos, u)
		got := append([]string{name}, args...)
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("%s: %q, want %q", goos, got, want)
		}
		for _, shell := range []string{"sh", "bash", "cmd", "cmd.exe", "powershell"} {
			if name == shell {
				t.Errorf("%s: the URL goes through a shell (%s)", goos, name)
			}
		}
	}
}

func TestSessionURL(t *testing.T) {
	s := startTestSession(t, pageConfig{}, (&sendRecorder{}).send, patient)
	u, err := url.Parse(s.url())
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "http" || u.Host != s.hosts[0] || u.Path != "/" || u.Query().Get("k") != s.key || len(u.Query()) != 1 {
		t.Errorf("url = %s", s.url())
	}
	if strings.Contains(s.url(), s.token) {
		t.Error("the URL carries the session token")
	}
}
