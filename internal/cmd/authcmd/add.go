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

func newAdd(rt *clictx.Context) *cobra.Command {
	var (
		workspaceURL, token, xoxc, xoxd string
		fromEnv                         bool
	)
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add credentials (standard token or browser xoxc/xoxd)",
		Long: fmt.Sprintf(`Save credentials for a workspace: either a standard token (xoxb-/xoxp-), or
a browser token (xoxc-) together with the browser's "d" cookie (xoxd-).

Prefer --from-env, which reads SLACK_TOKEN (and SLACK_COOKIE_D for an xoxc-
token) from the environment:

  SLACK_TOKEN=xoxb-… %[1]s auth add --workspace-url https://myteam.slack.com --from-env

--token, --xoxc and --xoxd put the secret on the command line, where other
processes on the machine and the shell history can see it.

--workspace-url must be an https Slack workspace URL (*.slack.com or
*.slack-gov.com): credentials are never saved for, or sent to, another host.
Credentials already saved for that workspace are replaced. The first
workspace saved becomes the default (see "auth set-default").`, appinfo.Name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			u, err := auth.NormalizeURL(workspaceURL)
			if err != nil {
				return fmt.Errorf("--workspace-url must be a workspace URL like https://myteam.slack.com (got %q): credentials are only saved for, and sent to, https Slack workspaces (*.slack.com, *.slack-gov.com)", workspaceURL)
			}
			onArgv := token != "" || xoxc != "" || xoxd != ""
			if fromEnv {
				if onArgv {
					return errors.New("--from-env cannot be combined with --token, --xoxc or --xoxd")
				}
				token, xoxc, xoxd, err = secretsFromEnv()
				if err != nil {
					return err
				}
			}
			a, err := authFromSecrets(strings.TrimSpace(token), strings.TrimSpace(xoxc), strings.TrimSpace(xoxd))
			if err != nil {
				return err
			}
			if err := rt.Store.Upsert(auth.Workspace{URL: u, Auth: a}); err != nil {
				return err
			}
			if onArgv {
				rt.Hintf("warning: secrets passed as flags are visible in the process list and the shell history; prefer --from-env")
			}
			message := "Saved standard token."
			if a.IsBrowser() {
				message = "Saved browser tokens."
			}
			_, err = fmt.Fprintln(rt.Stdout, message)
			return err
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&workspaceURL, "workspace-url", "", "Workspace URL like https://myteam.slack.com (required)")
	flags.StringVar(&token, "token", "", "Standard Slack token (xoxb-/xoxp-); visible in the process list, prefer --from-env")
	flags.StringVar(&xoxc, "xoxc", "", "Browser token (xoxc-...), with --xoxd; visible in the process list, prefer --from-env")
	flags.StringVar(&xoxd, "xoxd", "", "Browser cookie d (xoxd-...), with --xoxc; visible in the process list, prefer --from-env")
	flags.BoolVar(&fromEnv, "from-env", false, "Read the token from SLACK_TOKEN (and the cookie from SLACK_COOKIE_D for an xoxc- token) instead of flags")
	_ = cmd.MarkFlagRequired("workspace-url")
	return cmd
}

// secretsFromEnv reads the credentials to save from the environment.
func secretsFromEnv() (token, xoxc, xoxd string, err error) {
	value := strings.TrimSpace(os.Getenv("SLACK_TOKEN"))
	if value == "" {
		return "", "", "", errors.New("--from-env: SLACK_TOKEN is not set")
	}
	if !strings.HasPrefix(value, "xoxc-") {
		return value, "", "", nil
	}
	cookie := strings.TrimSpace(os.Getenv("SLACK_COOKIE_D"))
	if cookie == "" {
		return "", "", "", errors.New("--from-env: SLACK_TOKEN is a browser token (xoxc-), so SLACK_COOKIE_D must be set too")
	}
	return "", value, cookie, nil
}

// authFromSecrets validates the secrets given to "auth add". Catching a
// token passed under the wrong flag here beats a puzzling invalid_auth
// later.
func authFromSecrets(token, xoxc, xoxd string) (slackapi.Auth, error) {
	var none slackapi.Auth
	switch {
	case token != "" && (xoxc != "" || xoxd != ""):
		return none, errors.New("provide either --token or both --xoxc and --xoxd, not all of them")
	case token != "":
		if strings.HasPrefix(token, "xoxc-") {
			return none, errors.New("--token got a browser token (xoxc-): pass it with --xoxc, together with --xoxd")
		}
		if strings.HasPrefix(token, "xoxd-") {
			return none, errors.New("--token got a cookie (xoxd-): pass it with --xoxd, together with --xoxc")
		}
		return slackapi.Auth{Type: slackapi.AuthStandard, Token: token}, nil
	case xoxc != "" && xoxd != "":
		// Accept "d=xoxd-…" as copied from a Cookie header, encoded or not.
		xoxd = slackapi.DecodeCookieD(strings.TrimPrefix(xoxd, "d="))
		if !strings.HasPrefix(xoxc, "xoxc-") {
			return none, errors.New("--xoxc must be a browser token starting with xoxc-")
		}
		if !strings.HasPrefix(xoxd, "xoxd-") {
			return none, errors.New("--xoxd must be the value of the browser's \"d\" cookie, starting with xoxd-")
		}
		return slackapi.Auth{Type: slackapi.AuthBrowser, XoxcToken: xoxc, XoxdCookie: xoxd}, nil
	}
	return none, errors.New("provide either --token or both --xoxc and --xoxd (or --from-env)")
}
