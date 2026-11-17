// Package workflowcmd implements the "workflow" command.
package workflowcmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/cmd/cmdutil"
	"github.com/maelvls/slackcli/internal/slack/workflows"
)

const triggerArgHelp = `<trigger> is a trigger ID (Ft…, the "trigger_id" printed by "workflow list")
or a workflow shortcut URL (https://slack.com/shortcuts/Ft…/…, its "link").`

// New returns the "workflow" command.
func New(rt *clictx.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Discover and run Slack workflows",
		Long: `Discover and run Slack workflows.

Workflows are started by link triggers, which people bookmark or feature in
channels. "list" finds the triggers of a channel, "preview" and "get"
describe a trigger and the workflow behind it without side effects, and
"run" starts the workflow.`,
	}
	cmd.AddCommand(newList(rt), newPreview(rt), newGet(rt), newRun(rt))
	return cmd
}

func newList(rt *clictx.Context) *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "list <channel>",
		Short: "List workflows bookmarked or featured in a channel",
		Long: `List the workflows bookmarked or featured in a channel.

<channel> is a channel ID (C…) or name (#channel, channel).

Prints {"channel_id", "workflows": [{"title", "trigger_id", "link"?,
"app_id"?, "featured"}]}: bookmarked workflows first, then featured ones
that are not bookmarked; "workflows" is [] when there are none. If Slack refuses the featured-workflows lookup
(not every workspace or token has it), only bookmarked workflows are
listed and a warning is printed on stderr. No side effects.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			channel, err := cmdutil.ParseChannel(rt, args[0], workspace)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			payload, err := cmdutil.WithTarget(ctx, rt, channel, func(loc cmdutil.Located) (workflows.ChannelWorkflows, error) {
				return workflows.List(ctx, loc.Client, loc.ChannelID)
			})
			if err != nil {
				return err
			}
			if payload.FeaturedErr != nil {
				rt.Hintf("Warning: featured workflows could not be listed (%v); only bookmarked workflows are shown.", payload.FeaturedErr)
			}
			return cmdutil.PrintList(rt, payload, "workflows")
		},
	}
	clictx.AddWorkspaceFlag(cmd, &workspace)
	return cmd
}

func newPreview(rt *clictx.Context) *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "preview <trigger>",
		Short: "Get workflow metadata from a trigger (no side effects)",
		Long: `Get the metadata of a workflow trigger without running it.

` + triggerArgHelp + `

Prints {"trigger_id", "type", "name", "description", "shortcut_url"?,
"workflow": {"id", "title", "description", "app_id", "app_name"},
"collaborators"}. Use "workflow get" for the form fields and steps.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			trigger, err := workflows.ParseTrigger(args[0])
			if err != nil {
				return err
			}
			ws := rt.EffectiveWorkspace(workspace)
			ctx := cmd.Context()
			payload, err := clictx.WithAutoRefresh(ctx, rt, func() (workflows.Preview, error) {
				r, err := rt.Client(ctx, ws)
				if err != nil {
					return workflows.Preview{}, err
				}
				return workflows.PreviewTrigger(ctx, r.Client, trigger.ID)
			})
			if err != nil {
				return err
			}
			return rt.Print(payload)
		},
	}
	clictx.AddWorkspaceFlag(cmd, &workspace)
	return cmd
}

func newGet(rt *clictx.Context) *cobra.Command {
	var workspace string
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Get a workflow definition: its form fields and steps (no side effects)",
		Long: `Get a workflow's definition: the fields of the form shown when it starts,
and the titles of its steps. Read it before "workflow run" to know what the
workflow will do.

<id> is a workflow ID (Wf…), or a trigger ID (Ft…) or shortcut URL
(https://slack.com/shortcuts/Ft…/…), which is first resolved to its
workflow.

Prints {"workflow_id", "title", "description", "form_title"?, "fields":
[{"name", "title", "type", "description", "required", "long"?}], "steps"}.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("the workflow ID is empty: pass a workflow ID (Wf…), a trigger ID (Ft…) or a shortcut URL")
			}
			ws := rt.EffectiveWorkspace(workspace)
			ctx := cmd.Context()
			payload, err := clictx.WithAutoRefresh(ctx, rt, func() (workflows.Schema, error) {
				r, err := rt.Client(ctx, ws)
				if err != nil {
					return workflows.Schema{}, err
				}
				return workflows.GetSchema(ctx, r.Client, args[0])
			})
			if err != nil {
				return err
			}
			return rt.Print(payload)
		},
	}
	clictx.AddWorkspaceFlag(cmd, &workspace)
	return cmd
}

func newRun(rt *clictx.Context) *cobra.Command {
	var workspace, channel string
	cmd := &cobra.Command{
		Use:   "run <trigger>",
		Short: "Run a workflow by tripping its trigger (WRITE ACTION with side effects)",
		Long: `Run a workflow by tripping its trigger, as if you had clicked it in the
channel.

This is a write action, and it is not idempotent: every call starts the
workflow again, under your name. A workflow can post messages, notify
people, create or change records in other systems, or anything else its
steps do, and a run cannot be undone or cancelled from here. Only run a
workflow when explicitly asked to, and look at what it does first with
"workflow get". It is blocked in safe mode.

` + triggerArgHelp + `
--channel is the channel the workflow is run from: with a trigger ID, the
shortcut URL is looked up in that channel's bookmarks, then in the
trigger's metadata. Form inputs cannot be supplied from the command line.

Prints {"function_execution_id", "trigger_execution_id",
"is_slow_workflow"}: Slack accepted the trigger, which does not say
whether the workflow's steps then succeeded.`,
		Example: `  workflow list "#helpdesk"
  workflow get Ft0123ABCDEF
  workflow run Ft0123ABCDEF --channel "#helpdesk"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rt.CheckSafeMode("workflow run"); err != nil {
				return err
			}
			trigger, err := workflows.ParseTrigger(args[0])
			if err != nil {
				return err
			}
			from, err := cmdutil.ParseChannel(rt, channel, workspace)
			if err != nil {
				return fmt.Errorf("--channel: %w", err)
			}
			ctx := cmd.Context()
			// A retry with refreshed credentials only happens when Slack
			// rejected the credentials, i.e. when nothing was started: one
			// command never trips the trigger twice.
			result, err := cmdutil.WithTarget(ctx, rt, from, func(loc cmdutil.Located) (workflows.RunResult, error) {
				shortcutURL := trigger.URL
				if shortcutURL == "" {
					var err error
					if shortcutURL, err = workflows.ResolveShortcutURL(ctx, loc.Client, loc.ChannelID, trigger.ID); err != nil {
						return workflows.RunResult{}, err
					}
				}
				return workflows.Run(ctx, loc.Client, workflows.RunInput{ShortcutURL: shortcutURL, ChannelID: loc.ChannelID, TriggerID: trigger.ID})
			})
			if err != nil {
				return err
			}
			return rt.Print(result)
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "", "Channel ID or name (#channel, channel, C…) to run the workflow from; normally the channel where it is bookmarked or featured (required)")
	if err := cmd.MarkFlagRequired("channel"); err != nil {
		panic(err) // the flag is declared on the line above
	}
	clictx.AddWorkspaceFlag(cmd, &workspace)
	return cmd
}
