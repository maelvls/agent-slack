package draftcmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/channels"
	"github.com/maelvls/slackcli/internal/slack/messages"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
	"github.com/maelvls/slackcli/internal/slackapi"
	"github.com/maelvls/slackcli/internal/slackid"
)

// destination is a resolved draft destination.
type destination struct {
	channelID string
	threadTS  string
}

var errBroadcastDM = errors.New("--broadcast is not supported for DM targets: a DM has no channel to broadcast a thread reply to")

func errBroadcastNeedsThread(how string) error {
	return fmt.Errorf("--broadcast requires a thread (use %s)", how)
}

// isDM reports whether a conversation ID is a 1:1 DM.
func isDM(channelID string) bool { return strings.HasPrefix(channelID, "D") }

func validateThreadTS(ts string) error {
	if ts != "" && !slackurl.IsTS(ts) {
		return fmt.Errorf("invalid --thread-ts %q: expected a Slack message ts such as 1770165109.628379", ts)
	}
	return nil
}

// targetWorkspace returns the workspace selector for a target: the
// workspace its URL names, else --workspace / SLACK_WORKSPACE_URL / the
// default. Channel names need to know which workspace they are in.
func targetWorkspace(rt *clictx.Context, target slackurl.Target, flag string) (string, error) {
	if target.WorkspaceURL != "" {
		return target.WorkspaceURL, nil
	}
	workspace := rt.EffectiveWorkspace(flag)
	if target.Kind == slackurl.TargetChannel {
		if err := rt.AssertWorkspaceForChannelNames(workspace, target.Channel); err != nil {
			return "", err
		}
	}
	return workspace, nil
}

func hintTruncatedURL(rt *clictx.Context, target slackurl.Target) {
	if target.Kind != slackurl.TargetURL {
		return
	}
	if hint := slackurl.TruncatedURLHint(target.Ref); hint != "" {
		rt.Hintf("%s", hint)
	}
}

// checkBroadcastStatically rejects the --broadcast combinations that are
// invalid whatever Slack answers: a DM target, or a channel target without
// --thread-ts. A message URL into a channel gets its thread from the
// message, so it is checked after resolution (checkBroadcast).
func checkBroadcastStatically(target slackurl.Target, threadTS string) error {
	switch target.Kind {
	case slackurl.TargetUser:
		return errBroadcastDM
	case slackurl.TargetURL:
		if isDM(target.Ref.ChannelID) {
			return errBroadcastDM
		}
		return nil
	}
	if ch := slackid.ParseChannelInput(target.Channel); ch.IsID && isDM(ch.Value) {
		return errBroadcastDM
	}
	if threadTS == "" {
		return errBroadcastNeedsThread("--thread-ts or a message URL target")
	}
	return nil
}

// checkBroadcast validates --broadcast against a resolved destination.
func checkBroadcast(dest destination, how string) error {
	if isDM(dest.channelID) {
		return errBroadcastDM
	}
	if dest.threadTS == "" {
		return errBroadcastNeedsThread(how)
	}
	return nil
}

// resolveDestination turns a target into a channel and thread. A message
// URL drafts a reply in that message's thread, like `message send`; a user
// ID drafts a DM. threadTS (--thread-ts) wins over the URL's thread.
func resolveDestination(ctx context.Context, client *slackapi.Client, target slackurl.Target, threadTS string) (destination, error) {
	switch target.Kind {
	case slackurl.TargetURL:
		if threadTS == "" {
			msg, err := messages.FetchMessage(ctx, client, target.Ref, false)
			if err != nil {
				return destination{}, err
			}
			threadTS = cmp.Or(msg.ThreadTS, msg.TS)
		}
		return destination{channelID: target.Ref.ChannelID, threadTS: threadTS}, nil
	case slackurl.TargetUser:
		channelID, err := channels.OpenDM(ctx, client, target.UserID)
		if err != nil {
			return destination{}, err
		}
		return destination{channelID: channelID, threadTS: threadTS}, nil
	}
	channelID, err := channels.ResolveID(ctx, client, target.Channel)
	if err != nil {
		return destination{}, err
	}
	return destination{channelID: channelID, threadTS: threadTS}, nil
}
