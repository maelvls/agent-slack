package channeladmin

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/maelvls/slackcli/internal/slackapi"
)

// Failure is an invite that Slack refused for one target.
type Failure struct {
	// Target is the user ID or email address that could not be invited.
	Target string `json:"target"`
	// Error is Slack's error code, e.g. "user_not_found",
	// "cant_invite_self", "user_is_restricted".
	Error string `json:"error"`
}

// InviteResult reports what happened to each user passed to InviteUsers.
// Every user ID ends up in exactly one of the three lists, unless the
// invite was aborted (see InviteUsers).
type InviteResult struct {
	InvitedUserIDs          []string  `json:"invited_user_ids"`
	AlreadyInChannelUserIDs []string  `json:"already_in_channel_user_ids"`
	Failed                  []Failure `json:"failed"`
}

// Err returns nil if no invite failed, else an error naming the targets
// that could not be invited. Commands print the result, then return Err()
// so that a partial failure still exits non-zero.
func (r InviteResult) Err() error { return failuresErr(r.Failed) }

// ExternalInviteResult reports what happened to each email address passed
// to InviteExternal.
type ExternalInviteResult struct {
	InvitedEmails        []string  `json:"invited_emails"`
	AlreadyInvitedEmails []string  `json:"already_invited_emails"`
	Failed               []Failure `json:"failed"`
}

// Err is like InviteResult.Err.
func (r ExternalInviteResult) Err() error { return failuresErr(r.Failed) }

func failuresErr(failed []Failure) error {
	if len(failed) == 0 {
		return nil
	}
	parts := make([]string, len(failed))
	for i, f := range failed {
		parts[i] = fmt.Sprintf("%s (%s)", f.Target, f.Error)
	}
	return fmt.Errorf("could not invite %d of the targets: %s", len(failed), strings.Join(parts, ", "))
}

// channelLevelCodes are Slack errors that say the invite cannot work for
// anyone, as opposed to a problem with one invitee: carrying on would
// only repeat the same failure for every remaining target.
var channelLevelCodes = []string{
	"channel_not_found", "not_in_channel", "is_archived", "method_not_supported_for_channel_type",
	"missing_scope", "not_allowed_token_type", "no_permission", "restricted_action",
	"ekm_access_denied", "team_access_not_granted", "access_denied", "invalid_channel",
	"ratelimited", "fatal_error", "internal_error", "service_unavailable", "request_timeout",
}

// fatal reports whether err must abort the whole invite. Errors that are
// not a Slack error code (network, timeout, rate limit, cancellation) and
// credential problems always do.
func fatal(err error) bool {
	return slackapi.ErrorCode(err) == "" || slackapi.IsAuthError(err) || slackapi.IsErrorCode(err, channelLevelCodes...)
}

// InviteUsers invites users, by user ID, to a channel
// (conversations.invite), one call per user so that each outcome is known.
//
// A problem with one user (unknown, deactivated, guest restrictions,
// inviting yourself …) is recorded in InviteResult.Failed and the
// remaining users are still invited; the returned error is then nil, so
// callers must check result.Err(). Users already in the channel are not a
// failure.
//
// A problem that affects every user (channel not found, not a member,
// archived, missing permission, expired credentials, network failure)
// aborts: the error is returned together with the result so far, which
// callers should still report because those invites did happen.
func InviteUsers(ctx context.Context, client *slackapi.Client, channelID string, userIDs []string) (InviteResult, error) {
	var res InviteResult
	// Two inputs (a handle and an ID, say) may resolve to the same user.
	for _, userID := range dedupe(userIDs) {
		_, err := client.API(ctx, "conversations.invite", map[string]any{
			"channel": channelID,
			"users":   userID,
		})
		switch {
		case err == nil:
			res.InvitedUserIDs = append(res.InvitedUserIDs, userID)
		case slackapi.IsErrorCode(err, "already_in_channel"):
			res.AlreadyInChannelUserIDs = append(res.AlreadyInChannelUserIDs, userID)
		case fatal(err):
			return res, fmt.Errorf("inviting %s to channel %s: %w", userID, channelID, err)
		default:
			res.Failed = append(res.Failed, Failure{Target: userID, Error: slackapi.ErrorCode(err)})
		}
	}
	return res, nil
}

// InviteExternal sends Slack Connect invitations for a channel to people
// outside the workspace, by email address (conversations.inviteShared).
// externalLimited restricts the invitees to posting only: they cannot
// invite others or change the channel. Slack's (and the CLI's) default is
// true.
//
// Per-address failures and aborts are reported as by InviteUsers.
func InviteExternal(ctx context.Context, client *slackapi.Client, channelID string, emails []string, externalLimited bool) (ExternalInviteResult, error) {
	var res ExternalInviteResult
	for _, email := range dedupe(emails) {
		_, err := client.API(ctx, "conversations.inviteShared", map[string]any{
			"channel":          channelID,
			"emails":           []string{email},
			"external_limited": externalLimited,
		})
		switch {
		case err == nil:
			res.InvitedEmails = append(res.InvitedEmails, email)
		case slackapi.IsErrorCode(err, "already_in_channel", "already_invited"):
			res.AlreadyInvitedEmails = append(res.AlreadyInvitedEmails, email)
		case fatal(err):
			return res, fmt.Errorf("inviting %s to channel %s: %w", email, channelID, err)
		default:
			res.Failed = append(res.Failed, Failure{Target: email, Error: slackapi.ErrorCode(err)})
		}
	}
	return res, nil
}

func dedupe(in []string) []string {
	var out []string
	for _, v := range in {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
