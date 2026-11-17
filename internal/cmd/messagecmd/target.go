package messagecmd

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/channels"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
)

// located is a target resolved against Slack.
type located struct {
	*clictx.Resolved
	channelID string
}

// noUserTarget rejects user-ID targets for commands that act on a message.
func noUserTarget(target slackurl.Target, what string) error {
	if target.Kind == slackurl.TargetUser {
		return fmt.Errorf("%s does not support user ID targets: pass a message URL, or a channel (name or ID) with --ts", what)
	}
	return nil
}

// withTarget picks the workspace of target, resolves its conversation ID
// (opening the DM for a user target) and runs work, retrying once with
// re-imported credentials if they turn out to be expired.
func withTarget[T any](ctx context.Context, c *command, target slackurl.Target, workspaceFlag string, work func(loc located) (T, error)) (T, error) {
	var zero T
	// A URL names its workspace, except app.slack.com links, which only
	// carry a team ID: those fall back to --workspace like channel names.
	workspace := target.WorkspaceURL
	if workspace == "" {
		workspace = c.rt.EffectiveWorkspace(workspaceFlag)
		if target.Kind == slackurl.TargetChannel {
			if err := c.rt.AssertWorkspaceForChannelNames(workspace, target.Channel); err != nil {
				return zero, err
			}
		}
	}
	if target.Kind == slackurl.TargetURL {
		if hint := slackurl.TruncatedURLHint(target.Ref); hint != "" {
			c.rt.Hintf("%s", hint)
		}
	}
	return clictx.WithAutoRefresh(ctx, c.rt, func() (T, error) {
		r, err := c.rt.Client(ctx, workspace)
		if err != nil {
			return zero, err
		}
		loc := located{Resolved: r}
		switch target.Kind {
		case slackurl.TargetURL:
			loc.channelID = target.Ref.ChannelID
		case slackurl.TargetUser:
			loc.channelID, err = channels.OpenDM(ctx, r.Client, target.UserID)
		default:
			loc.channelID, err = channels.ResolveID(ctx, r.Client, target.Channel)
		}
		if err != nil {
			return zero, err
		}
		return work(loc)
	})
}

// parseTS validates a Slack timestamp flag value; "" stays "".
func parseTS(flag, raw string) (string, error) {
	ts := strings.TrimSpace(raw)
	if ts != "" && !slackurl.IsTS(ts) {
		return "", fmt.Errorf(`invalid %s value %q: expected a Slack message timestamp such as 1770165109.628379 (the "ts" of a message)`, flag, raw)
	}
	return ts, nil
}

// messageTS returns the ts of the message a command acts on: the one in
// the URL, or --ts for a channel target.
func messageTS(target slackurl.Target, tsFlag string) (string, error) {
	ts, err := parseTS("--ts", tsFlag)
	if err != nil {
		return "", err
	}
	if target.Kind == slackurl.TargetURL {
		if ts != "" && ts != target.Ref.MessageTS {
			return "", fmt.Errorf("--ts %s contradicts the message URL, which points at %s: drop --ts, or pass the channel instead of the URL", ts, target.Ref.MessageTS)
		}
		return target.Ref.MessageTS, nil
	}
	if ts == "" {
		return "", errors.New(`when targeting a channel, you must pass --ts "<seconds>.<micros>" (or pass a message URL instead)`)
	}
	return ts, nil
}

var historyBoundRe = regexp.MustCompile(`^\d+(\.\d+)?$`)

// parseHistoryBound validates --oldest / --latest: a message ts or a Unix
// time in seconds.
func parseHistoryBound(flag, raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v != "" && !historyBoundRe.MatchString(v) {
		return "", fmt.Errorf("invalid %s value %q: expected a Slack ts (1770165109.628379) or a Unix time in seconds", flag, raw)
	}
	return v, nil
}
