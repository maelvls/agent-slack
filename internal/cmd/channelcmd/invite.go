package channelcmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/cmd/cmdutil"
	"github.com/maelvls/slackcli/internal/slack/channeladmin"
	"github.com/maelvls/slackcli/internal/slack/users"
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
did go through.

This is a write action that reaches other people (and, with --external,
people outside the workspace): it is refused in safe mode.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := rt.CheckSafeMode("channel invite"); err != nil {
				return err
			}
			if allowExternalInvite && !external {
				return errors.New("--allow-external-user-invites requires --external")
			}
			targets := channeladmin.ParseUsersCSV(usersCSV)
			if len(targets) == 0 {
				return errors.New(`no users provided: pass --users "U01234567,@alice,bob@example.com"`)
			}
			inv := &invitation{rt: rt, external: external, limited: !allowExternalInvite, targets: targets}
			if external {
				// All or nothing: no invitation leaves the workspace while
				// part of the command line is not understood.
				emails, others := channeladmin.SplitEmails(targets)
				if len(others) > 0 {
					return fmt.Errorf(`--external only accepts email addresses in --users (e.g. --users "alice@example.com,bob@example.com"); not an email address: %s. Nothing was sent: drop or fix those targets (invite workspace members without --external)`, strings.Join(others, ", "))
				}
				inv.targets = emails
			}
			target, err := cmdutil.ParseChannel(rt, channel, workspace)
			if err != nil {
				return fmt.Errorf("--channel: %w", err)
			}

			// The progress is kept in inv, outside the work that is run
			// again once expired credentials have been re-imported: the
			// second run only invites those not yet dealt with.
			ctx := cmd.Context()
			_, err = cmdutil.WithTarget(ctx, rt, target, func(loc cmdutil.Located) (struct{}, error) {
				return struct{}{}, inv.run(ctx, loc)
			})
			if err == nil {
				err = inv.err()
			}
			// The payload is printed even with an error: it then lists
			// the invites that went through before the failure.
			if payload := inv.payload(); payload != nil {
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

// invitation is one `channel invite` and how far it got. run may be
// called several times (clictx.WithAutoRefresh): each call carries on
// where the previous one stopped, so that no invite is sent twice and the
// outcome of every target is reported once.
type invitation struct {
	rt       *clictx.Context
	external bool
	// limited is the external_limited flag of external invitations.
	limited bool
	// targets are the --users entries (email addresses with --external).
	targets []string

	channelID string
	// resolved says that pending holds the user IDs targets resolve to;
	// with --external pending are email addresses from the start.
	resolved bool
	// pending are the user IDs or email addresses still to be invited.
	pending []string

	invited, already, unresolved []string
	failed                       []channeladmin.Failure
}

// run invites the targets not yet dealt with. It returns an error only
// when the invite is aborted (see channeladmin.InviteUsers).
func (inv *invitation) run(ctx context.Context, loc cmdutil.Located) error {
	inv.channelID = loc.ChannelID
	if !inv.resolved {
		if inv.external {
			inv.pending = inv.targets
		} else {
			// One batch: the handles that need the user directory share
			// one scan.
			resolved, err := users.ResolveUserIDs(ctx, loc.Client, inv.targets)
			if err != nil {
				// Nobody has been invited yet: a clean failure.
				return err
			}
			for _, r := range resolved {
				if r.Err != nil {
					inv.unresolved = append(inv.unresolved, r.Input)
					inv.rt.Hintf("%v", r.Err)
					continue
				}
				inv.pending = append(inv.pending, r.ID)
			}
		}
		inv.resolved = true
	}

	var (
		invited, already []string
		failed           []channeladmin.Failure
		err              error
	)
	if inv.external {
		var res channeladmin.ExternalInviteResult
		res, err = channeladmin.InviteExternal(ctx, loc.Client, loc.ChannelID, inv.pending, inv.limited)
		invited, already, failed = res.InvitedEmails, res.AlreadyInvitedEmails, res.Failed
	} else {
		var res channeladmin.InviteResult
		res, err = channeladmin.InviteUsers(ctx, loc.Client, loc.ChannelID, inv.pending)
		invited, already, failed = res.InvitedUserIDs, res.AlreadyInChannelUserIDs, res.Failed
	}
	inv.invited = append(inv.invited, invited...)
	inv.already = append(inv.already, already...)
	inv.failed = append(inv.failed, failed...)
	inv.pending = slices.DeleteFunc(slices.Clone(inv.pending), func(target string) bool {
		return slices.Contains(invited, target) || slices.Contains(already, target) ||
			slices.ContainsFunc(failed, func(f channeladmin.Failure) bool { return f.Target == target })
	})
	return err
}

// err is the error of an invite that ran to its end: nil unless a target
// was refused by Slack or could not be resolved.
func (inv *invitation) err() error {
	var unresolved error
	if len(inv.unresolved) > 0 {
		unresolved = fmt.Errorf("could not resolve %d of the users: %s", len(inv.unresolved), strings.Join(inv.unresolved, ", "))
	}
	return errors.Join(channeladmin.InviteResult{Failed: inv.failed}.Err(), unresolved)
}

// payload is what to print; nil when nothing was attempted (the channel
// or the users could not even be looked up).
func (inv *invitation) payload() any {
	if !inv.resolved {
		return nil
	}
	if inv.external {
		return externalInviteResult{
			ChannelID:            inv.channelID,
			External:             true,
			ExternalLimited:      inv.limited,
			InvitedEmails:        inv.invited,
			AlreadyInvitedEmails: inv.already,
			Failed:               inv.failed,
		}
	}
	return inviteResult{
		ChannelID:               inv.channelID,
		InvitedUserIDs:          inv.invited,
		AlreadyInChannelUserIDs: inv.already,
		UnresolvedUsers:         inv.unresolved,
		Failed:                  inv.failed,
	}
}
