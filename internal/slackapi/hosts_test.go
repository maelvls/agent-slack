package slackapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWorkspaceOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://acme.slack.com":                 "https://acme.slack.com",
		"https://Acme.Slack.com/archives/C1?x=1": "https://acme.slack.com",
		" https://acme.slack.com/ ":              "https://acme.slack.com",
		"https://slack.com":                      "https://slack.com",
		"https://app.slack-gov.com/client":       "https://app.slack-gov.com",
		"https://a.b.slack.com.":                 "https://a.b.slack.com",
	} {
		if got, err := WorkspaceOrigin(in); err != nil || got != want {
			t.Errorf("WorkspaceOrigin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"",
		"acme.slack.com",
		"http://acme.slack.com",
		"ftp://acme.slack.com",
		"http://attacker.example",
		"https://attacker.example",
		"https://slack.com.attacker.example",
		"https://notslack.com",
		"https://evilslack.com",
		"https://acme.slack.com@attacker.example",
		"https://user:pw@acme.slack.com",
		"https://acme.slack.com:8443",
		"https://127.0.0.1",
		"https://files.slack-edge.com",
	} {
		if got, err := WorkspaceOrigin(bad); err == nil {
			t.Errorf("WorkspaceOrigin(%q) = %q, want an error", bad, got)
		}
	}
}

func TestIsSlackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"slack.com": true, "ACME.slack.com": true, "files.slack.com.": true, "x.slack-gov.com": true,
		"a.slack-edge.com": true, "slack-files.com": true,
		"attacker.example": false, "slack.com.attacker.example": false, "notslack.com": false, "": false,
	} {
		if got := IsSlackHost(host); got != want {
			t.Errorf("IsSlackHost(%q) = %v, want %v", host, got, want)
		}
	}
	if IsWorkspaceHost("a.slack-edge.com") {
		t.Error("a file host is not a workspace host")
	}
}

// A browser client never sends the xoxc token or the cookie to a URL that
// is not an https Slack workspace origin, whatever it was built with.
func TestBrowserCredentialsOnlyGoToSlack(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	auth := Auth{Type: AuthBrowser, XoxcToken: "xoxc-secret", XoxdCookie: "xoxd-secret"}

	// The transport sends everything to the test server: a request that
	// gets past the client's own check would be counted.
	capture := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hits.Add(1)
		return nil, http.ErrUseLastResponse
	})}
	for _, workspace := range []string{
		srv.URL, // loopback, without the test option
		"http://attacker.example",
		"https://attacker.example",
		"http://acme.slack.com",
		"https://acme.slack.com.attacker.example",
		"https://acme.slack.com:8443",
		"https://acme.slack.com/path",
	} {
		c := New(auth, workspace, WithHTTPClient(capture))
		_, err := c.API(context.Background(), "auth.test", nil)
		if err == nil || !strings.Contains(err.Error(), "refusing to send browser credentials") {
			t.Errorf("%s: err = %v", workspace, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: the error leaks a secret: %v", workspace, err)
		}
	}
	// The test option only opens loopback addresses.
	c := New(auth, "http://attacker.example", WithHTTPClient(capture), WithLoopbackWorkspaceForTesting())
	if _, err := c.API(context.Background(), "auth.test", nil); err == nil {
		t.Error("the test option let credentials go to a non-loopback host")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d request(s) were sent", n)
	}

	c = New(auth, srv.URL, WithLoopbackWorkspaceForTesting())
	if _, err := c.API(context.Background(), "auth.test", nil); err != nil || hits.Load() != 1 {
		t.Fatalf("loopback with the test option: err = %v, hits = %d", err, hits.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAuthorizeOnlyAddsCredentialsForSlackHosts(t *testing.T) {
	browser := New(Auth{Type: AuthBrowser, XoxcToken: "xoxc-1", XoxdCookie: "xoxd-1"}, "https://acme.slack.com")
	standard := New(Auth{Type: AuthStandard, Token: "xoxb-1"}, "")
	for target, want := range map[string]bool{
		"https://files.slack.com/files-pri/T1-F1/a.png": true,
		"https://a.slack-edge.com/x":                    true,
		"https://acme.slack-gov.com/x":                  true,
		"http://files.slack.com/x":                      false,
		"https://attacker.example/x":                    false,
		"https://files.slack.com.attacker.example/x":    false,
		"http://127.0.0.1:8080/x":                       false,
	} {
		for _, c := range []*Client{browser, standard} {
			req, err := http.NewRequest(http.MethodGet, target, nil)
			if err != nil {
				t.Fatal(err)
			}
			c.Authorize(req)
			got := req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != ""
			if got != want {
				t.Errorf("Authorize(%s) added credentials = %v, want %v", target, got, want)
			}
			if req.Header.Get("User-Agent") == "" {
				t.Errorf("Authorize(%s) did not set the User-Agent", target)
			}
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/x", nil)
	New(Auth{Type: AuthStandard, Token: "xoxb-1"}, "", WithLoopbackWorkspaceForTesting()).Authorize(req)
	if req.Header.Get("Authorization") != "Bearer xoxb-1" {
		t.Error("the test option did not allow a loopback request")
	}
}

func TestTimeoutSaysWhenTheRequestMayHaveBeenProcessed(t *testing.T) {
	for method, write := range map[string]bool{
		"chat.postMessage": true, "chat.update": true, "saved.update": true, "files.completeUploadExternal": true,
		"auth.test": false, "conversations.history": false, "users.info": false, "search.messages": false, "conversations.list": false,
	} {
		msg := timeoutError(method, APITimeout()).Error()
		if got := strings.Contains(msg, "the request may still have been processed"); got != write {
			t.Errorf("%s: %q", method, msg)
		}
		if !strings.Contains(msg, "timed out after") || !strings.Contains(msg, "SLACKCLI_API_TIMEOUT_MS") {
			t.Errorf("%s: %q", method, msg)
		}
	}
}
