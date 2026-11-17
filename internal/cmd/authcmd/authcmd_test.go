package authcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/authextract"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

type fakeKeychain map[string]string

func (k fakeKeychain) Get(service, account string) string { return k[service+"|"+account] }
func (k fakeKeychain) Set(service, account, value string) bool {
	k[service+"|"+account] = value
	return true
}
func (k fakeKeychain) Delete(service, account string) { delete(k, service+"|"+account) }

// harness is a Context wired to a temporary credentials store, with
// extractors that fail the test unless replaced.
type harness struct {
	t      *testing.T
	rt     *clictx.Context
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func newHarness(t *testing.T, kc auth.Keychain) *harness {
	t.Helper()
	for _, env := range []string{"SLACK_TOKEN", "SLACK_COOKIE_D", "SLACK_COOKIE", "SLACK_WORKSPACE_URL"} {
		t.Setenv(env, "")
	}
	h := &harness{t: t, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	unexpected := func(name string) clictx.Extractor {
		return func(context.Context) (*authextract.Extracted, error) {
			t.Errorf("unexpected call to the %s extractor", name)
			return nil, authextract.ErrNotFound
		}
	}
	h.rt = &clictx.Context{
		Store:   &auth.Store{Path: filepath.Join(t.TempDir(), "credentials.json"), Keychain: kc, Service: "svc"},
		Stdout:  h.stdout,
		Stderr:  h.stderr,
		Desktop: unexpected("Desktop"),
		Chrome:  unexpected("Chrome"),
		Brave:   unexpected("Brave"),
		Firefox: unexpected("Firefox"),
	}
	return h
}

// run executes "auth <args>" and returns what the command printed.
func (h *harness) run(stdin string, args ...string) (string, error) {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	cmd := New(h.rt)
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.ExecuteContext(context.Background())
	return h.stdout.String(), err
}

func (h *harness) mustRun(stdin string, args ...string) string {
	h.t.Helper()
	out, err := h.run(stdin, args...)
	if err != nil {
		h.t.Fatalf("auth %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// stored returns the usable credentials of a configured workspace.
func (h *harness) stored(url string) (auth.Workspace, slackapi.Auth) {
	h.t.Helper()
	creds, err := h.rt.Store.Load()
	if err != nil {
		h.t.Fatal(err)
	}
	w := creds.Find(url)
	if w == nil {
		h.t.Fatalf("workspace %s is not stored (have %d workspaces)", url, len(creds.Workspaces))
	}
	a, ok := h.rt.Store.Resolve(*w)
	if !ok {
		h.t.Fatalf("workspace %s has no usable secrets", url)
	}
	return *w, a
}

func (h *harness) workspaceCount() int {
	h.t.Helper()
	creds, err := h.rt.Store.Load()
	if err != nil {
		h.t.Fatal(err)
	}
	return len(creds.Workspaces)
}

func browserWorkspace(url, name, token string) auth.Workspace {
	return auth.Workspace{URL: url, Name: name, Auth: slackapi.Auth{
		Type: slackapi.AuthBrowser, XoxcToken: token, XoxdCookie: "xoxd-cookie-0123456789-abcdefghij",
	}}
}

func extraction(source string) *authextract.Extracted {
	return &authextract.Extracted{
		CookieD: "xoxd-cookie+decoded/0123456789==",
		Source:  source,
		Paths:   authextract.SourcePaths{LevelDB: "/slack/leveldb", Cookies: "/slack/Cookies"},
		Teams: []authextract.Team{
			{URL: "https://acme.slack.com/", Name: "Acme", Token: "xoxc-acme-0123456789"},
			{URL: "https://zeta.slack.com/", Name: "Zeta", Token: "xoxc-zeta-0123456789"},
		},
	}
}

func decodeJSON[T any](t *testing.T, out string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	return v
}

func TestRedactSecret(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "",
		"short":                  "[redacted]",
		"1234567890123":          "[redacted]", // 6 + 4 + 3: nothing would stay hidden
		"12345678901234":         "123456…1234",
		"xoxc-1234567890-abcdef": "xoxc-1…cdef",
	} {
		if got := redactSecret(in); got != want {
			t.Errorf("redactSecret(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWhoami(t *testing.T) {
	kc := fakeKeychain{}
	h := newHarness(t, kc)
	if err := h.rt.Store.Upsert(
		browserWorkspace("https://acme.slack.com", "Acme", "xoxc-acme-0123456789-secret"),
		browserWorkspace("https://lost.slack.com", "Lost", "xoxc-lost-0123456789-secret"),
		auth.Workspace{URL: "https://bot.slack.com", Auth: slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxb-bot-0123456789-secret"}},
	); err != nil {
		t.Fatal(err)
	}
	if err := h.rt.Store.SetDefault("https://bot.slack.com"); err != nil {
		t.Fatal(err)
	}
	// The user deleted one workspace's secrets from the keychain; a new
	// process then only sees the placeholders of the credentials file.
	delete(kc, "svc|xoxc:https://lost.slack.com")
	h.rt.Store = &auth.Store{Path: h.rt.Store.Path, Keychain: kc, Service: "svc"}

	out := h.mustRun("", "whoami")
	for _, leak := range []string{"0123456789", "__KEYCHAIN__"} {
		if strings.Contains(out, leak) {
			t.Errorf("output leaks %q:\n%s", leak, out)
		}
	}
	got := decodeJSON[whoamiOutput](t, out)
	if got.Version != 1 || got.UpdatedAt == "" || got.DefaultWorkspaceURL != "https://bot.slack.com" || got.Environment != nil {
		t.Errorf("unexpected header: %+v", got)
	}
	want := []whoamiWorkspace{
		{WorkspaceURL: "https://acme.slack.com", WorkspaceName: "Acme", AuthType: "browser", Token: "xoxc-a…cret", CookieD: "xoxd-c…ghij", Secrets: "ok"},
		{WorkspaceURL: "https://lost.slack.com", WorkspaceName: "Lost", AuthType: "browser", Secrets: "missing"},
		{WorkspaceURL: "https://bot.slack.com", AuthType: "standard", Token: "xoxb-b…cret", Default: true, Secrets: "ok"},
	}
	if len(got.Workspaces) != len(want) {
		t.Fatalf("got %d workspaces:\n%s", len(got.Workspaces), out)
	}
	for i, w := range got.Workspaces {
		if w.Secrets == "missing" {
			if !strings.Contains(w.Hint, "import") {
				t.Errorf("workspace %d: hint %q does not say to re-import", i, w.Hint)
			}
			w.Hint = ""
		}
		if w != want[i] {
			t.Errorf("workspace %d = %+v, want %+v", i, w, want[i])
		}
	}
}

func TestWhoamiEmptyAndEnvironment(t *testing.T) {
	h := newHarness(t, nil)
	got := decodeJSON[whoamiOutput](t, h.mustRun("", "whoami"))
	if got.Version != 1 || len(got.Workspaces) != 0 || got.Environment != nil {
		t.Errorf("empty store: %+v", got)
	}

	t.Setenv("SLACK_TOKEN", "xoxc-env-0123456789-secret")
	t.Setenv("SLACK_COOKIE_D", "d=xoxd-env-0123456789-secret")
	t.Setenv("SLACK_WORKSPACE_URL", "https://env.slack.com")
	out := h.mustRun("", "whoami")
	if strings.Contains(out, "0123456789") {
		t.Errorf("output leaks a secret:\n%s", out)
	}
	env := decodeJSON[whoamiOutput](t, out).Environment
	if env == nil || env.AuthType != "browser" || env.Token != "xoxc-e…cret" || env.CookieD != "xoxd-e…cret" || env.WorkspaceURL != "https://env.slack.com" {
		t.Errorf("environment = %+v", env)
	}
}

func TestAuthTest(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.rt.Store.Upsert(
		browserWorkspace("https://acme.slack.com", "Acme", "xoxc-acme"),
		browserWorkspace("https://zeta.slack.com", "Zeta", "xoxc-zeta"),
	); err != nil {
		t.Fatal(err)
	}
	var tokens []string
	handler := func(c *slackapi.Client) slackapi.Handler {
		return func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
			if method != "auth.test" {
				t.Errorf("unexpected method %s", method)
			}
			tokens = append(tokens, c.Auth().XoxcToken+"@"+c.WorkspaceURL())
			return map[string]any{"ok": true, "url": c.WorkspaceURL() + "/", "user": "mael", "empty": ""}, nil
		}
	}
	h.rt.ClientOptions = []slackapi.Option{func(c *slackapi.Client) { slackapi.WithHandler(handler(c))(c) }}

	got := decodeJSON[map[string]any](t, h.mustRun("", "test"))
	if got["user"] != "mael" || got["url"] != "https://acme.slack.com/" {
		t.Errorf("default workspace: %v", got)
	}
	if _, ok := got["empty"]; ok {
		t.Error("empty fields must be pruned")
	}
	got = decodeJSON[map[string]any](t, h.mustRun("", "test", "--workspace", "zeta"))
	if got["url"] != "https://zeta.slack.com/" {
		t.Errorf("--workspace zeta: %v", got)
	}
	t.Setenv("SLACK_WORKSPACE_URL", "https://zeta.slack.com")
	h.mustRun("", "test")
	want := []string{"xoxc-acme@https://acme.slack.com", "xoxc-zeta@https://zeta.slack.com", "xoxc-zeta@https://zeta.slack.com"}
	if fmt.Sprint(tokens) != fmt.Sprint(want) {
		t.Errorf("calls = %v, want %v", tokens, want)
	}
}

func TestAuthTestRefreshesExpiredCredentials(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.rt.Store.Upsert(browserWorkspace("https://acme.slack.com", "Acme", "xoxc-expired")); err != nil {
		t.Fatal(err)
	}
	h.rt.Desktop = func(context.Context) (*authextract.Extracted, error) { return extraction("Slack Desktop"), nil }
	h.rt.ClientOptions = []slackapi.Option{func(c *slackapi.Client) {
		slackapi.WithHandler(func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
			if c.Auth().XoxcToken == "xoxc-expired" {
				return nil, &slackapi.APIError{Method: method, Code: "invalid_auth"}
			}
			return map[string]any{"ok": true, "user": "mael"}, nil
		})(c)
	}}
	if got := decodeJSON[map[string]any](t, h.mustRun("", "test")); got["user"] != "mael" {
		t.Errorf("got %v", got)
	}

	// Without a Desktop session the Slack error is reported as is.
	h = newHarness(t, nil)
	if err := h.rt.Store.Upsert(browserWorkspace("https://acme.slack.com", "Acme", "xoxc-expired")); err != nil {
		t.Fatal(err)
	}
	h.rt.Desktop = func(context.Context) (*authextract.Extracted, error) { return nil, authextract.ErrNotFound }
	h.rt.ClientOptions = []slackapi.Option{slackapi.WithHandler(func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
		return nil, &slackapi.APIError{Method: method, Code: "invalid_auth"}
	})}
	if _, err := h.run("", "test"); !slackapi.IsErrorCode(err, "invalid_auth") {
		t.Errorf("err = %v, want invalid_auth", err)
	}
}

func TestImportDesktop(t *testing.T) {
	h := newHarness(t, nil)
	h.rt.Desktop = func(context.Context) (*authextract.Extracted, error) { return extraction("Slack Desktop"), nil }
	out := h.mustRun("", "import-desktop")
	want := `{
  "imported": 2,
  "source": {
    "leveldb_path": "/slack/leveldb",
    "cookies_path": "/slack/Cookies"
  },
  "workspaces": [
    {
      "workspace_url": "https://acme.slack.com",
      "workspace_name": "Acme"
    },
    {
      "workspace_url": "https://zeta.slack.com",
      "workspace_name": "Zeta"
    }
  ]
}
`
	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	w, a := h.stored("https://zeta.slack.com")
	if w.Name != "Zeta" || a.XoxcToken != "xoxc-zeta-0123456789" || a.XoxdCookie != "xoxd-cookie+decoded/0123456789==" {
		t.Errorf("stored %+v %+v", w, a)
	}
}

func TestImportBrowsers(t *testing.T) {
	for _, tc := range []struct {
		command, source string
		set             func(rt *clictx.Context, e clictx.Extractor)
	}{
		{"import-chrome", "Chrome", func(rt *clictx.Context, e clictx.Extractor) { rt.Chrome = e }},
		{"import-brave", "Brave", func(rt *clictx.Context, e clictx.Extractor) { rt.Brave = e }},
		{"import-firefox", "Firefox", func(rt *clictx.Context, e clictx.Extractor) { rt.Firefox = e }},
	} {
		t.Run(tc.command, func(t *testing.T) {
			h := newHarness(t, nil)
			tc.set(h.rt, func(context.Context) (*authextract.Extracted, error) { return extraction(tc.source), nil })
			if out, want := h.mustRun("", tc.command), "Imported 2 workspace token(s) from "+tc.source+".\n"; out != want {
				t.Errorf("output %q, want %q", out, want)
			}
			h.stored("https://acme.slack.com")
			h.stored("https://zeta.slack.com")

			// Absent application: the reason and what to do about it.
			h = newHarness(t, nil)
			tc.set(h.rt, func(context.Context) (*authextract.Extracted, error) {
				return nil, fmt.Errorf("%s is not running: %w", tc.source, authextract.ErrNotFound)
			})
			_, err := h.run("", tc.command)
			if err == nil || !strings.Contains(err.Error(), "is not running") || !strings.Contains(err.Error(), "make sure you are signed in") {
				t.Errorf("err = %v, want the reason and advice", err)
			}

			// A real failure is reported, not turned into "not logged in".
			tc.set(h.rt, func(context.Context) (*authextract.Extracted, error) {
				return nil, errors.New("could not decrypt the cookie: keychain item: timed out")
			})
			_, err = h.run("", tc.command)
			if err == nil || !strings.Contains(err.Error(), "timed out") || strings.Contains(err.Error(), "signed in") {
				t.Errorf("err = %v, want the real reason only", err)
			}

			// An extractor that finds nothing without saying why.
			tc.set(h.rt, func(context.Context) (*authextract.Extracted, error) { return nil, nil })
			if _, err = h.run("", tc.command); !errors.Is(err, authextract.ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound", err)
			}
			if n := h.workspaceCount(); n != 0 {
				t.Errorf("%d workspaces stored after failed imports", n)
			}
		})
	}
}

func TestImportReportsAppleScriptDisabled(t *testing.T) {
	h := newHarness(t, nil)
	h.rt.Brave = func(context.Context) (*authextract.Extracted, error) {
		return nil, authextract.ErrBraveAppleScriptDisabled
	}
	_, err := h.run("", "import-brave")
	if !errors.Is(err, authextract.ErrBraveAppleScriptDisabled) || !strings.Contains(err.Error(), "Allow JavaScript from Apple Events") {
		t.Errorf("err = %v", err)
	}
}

func TestImportSkipsUnusableTeams(t *testing.T) {
	h := newHarness(t, nil)
	h.rt.Firefox = func(context.Context) (*authextract.Extracted, error) {
		e := extraction("Firefox")
		e.Teams[0].URL = "not a url"
		return e, nil
	}
	if out := h.mustRun("", "import-firefox"); out != "Imported 1 workspace token(s) from Firefox.\n" {
		t.Errorf("output %q", out)
	}
	h.rt.Firefox = func(context.Context) (*authextract.Extracted, error) {
		e := extraction("Firefox")
		e.CookieD = ""
		return e, nil
	}
	if _, err := h.run("", "import-firefox"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("err = %v, want an error about incomplete credentials", err)
	}
}

func TestParseCurl(t *testing.T) {
	h := newHarness(t, nil)
	const curl = `curl 'https://acme.slack.com/api/client.counts' -b 'b=1; d=xoxd-abc%2Bdef%2Fghi%3D%3D' --data-raw 'token=xoxc-1-2-3-abcdef'`
	if out := h.mustRun(curl, "parse-curl"); out != "Imported tokens for https://acme.slack.com.\n" {
		t.Errorf("output %q", out)
	}
	_, a := h.stored("https://acme.slack.com")
	if a.XoxcToken != "xoxc-1-2-3-abcdef" || a.XoxdCookie != "xoxd-abc+def/ghi==" {
		t.Errorf("stored %+v", a)
	}

	for stdin, want := range map[string]string{
		"":                               "expected a cURL command on standard input",
		" \n":                            "expected a cURL command on standard input",
		"curl 'https://example.com/'":    "not an https request to Slack",
		strings.Repeat("x", 4<<20+1):     "larger than 4 MiB",
		"curl 'https://acme.slack.com/'": "xoxd cookie",
	} {
		if _, err := h.run(stdin, "parse-curl"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("stdin %.30q: err = %v, want %q", stdin, err, want)
		}
	}
	// The command line is not an input: secrets must not be passed there.
	if _, err := h.run("", "parse-curl", curl); err == nil {
		t.Error("parse-curl accepted the cURL command as an argument")
	}
}

func TestAdd(t *testing.T) {
	h := newHarness(t, nil)

	if out := h.mustRun("", "add", "--workspace-url", "bot.slack.com", "--token", "xoxb-1"); out != "Saved standard token.\n" {
		t.Errorf("output %q", out)
	}
	if !strings.Contains(h.stderr.String(), "--from-env") {
		t.Errorf("no warning about secrets on the command line: %q", h.stderr)
	}
	if _, a := h.stored("https://bot.slack.com"); a.Type != slackapi.AuthStandard || a.Token != "xoxb-1" {
		t.Errorf("stored %+v", a)
	}

	out := h.mustRun("", "add", "--workspace-url", "https://acme.slack.com/client/T1", "--xoxc", "xoxc-1", "--xoxd", "d=xoxd-abc%2Bdef")
	if out != "Saved browser tokens.\n" {
		t.Errorf("output %q", out)
	}
	// The cookie is stored decoded: the client encodes it when sending.
	if _, a := h.stored("https://acme.slack.com"); a.Type != slackapi.AuthBrowser || a.XoxcToken != "xoxc-1" || a.XoxdCookie != "xoxd-abc+def" {
		t.Errorf("stored %+v", a)
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"add", "--token", "xoxb-1"}, "workspace-url"},
		{[]string{"add", "--workspace-url", "nope", "--token", "xoxb-1"}, "--workspace-url must be a workspace URL"},
		{[]string{"add", "--workspace-url", "https://x.slack.com"}, "provide either --token or both --xoxc and --xoxd"},
		{[]string{"add", "--workspace-url", "https://x.slack.com", "--xoxc", "xoxc-1"}, "both --xoxc and --xoxd"},
		{[]string{"add", "--workspace-url", "https://x.slack.com", "--xoxd", "xoxd-1"}, "both --xoxc and --xoxd"},
		{[]string{"add", "--workspace-url", "https://x.slack.com", "--token", "xoxb-1", "--xoxc", "xoxc-1", "--xoxd", "xoxd-1"}, "not all of them"},
		{[]string{"add", "--workspace-url", "https://x.slack.com", "--token", "xoxc-1"}, "pass it with --xoxc"},
		{[]string{"add", "--workspace-url", "https://x.slack.com", "--xoxc", "xoxd-1", "--xoxd", "xoxc-1"}, "starting with xoxc-"},
		{[]string{"add", "--workspace-url", "https://x.slack.com", "--xoxc", "xoxc-1", "--xoxd", "nope"}, "starting with xoxd-"},
		{[]string{"add", "--workspace-url", "https://x.slack.com", "--from-env"}, "SLACK_TOKEN is not set"},
		{[]string{"add", "--workspace-url", "https://x.slack.com", "--from-env", "--token", "xoxb-1"}, "cannot be combined"},
		{[]string{"add", "--workspace-url", "https://x.slack.com", "extra"}, "unknown command"},
	} {
		if _, err := h.run("", tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	if n := h.workspaceCount(); n != 2 {
		t.Errorf("%d workspaces stored, want 2", n)
	}
}

func TestAddFromEnv(t *testing.T) {
	h := newHarness(t, nil)
	t.Setenv("SLACK_TOKEN", "xoxp-env")
	h.mustRun("", "add", "--workspace-url", "https://bot.slack.com", "--from-env")
	if _, a := h.stored("https://bot.slack.com"); a.Token != "xoxp-env" {
		t.Errorf("stored %+v", a)
	}
	if h.stderr.Len() != 0 {
		t.Errorf("unexpected warning: %q", h.stderr)
	}

	t.Setenv("SLACK_TOKEN", "xoxc-env")
	if _, err := h.run("", "add", "--workspace-url", "https://acme.slack.com", "--from-env"); err == nil || !strings.Contains(err.Error(), "SLACK_COOKIE_D") {
		t.Errorf("err = %v, want SLACK_COOKIE_D to be required", err)
	}
	t.Setenv("SLACK_COOKIE_D", "xoxd-env%2Bx")
	if out := h.mustRun("", "add", "--workspace-url", "https://acme.slack.com", "--from-env"); out != "Saved browser tokens.\n" {
		t.Errorf("output %q", out)
	}
	if _, a := h.stored("https://acme.slack.com"); a.XoxcToken != "xoxc-env" || a.XoxdCookie != "xoxd-env+x" {
		t.Errorf("stored %+v", a)
	}
}

func TestSetDefaultAndRemove(t *testing.T) {
	kc := fakeKeychain{}
	h := newHarness(t, kc)
	if err := h.rt.Store.Upsert(
		browserWorkspace("https://acme.slack.com", "Acme", "xoxc-acme"),
		browserWorkspace("https://acme-dev.slack.com", "Acme Dev", "xoxc-dev"),
		browserWorkspace("https://zeta.slack.com", "Zeta", "xoxc-zeta"),
	); err != nil {
		t.Fatal(err)
	}
	defaultURL := func() string {
		creds, err := h.rt.Store.Load()
		if err != nil {
			t.Fatal(err)
		}
		return creds.DefaultWorkspaceURL
	}

	if out := h.mustRun("", "set-default", "https://zeta.slack.com/archives/C1"); out != "Default workspace set to https://zeta.slack.com.\n" {
		t.Errorf("output %q", out)
	}
	if out := h.mustRun("", "set-default", "dev"); out != "Default workspace set to https://acme-dev.slack.com.\n" || defaultURL() != "https://acme-dev.slack.com" {
		t.Errorf("output %q, default %q", out, defaultURL())
	}
	for selector, want := range map[string]string{
		"slack":                   "matches several workspaces",
		"https://nope.slack.com":  "is not configured",
		"nope":                    "is not configured",
		"https://acme.slack.com ": "", // trimmed
	} {
		_, err := h.run("", "set-default", selector)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("set-default %q: err = %v, want %q", selector, err, want)
		}
	}
	for _, args := range [][]string{{"set-default"}, {"remove"}, {"remove", "a", "b"}} {
		if _, err := h.run("", args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}

	// Removing a workspace that is not configured is an error, not a
	// silent success.
	if _, err := h.run("", "remove", "https://nope.slack.com"); err == nil || !strings.Contains(err.Error(), "is not configured") {
		t.Errorf("err = %v", err)
	}
	if out := h.mustRun("", "remove", "https://acme.slack.com"); out != "Removed workspace https://acme.slack.com.\n" {
		t.Errorf("output %q", out)
	}
	if h.workspaceCount() != 2 || kc["svc|xoxc:https://acme.slack.com"] != "" {
		t.Errorf("workspace or its keychain secrets not removed: %d workspaces, keychain %v", h.workspaceCount(), kc)
	}
	// Removing the default falls back to another workspace.
	h.mustRun("", "remove", "acme-dev")
	if defaultURL() != "https://zeta.slack.com" {
		t.Errorf("default = %q", defaultURL())
	}
}

func TestCommandSurface(t *testing.T) {
	cmd := New(newHarness(t, nil).rt)
	var names []string
	for _, c := range cmd.Commands() {
		names = append(names, c.Name())
		if c.Short == "" || c.Long == "" {
			t.Errorf("%s lacks help text", c.Name())
		}
	}
	want := "add import-brave import-chrome import-desktop import-firefox parse-curl remove set-default test whoami"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("subcommands = %s, want %s", got, want)
	}
}
