// Package canvascmd implements the "canvas" command.
package canvascmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/canvas"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const (
	canvasArgHelp = `<canvas> is a canvas URL (https://<workspace>.slack.com/docs/T…/F…), the URL
of a channel's canvas (https://<workspace>.slack.com/canvas/C…) or a canvas
ID (F…). A URL names its workspace; with an ID, the default workspace is
used unless --workspace (or SLACK_WORKSPACE_URL) says otherwise.`

	canvasWorkspaceUsage = "Workspace selector (full URL or unique substring) for a <canvas> given as an ID; needed if you have multiple workspaces and the canvas is not in the default one. Ignored when <canvas> is a URL of a workspace"
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

// safeModeError refuses a write while safe mode is on. Safe mode promises
// that nothing is posted, edited or deleted without a human reviewing it,
// and there is no review step for canvases.
func safeModeError(rt *clictx.Context, command, verb string) error {
	if !rt.SafeMode() {
		return nil
	}
	return fmt.Errorf(`safe mode is active (%s or --safe-mode): %q is blocked so that an agent cannot %s canvases without human review. Ask the user to do it, or to disable safe mode`,
		appinfo.EnvName("SAFE_MODE"), command, verb)
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
	if strings.Contains(value, "://") {
		ref, err := canvas.ParseURL(value)
		if err != nil {
			return target{}, err
		}
		t := target{workspace: ref.WorkspaceURL, canvasID: ref.CanvasID, channelID: ref.ChannelID}
		if t.workspace == "" {
			t.workspace = rt.EffectiveWorkspace(workspaceFlag)
		}
		return t, nil
	}
	if !canvas.IsID(value) {
		return target{}, fmt.Errorf("unsupported canvas %q: expected a canvas URL (https://<workspace>.slack.com/docs/T…/F…), a channel canvas URL (https://<workspace>.slack.com/canvas/C…) or a canvas ID (F…)", value)
	}
	return target{workspace: rt.EffectiveWorkspace(workspaceFlag), canvasID: value}, nil
}

// id returns the canvas ID, looking it up for a channel canvas URL.
func (t target) id(ctx context.Context, client *slackapi.Client) (string, error) {
	if t.canvasID != "" {
		return t.canvasID, nil
	}
	return canvas.ChannelCanvasID(ctx, client, t.channelID)
}
