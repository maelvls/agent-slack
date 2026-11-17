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
}

// New returns the production context.
func New() *Context {
	return &Context{
		Store:   auth.DefaultStore(),
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Desktop: authextract.Desktop,
		Chrome:  authextract.Chrome,
		Brave:   authextract.Brave,
		Firefox: authextract.Firefox,
	}
}

// Print writes a command result to Stdout as pruned JSON.
func (c *Context) Print(v any) error { return output.Write(c.Stdout, v) }

// Hintf writes a line to Stderr (warnings, hints; never results).
func (c *Context) Hintf(format string, args ...any) {
	fmt.Fprintf(c.Stderr, strings.TrimRight(format, "\n")+"\n", args...)
}

// SafeMode reports whether safe mode is on (--safe-mode or
// SLACKCLI_SAFE_MODE): nothing may be posted, edited or deleted without a
// human reviewing it.
func (c *Context) SafeMode() bool { return c.SafeModeFlag || appinfo.EnvBool("SAFE_MODE") }

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
	if len(creds.Workspaces) <= 1 || creds.DefaultWorkspaceURL != "" {
		return nil
	}
	return fmt.Errorf(`ambiguous channel name across multiple workspaces: pass --workspace "<url-or-unique-substring>" (or set SLACK_WORKSPACE_URL, or run "%s auth set-default <url>")`, appinfo.Name)
}

// WithAutoRefresh runs work and, if it fails because stored credentials
// have expired, re-imports them from Slack Desktop and runs work once more.
// work must obtain its client through Context.Client so that the retry
// picks up the refreshed credentials.
func WithAutoRefresh[T any](ctx context.Context, c *Context, work func() (T, error)) (T, error) {
	v, err := work()
	if err == nil || !slackapi.IsAuthError(err) || EnvAuthConfigured() {
		return v, err
	}
	if !c.refreshFromDesktop(ctx) {
		return v, err
	}
	return work()
}

func (c *Context) refreshFromDesktop(ctx context.Context) bool {
	if c.Desktop == nil {
		return false
	}
	extracted, err := c.Desktop(ctx)
	if err != nil || extracted == nil || len(extracted.Teams) == 0 {
		return false
	}
	return c.Import(extracted) == nil
}

// Import saves every team of an extraction as a browser-auth workspace.
func (c *Context) Import(e *authextract.Extracted) error {
	return c.Store.Upsert(workspacesFrom(e, e.Teams)...)
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
