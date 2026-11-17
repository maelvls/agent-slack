package latercmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
)

// msgTarget designates one message.
type msgTarget struct {
	// workspace is the selector to pass to Context.Client ("" for the
	// default workspace).
	workspace string
	// channel is a conversation ID or a "#name" (see channels.ResolveID).
	channel string
	ts      string
}

// resolveTarget turns the <target> argument and the --workspace and --ts
// flags into the message they designate, without any API call.
func resolveTarget(rt *clictx.Context, arg, workspaceFlag, tsFlag string) (msgTarget, error) {
	target, err := slackurl.ParseTarget(arg)
	if err != nil {
		return msgTarget{}, err
	}
	workspaceFlag, tsFlag = strings.TrimSpace(workspaceFlag), strings.TrimSpace(tsFlag)
	if workspaceFlag != "" && target.WorkspaceURL != "" {
		return msgTarget{}, fmt.Errorf("--workspace cannot be used with a URL target; the workspace (%s) is derived from the URL", target.WorkspaceURL)
	}
	workspace := target.WorkspaceURL
	if workspace == "" {
		// A channel name/ID, or an app.slack.com link, which only carries
		// a team ID.
		workspace = rt.EffectiveWorkspace(workspaceFlag)
	}

	switch target.Kind {
	case slackurl.TargetURL:
		if tsFlag != "" {
			return msgTarget{}, errors.New("--ts cannot be used with a message URL: the URL already identifies the message")
		}
		if hint := slackurl.TruncatedURLHint(target.Ref); hint != "" {
			rt.Hintf("%s", hint)
		}
		return msgTarget{workspace: workspace, channel: target.Ref.ChannelID, ts: target.Ref.MessageTS}, nil
	case slackurl.TargetChannel:
		if tsFlag == "" {
			return msgTarget{}, errors.New(`--ts is required when the target is a channel: pass --ts "<seconds>.<micros>" (e.g. 1770165109.628379), or a Slack message URL as the target`)
		}
		if !slackurl.IsTS(tsFlag) {
			return msgTarget{}, fmt.Errorf(`invalid --ts value %q: expected a Slack message ts such as 1770165109.628379`, tsFlag)
		}
		if err := rt.AssertWorkspaceForChannelNames(workspace, target.Channel); err != nil {
			return msgTarget{}, err
		}
		return msgTarget{workspace: workspace, channel: target.Channel, ts: tsFlag}, nil
	}
	return msgTarget{}, fmt.Errorf(`user targets are not supported here: pass a Slack message URL, or the DM's channel ID (D…, see "%s user dm-open %s") with --ts`, appinfo.Name, target.UserID)
}
