package channelcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/channeladmin"
	"github.com/maelvls/slackcli/internal/slack/channels"
	"github.com/maelvls/slackcli/internal/slack/users"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// inviteResult is printed by `channel invite`.
type inviteResult struct {
	ChannelID               string   `json:"channel_id"`
	InvitedUserIDs          []string `json:"invited_user_ids"`
	AlreadyInChannelUserIDs []string `json:"already_in_channel_user_ids"`
	// UnresolvedUsers are the --users entries that match no (single)
	// workspace member.
	UnresolvedUsers []string               `json:"unresolved_users"`
	Failed          []channeladmin.Failure `json:"failed"`
}

// externalInviteResult is printed by `channel invite --external`.
type externalInviteResult struct {
	ChannelID            string                 `json:"channel_id"`
	External             bool                   `json:"external"`
	ExternalLimited      bool                   `json:"external_limited"`
	InvitedEmails        []string               `json:"invited_emails"`
	AlreadyInvitedEmails []string               `json:"already_invited_emails"`
	Failed               []channeladmin.Failure `json:"failed"`
}

func newInviteCommand(rt *clictx.Context) *cobra.Command {
	var (
		workspace, channel, usersCSV  string
		external, allowExternalInvite bool
	)
	cmd := &cobra.Command{
		Use:   "invite",
		Short: "Invite users to a channel",
		Long: `Invite workspace members to a channel, or with --external send Slack
Connect invitations to people outside the workspace, by email.

Every target is tried. Prints JSON saying what happened to each:
  channel_id                   the channel
  invited_user_ids             members added now
  already_in_channel_user_ids  members who were already in the channel
  unresolved_users             --users entries matching no single member
                               (the reason is printed on stderr)
  failed                       [{target, error}] invites Slack refused
With --external: channel_id, external, external_limited, invited_emails,
already_invited_emails, failed.

The exit status is non-zero when any target is in unresolved_users or
failed, or when the invite was aborted (channel not found, missing
permission …): the JSON is printed first and still lists the invites that
did go through.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if allowExternalInvite && !external {
				return errors.New("--allow-external-user-invites requires --external")
			}
			targets := channeladmin.ParseUsersCSV(usersCSV)
			if len(targets) == 0 {
				return errors.New(`no users provided: pass --users "U01234567,@alice,bob@example.com"`)
			}
			var emails []string
			if external {
				var others []string
				emails, others = channeladmin.SplitEmails(targets)
				if len(others) > 0 {
					return fmt.Errorf(`--external only accepts email addresses in --users (e.g. --users "alice@example.com,bob@example.com"); not an email address: %s. Nothing was sent`, strings.Join(others, ", "))
				}
			}
			ws := rt.EffectiveWorkspace(workspace)
			if err := rt.AssertWorkspaceForChannelNames(ws, channel); err != nil {
				return err
			}

			ctx := cmd.Context()
			// The payload is returned even with an error: it then lists
			// the invites that went through before the failure.
			payload, err := clictx.WithAutoRefresh(ctx, rt, func() (any, error) {
				r, err := rt.Client(ctx, ws)
				if err != nil {
					return nil, err
				}
				channelID, err := channels.ResolveID(ctx, r.Client, channel)
				if err != nil {
					return nil, err
				}
				if external {
					limited := !allowExternalInvite
					res, err := channeladmin.InviteExternal(ctx, r.Client, channelID, emails, limited)
					payload := externalInviteResult{
						ChannelID:            channelID,
						External:             true,
						ExternalLimited:      limited,
						InvitedEmails:        res.InvitedEmails,
						AlreadyInvitedEmails: res.AlreadyInvitedEmails,
						Failed:               res.Failed,
					}
					if err != nil {
						return payload, err
					}
					return payload, res.Err()
				}
				return inviteMembers(ctx, rt, r.Client, channelID, targets)
			})
			if payload != nil {
				if printErr := rt.Print(payload); printErr != nil {
					return errors.Join(err, printErr)
				}
			}
			return err
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "", "Channel to invite to (required): #name, name, or ID (C...)")
	_ = cmd.MarkFlagRequired("channel")
	cmd.Flags().StringVar(&usersCSV, "users", "", "Comma-separated users to invite (required): user ID (U.../W...), email, @handle or handle; with --external, email addresses only")
	_ = cmd.MarkFlagRequired("users")
	cmd.Flags().BoolVar(&external, "external", false, "Send Slack Connect invitations to people outside the workspace (email targets only)")
	cmd.Flags().BoolVar(&allowExternalInvite, "allow-external-user-invites", false, "With --external: let the invitees invite others and change the channel (by default they can only post)")
	clictx.AddWorkspaceFlag(cmd, &workspace)
	return cmd
}

// inviteMembers resolves targets to user IDs and invites them. It returns
// a nil payload only when nothing was attempted.
func inviteMembers(ctx context.Context, rt *clictx.Context, client *slackapi.Client, channelID string, targets []string) (any, error) {
	payload := inviteResult{ChannelID: channelID}
	var userIDs []string
	// One batch: the handles that need the user directory share one scan.
	resolved, err := users.ResolveUserIDs(ctx, client, targets)
	if err != nil {
		// Nobody has been invited yet: a clean failure.
		return nil, err
	}
	for _, r := range resolved {
		if r.Err != nil {
			payload.UnresolvedUsers = append(payload.UnresolvedUsers, r.Input)
			rt.Hintf("%v", r.Err)
			continue
		}
		userIDs = append(userIDs, r.ID)
	}

	res, err := channeladmin.InviteUsers(ctx, client, channelID, userIDs)
	payload.InvitedUserIDs = res.InvitedUserIDs
	payload.AlreadyInChannelUserIDs = res.AlreadyInChannelUserIDs
	payload.Failed = res.Failed
	if err != nil {
		return payload, err
	}
	if len(payload.UnresolvedUsers) > 0 {
		err = fmt.Errorf("could not resolve %d of the users: %s", len(payload.UnresolvedUsers), strings.Join(payload.UnresolvedUsers, ", "))
	}
	return payload, errors.Join(res.Err(), err)
}
