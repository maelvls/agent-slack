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
}

func (c *Context) resolved(a slackapi.Auth, workspaceURL string) *Resolved {
	return &Resolved{
		Client:       slackapi.New(a, workspaceURL, c.ClientOptions...),
		Auth:         a,
		WorkspaceURL: workspaceURL,
	}
}

// Client returns a Slack client for a workspace selector: a workspace URL
// (e.g. taken from a message URL), a unique substring, or "" for the
// default workspace.
//
// Credentials are looked up in this order: SLACK_TOKEN, the credentials
// store, then (importing on the fly) Slack Desktop, Chrome, Brave, Firefox.
func (c *Context) Client(ctx context.Context, workspace string) (*Resolved, error) {
	selector := strings.TrimSpace(workspace)
	selectorURL, _ := auth.NormalizeURL(selector) // "" unless selector is a URL

	if a, ok, err := authFromEnv(); err != nil {
		return nil, err
	} else if ok {
		return c.clientFromEnv(a, selector, selectorURL)
	}

	// 1. The credentials store.
	ws, err := c.pickStored(selector, selectorURL)
	if err != nil {
		return nil, err
	}
	if ws != nil {
		if a, ok := c.Store.Resolve(*ws); ok {
			return c.resolved(a, ws.URL), nil
		}
	}

	// 2. Slack Desktop: import every team, then look again.
	if c.Desktop != nil {
		if extracted, err := c.Desktop(ctx); err == nil && extracted != nil && len(extracted.Teams) > 0 {
			if err := c.Import(extracted); err == nil {
				ws, err := c.pickStored(selector, selectorURL)
				if err != nil {
					return nil, err
				}
				if ws != nil {
					if a, ok := c.Store.Resolve(*ws); ok {
						return c.resolved(a, ws.URL), nil
					}
				}
			}
		}
	}

	// 3. Browsers: import the matching team only.
	for _, extract := range []Extractor{c.Chrome, c.Brave, c.Firefox} {
		if extract == nil {
			continue
		}
		extracted, err := extract(ctx)
		if err != nil || extracted == nil || len(extracted.Teams) == 0 {
			continue // not installed, not logged in, AppleScript disabled…
		}
		team, err := pickTeam(extracted.Teams, selector, selectorURL)
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
		if err := c.Store.Upsert(wss...); err != nil {
			return nil, err
		}
		return c.resolved(wss[0].Auth, wss[0].URL), nil
	}

	if selector != "" {
		return nil, fmt.Errorf(`no Slack credentials for workspace %q. Run "%[2]s auth whoami" to list configured workspaces, or import credentials with "%[2]s auth import-desktop" (also: import-chrome, import-brave, import-firefox)`, selector, appinfo.Name)
	}
	return nil, fmt.Errorf(`no Slack credentials available. Run "%[1]s auth import-desktop" (also: import-chrome, import-brave, import-firefox), or set SLACK_TOKEN (plus SLACK_COOKIE_D for xoxc- tokens)`, appinfo.Name)
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
	if workspaceURL == "" {
		if env := strings.TrimSpace(os.Getenv("SLACK_WORKSPACE_URL")); env != "" {
			u, err := auth.NormalizeURL(env)
			if err != nil {
				if a.IsBrowser() {
					return nil, fmt.Errorf("SLACK_WORKSPACE_URL must be a workspace URL such as https://acme.slack.com when SLACK_TOKEN is a browser token (got %q)", env)
				}
			} else {
				workspaceURL = u
			}
		}
	}
	if a.IsBrowser() && workspaceURL == "" {
		if selector != "" {
			return nil, fmt.Errorf("cannot tell which workspace URL %q refers to: with a browser token in SLACK_TOKEN, pass --workspace (or SLACK_WORKSPACE_URL) as a full URL such as https://acme.slack.com", selector)
		}
		return nil, errors.New("SLACK_TOKEN is a browser token (xoxc-): also set SLACK_WORKSPACE_URL (e.g. https://acme.slack.com), pass --workspace, or use a Slack message URL as the target")
	}
	return c.resolved(a, workspaceURL), nil
}

// pickStored selects a stored workspace: by selector if given, else the
// default. It returns nil when nothing matches.
func (c *Context) pickStored(selector, selectorURL string) (*auth.Workspace, error) {
	creds, err := c.Store.Load()
	if err != nil {
		return nil, err
	}
	if selector == "" {
		return creds.Default(), nil
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
// matching the selector, or the first one when there is no selector.
func pickTeam(teams []authextract.Team, selector, selectorURL string) (*authextract.Team, error) {
	if selector == "" {
		return &teams[0], nil
	}
	if selectorURL != "" {
		for i := range teams {
			if u, err := auth.NormalizeURL(teams[i].URL); err == nil && u == selectorURL {
				return &teams[i], nil
			}
		}
		return nil, nil
	}
	var hits []int
	for i, t := range teams {
		u, err := auth.NormalizeURL(t.URL)
		if err != nil {
			continue
		}
		if auth.MatchesSelector(u, t.Name, selector) {
			hits = append(hits, i)
		}
	}
	switch len(hits) {
	case 0:
		return nil, nil
	case 1:
		return &teams[hits[0]], nil
	}
	urls := make([]string, len(hits))
	for i, h := range hits {
		urls[i], _ = auth.NormalizeURL(teams[h].URL)
	}
	return nil, fmt.Errorf("workspace selector %q is ambiguous (matches %s): pass a more specific selector or the full workspace URL", selector, strings.Join(urls, ", "))
}
