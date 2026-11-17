package canvascmd

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/canvas"
)

func newEdit(rt *clictx.Context) *cobra.Command {
	var file, markdown, operation, sectionID, title, workspace string
	cmd := &cobra.Command{
		Use:   "edit <canvas>",
		Short: "Edit a Slack canvas with one documented operation",
		Long: `Edit a Slack canvas with one documented operation of Slack's canvases.edit
method. One call applies exactly one operation:

  replace          --file|--markdown [--section-id]  replace the WHOLE canvas,
                                                     or only that section
  insert_at_start  --file|--markdown                 add content at the top
  insert_at_end    --file|--markdown                 add content at the bottom
  insert_before    --file|--markdown --section-id    add content before a section
  insert_after     --file|--markdown --section-id    add content after a section
  delete           --section-id                      delete a section
  rename           --title                           change the canvas title

The default operation is replace: without --section-id it overwrites the
entire canvas. Flags an operation does not list are rejected.

` + canvasArgHelp + `

Editing requires a standard Slack token (SLACK_TOKEN) with the canvases:write
scope; imported browser credentials can create standalone canvases but
cannot edit. Prints {"ok": true, "canvas": {"id", "operation"}} once Slack
has accepted the change. This is a write action: it is blocked in safe mode.`,
		Example: `  canvas edit F12345678 --file ./revised-plan.md
  canvas edit F12345678 --operation insert_at_end --markdown "- [ ] One more thing"
  canvas edit F12345678 --operation delete --section-id "temp:C:SECTION"
  canvas edit "https://acme.slack.com/docs/T12345678/F12345678" --operation rename --title "Launch plan (final)"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rt.CheckSafeMode("canvas edit"); err != nil {
				return err
			}
			t, err := parseTarget(rt, args[0], workspace)
			if err != nil {
				return err
			}
			edit := canvas.Edit{Operation: strings.TrimSpace(operation), SectionID: sectionID, Title: title}
			switch {
			case !slices.Contains(canvas.Operations, edit.Operation):
				return fmt.Errorf("unsupported canvas edit operation %q: expected one of %s", operation, strings.Join(canvas.Operations, ", "))
			case canvas.NeedsMarkdown(edit.Operation):
				if edit.Markdown, err = readMarkdown(cmd, file, markdown); err != nil {
					return err
				}
			case cmd.Flags().Changed("file") || cmd.Flags().Changed("markdown"):
				return fmt.Errorf("canvas edit operation %q does not accept Markdown content: drop --file/--markdown", edit.Operation)
			}
			if err := edit.Validate(); err != nil {
				return err
			}

			ctx := cmd.Context()
			payload, err := clictx.WithAutoRefresh(ctx, rt, func() (canvas.EditResult, error) {
				r, err := rt.Client(ctx, t.workspace)
				if err != nil {
					return canvas.EditResult{}, err
				}
				if r.Auth.IsBrowser() {
					return canvas.EditResult{}, canvas.ErrBrowserEdit
				}
				if edit.CanvasID, err = t.id(ctx, r.Client); err != nil {
					return canvas.EditResult{}, err
				}
				return canvas.Apply(ctx, r.Client, edit)
			})
			if err != nil {
				return err
			}
			return rt.Print(payload)
		},
	}
	cmd.Flags().StringVar(&operation, "operation", "replace", "Operation: "+strings.Join(canvas.Operations, ", "))
	cmd.Flags().StringVar(&file, "file", "", fileUsage+"; required for replace and insert_* operations")
	cmd.Flags().StringVar(&markdown, "markdown", "", markdownUsage+"; required for replace and insert_* operations")
	cmd.Flags().StringVar(&sectionID, "section-id", "", `Canvas section ID (e.g. "temp:C:…"): required for insert_before, insert_after and delete; optional for replace, where it narrows the replacement to that section; rejected otherwise`)
	cmd.Flags().StringVar(&title, "title", "", "New canvas title; required for rename, rejected otherwise")
	cmd.Flags().StringVar(&workspace, "workspace", "", canvasWorkspaceUsage)
	return cmd
}
