package channelcmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/channels"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
)

// markResult is printed by `channel mark`.
type markResult struct {
	OK      bool   `json:"ok"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// markTarget is what `channel mark` acts on.
type markTarget struct {
	// workspace is the selector to pass to Context.Client ("" for the
	// default workspace).
	workspace string
	// channel is a conversation ID or a "#name" (see channels.ResolveID).
	channel string
	ts      string
}

// resolveMarkTarget turns the <target> argument and the --workspace and
// --ts flags into the read position they designate, without any API call.
func resolveMarkTarget(rt *clictx.Context, arg, workspaceFlag, tsFlag string) (markTarget, error) {
	target, err := slackurl.ParseTarget(arg)
	if err != nil {
		return markTarget{}, err
	}
	workspaceFlag, tsFlag = strings.TrimSpace(workspaceFlag), strings.TrimSpace(tsFlag)
	if workspaceFlag != "" && target.WorkspaceURL != "" {
		return markTarget{}, fmt.Errorf("--workspace cannot be used with a URL target; the workspace (%s) is derived from the URL", target.WorkspaceURL)
	}
	out := markTarget{workspace: target.WorkspaceURL, ts: tsFlag}
	if out.workspace == "" {
		// A channel name/ID, or an app.slack.com link, which only carries
		// a team ID.
		out.workspace = rt.EffectiveWorkspace(workspaceFlag)
	}

	switch target.Kind {
	case slackurl.TargetURL:
		out.channel = target.Ref.ChannelID
		if out.ts == "" {
			out.ts = target.Ref.MessageTS
		}
		if hint := slackurl.TruncatedURLHint(target.Ref); hint != "" {
			rt.Hintf("%s", hint)
		}
	case slackurl.TargetChannel:
		if out.ts == "" {
			return markTarget{}, errors.New(`--ts is required when target is a channel name or ID: pass --ts "<seconds>.<micros>" (e.g. 1770165109.628379), or a Slack message URL as the target`)
		}
		out.channel = target.Channel
	default:
		return markTarget{}, fmt.Errorf(`user targets are not supported for channel mark: pass a Slack message URL, or the DM's channel ID (D…, see "%s user dm-open %s") with --ts`, appinfo.Name, target.UserID)
	}
	if !slackurl.IsTS(out.ts) {
		return markTarget{}, fmt.Errorf("invalid --ts value %q: expected a Slack message ts such as 1770165109.628379", out.ts)
	}
	if err := rt.AssertWorkspaceForChannelNames(out.workspace, out.channel); err != nil {
		return markTarget{}, err
	}
	return out, nil
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
			target, err := resolveMarkTarget(rt, args[0], workspace, ts)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			channelID, err := clictx.WithAutoRefresh(ctx, rt, func() (string, error) {
				r, err := rt.Client(ctx, target.workspace)
				if err != nil {
					return "", err
				}
				channelID, err := channels.ResolveID(ctx, r.Client, target.channel)
				if err != nil {
					return "", err
				}
				return channelID, channels.Mark(ctx, r.Client, channelID, target.ts)
			})
			if err != nil {
				return err
			}
			return rt.Print(markResult{OK: true, Channel: channelID, TS: target.ts})
		},
	}
	cmd.Flags().StringVar(&ts, "ts", "", "Message ts to mark as read, e.g. 1770165109.628379 (required for channel name/ID targets; overrides a URL timestamp)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Workspace selector for channel name/ID targets (full URL or unique substring); cannot be used with URL targets that name their workspace")
	return cmd
}
