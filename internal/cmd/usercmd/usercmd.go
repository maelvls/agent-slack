// Package usercmd implements the "user" command.
package usercmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/users"
)

// maxListLimit is the most users one `user list` call returns (the
// library clamps to it; the command refuses instead of truncating
// silently).
const maxListLimit = 1000

const userArgHelp = `A user is given as a user ID (U…/W…, no lookup needed), an email address,
a @handle (the "@" is optional), or the exact display name or real name of
a single member. Handles and names are looked up by scanning the user
directory, which is slow in large workspaces: prefer IDs or emails.`

// New returns the "user" command.
func New(rt *clictx.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Workspace user directory",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newListCommand(rt), newGetCommand(rt), newDMOpenCommand(rt))
	return cmd
}

func newListCommand(rt *clictx.Context) *cobra.Command {
	var (
		workspace, cursor string
		limit             int
		includeBots       bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List users in the workspace",
		Long: `List users in the workspace, deactivated ones included (they have "deleted": true).

Prints {"users": […], "next_cursor": …}. Each user has id, name (the
handle), real_name, display_name, email, title, tz, is_bot, deleted,
status_text, status_emoji, status_expiration, and dm_id when you already
have a DM with them. next_cursor is present when there are more users:
pass it to --cursor.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 1 || limit > maxListLimit {
				return fmt.Errorf("invalid --limit value %d: must be between 1 and %d (use --cursor with next_cursor to get more)", limit, maxListLimit)
			}
			ctx := cmd.Context()
			ws := rt.EffectiveWorkspace(workspace)
			payload, err := clictx.WithAutoRefresh(ctx, rt, func() (users.UsersPage, error) {
				r, err := rt.Client(ctx, ws)
				if err != nil {
					return users.UsersPage{}, err
				}
				return users.ListUsers(ctx, r.Client, users.ListOptions{Limit: limit, Cursor: cursor, IncludeBots: includeBots})
			})
			if err != nil {
				return err
			}
			return rt.Print(payload)
		},
	}
	clictx.AddWorkspaceFlag(cmd, &workspace)
	cmd.Flags().IntVar(&limit, "limit", 200, fmt.Sprintf("Max users to return (1-%d); bots that are filtered out do not count", maxListLimit))
	cmd.Flags().StringVar(&cursor, "cursor", "", "Pagination cursor: the next_cursor value of the previous page")
	cmd.Flags().BoolVar(&includeBots, "include-bots", false, "Include bot users")
	return cmd
}

func newGetCommand(rt *clictx.Context) *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "get <user>",
		Short: "Get a single workspace user",
		Long: `Get a single workspace user.

` + userArgHelp + `

Prints the user: id, name (the handle), real_name, display_name, email,
title, tz, is_bot, deleted, status_text, status_emoji, status_expiration.
Fails when nobody matches, or when a name designates several members (the
error lists their IDs).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ws := rt.EffectiveWorkspace(workspace)
			payload, err := clictx.WithAutoRefresh(ctx, rt, func() (users.CompactUser, error) {
				r, err := rt.Client(ctx, ws)
				if err != nil {
					return users.CompactUser{}, err
				}
				return users.GetUser(ctx, r.Client, args[0])
			})
			if err != nil {
				return err
			}
			return rt.Print(payload)
		},
	}
	clictx.AddWorkspaceFlag(cmd, &workspace)
	return cmd
}

func newDMOpenCommand(rt *clictx.Context) *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "dm-open <users...>",
		Short: "Open or get a DM / group DM channel",
		Long: `Open the DM with one user, or the group DM with several, and print its
channel ID. An existing conversation is reused; nothing is posted.

<users...>: One to 8 other users; the caller is implicit.

` + userArgHelp + `

Prints {"user_ids": […], "dm_channel_id": "D…"|"G…", "channel_type":
"dm"|"group_dm"}. Use dm_channel_id as the target of other commands.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ws := rt.EffectiveWorkspace(workspace)
			payload, err := clictx.WithAutoRefresh(ctx, rt, func() (users.DMChannel, error) {
				r, err := rt.Client(ctx, ws)
				if err != nil {
					return users.DMChannel{}, err
				}
				return users.GetDMChannelForUsers(ctx, r.Client, args)
			})
			if err != nil {
				return err
			}
			return rt.Print(payload)
		},
	}
	clictx.AddWorkspaceFlag(cmd, &workspace)
	return cmd
}
