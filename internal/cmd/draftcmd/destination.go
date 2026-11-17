package draftcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maelvls/slackcli/internal/cmd/cmdutil"
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

// checkBroadcastStatically rejects the --broadcast combinations that are
// invalid whatever Slack answers: a DM target, or a channel target without
// --thread-ts. A message URL into a channel gets its thread from the
// message, so it is checked after resolution (checkBroadcast).
func checkBroadcastStatically(target cmdutil.Target, threadTS string) error {
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
func resolveDestination(ctx context.Context, client *slackapi.Client, target cmdutil.Target, threadTS string) (destination, error) {
	channelID, err := target.ConversationID(ctx, client)
	if err != nil {
		return destination{}, err
	}
	if threadTS == "" {
		if threadTS, err = target.ReplyThreadTS(ctx, client); err != nil {
			return destination{}, err
		}
	}
	return destination{channelID: channelID, threadTS: threadTS}, nil
}
