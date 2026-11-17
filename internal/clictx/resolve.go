package clictx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/authextract"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// Resolved is a ready-to-use client and the workspace it talks to.
type Resolved struct {
	Client *slackapi.Client
	Auth   slackapi.Auth
	// WorkspaceURL is the workspace origin; "" only for a standard token
	// from SLACK_TOKEN with no workspace given.
	WorkspaceURL string
	// fromStore says that the credentials are those of a stored workspace
	// (as opposed to SLACK_TOKEN), so that they can be refreshed.
	fromStore bool
}

func (c *Context) resolved(a slackapi.Auth, workspaceURL string, fromStore bool) *Resolved {
	return &Resolved{
		Client:       slackapi.New(a, workspaceURL, c.ClientOptions...),
		Auth:         a,
		WorkspaceURL: workspaceURL,
		fromStore:    fromStore,
	}
}

// Client returns a Slack client for a workspace selector: a workspace URL
// (e.g. taken from a message URL), a unique substring, or "" for the
// default workspace.
//
// Credentials are looked up in this order: SLACK_TOKEN, the credentials
// store, then (importing on the fly) Slack Desktop, Chrome, Brave, Firefox.
//
// It never guesses a workspace: with several workspaces configured, no
// selector and no default, it fails and lists them. And once a stored
// workspace has been selected, only that workspace is used: if its secrets
// cannot be read, the on-the-fly import may refresh that workspace, but the
// call never falls back to another one.
func (c *Context) Client(ctx context.Context, workspace string) (*Resolved, error) {
	r, err := c.client(ctx, workspace)
	c.mu.Lock()
	c.last = r
	c.mu.Unlock()
	return r, err
}

func (c *Context) client(ctx context.Context, workspace string) (*Resolved, error) {
	selector := strings.TrimSpace(workspace)
	selectorURL, _ := auth.NormalizeURL(selector) // "" unless selector is a URL

	if a, ok, err := authFromEnv(); err != nil {
		return nil, err
	} else if ok {
		return c.clientFromEnv(a, selector, selectorURL)
	}

	// 1. The credentials store.
	ws, err := c.pickStored(selector)
	if err != nil {
		return nil, err
	}
	// pinned is the stored workspace that was selected but whose secrets
	// cannot be used. From here on the lookup is about that workspace and
	// no other: falling back to "whatever a browser is signed in to" would
	// run the command in the wrong workspace.
	var (
		pinned   *auth.Workspace
		unusable error
	)
	if ws != nil {
		a, err := c.Store.Resolve(*ws)
		if err == nil {
			return c.resolved(a, ws.URL, true), nil
		}
		pinned, unusable = ws, err
		selector, selectorURL = ws.URL, ws.URL
	}

	// Why a source that should have had credentials did not yield them.
	// "Not installed" and "not signed in" (authextract.ErrNotFound) are
	// the normal case and say nothing.
	var failures []string
	failed := func(source string, err error) {
		if err != nil && !errors.Is(err, authextract.ErrNotFound) {
			failures = append(failures, source+": "+err.Error())
		}
	}
	tried := func() string {
		if len(failures) == 0 {
			return ""
		}
		return ". The automatic lookup failed for: " + strings.Join(failures, "; ")
	}

	// An import never replaces a standard token with a browser session, so
	// there is nothing to look for when that is what the pinned workspace
	// holds.
	if pinned != nil && !pinned.Auth.IsBrowser() {
		return nil, secretsError(pinned, unusable, "")
	}

	// 2. Slack Desktop: import every team (only the pinned one, if any),
	// then look again.
	if c.Desktop != nil {
		extracted, err := c.Desktop(ctx)
		failed("Slack Desktop", err)
		if err == nil && extracted != nil && len(extracted.Teams) > 0 {
			found := workspacesFrom(extracted, extracted.Teams)
			if pinned != nil {
				found = onlyURL(found, pinned.URL)
			}
			if _, err := c.Store.UpsertImported(found...); err != nil {
				failed("saving the Slack Desktop credentials", err)
			} else if len(found) > 0 {
				ws, err := c.pickStored(selector)
				if err != nil {
					return nil, err
				}
				if ws != nil {
					if a, err := c.Store.Resolve(*ws); err == nil {
						return c.resolved(a, ws.URL, true), nil
					}
				}
			}
		}
	}

	// 3. Browsers: import the matching team only.
	for _, browser := range []struct {
		name    string
		extract Extractor
	}{{"Chrome", c.Chrome}, {"Brave", c.Brave}, {"Firefox", c.Firefox}} {
		if browser.extract == nil {
			continue
		}
		extracted, err := browser.extract(ctx)
		failed(browser.name, err)
		if err != nil || extracted == nil || len(extracted.Teams) == 0 {
			continue // not installed, not logged in, AppleScript disabled…
		}
		team, err := pickTeam(browser.name, extracted.Teams, selector, selectorURL)
		if err != nil {
			return nil, err
		}
		if team == nil {
			continue
		}
		wss := workspacesFrom(extracted, []authextract.Team{*team})
		if len(wss) == 0 {
			continue
		}
		kept, err := c.Store.UpsertImported(wss...)
		if err != nil {
			return nil, err
		}
		if len(kept) > 0 {
			continue // stored with a standard token: not ours to replace
		}
		return c.resolved(wss[0].Auth, wss[0].URL, true), nil
	}

	if pinned != nil {
		return nil, secretsError(pinned, unusable, tried())
	}
	if selector != "" {
		return nil, fmt.Errorf(`no Slack credentials for workspace %q. Run "%[2]s auth whoami" to list configured workspaces, or import credentials with "%[2]s auth import-desktop" (also: import-chrome, import-brave, import-firefox)%[3]s`, selector, appinfo.Name, tried())
	}
	return nil, fmt.Errorf(`no Slack credentials available. Run "%[1]s auth import-desktop" (also: import-chrome, import-brave, import-firefox), or set SLACK_TOKEN (plus SLACK_COOKIE_D for xoxc- tokens)%[2]s`, appinfo.Name, tried())
}

// secretsError explains that the selected workspace cannot be used because
// its secrets cannot be read, and what to do about it. tried is the
// "automatic lookup failed" suffix, if any.
func secretsError(w *auth.Workspace, cause error, tried string) error {
	var se *auth.SecretsError
	if errors.As(cause, &se) && !se.Missing() {
		return fmt.Errorf(`secrets for %s are unavailable (keychain locked or timed out?): %v. Unlock it, raise %s, or re-import with "%s auth import-desktop"%s`,
			w.URL, se.Cause, appinfo.EnvName("KEYCHAIN_TIMEOUT_MS"), appinfo.Name, tried)
	}
	if !w.Auth.IsBrowser() {
		return fmt.Errorf(`the token stored for %[1]s is gone (deleted from the keychain?): save it again with "%[2]s auth add --workspace-url %[1]s --from-env", or remove the workspace with "%[2]s auth remove %[1]s"`,
			w.URL, appinfo.Name)
	}
	return fmt.Errorf(`the secrets stored for %s are gone (deleted from the keychain?) and it could not be re-imported automatically: sign in to it in Slack Desktop or a browser and run "%s auth import-desktop" (also: import-chrome, import-brave, import-firefox)%s`,
		w.URL, appinfo.Name, tried)
}

// onlyURL keeps the workspaces with the given URL.
func onlyURL(workspaces []auth.Workspace, url string) []auth.Workspace {
	var out []auth.Workspace
	for _, w := range workspaces {
		if w.URL == url {
			out = append(out, w)
		}
	}
	return out
}

func (c *Context) clientFromEnv(a slackapi.Auth, selector, selectorURL string) (*Resolved, error) {
	workspaceURL := selectorURL
	if workspaceURL == "" && selector != "" {
		// A substring selector can still name a stored workspace.
		if creds, err := c.Store.Load(); err == nil {
			match, ambiguous := auth.SelectWorkspace(creds.Workspaces, selector)
			if len(ambiguous) > 0 {
				return nil, ambiguousError(selector, ambiguous)
			}
			if match != nil {
				workspaceURL = match.URL
			}
		}
	}
	// SLACK_WORKSPACE_URL only applies when nothing else was asked for: a
	// selector that designates nothing must not silently become another
	// workspace.
	if workspaceURL == "" && selector == "" {
		if env := strings.TrimSpace(os.Getenv("SLACK_WORKSPACE_URL")); env != "" {
			u, err := auth.NormalizeURL(env)
			if err != nil {
				if a.IsBrowser() {
					return nil, fmt.Errorf("SLACK_WORKSPACE_URL must be an https Slack workspace URL such as https://acme.slack.com when SLACK_TOKEN is a browser token (got %q)", env)
				}
			} else {
				workspaceURL = u
			}
		}
	}
	if a.IsBrowser() && workspaceURL == "" {
		if selector != "" {
			return nil, fmt.Errorf("cannot tell which workspace %q refers to: with a browser token in SLACK_TOKEN, pass --workspace (or SLACK_WORKSPACE_URL) as a full https Slack workspace URL such as https://acme.slack.com; the token and cookie are only ever sent to Slack", selector)
		}
		return nil, errors.New("SLACK_TOKEN is a browser token (xoxc-): also set SLACK_WORKSPACE_URL (e.g. https://acme.slack.com), pass --workspace, or use a Slack message URL as the target")
	}
	return c.resolved(a, workspaceURL, false), nil
}

// pickStored selects a stored workspace: by selector if given, else the
// default. It returns nil when nothing matches, and an error rather than a
// guess when the selector is ambiguous or when, without a selector, there
// are several workspaces and no default.
func (c *Context) pickStored(selector string) (*auth.Workspace, error) {
	creds, err := c.Store.Load()
	if err != nil {
		return nil, err
	}
	if selector == "" {
		def := creds.Default()
		if def == nil && len(creds.Workspaces) > 1 {
			return nil, fmt.Errorf("cannot tell which workspace to use: %s", chooseWorkspaceAdvice(creds.Workspaces))
		}
		return def, nil
	}
	match, ambiguous := auth.SelectWorkspace(creds.Workspaces, selector)
	if len(ambiguous) > 0 {
		return nil, ambiguousError(selector, ambiguous)
	}
	return match, nil
}

func ambiguousError(selector string, candidates []auth.Workspace) error {
	urls := make([]string, len(candidates))
	for i, w := range candidates {
		urls[i] = w.URL
	}
	return fmt.Errorf("workspace selector %q is ambiguous (matches %s): pass a more specific selector or the full workspace URL", selector, strings.Join(urls, ", "))
}

// pickTeam chooses the team a browser extraction should yield: the one
// matching the selector. Without a selector it is the only team the
// browser is signed in to; with several, it is an error rather than a
// guess.
func pickTeam(browser string, teams []authextract.Team, selector, selectorURL string) (*authextract.Team, error) {
	if selector == "" {
		if len(teams) == 1 {
			return &teams[0], nil
		}
		return nil, fmt.Errorf(`cannot tell which workspace to use: %s is signed in to %d workspaces (%s). Pass --workspace "<url-or-unique-substring>" or set SLACK_WORKSPACE_URL`,
			browser, len(teams), strings.Join(teamURLs(teams), ", "))
	}
	if selectorURL != "" {
		for i := range teams {
			if u, err := auth.NormalizeURL(teams[i].URL); err == nil && u == selectorURL {
				return &teams[i], nil
			}
		}
		return nil, nil
	}
	var hits []authextract.Team
	var hit *authextract.Team
	for i, t := range teams {
		u, err := auth.NormalizeURL(t.URL)
		if err != nil {
			continue
		}
		if auth.MatchesSelector(u, t.Name, selector) {
			hits = append(hits, t)
			hit = &teams[i]
		}
	}
	switch len(hits) {
	case 0:
		return nil, nil
	case 1:
		return hit, nil
	}
	return nil, fmt.Errorf("workspace selector %q is ambiguous (matches %s): pass a more specific selector or the full workspace URL", selector, strings.Join(teamURLs(hits), ", "))
}

// teamURLs returns the normalized URLs of teams, for error messages.
func teamURLs(teams []authextract.Team) []string {
	urls := make([]string, 0, len(teams))
	for _, t := range teams {
		if u, err := auth.NormalizeURL(t.URL); err == nil {
			urls = append(urls, u)
		}
	}
	return urls
}
