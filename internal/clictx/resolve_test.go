package clictx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/authextract"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// lockableKeychain is an in-memory keychain that can be made to fail like
// a locked one, or one that times out on a prompt.
type lockableKeychain struct {
	items  map[string]string
	locked error
}

func (k *lockableKeychain) Get(service, account string) (string, error) {
	if k.locked != nil {
		return "", k.locked
	}
	return k.items[service+"|"+account], nil
}

func (k *lockableKeychain) Set(service, account, value string) bool {
	if k.locked != nil {
		return false
	}
	k.items[service+"|"+account] = value
	return true
}

func (k *lockableKeychain) Delete(service, account string) { delete(k.items, service+"|"+account) }

func browser(url, token string) auth.Workspace {
	return auth.Workspace{URL: url, Auth: slackapi.Auth{Type: slackapi.AuthBrowser, XoxcToken: token, XoxdCookie: "xoxd-stored"}}
}

func standard(url, token string) auth.Workspace {
	return auth.Workspace{URL: url, Auth: slackapi.Auth{Type: slackapi.AuthStandard, Token: token}}
}

// signedInTo returns an extractor for an application signed in to the given
// workspaces ("acme" → https://acme.slack.com with token xoxc-<app>-acme),
// and a counter of its calls.
func signedInTo(app string, names ...string) (Extractor, *int) {
	calls := new(int)
	return func(context.Context) (*authextract.Extracted, error) {
		*calls++
		e := &authextract.Extracted{CookieD: "xoxd-" + app}
		for _, n := range names {
			e.Teams = append(e.Teams, authextract.Team{URL: "https://" + n + ".slack.com/", Name: n, Token: "xoxc-" + app + "-" + n})
		}
		return e, nil
	}, calls
}

func mustUpsert(t *testing.T, c *Context, workspaces ...auth.Workspace) {
	t.Helper()
	if err := c.Store.Upsert(workspaces...); err != nil {
		t.Fatal(err)
	}
}

func storedURLs(t *testing.T, c *Context) string {
	t.Helper()
	creds, err := c.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	urls := make([]string, len(creds.Workspaces))
	for i, w := range creds.Workspaces {
		urls[i] = w.URL
	}
	return strings.Join(urls, " ")
}

func TestSafeModeFailsClosed(t *testing.T) {
	c := newContext(t)
	if c.SafeMode() {
		t.Fatal("safe mode is on with nothing set")
	}
	for _, tc := range []struct {
		current, legacy string
		flag, want      bool
	}{
		{"", "", true, true},
		{"1", "", false, true},
		{"enabled", "", false, true},
		{"y", "", false, true},
		{"always", "", false, true},
		{"", "1", false, true},
		// The new name saying "false" does not undo the legacy one.
		{"false", "1", false, true},
		{"1", "false", false, true},
		{"false", "off", false, false},
		{"0", "", false, false},
		{"false", "no", true, true},
	} {
		t.Setenv("SLACKCLI_SAFE_MODE", tc.current)
		t.Setenv("AGENT_SLACK_SAFE_MODE", tc.legacy)
		c.SafeModeFlag = tc.flag
		if got := c.SafeMode(); got != tc.want {
			t.Errorf("SLACKCLI_SAFE_MODE=%q AGENT_SLACK_SAFE_MODE=%q --safe-mode=%v: SafeMode() = %v, want %v",
				tc.current, tc.legacy, tc.flag, got, tc.want)
		}
		if blocked := c.CheckSafeMode("message delete") != nil; blocked != tc.want {
			t.Errorf("SLACKCLI_SAFE_MODE=%q AGENT_SLACK_SAFE_MODE=%q: message delete blocked = %v", tc.current, tc.legacy, blocked)
		}
	}
}

// With several workspaces and no default, nothing is picked silently.
func TestNoDefaultWorkspaceIsAnErrorNotAGuess(t *testing.T) {
	c := newContext(t)
	mustUpsert(t, c, browser("https://acme.slack.com", "xoxc-acme"), browser("https://zeta.slack.com", "xoxc-zeta"))

	_, err := c.Client(context.Background(), "")
	for _, want := range []string{"cannot tell which workspace to use", "https://acme.slack.com, https://zeta.slack.com", "--workspace", "SLACK_WORKSPACE_URL", "auth set-default"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Client(\"\"): err = %v, want it to contain %q", err, want)
		}
	}
	err = c.AssertWorkspaceForChannelNames("", "#general")
	for _, want := range []string{"ambiguous channel name", "https://acme.slack.com, https://zeta.slack.com", "--workspace", "auth set-default"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("AssertWorkspaceForChannelNames: err = %v, want it to contain %q", err, want)
		}
	}
	// Channel IDs, a selector, or a default settle it.
	if err := c.AssertWorkspaceForChannelNames("", "C0123456789"); err != nil {
		t.Errorf("channel ID: %v", err)
	}
	if err := c.AssertWorkspaceForChannelNames("zeta", "#general"); err != nil {
		t.Errorf("with a selector: %v", err)
	}
	if r, err := c.Client(context.Background(), "zeta"); err != nil || r.WorkspaceURL != "https://zeta.slack.com" {
		t.Errorf("Client(zeta) = %+v, %v", r, err)
	}
	if err := c.Store.SetDefault("https://zeta.slack.com"); err != nil {
		t.Fatal(err)
	}
	if err := c.AssertWorkspaceForChannelNames("", "#general"); err != nil {
		t.Errorf("with a default: %v", err)
	}
	if r, err := c.Client(context.Background(), ""); err != nil || r.WorkspaceURL != "https://zeta.slack.com" {
		t.Errorf("Client(\"\") = %+v, %v", r, err)
	}
}

// The on-the-fly import does not pick "the first team" either.
func TestOnTheFlyImportDoesNotGuessAWorkspace(t *testing.T) {
	t.Run("Slack Desktop with several teams", func(t *testing.T) {
		c := newContext(t)
		c.Desktop, _ = signedInTo("desktop", "acme", "zeta")
		_, err := c.Client(context.Background(), "")
		if err == nil || !strings.Contains(err.Error(), "cannot tell which workspace to use") || !strings.Contains(err.Error(), "https://acme.slack.com, https://zeta.slack.com") {
			t.Fatalf("err = %v", err)
		}
		// They are imported all the same: a selector now works offline.
		c.Desktop = nil
		if r, err := c.Client(context.Background(), "zeta"); err != nil || r.Auth.XoxcToken != "xoxc-desktop-zeta" {
			t.Errorf("Client(zeta) = %+v, %v", r, err)
		}
	})

	t.Run("a browser with several teams", func(t *testing.T) {
		c := newContext(t)
		c.Chrome, _ = signedInTo("chrome", "acme", "zeta")
		_, err := c.Client(context.Background(), "")
		if err == nil || !strings.Contains(err.Error(), "Chrome is signed in to 2 workspaces (https://acme.slack.com, https://zeta.slack.com)") {
			t.Fatalf("err = %v", err)
		}
		if got := storedURLs(t, c); got != "" {
			t.Errorf("stored %q", got)
		}
		r, err := c.Client(context.Background(), "zeta")
		if err != nil || r.WorkspaceURL != "https://zeta.slack.com" || storedURLs(t, c) != "https://zeta.slack.com" {
			t.Errorf("Client(zeta) = %+v, %v; stored %q", r, err, storedURLs(t, c))
		}
	})

	t.Run("a single team is unambiguous", func(t *testing.T) {
		c := newContext(t)
		c.Desktop, _ = signedInTo("desktop", "acme")
		if r, err := c.Client(context.Background(), ""); err != nil || r.WorkspaceURL != "https://acme.slack.com" {
			t.Errorf("got %+v, %v", r, err)
		}
	})
}

// When the secrets of the selected workspace cannot be read, the command
// must not run in whatever other workspace a browser is signed in to.
func TestUnreadableSecretsNeverSelectAnotherWorkspace(t *testing.T) {
	setup := func(t *testing.T) (*Context, *lockableKeychain) {
		c := newContext(t)
		kc := &lockableKeychain{items: map[string]string{}}
		c.Store.Keychain, c.Store.Service = kc, "svc"
		mustUpsert(t, c, browser("https://acme.slack.com", "xoxc-acme"))
		// A new process: nothing is cached, the file only has placeholders.
		c.Store = &auth.Store{Path: c.Store.Path, Keychain: kc, Service: "svc"}
		return c, kc
	}

	t.Run("keychain timeout, browsers signed in elsewhere", func(t *testing.T) {
		c, kc := setup(t)
		kc.locked = errors.New("the keychain did not answer within 3s")
		c.Desktop, _ = signedInTo("desktop", "other")
		c.Chrome, _ = signedInTo("chrome", "other")
		c.Firefox, _ = signedInTo("firefox", "other", "third")

		for _, selector := range []string{"", "acme", "https://acme.slack.com"} {
			r, err := c.Client(context.Background(), selector)
			if err == nil {
				t.Fatalf("selector %q: got a client for %s", selector, r.WorkspaceURL)
			}
			for _, want := range []string{
				"secrets for https://acme.slack.com are unavailable (keychain locked or timed out?)",
				"did not answer within 3s", "SLACKCLI_KEYCHAIN_TIMEOUT_MS", "auth import-desktop",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("selector %q: error %q does not contain %q", selector, err, want)
				}
			}
		}
		if got := storedURLs(t, c); got != "https://acme.slack.com" {
			t.Errorf("other workspaces were imported: %q", got)
		}
	})

	t.Run("the import may refresh that workspace only", func(t *testing.T) {
		c, kc := setup(t)
		kc.locked = errors.New("locked")
		c.Desktop, _ = signedInTo("desktop", "other", "acme")
		r, err := c.Client(context.Background(), "")
		if err != nil || r.WorkspaceURL != "https://acme.slack.com" || r.Auth.XoxcToken != "xoxc-desktop-acme" {
			t.Fatalf("got %+v, %v", r, err)
		}
		if got := storedURLs(t, c); got != "https://acme.slack.com" {
			t.Errorf("stored %q", got)
		}
	})

	t.Run("a browser may refresh that workspace only", func(t *testing.T) {
		c, kc := setup(t)
		delete(kc.items, "svc|xoxc:https://acme.slack.com") // gone, not locked
		c.Chrome, _ = signedInTo("chrome", "other")
		c.Firefox, _ = signedInTo("firefox", "other", "acme")
		r, err := c.Client(context.Background(), "")
		if err != nil || r.WorkspaceURL != "https://acme.slack.com" || r.Auth.XoxcToken != "xoxc-firefox-acme" {
			t.Fatalf("got %+v, %v", r, err)
		}
	})

	t.Run("secrets that are gone", func(t *testing.T) {
		c, kc := setup(t)
		delete(kc.items, "svc|xoxc:https://acme.slack.com")
		c.Chrome, _ = signedInTo("chrome", "other")
		r, err := c.Client(context.Background(), "")
		if err == nil || !strings.Contains(err.Error(), "the secrets stored for https://acme.slack.com are gone") || !strings.Contains(err.Error(), "auth import-desktop") {
			t.Fatalf("got %+v, %v", r, err)
		}
	})

	t.Run("a standard token is never replaced by an import", func(t *testing.T) {
		c := newContext(t)
		kc := &lockableKeychain{items: map[string]string{}}
		c.Store.Keychain, c.Store.Service = kc, "svc"
		mustUpsert(t, c, standard("https://acme.slack.com", "xoxb-bot"))
		c.Store = &auth.Store{Path: c.Store.Path, Keychain: kc, Service: "svc"}
		kc.locked = errors.New("locked")
		var calls *int
		c.Desktop, calls = signedInTo("desktop", "acme")
		c.Chrome, _ = signedInTo("chrome", "acme")
		r, err := c.Client(context.Background(), "")
		if err == nil || !strings.Contains(err.Error(), "secrets for https://acme.slack.com are unavailable") {
			t.Fatalf("got %+v, %v", r, err)
		}
		if *calls != 0 {
			t.Errorf("Slack Desktop was read %d time(s)", *calls)
		}
		kc.locked = nil
		if r, err := c.Client(context.Background(), ""); err != nil || r.Auth.Token != "xoxb-bot" {
			t.Errorf("after unlocking: %+v, %v", r, err)
		}
	})
}

// authError is what Slack answers to credentials it no longer accepts.
var authError = &slackapi.APIError{Method: "chat.postMessage", Code: "invalid_auth"}

// refreshHarness runs WithAutoRefresh over work that fails with
// invalid_auth for the tokens in rejected, and records the tokens used.
func runWithRefresh(c *Context, selector string, rejected ...string) (used []string, err error) {
	ctx := context.Background()
	_, err = WithAutoRefresh(ctx, c, func() (string, error) {
		r, err := c.Client(ctx, selector)
		if err != nil {
			return "", err
		}
		token := r.Auth.BearerToken()
		used = append(used, token+"@"+r.WorkspaceURL)
		for _, bad := range rejected {
			if token == bad {
				return "", fmt.Errorf("posting: %w", authError)
			}
		}
		return "sent", nil
	})
	return used, err
}

func TestAutoRefresh(t *testing.T) {
	t.Run("an expired browser session is refreshed and retried once", func(t *testing.T) {
		c := newContext(t)
		mustUpsert(t, c, browser("https://acme.slack.com", "xoxc-old"))
		c.Desktop, _ = signedInTo("desktop", "other", "acme")
		used, err := runWithRefresh(c, "", "xoxc-old")
		if err != nil || fmt.Sprint(used) != "[xoxc-old@https://acme.slack.com xoxc-desktop-acme@https://acme.slack.com]" {
			t.Fatalf("used = %v, err = %v", used, err)
		}
		// Only the workspace that failed was touched.
		if got := storedURLs(t, c); got != "https://acme.slack.com" {
			t.Errorf("stored %q", got)
		}
	})

	t.Run("a standard token is never swapped for the human's session", func(t *testing.T) {
		c := newContext(t)
		mustUpsert(t, c, standard("https://acme.slack.com", "xoxb-bot"))
		var calls *int
		c.Desktop, calls = signedInTo("desktop", "acme")
		used, err := runWithRefresh(c, "", "xoxb-bot")
		if !slackapi.IsAuthError(err) || fmt.Sprint(used) != "[xoxb-bot@https://acme.slack.com]" || *calls != 0 {
			t.Fatalf("used = %v, err = %v, Desktop calls = %d", used, err, *calls)
		}
		creds, _ := c.Store.Load()
		if a, err := c.Store.Resolve(creds.Workspaces[0]); err != nil || a.Type != slackapi.AuthStandard || a.Token != "xoxb-bot" {
			t.Errorf("stored auth = %+v, %v", a, err)
		}
	})

	t.Run("no retry when Slack Desktop holds the same session", func(t *testing.T) {
		c := newContext(t)
		c.Desktop, _ = signedInTo("desktop", "acme")
		// The stored session is the one Desktop has.
		mustUpsert(t, c, auth.Workspace{URL: "https://acme.slack.com", Auth: slackapi.Auth{
			Type: slackapi.AuthBrowser, XoxcToken: "xoxc-desktop-acme", XoxdCookie: "xoxd-desktop"}})
		used, err := runWithRefresh(c, "", "xoxc-desktop-acme")
		if !slackapi.IsAuthError(err) || len(used) != 1 {
			t.Fatalf("used = %v, err = %v", used, err)
		}
	})

	t.Run("no retry when Slack Desktop is not signed in to that workspace", func(t *testing.T) {
		c := newContext(t)
		mustUpsert(t, c, browser("https://acme.slack.com", "xoxc-old"))
		c.Desktop, _ = signedInTo("desktop", "other")
		used, err := runWithRefresh(c, "", "xoxc-old")
		if !slackapi.IsAuthError(err) || len(used) != 1 || storedURLs(t, c) != "https://acme.slack.com" {
			t.Fatalf("used = %v, err = %v, stored %q", used, err, storedURLs(t, c))
		}
	})

	t.Run("the workspace that failed is the one refreshed", func(t *testing.T) {
		c := newContext(t)
		mustUpsert(t, c, browser("https://acme.slack.com", "xoxc-acme-old"))
		mustUpsert(t, c, browser("https://zeta.slack.com", "xoxc-zeta-old"))
		c.Desktop, _ = signedInTo("desktop", "acme", "zeta")
		used, err := runWithRefresh(c, "zeta", "xoxc-zeta-old")
		if err != nil || fmt.Sprint(used) != "[xoxc-zeta-old@https://zeta.slack.com xoxc-desktop-zeta@https://zeta.slack.com]" {
			t.Fatalf("used = %v, err = %v", used, err)
		}
		creds, _ := c.Store.Load()
		if a, _ := c.Store.Resolve(*creds.Find("https://acme.slack.com")); a.XoxcToken != "xoxc-acme-old" {
			t.Errorf("acme was refreshed too: %+v", a)
		}
	})

	t.Run("credentials from the environment are never refreshed", func(t *testing.T) {
		c := newContext(t)
		mustUpsert(t, c, browser("https://acme.slack.com", "xoxc-old"))
		t.Setenv("SLACK_TOKEN", "xoxc-env")
		t.Setenv("SLACK_COOKIE_D", "xoxd-env")
		var calls *int
		c.Desktop, calls = signedInTo("desktop", "acme")
		used, err := runWithRefresh(c, "https://acme.slack.com", "xoxc-env")
		if !slackapi.IsAuthError(err) || len(used) != 1 || *calls != 0 {
			t.Fatalf("used = %v, err = %v, Desktop calls = %d", used, err, *calls)
		}
	})

	t.Run("other errors are not retried", func(t *testing.T) {
		c := newContext(t)
		mustUpsert(t, c, browser("https://acme.slack.com", "xoxc-old"))
		var calls *int
		c.Desktop, calls = signedInTo("desktop", "acme")
		runs := 0
		_, err := WithAutoRefresh(context.Background(), c, func() (string, error) {
			runs++
			if _, err := c.Client(context.Background(), ""); err != nil {
				return "", err
			}
			return "", &slackapi.APIError{Method: "chat.postMessage", Code: "channel_not_found"}
		})
		if !slackapi.IsErrorCode(err, "channel_not_found") || runs != 1 || *calls != 0 {
			t.Fatalf("err = %v, runs = %d, Desktop calls = %d", err, runs, *calls)
		}
	})
}

// With credentials in the environment, a selector that designates nothing
// is an error: it must not silently become SLACK_WORKSPACE_URL, and a URL
// that is not Slack's is never used.
func TestEnvCredentialsAndWorkspaceSelection(t *testing.T) {
	c := newContext(t)
	t.Setenv("SLACK_TOKEN", "xoxc-env")
	t.Setenv("SLACK_COOKIE_D", "xoxd-env")

	for _, selector := range []string{"http://attacker.example", "https://attacker.example", "http://acme.slack.com", "nope"} {
		t.Setenv("SLACK_WORKSPACE_URL", "https://acme.slack.com")
		if r, err := c.Client(context.Background(), selector); err == nil {
			t.Errorf("selector %q: got a client for %q", selector, r.WorkspaceURL)
		}
	}
	t.Setenv("SLACK_WORKSPACE_URL", "http://attacker.example")
	if r, err := c.Client(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "SLACK_WORKSPACE_URL must be an https Slack workspace URL") {
		t.Errorf("got %+v, %v", r, err)
	}
	t.Setenv("SLACK_WORKSPACE_URL", "https://acme.slack.com")
	if r, err := c.Client(context.Background(), ""); err != nil || r.WorkspaceURL != "https://acme.slack.com" {
		t.Errorf("got %+v, %v", r, err)
	}

	// A standard token goes to slack.com whatever the workspace: a bad
	// SLACK_WORKSPACE_URL is ignored rather than used.
	t.Setenv("SLACK_TOKEN", "xoxb-env")
	t.Setenv("SLACK_WORKSPACE_URL", "http://attacker.example")
	if r, err := c.Client(context.Background(), ""); err != nil || r.WorkspaceURL != "" {
		t.Errorf("got %+v, %v", r, err)
	}
}
