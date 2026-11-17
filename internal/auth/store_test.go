package auth

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/slackcli/internal/slackapi"
)

// fakeKeychain is an in-memory keychain. The key unavailableKey makes every
// Get fail, like a locked keychain or one that times out on a prompt.
type fakeKeychain map[string]string

const unavailableKey = "!unavailable"

func (k fakeKeychain) Get(service, account string) (string, error) {
	if msg, ok := k[unavailableKey]; ok {
		return "", errors.New(msg)
	}
	return k[service+"|"+account], nil
}
func (k fakeKeychain) Set(service, account, value string) bool {
	k[service+"|"+account] = value
	return true
}
func (k fakeKeychain) Delete(service, account string) { delete(k, service+"|"+account) }

func browserWS(u, xoxc, xoxd string) Workspace {
	return Workspace{URL: u, Auth: slackapi.Auth{Type: slackapi.AuthBrowser, XoxcToken: xoxc, XoxdCookie: xoxd}}
}

func standardWS(u, token string) Workspace {
	return Workspace{URL: u, Auth: slackapi.Auth{Type: slackapi.AuthStandard, Token: token}}
}

func TestStoreRoundTripWithKeychain(t *testing.T) {
	kc := fakeKeychain{}
	path := filepath.Join(t.TempDir(), "sub", "credentials.json")
	s := &Store{Path: path, Keychain: kc, Service: "svc"}

	if err := s.Upsert(
		browserWS("https://A.slack.com/archives/C1", "xoxc-a", "xoxd-a"),
		browserWS("https://b.slack.com", "xoxc-b", "xoxd-b"),
		standardWS("https://c.slack.com", "xoxb-c"),
	); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	for _, secret := range []string{"xoxc-", "xoxd-", "xoxb-"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret %q written to disk:\n%s", secret, raw)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the lock file was left behind: %v", err)
	}
	// Each workspace has its own cookie: two browser accounts must not
	// overwrite each other.
	if kc["svc|xoxd:https://a.slack.com"] != "xoxd-a" || kc["svc|xoxd:https://b.slack.com"] != "xoxd-b" {
		t.Fatalf("keychain = %v", kc)
	}

	fresh := &Store{Path: path, Keychain: kc, Service: "svc"}
	creds, err := fresh.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(creds.Workspaces) != 3 || creds.Workspaces[0].URL != "https://a.slack.com" {
		t.Fatalf("creds = %+v", creds)
	}
	a, err := fresh.Resolve(creds.Workspaces[1])
	if err != nil || a.XoxcToken != "xoxc-b" || a.XoxdCookie != "xoxd-b" {
		t.Fatalf("resolve = %+v %v", a, err)
	}
	c, err := fresh.Resolve(creds.Workspaces[2])
	if err != nil || c.Token != "xoxb-c" {
		t.Fatalf("resolve = %+v %v", c, err)
	}

	// A secret that vanished from the keychain is reported, not sent to
	// Slack as the literal placeholder.
	delete(kc, "svc|xoxc:https://a.slack.com")
	got, err := (&Store{Path: path, Keychain: kc, Service: "svc"}).Resolve(creds.Workspaces[0])
	var se *SecretsError
	if !errors.As(err, &se) || !se.Missing() || se.WorkspaceURL != "https://a.slack.com" {
		t.Fatalf("resolved a workspace whose token is gone: %+v, %v", got, err)
	}
	if got.BearerToken() != "" || got.XoxdCookie != "" {
		t.Fatalf("a failed Resolve returned secrets or placeholders: %+v", got)
	}
}

// A keychain that cannot be read is not the same as a secret that is gone:
// callers must be able to tell, to avoid acting on another workspace.
func TestResolveDistinguishesMissingFromUnavailable(t *testing.T) {
	kc := fakeKeychain{}
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := &Store{Path: path, Keychain: kc, Service: "svc"}
	if err := s.Upsert(browserWS("https://a.slack.com", "xoxc-a", "xoxd-a")); err != nil {
		t.Fatal(err)
	}

	kc[unavailableKey] = "the keychain did not answer within 3s"
	fresh := &Store{Path: path, Keychain: kc, Service: "svc"}
	creds, _ := fresh.Load()
	_, err := fresh.Resolve(creds.Workspaces[0])
	var se *SecretsError
	if !errors.As(err, &se) || se.Missing() {
		t.Fatalf("err = %v, want an unavailable keychain", err)
	}
	for _, want := range []string{"https://a.slack.com", "unavailable", "did not answer within 3s"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}

	// Nothing was cached: once the keychain answers again, so does Resolve.
	delete(kc, unavailableKey)
	if a, err := fresh.Resolve(creds.Workspaces[0]); err != nil || a.XoxcToken != "xoxc-a" {
		t.Fatalf("resolve = %+v, %v", a, err)
	}
}

func TestKeychainGetResult(t *testing.T) {
	exit := func(code int) error {
		err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
		if err == nil {
			t.Fatal("expected an exit error")
		}
		return err
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	if v, err := keychainGetResult([]byte(" xoxc-1\n"), nil, false, time.Second); v != "xoxc-1" || err != nil {
		t.Errorf("found: %q, %v", v, err)
	}
	if v, err := keychainGetResult(nil, exit(securityItemNotFound), false, time.Second); v != "" || err != nil {
		t.Errorf("not found: %q, %v", v, err)
	}
	if _, err := keychainGetResult(nil, exit(36), false, time.Second); err == nil || !strings.Contains(err.Error(), "status 36") {
		t.Errorf("locked: %v", err)
	}
	if _, err := keychainGetResult(nil, exit(1), true, 3*time.Second); err == nil || !strings.Contains(err.Error(), "did not answer within 3s") {
		t.Errorf("timeout: %v", err)
	}
}

func TestStoreWithoutKeychainKeepsSecretsInFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := &Store{Path: path}
	if err := s.Upsert(browserWS("https://a.slack.com", "xoxc-a", "xoxd-a")); err != nil {
		t.Fatal(err)
	}
	creds, _ := (&Store{Path: path}).Load()
	a, err := (&Store{Path: path}).Resolve(creds.Workspaces[0])
	if err != nil || a.XoxcToken != "xoxc-a" {
		t.Fatalf("resolve = %+v %v", a, err)
	}
}

func TestUpsertKeepsMetadataAndRemove(t *testing.T) {
	kc := fakeKeychain{}
	s := &Store{Path: filepath.Join(t.TempDir(), "credentials.json"), Keychain: kc, Service: "svc"}
	w := browserWS("https://a.slack.com", "xoxc-1", "xoxd-1")
	w.Name = "Acme"
	if err := s.Upsert(w, browserWS("https://b.slack.com", "xoxc-b", "xoxd-b")); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(browserWS("https://a.slack.com", "xoxc-2", "xoxd-2")); err != nil {
		t.Fatal(err)
	}
	creds, _ := s.Load()
	if got := creds.Find("https://a.slack.com"); got.Name != "Acme" {
		t.Fatalf("name lost: %+v", got)
	}
	if a, _ := s.Resolve(*creds.Find("https://a.slack.com")); a.XoxcToken != "xoxc-2" {
		t.Fatalf("token not refreshed: %+v", a)
	}
	// The untouched workspace is still resolvable after a save.
	if b, err := s.Resolve(*creds.Find("https://b.slack.com")); err != nil || b.XoxcToken != "xoxc-b" {
		t.Fatalf("b = %+v %v", b, err)
	}

	if err := s.SetDefault("https://nope.slack.com"); err == nil {
		t.Fatal("SetDefault accepted an unknown workspace")
	}
	removed, err := s.Remove("https://a.slack.com")
	if err != nil || !removed {
		t.Fatal(removed, err)
	}
	creds, _ = s.Load()
	if creds.DefaultWorkspaceURL != "https://b.slack.com" || len(creds.Workspaces) != 1 {
		t.Fatalf("creds = %+v", creds)
	}
	if _, ok := kc["svc|xoxc:https://a.slack.com"]; ok {
		t.Fatal("keychain entry not deleted")
	}
	if removed, err := s.Remove("https://a.slack.com"); err != nil || removed {
		t.Fatalf("removing an absent workspace = %v, %v", removed, err)
	}
}

// The default workspace is only set implicitly when there is no doubt.
func TestDefaultWorkspaceRule(t *testing.T) {
	newStore := func(t *testing.T) *Store {
		return &Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
	}
	state := func(t *testing.T, s *Store) (def string, picked string) {
		t.Helper()
		// A fresh Store: what the next invocation will see.
		creds, err := (&Store{Path: s.Path}).Load()
		if err != nil {
			t.Fatal(err)
		}
		if w := creds.Default(); w != nil {
			picked = w.URL
		}
		return creds.DefaultWorkspaceURL, picked
	}
	a, b, c := browserWS("https://a.slack.com", "xoxc-a", "xoxd"), browserWS("https://b.slack.com", "xoxc-b", "xoxd"), browserWS("https://c.slack.com", "xoxc-c", "xoxd")
	must := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	t.Run("several at once: none", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Upsert(a, b, c))
		if def, picked := state(t, s); def != "" || picked != "" {
			t.Fatalf("default = %q, Default() = %q; want none", def, picked)
		}
		// Importing them again changes nothing.
		_, err := s.UpsertImported(a, b, c)
		must(t, err)
		if def, _ := state(t, s); def != "" {
			t.Fatalf("default = %q after a second import", def)
		}
	})

	t.Run("a single one is the default and stays it", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Upsert(b))
		if def, picked := state(t, s); def != b.URL || picked != b.URL {
			t.Fatalf("default = %q, Default() = %q", def, picked)
		}
		must(t, s.Upsert(a, c))
		if def, picked := state(t, s); def != b.URL || picked != b.URL {
			t.Fatalf("default = %q, Default() = %q after adding more", def, picked)
		}
	})

	t.Run("an explicit default is kept", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Upsert(a, b, c))
		must(t, s.SetDefault(c.URL))
		must(t, s.Upsert(a))
		if def, _ := state(t, s); def != c.URL {
			t.Fatalf("default = %q", def)
		}
	})

	t.Run("removing the default leaves none unless one remains", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Upsert(a))
		must(t, s.Upsert(b, c))
		if _, err := s.Remove(a.URL); err != nil {
			t.Fatal(err)
		}
		if def, picked := state(t, s); def != "" || picked != "" {
			t.Fatalf("default = %q, Default() = %q; want none with two left", def, picked)
		}
		if _, err := s.Remove(b.URL); err != nil {
			t.Fatal(err)
		}
		if def, _ := state(t, s); def != c.URL {
			t.Fatalf("default = %q; want the only workspace left", def)
		}
	})

	t.Run("removing another workspace keeps the default", func(t *testing.T) {
		s := newStore(t)
		must(t, s.Upsert(a))
		must(t, s.Upsert(b, c))
		if _, err := s.Remove(b.URL); err != nil {
			t.Fatal(err)
		}
		if def, _ := state(t, s); def != a.URL {
			t.Fatalf("default = %q", def)
		}
	})
}

// An import never replaces a standard token with a browser session: that
// would change who commands act as.
func TestUpsertImportedKeepsStandardTokens(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "credentials.json"), Keychain: fakeKeychain{}, Service: "svc"}
	if err := s.Upsert(standardWS("https://bot.slack.com", "xoxb-bot")); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(browserWS("https://human.slack.com", "xoxc-old", "xoxd-old")); err != nil {
		t.Fatal(err)
	}

	kept, err := s.UpsertImported(
		browserWS("https://bot.slack.com", "xoxc-human", "xoxd-new"),
		browserWS("https://human.slack.com", "xoxc-new", "xoxd-new"),
		browserWS("https://other.slack.com", "xoxc-other", "xoxd-new"),
	)
	if err != nil || len(kept) != 1 || kept[0] != "https://bot.slack.com" {
		t.Fatalf("kept = %v, err = %v", kept, err)
	}
	creds, _ := s.Load()
	if len(creds.Workspaces) != 3 {
		t.Fatalf("creds = %+v", creds)
	}
	bot, err := s.Resolve(*creds.Find("https://bot.slack.com"))
	if err != nil || bot.Type != slackapi.AuthStandard || bot.Token != "xoxb-bot" {
		t.Fatalf("the bot token was replaced: %+v, %v", bot, err)
	}
	human, err := s.Resolve(*creds.Find("https://human.slack.com"))
	if err != nil || human.XoxcToken != "xoxc-new" {
		t.Fatalf("the browser workspace was not refreshed: %+v, %v", human, err)
	}

	// An explicit Upsert for that workspace does replace it.
	if err := s.Upsert(browserWS("https://bot.slack.com", "xoxc-human", "xoxd-new")); err != nil {
		t.Fatal(err)
	}
	creds, _ = s.Load()
	if got, _ := s.Resolve(*creds.Find("https://bot.slack.com")); !got.IsBrowser() {
		t.Fatalf("explicit Upsert did not replace: %+v", got)
	}
}

func TestMigrateLegacy(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy.json")
	_ = os.WriteFile(legacy, []byte(`{"version":1,"default_workspace_url":"https://b.slack.com","workspaces":[
	  {"workspace_url":"https://a.slack.com","workspace_name":"A","auth":{"auth_type":"browser","xoxc_token":"__KEYCHAIN__","xoxd_cookie":"__KEYCHAIN__"}},
	  {"workspace_url":"https://b.slack.com","auth":{"auth_type":"browser","xoxc_token":"__KEYCHAIN__","xoxd_cookie":"__KEYCHAIN__"}},
	  {"workspace_url":"https://gone.slack.com","auth":{"auth_type":"browser","xoxc_token":"__KEYCHAIN__","xoxd_cookie":"__KEYCHAIN__"}},
	  {"workspace_url":"not a url","auth":{"auth_type":"browser","xoxc_token":"x","xoxd_cookie":"y"}},
	  {"workspace_url":"https://attacker.example","auth":{"auth_type":"browser","xoxc_token":"x","xoxd_cookie":"y"}}
	]}`), 0o600)
	kc := fakeKeychain{
		"old|xoxc:https://a.slack.com": "xoxc-a",
		"old|xoxc:https://b.slack.com": "xoxc-b",
		"old|xoxd":                     "xoxd-shared",
	}
	newPath := filepath.Join(dir, "new", "credentials.json")

	// While the keychain cannot be read nothing is migrated, so that no
	// workspace is dropped for good.
	kc[unavailableKey] = "locked"
	locked := &Store{Path: newPath, Keychain: kc, Service: "new", LegacyPath: legacy, LegacyService: "old"}
	if creds, err := locked.Load(); err != nil || len(creds.Workspaces) != 0 {
		t.Fatalf("creds = %+v, err = %v", creds, err)
	}
	if _, err := os.Stat(newPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a credentials file was written while the keychain was locked: %v", err)
	}
	delete(kc, unavailableKey)

	s := &Store{Path: newPath, Keychain: kc, Service: "new", LegacyPath: legacy, LegacyService: "old"}
	creds, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(creds.Workspaces) != 2 || creds.DefaultWorkspaceURL != "https://b.slack.com" || creds.Workspaces[0].Name != "A" {
		t.Fatalf("creds = %+v", creds)
	}
	if kc["new|xoxc:https://a.slack.com"] != "xoxc-a" || kc["new|xoxd:https://b.slack.com"] != "xoxd-shared" {
		t.Fatalf("keychain = %v", kc)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatal("new credentials file not written")
	}
	a, err := s.Resolve(creds.Workspaces[0])
	if err != nil || a.XoxdCookie != "xoxd-shared" {
		t.Fatalf("resolve = %+v", a)
	}
}

// A corrupt file reads as empty, but is not silently thrown away by the
// next save.
func TestCorruptFileIsPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	_ = os.WriteFile(path, []byte("{nope"), 0o600)
	var warnings []string
	s := &Store{Path: path, Warnf: func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }}
	creds, err := s.Load()
	if err != nil || len(creds.Workspaces) != 0 {
		t.Fatalf("creds=%+v err=%v", creds, err)
	}
	if err := s.Upsert(browserWS("https://a.slack.com", "xoxc-a", "xoxd-a")); err != nil {
		t.Fatal(err)
	}

	backups, _ := filepath.Glob(path + ".corrupt-*")
	if len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}
	if raw, _ := os.ReadFile(backups[0]); string(raw) != "{nope" {
		t.Errorf("backup content = %q", raw)
	}
	if fi, _ := os.Stat(backups[0]); fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("backup mode = %v", fi.Mode().Perm())
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], backups[0]) || !strings.Contains(warnings[0], "auth import-desktop") {
		t.Errorf("warnings = %q", warnings)
	}

	// The file is valid again; nothing more is copied or said.
	fresh := &Store{Path: path, Warnf: s.Warnf}
	if creds, err := fresh.Load(); err != nil || len(creds.Workspaces) != 1 {
		t.Fatalf("creds=%+v err=%v", creds, err)
	}
	if again, _ := filepath.Glob(path + ".corrupt-*"); len(again) != 1 || len(warnings) != 1 {
		t.Errorf("backups = %v, warnings = %q", again, warnings)
	}
}

// Two invocations that modify the credentials at the same time both win.
func TestConcurrentStoresDoNotLoseUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	const writers, each = 6, 5
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for i := 0; i < writers; i++ {
		// One Store per goroutine, like one process each: every one of
		// them has its own, soon stale, view of the file.
		s := &Store{Path: path}
		if _, err := s.Load(); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				errs <- s.Upsert(browserWS(fmt.Sprintf("https://w%d-%d.slack.com", i, j), "xoxc", "xoxd"))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	creds, err := (&Store{Path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(creds.Workspaces) != writers*each {
		t.Fatalf("%d workspaces survived, want %d", len(creds.Workspaces), writers*each)
	}
	leftovers, _ := filepath.Glob(path + ".*")
	if len(leftovers) != 0 {
		t.Errorf("left behind: %v", leftovers)
	}
}

func TestLockFile(t *testing.T) {
	oldWait, oldStale := lockWait, lockStale
	t.Cleanup(func() { lockWait, lockStale = oldWait, oldStale })
	path := filepath.Join(t.TempDir(), "credentials.json")

	// A live lock makes a second writer wait, then fail with advice.
	unlock, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lockWait = 60 * time.Millisecond
	if _, err := lockFile(path); err == nil || !strings.Contains(err.Error(), path+".lock") || !strings.Contains(err.Error(), "locked by another") {
		t.Fatalf("err = %v", err)
	}
	unlock()
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unlock left the lock file: %v", err)
	}

	// A lock left by a process that died is taken over.
	if err := os.WriteFile(path+".lock", []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path+".lock", old, old); err != nil {
		t.Fatal(err)
	}
	unlock, err = lockFile(path)
	if err != nil {
		t.Fatalf("a stale lock was not taken over: %v", err)
	}
	unlock()
}

func TestSelectWorkspace(t *testing.T) {
	ws := []Workspace{
		{URL: "https://acme.slack.com", Name: "Acme Corp"},
		{URL: "https://acme-dev.slack.com", Name: "Acme Dev", TeamDomain: "acmedev"},
		{URL: "https://kubernetes.slack.com", Name: "Kubernetes"},
	}
	cases := []struct {
		sel       string
		want      string
		ambiguous int
	}{
		{"https://acme.slack.com/archives/C1", "https://acme.slack.com", 0},
		{"kube", "https://kubernetes.slack.com", 0},
		{"KUBERNETES", "https://kubernetes.slack.com", 0},
		{"acme", "https://acme.slack.com", 0}, // exact host label beats substring
		{"acmedev", "https://acme-dev.slack.com", 0},
		{"acm", "", 2},
		{"slack.com", "", 3},
		{"nope", "", 0},
		{"  ", "", 0},
	}
	for _, c := range cases {
		m, amb := SelectWorkspace(ws, c.sel)
		got := ""
		if m != nil {
			got = m.URL
		}
		if got != c.want || len(amb) != c.ambiguous {
			t.Errorf("SelectWorkspace(%q) = %q, %d ambiguous; want %q, %d", c.sel, got, len(amb), c.want, c.ambiguous)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://Acme.slack.com/archives/C1?x=1": "https://acme.slack.com",
		"acme.slack.com":                         "https://acme.slack.com",
		" https://acme.slack.com/ ":              "https://acme.slack.com",
		"https://acme.slack-gov.com/x":           "https://acme.slack-gov.com",
		"app.slack.com/client/T1":                "https://app.slack.com",
	} {
		got, err := NormalizeURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{
		"", "acme", "ftp://acme.slack.com", "#general",
		// Credentials are sent to the workspace URL: only https Slack hosts.
		"http://acme.slack.com",
		"http://attacker.example",
		"https://attacker.example",
		"attacker.example",
		"https://acme.slack.com.attacker.example",
		"https://acme.slack.com@attacker.example/",
		"https://acme.slack.com:8443",
		"http://127.0.0.1:8080",
	} {
		if got, err := NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) = %q, want error", bad, got)
		}
	}
}

// Stored credentials for a URL that is not Slack are never handed out, and
// a store refuses to save any.
func TestStoreRefusesNonSlackWorkspaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	_ = os.WriteFile(path, []byte(`{"version":1,"default_workspace_url":"http://attacker.example","workspaces":[
	  {"workspace_url":"http://attacker.example","auth":{"auth_type":"browser","xoxc_token":"xoxc-1","xoxd_cookie":"xoxd-1"}},
	  {"workspace_url":"https://acme.slack.com","auth":{"auth_type":"browser","xoxc_token":"xoxc-2","xoxd_cookie":"xoxd-2"}}
	]}`), 0o600)
	s := &Store{Path: path}
	creds, err := s.Load()
	if err != nil || len(creds.Workspaces) != 1 || creds.Workspaces[0].URL != "https://acme.slack.com" || creds.DefaultWorkspaceURL != "" {
		t.Fatalf("creds = %+v, err = %v", creds, err)
	}
	if err := s.Upsert(browserWS("https://attacker.example", "xoxc-3", "xoxd-3")); err == nil {
		t.Fatal("Upsert accepted a non-Slack workspace URL")
	}
	if err := s.SetDefault("http://attacker.example"); err == nil {
		t.Fatal("SetDefault accepted a non-Slack workspace URL")
	}
}
