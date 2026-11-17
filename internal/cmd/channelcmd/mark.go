package channelcmd

import (
	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/cmd/cmdutil"
	"github.com/maelvls/slackcli/internal/slack/channels"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
)

// markResult is printed by `channel mark`.
type markResult struct {
	OK      bool   `json:"ok"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

func newMarkCommand(rt *clictx.Context) *cobra.Command {
	var workspace, ts string
	cmd := &cobra.Command{
		Use:   "mark <target>",
		Short: "Mark a channel/DM as read up to a given message",
		Long: `Move your read cursor in a conversation to a message: that message and
everything before it become read, everything after it unread (so an older
ts marks messages as unread again).

<target> is a Slack message URL (read up to that message), or a channel
(#name or ID) together with --ts.

Prints {"ok": true, "channel": <channel ID>, "ts": <new read position>}.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := cmdutil.ParseTarget(rt, args[0], workspace)
			if err != nil {
				return err
			}
			if err := target.NoUser(cmd.CommandPath()); err != nil {
				return err
			}
			// Unlike the commands that act on one message, the target
			// here is foremost a conversation: --ts may move the read
			// position away from the message of a URL.
			readTS, err := cmdutil.ParseTS("--ts", ts)
			if err != nil {
				return err
			}
			if readTS == "" || target.Kind != slackurl.TargetURL {
				if readTS, err = target.MessageTS(ts); err != nil {
					return err
				}
			}
			ctx := cmd.Context()
			channelID, err := cmdutil.WithTarget(ctx, rt, target, func(loc cmdutil.Located) (string, error) {
				return loc.ChannelID, channels.Mark(ctx, loc.Client, loc.ChannelID, readTS)
			})
			if err != nil {
				return err
			}
			return rt.Print(markResult{OK: true, Channel: channelID, TS: readTS})
		},
	}
	cmd.Flags().StringVar(&ts, "ts", "", "Message ts to mark as read, e.g. 1770165109.628379 (required for channel name/ID targets; overrides a URL timestamp)")
	cmdutil.AddTargetWorkspaceFlag(cmd, &workspace)
	return cmd
}
