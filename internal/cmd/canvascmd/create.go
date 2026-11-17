package canvascmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/cmd/cmdutil"
	"github.com/maelvls/slackcli/internal/slack/canvas"
)

func newCreate(rt *clictx.Context) *cobra.Command {
	var file, markdown, title, channel, workspace string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Slack canvas from exactly one Markdown source",
		Long: `Create a Slack canvas from exactly one Markdown source: --file or --markdown.

Without --channel the canvas is standalone (visible to you until you share
it). With --channel it is added as a tab of that channel; this is the only
kind of canvas free Slack plans allow, and it requires a standard token
(SLACK_TOKEN) with the canvases:write scope. Imported browser credentials can
create standalone canvases only.

Prints {"canvas": {"id", "title"?, "channel_id"?}}; "id" (F…) is what
"canvas get" and "canvas edit" take. This is a write action: it is blocked
in safe mode.`,
		Example: `  canvas create --file ./launch-plan.md --title "Launch plan"
  generate-report | canvas create --file - --title "Weekly report"
  canvas create --markdown $'# Launch plan\n\n- [ ] Ship it' --channel "#project-launch"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := rt.CheckSafeMode("canvas create"); err != nil {
				return err
			}
			content, err := readMarkdown(cmd, file, markdown)
			if err != nil {
				return err
			}
			channel = strings.TrimSpace(channel)
			if cmd.Flags().Changed("channel") && channel == "" {
				return errors.New("--channel is empty: pass a channel ID or name, or drop the flag for a standalone canvas")
			}
			ws := rt.EffectiveWorkspace(workspace)
			var tab cmdutil.Target
			if channel != "" {
				if tab, err = cmdutil.ParseChannel(rt, channel, workspace); err != nil {
					return fmt.Errorf("--channel: %w", err)
				}
				ws = tab.Workspace
			}

			ctx := cmd.Context()
			payload, err := clictx.WithAutoRefresh(ctx, rt, func() (canvas.CreateResult, error) {
				r, err := rt.Client(ctx, ws)
				if err != nil {
					return canvas.CreateResult{}, err
				}
				in := canvas.CreateInput{Markdown: content, Title: title}
				if channel != "" {
					// Checked before the channel lookup, which can take
					// several calls and would be for nothing.
					if r.Auth.IsBrowser() {
						return canvas.CreateResult{}, canvas.ErrBrowserChannel
					}
					if in.ChannelID, err = tab.ConversationID(ctx, r.Client); err != nil {
						return canvas.CreateResult{}, err
					}
				}
				return canvas.Create(ctx, r.Client, in)
			})
			if err != nil {
				return err
			}
			return rt.Print(payload)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", fileUsage)
	cmd.Flags().StringVar(&markdown, "markdown", "", markdownUsage)
	cmd.Flags().StringVar(&title, "title", "", `Canvas title (default: none with a standard token, "Untitled" with browser auth)`)
	cmd.Flags().StringVar(&channel, "channel", "", "Channel ID or name (#channel, channel, C…): add the canvas as a tab of this channel instead of creating a standalone canvas (required on free Slack plans; requires a standard token with canvases:write; browser auth supports standalone canvases only)")
	clictx.AddWorkspaceFlag(cmd, &workspace)
	return cmd
}
