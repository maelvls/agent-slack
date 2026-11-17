// Package auth persists Slack credentials: workspace metadata in
// ~/.config/slackcli/credentials.json (0600) and, on macOS, the secrets in
// the login keychain.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

// Default returns the workspace to use when none is selected: the
// configured default, else the only workspace there is. It is nil when
// there is no workspace, and when there are several without a default:
// picking one of them would be a guess (see Store.Upsert for when a default
// is set).
func (c *Credentials) Default() *Workspace {
	if c.DefaultWorkspaceURL != "" {
		if w := c.Find(c.DefaultWorkspaceURL); w != nil {
			return w
		}
	}
	if len(c.Workspaces) == 1 {
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
	// Warnf reports a problem that does not stop the command (a corrupt
	// credentials file). Nil discards the warning.
	Warnf func(format string, args ...any)

	mu            sync.Mutex
	loaded        *Credentials
	resolved      map[string]slackapi.Auth
	warnedCorrupt bool
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
//
// Credentials are sent to that origin, so it must be an https URL on a
// Slack workspace host (slack.com, slack-gov.com and their subdomains; see
// slackapi.WorkspaceOrigin): anything else is an error.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") && strings.Contains(raw, ".") {
		raw = "https://" + raw
	}
	return slackapi.WorkspaceOrigin(raw)
}

// SecretsError says that the secrets of a stored workspace cannot be used.
type SecretsError struct {
	// WorkspaceURL is the workspace whose secrets were asked for.
	WorkspaceURL string
	// Cause is why the keychain could not be read (locked, timed out…).
	// It is nil when the keychain answered and the secrets are not there.
	Cause error
}

// Missing reports whether the secrets are gone (as opposed to temporarily
// unreadable), in which case only a new import brings them back.
func (e *SecretsError) Missing() bool { return e.Cause == nil }

func (e *SecretsError) Error() string {
	if e.Missing() {
		return fmt.Sprintf("the secrets of %s are not stored any more (deleted from the keychain?)", e.WorkspaceURL)
	}
	return fmt.Sprintf("the secrets of %s are unavailable: %v", e.WorkspaceURL, e.Cause)
}

func (e *SecretsError) Unwrap() error { return e.Cause }

// Load returns the stored credentials (never nil). A missing file yields an
// empty set, and so does a corrupted one, which is preserved (see
// preserveCorrupt); invalid workspaces are skipped. The result is cached
// for the lifetime of the Store and must not be mutated: use the Store's
// mutating methods instead.
func (s *Store) Load() (*Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) load() (*Credentials, error) {
	if s.loaded != nil {
		return s.loaded, nil
	}
	creds, err := s.readFile()
	if errors.Is(err, fs.ErrNotExist) {
		if s.hasLegacy() {
			// The migration writes the file: do it under the lock.
			unlock, err := lockFile(s.Path)
			if err != nil {
				return nil, err
			}
			defer unlock()
			return s.reload()
		}
		creds, err = &Credentials{Version: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	s.loaded = creds
	return creds, nil
}

// reload reads the credentials file again, ignoring the cache, and
// migrates the predecessor's credentials when there is no file yet. The
// caller holds the file lock.
func (s *Store) reload() (*Credentials, error) {
	creds, err := s.readFile()
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

// update is one read-modify-write cycle on the credentials file. It holds
// the cross-process lock and starts from the file as it is now, not from
// what this Store loaded earlier, so that concurrent invocations do not
// overwrite each other's changes.
func (s *Store) update(change func(next *Credentials) error) error {
	unlock, err := lockFile(s.Path)
	if err != nil {
		return err
	}
	defer unlock()
	cur, err := s.reload()
	if err != nil {
		return err
	}
	next := *cur
	next.Workspaces = append([]Workspace(nil), cur.Workspaces...)
	if err := change(&next); err != nil {
		return err
	}
	return s.save(&next)
}

// errCorrupt marks a credentials file that is not valid JSON.
var errCorrupt = errors.New("not valid JSON")

// readFile reads the credentials file. A corrupt file is preserved and
// read as empty: it must not brick the CLI, since credentials can be
// imported again.
func (s *Store) readFile() (*Credentials, error) {
	creds, err := readCredentialsFile(s.Path)
	if errors.Is(err, errCorrupt) {
		s.preserveCorrupt(err)
		return &Credentials{Version: 1}, nil
	}
	return creds, err
}

// preserveCorrupt keeps a 0600 copy of a corrupt credentials file next to
// it ("credentials.json.corrupt-<time of last change>") before the next
// save replaces it, and warns once. The copy is named after the file's
// modification time so that repeated runs do not pile up copies.
func (s *Store) preserveCorrupt(cause error) {
	if s.warnedCorrupt {
		return
	}
	s.warnedCorrupt = true
	kept := "it could not be copied"
	if fi, err := os.Stat(s.Path); err == nil {
		backup := s.Path + ".corrupt-" + fi.ModTime().UTC().Format("20060102T150405Z")
		if copyFileExclusive(s.Path, backup) == nil {
			kept = "a copy was kept at " + backup
		}
	}
	if s.Warnf != nil {
		s.Warnf(`warning: the credentials file %s is unreadable (%v), so no stored workspace is available; %s. Import credentials again (e.g. "%s auth import-desktop") or repair the file`,
			s.Path, cause, kept, appinfo.Name)
	}
}

// copyFileExclusive copies src to dst (0600). An existing dst is left
// alone: it is an earlier copy of the same file.
func copyFileExclusive(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	return f.Close()
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
		return nil, fmt.Errorf("%w: %v", errCorrupt, err)
	}
	out := &Credentials{Version: 1, UpdatedAt: parsed.UpdatedAt}
	for _, w := range parsed.Workspaces {
		// A workspace whose URL is not a Slack origin is dropped: its
		// credentials must never be sent there.
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

// hasLegacy reports whether there are predecessor credentials to migrate.
func (s *Store) hasLegacy() bool {
	if s.LegacyPath == "" || s.LegacyPath == s.Path {
		return false
	}
	_, err := os.Stat(s.LegacyPath)
	return err == nil
}

// migrateLegacy imports the predecessor's credentials (file + keychain
// service) into this store. It returns nil when there is nothing to import.
// Workspaces whose secrets are gone are left behind; if the keychain cannot
// be read at all, nothing is migrated, so that the next run tries again
// instead of dropping workspaces for good.
func (s *Store) migrateLegacy() (*Credentials, error) {
	if !s.hasLegacy() {
		return nil, nil
	}
	legacy, err := readCredentialsFile(s.LegacyPath)
	if err != nil || len(legacy.Workspaces) == 0 {
		return nil, err
	}
	next := &Credentials{Version: 1}
	for _, w := range legacy.Workspaces {
		a, err := resolveAuth(s.Keychain, s.LegacyService, w)
		var se *SecretsError
		if errors.As(err, &se) && !se.Missing() {
			return nil, err
		}
		if err != nil {
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
// the keychain. The error is a *SecretsError: a secret is missing, or the
// keychain could not be read.
func resolveAuth(kc Keychain, service string, w Workspace) (slackapi.Auth, error) {
	a := w.Auth
	var cause error
	get := func(stored string, accounts ...string) string {
		if isSecret(stored) {
			return stored
		}
		if kc == nil || cause != nil {
			return ""
		}
		for _, account := range accounts {
			v, err := kc.Get(service, account)
			if err != nil {
				cause = err
				return ""
			}
			if v != "" {
				return v
			}
		}
		return ""
	}
	ok := false
	switch a.Type {
	case slackapi.AuthStandard:
		a.Token = get(a.Token, "token:"+w.URL)
		ok = a.Token != ""
	case slackapi.AuthBrowser:
		a.XoxcToken = get(a.XoxcToken, "xoxc:"+w.URL)
		// "xoxd" (no workspace) is the predecessor's single shared cookie.
		a.XoxdCookie = get(a.XoxdCookie, "xoxd:"+w.URL, "xoxd")
		ok = a.XoxcToken != "" && a.XoxdCookie != ""
	}
	if !ok {
		return slackapi.Auth{Type: a.Type}, &SecretsError{WorkspaceURL: w.URL, Cause: cause}
	}
	return a, nil
}

// Resolve returns usable credentials for a workspace returned by Load. It
// fails with a *SecretsError when its secrets cannot be used: either they
// are gone (Missing: the caller should re-import that workspace), or the
// keychain could not be read just now (locked, timed out), in which case
// the caller must not conclude anything about the workspace.
func (s *Store) Resolve(w Workspace) (slackapi.Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.resolved[w.URL]; ok {
		return a, nil
	}
	a, err := resolveAuth(s.Keychain, s.Service, w)
	if err != nil {
		return a, err
	}
	if s.resolved == nil {
		s.resolved = map[string]slackapi.Auth{}
	}
	s.resolved[w.URL] = a
	return a, nil
}

// save writes creds, moving real secrets to the keychain when possible.
//
// It also maintains the default workspace: a default that no longer exists
// is dropped, and a store holding exactly one workspace has it as its
// default. Several workspaces without a default stay that way: nothing
// says which one the user means.
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
	}
	if out.DefaultWorkspaceURL == "" && len(out.Workspaces) == 1 {
		out.DefaultWorkspaceURL = out.Workspaces[0].URL
	}

	stash := func(account string, secret *string) {
		if s.Keychain == nil || !isSecret(*secret) {
			return
		}
		if cur, err := s.Keychain.Get(s.Service, account); (err == nil && cur == *secret) || s.Keychain.Set(s.Service, account, *secret) {
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

// Upsert adds or replaces workspaces (matched by URL) and saves. Metadata
// that the new entry leaves blank (name, team id, team domain) is kept from
// the old one. Use it for what the user asked for explicitly, workspace by
// workspace ("auth add", "auth parse-curl"); imports go through
// UpsertImported.
//
// The default workspace is only ever set implicitly when the store holds
// exactly one workspace: the first one added on its own becomes the default
// and stays it as others are added later, whereas several workspaces added
// at once to an empty store leave it without a default (see SetDefault).
func (s *Store) Upsert(workspaces ...Workspace) error {
	_, err := s.upsert(false, workspaces)
	return err
}

// UpsertImported is Upsert for credentials found in a local application
// (Slack Desktop, a browser) rather than given for one workspace: it adds
// new workspaces and refreshes browser-auth ones, but never replaces a
// workspace stored with a standard token (xoxb-/xoxp-, saved by "auth add")
// with a browser session, which would silently change the identity that
// commands act as. Such workspaces are left untouched and their URLs
// returned in kept.
func (s *Store) UpsertImported(workspaces ...Workspace) (kept []string, err error) {
	return s.upsert(true, workspaces)
}

func (s *Store) upsert(keepStandard bool, workspaces []Workspace) (kept []string, err error) {
	if len(workspaces) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.update(func(next *Credentials) error {
		kept = nil
		for _, w := range workspaces {
			u, err := NormalizeURL(w.URL)
			if err != nil {
				return err
			}
			w.URL = u
			old := next.Find(u)
			if old == nil {
				next.Workspaces = append(next.Workspaces, w)
				continue
			}
			if keepStandard && old.Auth.Type == slackapi.AuthStandard && w.Auth.IsBrowser() {
				kept = append(kept, u)
				continue
			}
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
			delete(s.resolved, u)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return kept, nil
}

// SetDefault makes a configured workspace the default.
func (s *Store) SetDefault(workspaceURL string) error {
	u, err := NormalizeURL(workspaceURL)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.update(func(next *Credentials) error {
		if next.Find(u) == nil {
			return fmt.Errorf("workspace %s is not configured", u)
		}
		next.DefaultWorkspaceURL = u
		return nil
	})
}

// Remove deletes a workspace and its keychain secrets. It reports whether
// the workspace existed. Removing the default workspace leaves the store
// without a default, unless exactly one workspace remains.
func (s *Store) Remove(workspaceURL string) (bool, error) {
	u, err := NormalizeURL(workspaceURL)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	errAbsent := errors.New("absent")
	err = s.update(func(next *Credentials) error {
		if next.Find(u) == nil {
			return errAbsent
		}
		kept := next.Workspaces[:0]
		for _, w := range next.Workspaces {
			if w.URL != u {
				kept = append(kept, w)
			}
		}
		next.Workspaces = kept
		if next.DefaultWorkspaceURL == u {
			next.DefaultWorkspaceURL = ""
		}
		delete(s.resolved, u)
		return nil
	})
	if errors.Is(err, errAbsent) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if s.Keychain != nil {
		for _, prefix := range []string{"token:", "xoxc:", "xoxd:"} {
			s.Keychain.Delete(s.Service, prefix+u)
		}
	}
	return true, nil
}
