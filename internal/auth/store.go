// Package auth persists Slack credentials: workspace metadata in
// ~/.config/slackcli/credentials.json (0600) and, on macOS, the secrets in
// the login keychain.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// keychainPlaceholder replaces a secret in the credentials file when the
// real value lives in the keychain.
const keychainPlaceholder = "__KEYCHAIN__"

// Workspace is one configured Slack workspace.
type Workspace struct {
	URL        string        `json:"workspace_url"`
	Name       string        `json:"workspace_name,omitempty"`
	TeamID     string        `json:"team_id,omitempty"`
	TeamDomain string        `json:"team_domain,omitempty"`
	Auth       slackapi.Auth `json:"auth"`
}

// Credentials is the content of the credentials file. Secrets in
// Workspaces[i].Auth may be keychain placeholders: use Store.Resolve to get
// usable credentials.
type Credentials struct {
	Version             int         `json:"version"`
	UpdatedAt           string      `json:"updated_at,omitempty"`
	DefaultWorkspaceURL string      `json:"default_workspace_url,omitempty"`
	Workspaces          []Workspace `json:"workspaces"`
}

// Find returns the workspace with the given (normalized) URL.
func (c *Credentials) Find(workspaceURL string) *Workspace {
	for i := range c.Workspaces {
		if c.Workspaces[i].URL == workspaceURL {
			return &c.Workspaces[i]
		}
	}
	return nil
}

// Default returns the default workspace: the configured default if it
// exists, else the first one, else nil.
func (c *Credentials) Default() *Workspace {
	if c.DefaultWorkspaceURL != "" {
		if w := c.Find(c.DefaultWorkspaceURL); w != nil {
			return w
		}
	}
	if len(c.Workspaces) > 0 {
		return &c.Workspaces[0]
	}
	return nil
}

// Store reads and writes credentials.
type Store struct {
	// Path is the credentials file.
	Path string
	// Keychain holds the secrets; nil means secrets stay in the file.
	Keychain Keychain
	// Service is the keychain service name.
	Service string
	// LegacyPath and LegacyService locate the predecessor's (agent-slack)
	// credentials, imported once when Path does not exist. Empty disables
	// the migration.
	LegacyPath    string
	LegacyService string

	mu       sync.Mutex
	loaded   *Credentials
	resolved map[string]slackapi.Auth
}

// DefaultStore is the store used by the CLI.
func DefaultStore() *Store {
	return &Store{
		Path:          filepath.Join(appinfo.ConfigDir(), "credentials.json"),
		Keychain:      SystemKeychain(),
		Service:       appinfo.Name,
		LegacyPath:    filepath.Join(appinfo.LegacyConfigDir(), "credentials.json"),
		LegacyService: appinfo.LegacyName,
	}
}

// NormalizeURL reduces a workspace URL to its origin
// ("https://acme.slack.com"). A bare host is accepted.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err == nil && u.Host == "" && !strings.Contains(raw, "://") && strings.Contains(raw, ".") {
		u, err = url.Parse("https://" + raw)
	}
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("invalid workspace URL: %q", raw)
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), nil
}

// Load returns the stored credentials (never nil). A missing or corrupted
// file yields an empty set; invalid workspaces are skipped. The result is
// cached for the lifetime of the Store and must not be mutated: use the
// Store's mutating methods instead.
func (s *Store) Load() (*Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) load() (*Credentials, error) {
	if s.loaded != nil {
		return s.loaded, nil
	}
	creds, err := readCredentialsFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		if migrated, mErr := s.migrateLegacy(); mErr == nil && migrated != nil {
			return migrated, nil
		}
		creds, err = &Credentials{Version: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	s.loaded = creds
	return creds, nil
}

func readCredentialsFile(path string) (*Credentials, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("reading credentials: %w", err)
	}
	var parsed Credentials
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// A corrupted file must not brick the CLI; credentials can be
		// re-imported.
		return &Credentials{Version: 1}, nil
	}
	out := &Credentials{Version: 1, UpdatedAt: parsed.UpdatedAt}
	for _, w := range parsed.Workspaces {
		u, err := NormalizeURL(w.URL)
		if err != nil {
			continue
		}
		if w.Auth.Type != slackapi.AuthStandard && w.Auth.Type != slackapi.AuthBrowser {
			continue
		}
		w.URL = u
		if out.Find(u) == nil {
			out.Workspaces = append(out.Workspaces, w)
		}
	}
	if u, err := NormalizeURL(parsed.DefaultWorkspaceURL); err == nil && out.Find(u) != nil {
		out.DefaultWorkspaceURL = u
	}
	return out, nil
}

// migrateLegacy imports the predecessor's credentials (file + keychain
// service) into this store. It returns nil when there is nothing to import.
func (s *Store) migrateLegacy() (*Credentials, error) {
	if s.LegacyPath == "" || s.LegacyPath == s.Path {
		return nil, nil
	}
	legacy, err := readCredentialsFile(s.LegacyPath)
	if err != nil || len(legacy.Workspaces) == 0 {
		return nil, err
	}
	next := &Credentials{Version: 1}
	for _, w := range legacy.Workspaces {
		a, ok := resolveAuth(s.Keychain, s.LegacyService, w)
		if !ok {
			continue
		}
		w.Auth = a
		next.Workspaces = append(next.Workspaces, w)
	}
	if len(next.Workspaces) == 0 {
		return nil, nil
	}
	if next.Find(legacy.DefaultWorkspaceURL) != nil {
		next.DefaultWorkspaceURL = legacy.DefaultWorkspaceURL
	}
	if err := s.save(next); err != nil {
		return nil, err
	}
	return s.loaded, nil
}

func isSecret(v string) bool { return v != "" && v != keychainPlaceholder }

// resolveAuth returns usable credentials for w, reading placeholders from
// the keychain. ok is false when a secret is missing.
func resolveAuth(kc Keychain, service string, w Workspace) (slackapi.Auth, bool) {
	a := w.Auth
	get := func(stored string, accounts ...string) string {
		if isSecret(stored) {
			return stored
		}
		if kc == nil {
			return ""
		}
		for _, account := range accounts {
			if v := kc.Get(service, account); v != "" {
				return v
			}
		}
		return ""
	}
	switch a.Type {
	case slackapi.AuthStandard:
		a.Token = get(a.Token, "token:"+w.URL)
		return a, a.Token != ""
	case slackapi.AuthBrowser:
		a.XoxcToken = get(a.XoxcToken, "xoxc:"+w.URL)
		// "xoxd" (no workspace) is the predecessor's single shared cookie.
		a.XoxdCookie = get(a.XoxdCookie, "xoxd:"+w.URL, "xoxd")
		return a, a.XoxcToken != "" && a.XoxdCookie != ""
	}
	return a, false
}

// Resolve returns usable credentials for a workspace returned by Load. ok is
// false when its secrets are unavailable (e.g. deleted from the keychain),
// in which case the caller should re-import.
func (s *Store) Resolve(w Workspace) (slackapi.Auth, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.resolved[w.URL]; ok {
		return a, true
	}
	a, ok := resolveAuth(s.Keychain, s.Service, w)
	if ok {
		if s.resolved == nil {
			s.resolved = map[string]slackapi.Auth{}
		}
		s.resolved[w.URL] = a
	}
	return a, ok
}

// save writes creds, moving real secrets to the keychain when possible.
func (s *Store) save(creds *Credentials) error {
	out := &Credentials{
		Version:             1,
		UpdatedAt:           time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		DefaultWorkspaceURL: creds.DefaultWorkspaceURL,
		Workspaces:          make([]Workspace, len(creds.Workspaces)),
	}
	copy(out.Workspaces, creds.Workspaces)
	if out.Find(out.DefaultWorkspaceURL) == nil {
		out.DefaultWorkspaceURL = ""
		if len(out.Workspaces) > 0 {
			out.DefaultWorkspaceURL = out.Workspaces[0].URL
		}
	}

	stash := func(account string, secret *string) {
		if s.Keychain == nil || !isSecret(*secret) {
			return
		}
		if s.Keychain.Get(s.Service, account) == *secret || s.Keychain.Set(s.Service, account, *secret) {
			*secret = keychainPlaceholder
		}
	}
	resolved := map[string]slackapi.Auth{}
	for i := range out.Workspaces {
		w := &out.Workspaces[i]
		if a, ok := s.resolved[w.URL]; ok && !isSecret(w.Auth.BearerToken()) {
			resolved[w.URL] = a
		} else if isSecret(w.Auth.BearerToken()) {
			resolved[w.URL] = w.Auth
		}
		switch w.Auth.Type {
		case slackapi.AuthStandard:
			stash("token:"+w.URL, &w.Auth.Token)
		case slackapi.AuthBrowser:
			stash("xoxc:"+w.URL, &w.Auth.XoxcToken)
			stash("xoxd:"+w.URL, &w.Auth.XoxdCookie)
		}
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.Path, append(data, '\n')); err != nil {
		return fmt.Errorf("writing credentials: %w", err)
	}
	s.loaded = out
	s.resolved = resolved
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Upsert adds or replaces workspaces (matched by URL) and saves. The first
// workspace ever added becomes the default. Metadata that the new entry
// leaves blank (name, team id, team domain) is kept from the old one.
func (s *Store) Upsert(workspaces ...Workspace) error {
	if len(workspaces) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	next := *cur
	next.Workspaces = append([]Workspace(nil), cur.Workspaces...)
	for _, w := range workspaces {
		u, err := NormalizeURL(w.URL)
		if err != nil {
			return err
		}
		w.URL = u
		if old := next.Find(u); old != nil {
			if w.Name == "" {
				w.Name = old.Name
			}
			if w.TeamID == "" {
				w.TeamID = old.TeamID
			}
			if w.TeamDomain == "" {
				w.TeamDomain = old.TeamDomain
			}
			*old = w
		} else {
			next.Workspaces = append(next.Workspaces, w)
		}
		delete(s.resolved, u)
	}
	return s.save(&next)
}

// SetDefault makes a configured workspace the default.
func (s *Store) SetDefault(workspaceURL string) error {
	u, err := NormalizeURL(workspaceURL)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return err
	}
	if cur.Find(u) == nil {
		return fmt.Errorf("workspace %s is not configured", u)
	}
	next := *cur
	next.DefaultWorkspaceURL = u
	return s.save(&next)
}

// Remove deletes a workspace and its keychain secrets. It reports whether
// the workspace existed.
func (s *Store) Remove(workspaceURL string) (bool, error) {
	u, err := NormalizeURL(workspaceURL)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.load()
	if err != nil {
		return false, err
	}
	if cur.Find(u) == nil {
		return false, nil
	}
	next := *cur
	next.Workspaces = nil
	for _, w := range cur.Workspaces {
		if w.URL != u {
			next.Workspaces = append(next.Workspaces, w)
		}
	}
	if next.DefaultWorkspaceURL == u {
		next.DefaultWorkspaceURL = ""
	}
	delete(s.resolved, u)
	if err := s.save(&next); err != nil {
		return false, err
	}
	if s.Keychain != nil {
		for _, prefix := range []string{"token:", "xoxc:", "xoxd:"} {
			s.Keychain.Delete(s.Service, prefix+u)
		}
	}
	return true, nil
}
