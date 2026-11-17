// Package authcmd implements the "auth" command: inspecting, importing and
// managing Slack credentials.
package authcmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/clictx"
)

// New returns the "auth" command.
func New(rt *clictx.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage Slack authentication",
		Long: fmt.Sprintf(`Manage Slack authentication.

Credentials are looked up in this order: the SLACK_TOKEN environment variable
(plus SLACK_COOKIE_D for xoxc- browser tokens), the workspaces saved by the
commands below, then Slack Desktop, Chrome, Brave and Firefox, which are
imported on the fly.

Typical setup: "%[1]s auth import-desktop", then "%[1]s auth test".`, appinfo.Name),
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newWhoami(rt),
		newTest(rt),
		newImportChrome(rt),
		newImportBrave(rt),
		newImportFirefox(rt),
		newParseCurl(rt),
		newImportDesktop(rt),
		newAdd(rt),
		newSetDefault(rt),
		newRemove(rt),
	)
	return cmd
}

func newTest(rt *clictx.Context) *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Verify credentials (calls Slack auth.test)",
		Long: `Verify credentials by calling Slack's auth.test, and print its answer
(workspace URL, team, user and their IDs).

Expired browser credentials are re-imported from Slack Desktop once before
giving up.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			selector := rt.EffectiveWorkspace(workspace)
			resp, err := clictx.WithAutoRefresh(ctx, rt, func() (map[string]any, error) {
				r, err := rt.Client(ctx, selector)
				if err != nil {
					return nil, err
				}
				return r.Client.API(ctx, "auth.test", nil)
			})
			if err != nil {
				return err
			}
			return rt.Print(resp)
		},
	}
	clictx.AddWorkspaceFlag(cmd, &workspace)
	return cmd
}

// resolveConfigured finds the configured workspace an argument designates:
// its URL, or a unique substring of its URL or name.
func resolveConfigured(rt *clictx.Context, selector string) (*auth.Workspace, error) {
	creds, err := rt.Store.Load()
	if err != nil {
		return nil, err
	}
	match, ambiguous := auth.SelectWorkspace(creds.Workspaces, selector)
	if len(ambiguous) > 0 {
		urls := make([]string, len(ambiguous))
		for i, w := range ambiguous {
			urls[i] = w.URL
		}
		return nil, fmt.Errorf("%q matches several workspaces (%s): pass the full workspace URL", selector, strings.Join(urls, ", "))
	}
	if match == nil {
		return nil, fmt.Errorf(`workspace %q is not configured: run "%s auth whoami" to list configured workspaces`, selector, appinfo.Name)
	}
	return match, nil
}

func newSetDefault(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "set-default <workspace-url>",
		Short: "Set the default workspace",
		Long: `Set the workspace used when neither --workspace nor SLACK_WORKSPACE_URL is
given. The argument is the URL of a configured workspace (like
https://myteam.slack.com) or a unique substring of its URL or name.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			w, err := resolveConfigured(rt, args[0])
			if err != nil {
				return err
			}
			if err := rt.Store.SetDefault(w.URL); err != nil {
				return err
			}
			_, err = fmt.Fprintf(rt.Stdout, "Default workspace set to %s.\n", w.URL)
			return err
		},
	}
}

func newRemove(rt *clictx.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <workspace-url>",
		Short: "Remove a workspace from local config",
		Long: `Remove a workspace and its saved credentials (including its keychain
entries on macOS). The argument is the URL of a configured workspace (like
https://myteam.slack.com) or a unique substring of its URL or name.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			w, err := resolveConfigured(rt, args[0])
			if err != nil {
				return err
			}
			url := w.URL
			if _, err := rt.Store.Remove(url); err != nil {
				return err
			}
			_, err = fmt.Fprintf(rt.Stdout, "Removed workspace %s.\n", url)
			return err
		},
	}
}
