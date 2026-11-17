// Package canvascmd implements the "canvas" command.
package canvascmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/cmd/cmdutil"
	"github.com/maelvls/slackcli/internal/slack/canvas"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const (
	canvasArgHelp = `<canvas> is a canvas URL (https://<workspace>.slack.com/docs/T…/F…), the URL
of a channel's canvas (https://<workspace>.slack.com/canvas/C…) or a canvas
ID (F…). A URL names its workspace; with an ID, the default workspace is
used unless --workspace (or SLACK_WORKSPACE_URL) says otherwise.`

	canvasWorkspaceUsage = "Workspace selector (full URL or unique substring) for a <canvas> given as an ID; needed if you have multiple workspaces and the canvas is not in the default one. A <canvas> URL names its own workspace, which --workspace may repeat but not contradict"
)

// New returns the "canvas" command.
func New(rt *clictx.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "canvas",
		Short: "Create, edit and read Slack canvases as Markdown",
	}
	cmd.AddCommand(newCreate(rt), newGet(rt), newEdit(rt))
	return cmd
}

// target is a parsed <canvas> argument.
type target struct {
	// workspace is the selector to hand to clictx.Context.Client.
	workspace string
	// canvasID is "" for a channel canvas URL, in which case channelID
	// is set and the ID has to be looked up.
	canvasID  string
	channelID string
}

// parseTarget interprets a <canvas> argument. A URL carries its own
// workspace; anything else relies on --workspace or the default.
func parseTarget(rt *clictx.Context, value, workspaceFlag string) (target, error) {
	value = strings.TrimSpace(value)
	var ref canvas.Ref
	if strings.Contains(value, "://") {
		var err error
		if ref, err = canvas.ParseURL(value); err != nil {
			return target{}, err
		}
	} else if canvas.IsID(value) {
		ref.CanvasID = value
	} else {
		return target{}, fmt.Errorf("unsupported canvas %q: expected a canvas URL (https://<workspace>.slack.com/docs/T…/F…), a channel canvas URL (https://<workspace>.slack.com/canvas/C…) or a canvas ID (F…)", value)
	}
	workspace, err := cmdutil.Workspace(rt, ref.WorkspaceURL, workspaceFlag)
	if err != nil {
		return target{}, err
	}
	return target{workspace: workspace, canvasID: ref.CanvasID, channelID: ref.ChannelID}, nil
}

// id returns the canvas ID, looking it up for a channel canvas URL.
func (t target) id(ctx context.Context, client *slackapi.Client) (string, error) {
	if t.canvasID != "" {
		return t.canvasID, nil
	}
	return canvas.ChannelCanvasID(ctx, client, t.channelID)
}
