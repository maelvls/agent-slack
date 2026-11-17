// Package channelcmd implements the "channel" command.
package channelcmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/channeladmin"
	"github.com/maelvls/slackcli/internal/slack/channels"
	"github.com/maelvls/slackcli/internal/slack/users"
)

// maxListLimit is the largest page Slack serves (the library clamps to
// it; the command refuses instead of truncating silently).
const maxListLimit = 1000

// New returns the "channel" command.
func New(rt *clictx.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channel",
		Short: "List conversations, create channels, manage invites, and mark as read",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newListCommand(rt), newNewCommand(rt), newInviteCommand(rt), newMarkCommand(rt))
	return cmd
}

func newListCommand(rt *clictx.Context) *cobra.Command {
	var (
		workspace, user, cursor string
		all, viaCounts          bool
		limit                   int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List conversations for a user (or current user), or all conversations",
		Long: `List conversations: public and private channels, DMs and group DMs;
archived channels are left out.

By default, lists the conversations you are a member of
(users.conversations); with --user, those of another user that you can
see; with --all, every conversation visible to you (conversations.list);
with --via-counts, your conversations as the Slack client lists them.

Prints {"channels": […], "next_cursor": …}: channels are Slack's
conversation objects (id, name, is_private, is_im, user, …). One call
returns one page; next_cursor is present when there are more: pass it to
--cursor, with the same other flags.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch {
			case all && user != "":
				return errors.New("--all cannot be used with --user")
			case viaCounts && all:
				return errors.New("--via-counts cannot be used with --all")
			case viaCounts && user != "":
				return errors.New("--via-counts cannot be used with --user")
			}
			if limit < 1 || limit > maxListLimit {
				return fmt.Errorf("invalid --limit value %d: must be between 1 and %d (use --cursor with next_cursor to get more)", limit, maxListLimit)
			}
			ctx := cmd.Context()
			ws := rt.EffectiveWorkspace(workspace)
			payload, err := clictx.WithAutoRefresh(ctx, rt, func() (channels.ConversationsPage, error) {
				var none channels.ConversationsPage
				r, err := rt.Client(ctx, ws)
				if err != nil {
					return none, err
				}
				opts := channels.ListOptions{Limit: limit, Cursor: cursor}
				switch {
				case viaCounts:
					if !r.Auth.IsBrowser() {
						return none, fmt.Errorf(`--via-counts uses Slack's internal client.counts API, which only works with browser session credentials, not with a bot or user token (xoxb-/xoxp-): drop --via-counts, or import browser credentials with "%s auth import-desktop"`, appinfo.Name)
					}
					return channels.ListViaCounts(ctx, r.Client, opts)
				case all:
					return channels.ListAll(ctx, r.Client, opts)
				}
				if user != "" {
					if opts.User, err = users.ResolveUserID(ctx, r.Client, user); err != nil {
						return none, err
					}
				}
				return channels.ListUser(ctx, r.Client, opts)
			})
			if err != nil {
				return err
			}
			return rt.Print(payload)
		},
	}
	clictx.AddWorkspaceFlag(cmd, &workspace)
	cmd.Flags().StringVar(&user, "user", "", "List the conversations of this user instead of yours: user ID (U.../W...), email, or @handle/handle")
	cmd.Flags().BoolVar(&all, "all", false, "List all conversations (conversations.list); incompatible with --user")
	cmd.Flags().BoolVar(&viaCounts, "via-counts", false, "List joined conversations via client.counts (works on Enterprise Grid where users.conversations/conversations.list are restricted for browser tokens; needs browser credentials; one conversations.info call per conversation listed; incompatible with --all/--user)")
	cmd.Flags().IntVar(&limit, "limit", 100, fmt.Sprintf("Max conversations in one page (1-%d)", maxListLimit))
	cmd.Flags().StringVar(&cursor, "cursor", "", "Pagination cursor for the next page: the next_cursor value of the previous page (with --via-counts it is a numeric offset)")
	return cmd
}

func newNewCommand(rt *clictx.Context) *cobra.Command {
	var (
		workspace, name string
		private         bool
	)
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Create a new channel",
		Long: `Create a new channel. You become its only member: add people with
"channel invite".

Prints {"channel": {"id": …, "name": …, "is_private": …}}.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Checked before any credential lookup: it needs no network.
			if _, err := channeladmin.NormalizeName(name); err != nil {
				return err
			}
			ctx := cmd.Context()
			ws := rt.EffectiveWorkspace(workspace)
			payload, err := clictx.WithAutoRefresh(ctx, rt, func() (channeladmin.Created, error) {
				r, err := rt.Client(ctx, ws)
				if err != nil {
					return channeladmin.Created{}, err
				}
				return channeladmin.Create(ctx, r.Client, name, private)
			})
			if err != nil {
				return err
			}
			return rt.Print(payload)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", fmt.Sprintf(`Channel name (required): lowercase letters, digits, hyphens and underscores, at most %d characters; a leading "#" is ignored`, channeladmin.MaxNameLength))
	_ = cmd.MarkFlagRequired("name")
	cmd.Flags().BoolVar(&private, "private", false, "Create as a private channel")
	clictx.AddWorkspaceFlag(cmd, &workspace)
	return cmd
}
