package authcmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// redactSecret keeps just enough of a secret to recognise it: its first 6
// and last 4 characters. Short values are hidden entirely.
func redactSecret(value string) string {
	const keepStart, keepEnd = 6, 4
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= keepStart+keepEnd+3 {
		return "[redacted]"
	}
	return string(runes[:keepStart]) + "…" + string(runes[len(runes)-keepEnd:])
}

type whoamiOutput struct {
	Version             int               `json:"version"`
	UpdatedAt           string            `json:"updated_at"`
	DefaultWorkspaceURL string            `json:"default_workspace_url"`
	Workspaces          []whoamiWorkspace `json:"workspaces"`
	Environment         *whoamiEnv        `json:"environment,omitempty"`
	// Hint says what to do when several workspaces are configured and
	// none is the default.
	Hint string `json:"hint,omitempty"`
}

type whoamiWorkspace struct {
	WorkspaceURL  string `json:"workspace_url"`
	WorkspaceName string `json:"workspace_name"`
	AuthType      string `json:"auth_type"`
	// Token and CookieD are redacted.
	Token   string `json:"token"`
	CookieD string `json:"cookie_d"`
	// Default marks the workspace used when none is selected.
	Default bool `json:"default,omitempty"`
	// Secrets is "ok", "missing" when the credentials file references
	// secrets that are no longer in the keychain, or "unavailable" when
	// the keychain could not be read (locked, timed out).
	Secrets string `json:"secrets"`
	Hint    string `json:"hint,omitempty"`
}

// whoamiEnv describes credentials taken from the environment, which take
// precedence over every stored workspace.
type whoamiEnv struct {
	Note         string `json:"note"`
	AuthType     string `json:"auth_type"`
	Token        string `json:"token"`
	CookieD      string `json:"cookie_d"`
	WorkspaceURL string `json:"workspace_url"`
}

func newWhoami(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show configured workspaces and token sources",
		Long: `Show the configured workspaces, which one is the default, and where
credentials come from. Tokens and cookies are redacted.

This reads local configuration only; use "auth test" to check that the
credentials still work. A workspace with "secrets": "missing" has lost its
keychain entries and must be imported again; "unavailable" means that the
keychain could not be read just now (locked, or a prompt timed out).

"default_workspace_url" is empty when several workspaces are configured and
none was chosen: commands then need --workspace (or SLACK_WORKSPACE_URL)
until "auth set-default" is run; "hint" says so.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			creds, err := rt.Store.Load()
			if err != nil {
				return err
			}
			out := whoamiOutput{
				Version:             creds.Version,
				UpdatedAt:           creds.UpdatedAt,
				DefaultWorkspaceURL: creds.DefaultWorkspaceURL,
				Environment:         environmentAuth(),
				Hint:                noDefaultAdvice(rt),
			}
			def := creds.Default()
			for _, w := range creds.Workspaces {
				row := whoamiWorkspace{
					WorkspaceURL:  w.URL,
					WorkspaceName: w.Name,
					AuthType:      string(w.Auth.Type),
					Default:       def != nil && def.URL == w.URL,
					Secrets:       "ok",
				}
				// The file may only hold keychain placeholders: redact
				// the real values, never a placeholder.
				resolved, err := rt.Store.Resolve(w)
				var unusable *auth.SecretsError
				switch {
				case err == nil:
					row.Token = redactSecret(resolved.BearerToken())
					row.CookieD = redactSecret(resolved.XoxdCookie)
				case errors.As(err, &unusable) && !unusable.Missing():
					row.Secrets = "unavailable"
					row.Hint = fmt.Sprintf(`the keychain could not be read (%v): unlock it or raise %s, then try again; the workspace itself is still configured`,
						unusable.Cause, appinfo.EnvName("KEYCHAIN_TIMEOUT_MS"))
				default:
					row.Secrets = "missing"
					row.Hint = fmt.Sprintf(`the secrets of this workspace are not in the keychain any more: import them again (e.g. "%s auth import-desktop")`, appinfo.Name)
				}
				out.Workspaces = append(out.Workspaces, row)
			}
			return rt.Print(out)
		},
	}
}

func environmentAuth() *whoamiEnv {
	token := strings.TrimSpace(os.Getenv("SLACK_TOKEN"))
	if token == "" {
		return nil
	}
	env := &whoamiEnv{
		Note:         "SLACK_TOKEN is set: it is used instead of the stored workspaces",
		AuthType:     string(slackapi.AuthStandard),
		Token:        redactSecret(token),
		WorkspaceURL: strings.TrimSpace(os.Getenv("SLACK_WORKSPACE_URL")),
	}
	if strings.HasPrefix(token, "xoxc-") {
		env.AuthType = string(slackapi.AuthBrowser)
		cookie := strings.TrimSpace(os.Getenv("SLACK_COOKIE_D"))
		if cookie == "" {
			cookie = strings.TrimSpace(os.Getenv("SLACK_COOKIE"))
		}
		env.CookieD = redactSecret(strings.TrimPrefix(cookie, "d="))
	}
	return env
}
