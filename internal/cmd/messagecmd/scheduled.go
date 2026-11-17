package messagecmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/scheduled"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
)

type scheduledListOptions struct {
	workspace string
	channel   string
	oldest    string
	latest    string
	cursor    string
	limit     string
}

// cancelResult is the output of "scheduled cancel".
type cancelResult struct {
	OK                 bool   `json:"ok"`
	ChannelID          string `json:"channel_id"`
	ScheduledMessageID string `json:"scheduled_message_id"`
}

const scheduledChannelHelp = "a channel (#name, name or ID), a user ID (its DM) or a Slack URL of the conversation"

func addScheduledListFlags(cmd *cobra.Command, o *scheduledListOptions) {
	f := cmd.Flags()
	f.StringVar(&o.workspace, "workspace", "", workspaceUsage)
	f.StringVar(&o.channel, "channel", "", "Only messages scheduled in this conversation: "+scheduledChannelHelp)
	f.StringVar(&o.oldest, "oldest", "", "Only messages scheduled to post after this Unix timestamp (seconds)")
	f.StringVar(&o.latest, "latest", "", "Only messages scheduled to post before this Unix timestamp (seconds)")
	f.StringVar(&o.cursor, "cursor", "", `Fetch the next page: the "next_cursor" of the previous output`)
	f.StringVar(&o.limit, "limit", "", "Max scheduled messages per page (positive integer; default: Slack's)")
}

func (c *command) newScheduled() *cobra.Command {
	var opts scheduledListOptions
	cmd := &cobra.Command{
		Use:   "scheduled",
		Short: "List or cancel pending scheduled messages",
		Long: fmt.Sprintf(`List or cancel the messages you scheduled with "%[1]s message send --schedule/--schedule-in".

"%[1]s message scheduled" is short for "%[1]s message scheduled list".`, appinfo.Name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return c.runScheduledList(cmd, opts) },
	}
	addScheduledListFlags(cmd, &opts)
	cmd.AddCommand(c.newScheduledList(), c.newScheduledCancel())
	return cmd
}

func (c *command) newScheduledList() *cobra.Command {
	var opts scheduledListOptions
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List pending scheduled messages",
		Long: `List pending scheduled messages.

Prints {ok, scheduled_messages: [{id, channel_id, post_at, date_created, text}],
next_cursor}. "id" and "channel_id" are what "scheduled cancel" needs; pass
"next_cursor", when present, as --cursor to get the next page.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return c.runScheduledList(cmd, opts) },
	}
	addScheduledListFlags(cmd, &opts)
	return cmd
}

func (c *command) runScheduledList(cmd *cobra.Command, opts scheduledListOptions) error {
	ctx := cmd.Context()
	limit, err := scheduled.ParseLimit(opts.limit)
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("limit") && limit == 0 {
		return errors.New("invalid --limit value \"\": must be a positive integer")
	}
	listOpts := scheduled.ListOptions{
		Cursor: strings.TrimSpace(opts.cursor),
		Oldest: opts.oldest,
		Latest: opts.latest,
		Limit:  limit,
	}

	var result scheduled.ListResult
	if strings.TrimSpace(opts.channel) == "" {
		if cmd.Flags().Changed("channel") {
			return errors.New("--channel needs a value: " + scheduledChannelHelp)
		}
		result, err = clictx.WithAutoRefresh(ctx, c.rt, func() (scheduled.ListResult, error) {
			r, err := c.rt.Client(ctx, c.rt.EffectiveWorkspace(opts.workspace))
			if err != nil {
				return scheduled.ListResult{}, err
			}
			return scheduled.List(ctx, r.Client, listOpts)
		})
	} else {
		var target slackurl.Target
		if target, err = slackurl.ParseTarget(opts.channel); err != nil {
			return fmt.Errorf("--channel: %w", err)
		}
		result, err = withTarget(ctx, c, target, opts.workspace, func(loc located) (scheduled.ListResult, error) {
			listOpts := listOpts
			listOpts.ChannelID = loc.channelID
			return scheduled.List(ctx, loc.Client, listOpts)
		})
	}
	if err != nil {
		return err
	}
	return c.rt.Print(result)
}

func (c *command) newScheduledCancel() *cobra.Command {
	var workspace, channel string
	cmd := &cobra.Command{
		Use:   "cancel <id>",
		Short: "Cancel a pending scheduled message",
		Long: `Cancel a pending scheduled message, so that it is never sent.

<id> is the scheduled_message_id printed by "message send --schedule" (or the
"id" printed by "message scheduled list"). Slack refuses to cancel a message
due within the next 60 seconds.

Prints {ok, channel_id, scheduled_message_id}. Not covered by safe mode.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			id := strings.TrimSpace(args[0])
			if id == "" {
				return errors.New("<id> is empty: pass the scheduled_message_id of the message to cancel")
			}
			target, err := slackurl.ParseTarget(channel)
			if err != nil {
				return fmt.Errorf("--channel: %w", err)
			}
			result, err := withTarget(ctx, c, target, workspace, func(loc located) (cancelResult, error) {
				if err := scheduled.Cancel(ctx, loc.Client, loc.channelID, id); err != nil {
					return cancelResult{}, err
				}
				return cancelResult{OK: true, ChannelID: loc.channelID, ScheduledMessageID: id}, nil
			})
			if err != nil {
				return err
			}
			return c.rt.Print(result)
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "", "Required: the conversation the message was scheduled in ("+scheduledChannelHelp+`); it is the "channel_id" printed when scheduling`)
	cmd.Flags().StringVar(&workspace, "workspace", "", workspaceUsage)
	// MarkFlagRequired only fails for a flag name that does not exist.
	_ = cmd.MarkFlagRequired("channel")
	return cmd
}
