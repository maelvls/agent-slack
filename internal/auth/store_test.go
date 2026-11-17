package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/slackapi"
)

type fakeKeychain map[string]string

func (k fakeKeychain) Get(service, account string) string { return k[service+"|"+account] }
func (k fakeKeychain) Set(service, account, value string) bool {
	k[service+"|"+account] = value
	return true
}
func (k fakeKeychain) Delete(service, account string) { delete(k, service+"|"+account) }

func browserWS(u, xoxc, xoxd string) Workspace {
	return Workspace{URL: u, Auth: slackapi.Auth{Type: slackapi.AuthBrowser, XoxcToken: xoxc, XoxdCookie: xoxd}}
}

func TestStoreRoundTripWithKeychain(t *testing.T) {
	kc := fakeKeychain{}
	path := filepath.Join(t.TempDir(), "sub", "credentials.json")
	s := &Store{Path: path, Keychain: kc, Service: "svc"}

	if err := s.Upsert(
		browserWS("https://A.slack.com/archives/C1", "xoxc-a", "xoxd-a"),
		browserWS("https://b.slack.com", "xoxc-b", "xoxd-b"),
		Workspace{URL: "https://c.slack.com", Auth: slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxb-c"}},
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
	if creds.DefaultWorkspaceURL != "https://a.slack.com" || len(creds.Workspaces) != 3 {
		t.Fatalf("creds = %+v", creds)
	}
	a, ok := fresh.Resolve(creds.Workspaces[1])
	if !ok || a.XoxcToken != "xoxc-b" || a.XoxdCookie != "xoxd-b" {
		t.Fatalf("resolve = %+v %v", a, ok)
	}
	c, ok := fresh.Resolve(creds.Workspaces[2])
	if !ok || c.Token != "xoxb-c" {
		t.Fatalf("resolve = %+v %v", c, ok)
	}

	// A secret that vanished from the keychain is reported, not sent to
	// Slack as the literal placeholder.
	delete(kc, "svc|xoxc:https://a.slack.com")
	if _, ok := (&Store{Path: path, Keychain: kc, Service: "svc"}).Resolve(creds.Workspaces[0]); ok {
		t.Fatal("resolved a workspace whose token is gone")
	}
}

func TestStoreWithoutKeychainKeepsSecretsInFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := &Store{Path: path}
	if err := s.Upsert(browserWS("https://a.slack.com", "xoxc-a", "xoxd-a")); err != nil {
		t.Fatal(err)
	}
	creds, _ := (&Store{Path: path}).Load()
	a, ok := (&Store{Path: path}).Resolve(creds.Workspaces[0])
	if !ok || a.XoxcToken != "xoxc-a" {
		t.Fatalf("resolve = %+v %v", a, ok)
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
	if b, ok := s.Resolve(*creds.Find("https://b.slack.com")); !ok || b.XoxcToken != "xoxc-b" {
		t.Fatalf("b = %+v %v", b, ok)
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
}

func TestMigrateLegacy(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy.json")
	_ = os.WriteFile(legacy, []byte(`{"version":1,"default_workspace_url":"https://b.slack.com","workspaces":[
	  {"workspace_url":"https://a.slack.com","workspace_name":"A","auth":{"auth_type":"browser","xoxc_token":"__KEYCHAIN__","xoxd_cookie":"__KEYCHAIN__"}},
	  {"workspace_url":"https://b.slack.com","auth":{"auth_type":"browser","xoxc_token":"__KEYCHAIN__","xoxd_cookie":"__KEYCHAIN__"}},
	  {"workspace_url":"https://gone.slack.com","auth":{"auth_type":"browser","xoxc_token":"__KEYCHAIN__","xoxd_cookie":"__KEYCHAIN__"}},
	  {"workspace_url":"not a url","auth":{"auth_type":"browser","xoxc_token":"x","xoxd_cookie":"y"}}
	]}`), 0o600)
	kc := fakeKeychain{
		"old|xoxc:https://a.slack.com": "xoxc-a",
		"old|xoxc:https://b.slack.com": "xoxc-b",
		"old|xoxd":                     "xoxd-shared",
	}
	s := &Store{Path: filepath.Join(dir, "new", "credentials.json"), Keychain: kc, Service: "new", LegacyPath: legacy, LegacyService: "old"}
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
	a, ok := s.Resolve(creds.Workspaces[0])
	if !ok || a.XoxdCookie != "xoxd-shared" {
		t.Fatalf("resolve = %+v", a)
	}
}

func TestCorruptedFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	_ = os.WriteFile(path, []byte("{nope"), 0o600)
	creds, err := (&Store{Path: path}).Load()
	if err != nil || len(creds.Workspaces) != 0 {
		t.Fatalf("creds=%+v err=%v", creds, err)
	}
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
	} {
		got, err := NormalizeURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "acme", "ftp://acme.slack.com", "#general"} {
		if got, err := NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) = %q, want error", bad, got)
		}
	}
}
