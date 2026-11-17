// Package clictx is the runtime shared by every command: credential
// resolution, the Slack client factory, safe mode, and result printing.
package clictx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/authextract"
	"github.com/maelvls/slackcli/internal/output"
	"github.com/maelvls/slackcli/internal/slackapi"
	"github.com/maelvls/slackcli/internal/slackid"
)

// WorkspaceFlagUsage is the help text shared by every --workspace flag.
const WorkspaceFlagUsage = "Workspace selector (full URL or unique substring; required if you have multiple workspaces)"

// Extractor pulls credentials out of a local application.
type Extractor func(ctx context.Context) (*authextract.Extracted, error)

// Context is created once by the root command and handed to every command
// constructor.
type Context struct {
	// Store persists credentials.
	Store *auth.Store
	// Stdout receives command results; Stderr receives hints and warnings.
	Stdout io.Writer
	Stderr io.Writer
	// SafeModeFlag is bound to the global --safe-mode flag.
	SafeModeFlag bool

	// Extractors, replaceable in tests.
	Desktop Extractor
	Chrome  Extractor
	Brave   Extractor
	Firefox Extractor

	// ClientOptions are passed to every slackapi.New call (tests).
	ClientOptions []slackapi.Option

	mu              sync.Mutex
	disableWatchdog func()
	// last is what Client resolved most recently (nil if it failed):
	// WithAutoRefresh uses it to know whose credentials were rejected.
	last *Resolved
}

// New returns the production context.
func New() *Context {
	c := &Context{
		Store:   auth.DefaultStore(),
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Desktop: authextract.Desktop,
		Chrome:  authextract.Chrome,
		Brave:   authextract.Brave,
		Firefox: authextract.Firefox,
	}
	c.Store.Warnf = c.Hintf
	return c
}

// Print writes a command result to Stdout as pruned JSON.
func (c *Context) Print(v any) error { return output.Write(c.Stdout, v) }

// Hintf writes a line to Stderr (warnings, hints; never results).
func (c *Context) Hintf(format string, args ...any) {
	fmt.Fprintf(c.Stderr, strings.TrimRight(format, "\n")+"\n", args...)
}

// SafeMode reports whether safe mode is on (--safe-mode or
// SLACKCLI_SAFE_MODE): nothing may be posted, edited or deleted without a
// human reviewing it. See SafeModeSummary for what it covers, and
// CheckSafeMode to refuse a command.
//
// It fails closed. The flag and the environment can each turn it on and
// neither can turn it off for the other: --safe-mode=false does not undo
// SLACKCLI_SAFE_MODE=1. In the environment, anything but an explicit off
// value ("", "0", "false", "no", "off") means on, under the current or the
// legacy AGENT_SLACK_ name (see appinfo.SafeModeEnv).
func (c *Context) SafeMode() bool { return c.SafeModeFlag || appinfo.SafeModeEnv() }

// EffectiveWorkspace returns the workspace selector to use: the --workspace
// flag if set, else SLACK_WORKSPACE_URL, else "".
func (c *Context) EffectiveWorkspace(flag string) string {
	if f := strings.TrimSpace(flag); f != "" {
		return f
	}
	return strings.TrimSpace(os.Getenv("SLACK_WORKSPACE_URL"))
}

// AddWorkspaceFlag registers the standard --workspace flag on cmd.
func AddWorkspaceFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVar(target, "workspace", "", WorkspaceFlagUsage)
}

// noTimeoutAnnotation marks commands that wait on a human.
const noTimeoutAnnotation = "slackcli/no-timeout"

// NoTimeout exempts cmd from the global command timeout. Use it for
// commands that wait on a human or legitimately run long (interactive
// editor, self-update).
func NoTimeout(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[noTimeoutAnnotation] = "true"
	return cmd
}

// HasNoTimeout reports whether cmd was marked with NoTimeout.
func HasNoTimeout(cmd *cobra.Command) bool { return cmd.Annotations[noTimeoutAnnotation] == "true" }

// SetWatchdogCanceller is called by the root command to let DisableTimeout
// stop the running command-timeout watchdog.
func (c *Context) SetWatchdogCanceller(cancel func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disableWatchdog = cancel
}

// DisableTimeout stops the command timeout for the rest of this run. Call
// it when a command that normally finishes quickly decides at runtime to
// wait on a human (e.g. `message send` redirected to the editor by safe
// mode).
func (c *Context) DisableTimeout() {
	c.mu.Lock()
	cancel := c.disableWatchdog
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// EnvAuthConfigured reports whether credentials come from SLACK_TOKEN.
func EnvAuthConfigured() bool { return strings.TrimSpace(os.Getenv("SLACK_TOKEN")) != "" }

// authFromEnv builds credentials from SLACK_TOKEN (+ SLACK_COOKIE_D for
// xoxc tokens). ok is false when SLACK_TOKEN is unset.
func authFromEnv() (a slackapi.Auth, ok bool, err error) {
	token := strings.TrimSpace(os.Getenv("SLACK_TOKEN"))
	if token == "" {
		return a, false, nil
	}
	if strings.HasPrefix(token, "xoxc-") {
		cookie := strings.TrimSpace(os.Getenv("SLACK_COOKIE_D"))
		if cookie == "" {
			cookie = strings.TrimSpace(os.Getenv("SLACK_COOKIE"))
		}
		// Accept "d=xoxd-…" as copied from a Cookie header.
		cookie = strings.TrimPrefix(cookie, "d=")
		if cookie == "" {
			return a, false, errors.New("SLACK_TOKEN is a browser token (xoxc-) but SLACK_COOKIE_D is not set")
		}
		return slackapi.Auth{Type: slackapi.AuthBrowser, XoxcToken: token, XoxdCookie: slackapi.DecodeCookieD(cookie)}, true, nil
	}
	return slackapi.Auth{Type: slackapi.AuthStandard, Token: token}, true, nil
}

// AssertWorkspaceForChannelNames fails when a channel is given by name
// (not ID) while several workspaces are configured and nothing says which
// one to use. workspace is the effective selector (may be "").
func (c *Context) AssertWorkspaceForChannelNames(workspace string, channels ...string) error {
	hasName := false
	for _, ch := range channels {
		if !slackid.ParseChannelInput(ch).IsID {
			hasName = true
			break
		}
	}
	if !hasName || strings.TrimSpace(workspace) != "" || EnvAuthConfigured() {
		return nil
	}
	creds, err := c.Store.Load()
	if err != nil {
		return err
	}
	if len(creds.Workspaces) <= 1 || creds.Default() != nil {
		return nil
	}
	return fmt.Errorf("ambiguous channel name across multiple workspaces: %s", chooseWorkspaceAdvice(creds.Workspaces))
}

// chooseWorkspaceAdvice lists the configured workspaces and the ways to
// say which one is meant, for errors raised when nothing does.
func chooseWorkspaceAdvice(workspaces []auth.Workspace) string {
	urls := make([]string, len(workspaces))
	for i, w := range workspaces {
		urls[i] = w.URL
	}
	return fmt.Sprintf(`%d workspaces are configured (%s) and none is the default. Pass --workspace "<url-or-unique-substring>", set SLACK_WORKSPACE_URL, or choose a default with "%s auth set-default <url>"`,
		len(urls), strings.Join(urls, ", "), appinfo.Name)
}

// WithAutoRefresh runs work and, if it fails because the browser session
// stored for a workspace has expired, re-imports that workspace from Slack
// Desktop and runs work once more. work must obtain its client through
// Context.Client: that is how the refresh knows which credentials were
// rejected, and how the retry picks up the new ones.
//
// The retry must act as the same identity, so nothing is refreshed, and
// the original error is returned, when the rejected credentials are a
// standard token (xoxb-/xoxp-: a bot must never be replaced by the human
// signed in to Slack Desktop) or come from SLACK_TOKEN, when Slack Desktop
// is not signed in to that workspace, or when what it holds is what was
// just rejected.
func WithAutoRefresh[T any](ctx context.Context, c *Context, work func() (T, error)) (T, error) {
	v, err := work()
	if err == nil || !slackapi.IsAuthError(err) {
		return v, err
	}
	if !c.refreshFromDesktop(ctx) {
		return v, err
	}
	return work()
}

// refreshFromDesktop replaces the stored browser session that Client
// resolved last with the one Slack Desktop holds for the same workspace. It
// reports whether the stored credentials changed.
func (c *Context) refreshFromDesktop(ctx context.Context) bool {
	c.mu.Lock()
	failed := c.last
	c.mu.Unlock()
	if failed == nil || !failed.fromStore || !failed.Auth.IsBrowser() || c.Desktop == nil {
		return false
	}
	extracted, err := c.Desktop(ctx)
	if err != nil || extracted == nil {
		return false
	}
	for _, w := range workspacesFrom(extracted, extracted.Teams) {
		if w.URL != failed.WorkspaceURL {
			continue
		}
		if w.Auth == failed.Auth {
			return false // the same session: retrying would fail again
		}
		// UpsertImported re-reads the file: if the workspace now holds a
		// standard token, it is kept and nothing is retried.
		kept, err := c.Store.UpsertImported(w)
		return err == nil && len(kept) == 0
	}
	return false
}

// Import saves the teams of an extraction as browser-auth workspaces. It
// adds new workspaces and refreshes those stored with a browser session.
//
// A workspace stored with a standard token (xoxb-/xoxp-, from "auth add")
// is only replaced when replaceStandard is true, which is for credentials
// the user gave for that very workspace ("auth parse-curl"). A bulk import
// ("auth import-desktop" and the like, or the automatic lookup) passes
// false: it keeps such workspaces as they are and returns their URLs in
// kept, because swapping a bot token for the user's own session changes
// who commands act as.
func (c *Context) Import(e *authextract.Extracted, replaceStandard bool) (kept []string, err error) {
	workspaces := workspacesFrom(e, e.Teams)
	if replaceStandard {
		return nil, c.Store.Upsert(workspaces...)
	}
	return c.Store.UpsertImported(workspaces...)
}

func workspacesFrom(e *authextract.Extracted, teams []authextract.Team) []auth.Workspace {
	out := make([]auth.Workspace, 0, len(teams))
	for _, t := range teams {
		u, err := auth.NormalizeURL(t.URL)
		if err != nil || t.Token == "" || e.CookieD == "" {
			continue
		}
		out = append(out, auth.Workspace{
			URL:  u,
			Name: t.Name,
			Auth: slackapi.Auth{Type: slackapi.AuthBrowser, XoxcToken: t.Token, XoxdCookie: e.CookieD},
		})
	}
	return out
}
