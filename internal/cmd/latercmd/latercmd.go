// Package latercmd implements the "later" command.
package latercmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/cmd/cmdutil"
	"github.com/maelvls/slackcli/internal/slack/later"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// New returns the "later" command.
func New(rt *clictx.Context) *cobra.Command { return newCommand(rt, time.Now, nil) }

// newCommand takes the clock and the local timezone (nil: time.Local)
// that `later remind --in` is evaluated against.
func newCommand(rt *clictx.Context, now func() time.Time, loc *time.Location) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "later",
		Short: "Manage saved-for-later messages (Slack's Later tab)",
		Long: fmt.Sprintf(`Manage saved-for-later messages (Slack's Later tab).

Without a sub-command, lists the saved messages ("%[1]s later" is
"%[1]s later list" and takes the same flags).

A saved message is in one of three states: in_progress, completed or
archived. "save" adds a message (in_progress); "complete" and "archive"
move it; "reopen" moves it back to in_progress; "remove" takes it out of
Later entirely; "remind" sets its reminder.

These commands use Slack's internal saved.* API: they need browser session
credentials (xoxc- token and "d" cookie), not a bot or user token.`, appinfo.Name),
		Args: cobra.NoArgs,
	}
	list := newListCommand(rt)
	// `later` alone is `later list`, flags included.
	cmd.RunE = list.RunE
	cmd.Flags().AddFlagSet(list.Flags())

	cmd.AddCommand(
		list,
		newActionCommand(rt, "complete", "Mark a saved message as completed",
			func(ctx context.Context, c *slackapi.Client, channelID, ts string) error {
				return later.Mark(ctx, c, channelID, ts, later.MarkCompleted)
			}),
		newActionCommand(rt, "archive", "Archive a saved message",
			func(ctx context.Context, c *slackapi.Client, channelID, ts string) error {
				return later.Mark(ctx, c, channelID, ts, later.MarkArchived)
			}),
		newActionCommand(rt, "reopen", "Move a completed or archived saved message back to in-progress", later.Reopen),
		newActionCommand(rt, "save", "Save a message for later", later.Save),
		newActionCommand(rt, "remove", "Remove a message from Later entirely", later.Remove),
		newRemindCommand(rt, now, loc),
	)
	return cmd
}

func newListCommand(rt *clictx.Context) *cobra.Command {
	var (
		workspace, state, cursor string
		limit, maxBodyChars      int
		countsOnly               bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved-for-later messages",
		Long: `List saved-for-later messages, in Slack's order (most recently saved first).

Prints JSON:
  counts       saved items per state (in_progress, archived, completed, total),
               as Slack counts them
  items        the saved messages in the requested state, each with
               channel_id, channel_name, ts, state, date_saved,
               date_completed, date_due (the reminder; Unix seconds) and
               message (author, content as Markdown, thread_ts, reply_count).
               When the message itself cannot be read (deleted, channel no
               longer visible), message is absent and message_error says why.
               [] when there are none; absent with --counts-only.
  next_cursor  present when there may be more items: pass it to --cursor`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := later.ParseState(state)
			if err != nil {
				return err
			}
			if limit < 1 {
				return fmt.Errorf("invalid --limit value %d: must be a positive integer", limit)
			}
			if err := cmdutil.CheckMaxChars("--max-body-chars", maxBodyChars); err != nil {
				return err
			}
			ctx := cmd.Context()
			ws := rt.EffectiveWorkspace(workspace)
			payload, err := clictx.WithAutoRefresh(ctx, rt, func() (later.ListResult, error) {
				r, err := rt.Client(ctx, ws)
				if err != nil {
					return later.ListResult{}, err
				}
				return later.List(ctx, r.Client, later.ListOptions{
					State:        st,
					Limit:        limit,
					MaxBodyChars: maxBodyChars,
					CountsOnly:   countsOnly,
					Cursor:       cursor,
				})
			})
			if err != nil {
				return err
			}
			for _, w := range payload.Warnings {
				rt.Hintf("Warning: %s", w)
			}
			if countsOnly {
				return rt.Print(payload)
			}
			return cmdutil.PrintList(rt, payload, "items")
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", clictx.WorkspaceFlagUsage)
	cmd.Flags().StringVar(&state, "state", string(later.StateInProgress), "Filter by state: in_progress, archived, completed, or all")
	cmd.Flags().IntVar(&limit, "limit", later.DefaultLimit, "Max items to show; when there are more, the output has a next_cursor")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Continue a listing: the next_cursor value of the previous page (keep the same --state)")
	cmd.Flags().IntVar(&maxBodyChars, "max-body-chars", later.DefaultMaxBodyChars, "Max content characters per message (-1 for unlimited)")
	cmd.Flags().BoolVar(&countsOnly, "counts-only", false, "Only show counts per state: no items, no message content")
	return cmd
}

const targetArgHelp = `<target> is the saved message: a Slack message URL, or a channel (#name or
ID) together with --ts.`

// actionResult is printed by the commands that change a saved message.
type actionResult struct {
	OK bool `json:"ok"`
	// Action is the sub-command that ran.
	Action    string `json:"action"`
	ChannelID string `json:"channel_id"`
	TS        string `json:"ts"`
	// RemindAt (Unix seconds) and RemindAtLocal (RFC 3339) are set by
	// "remind".
	RemindAt      int64  `json:"remind_at,omitempty"`
	RemindAtLocal string `json:"remind_at_local,omitempty"`
}

func addTargetFlags(cmd *cobra.Command, workspace, ts *string) {
	cmdutil.AddTargetWorkspaceFlag(cmd, workspace)
	cmd.Flags().StringVar(ts, "ts", "", cmdutil.MessageTSUsage)
}

// newActionCommand builds a sub-command that applies do to one message.
func newActionCommand(rt *clictx.Context, name, short string, do func(ctx context.Context, c *slackapi.Client, channelID, ts string) error) *cobra.Command {
	var workspace, ts string
	cmd := &cobra.Command{
		Use:   name + " <target>",
		Short: short,
		Long: short + ".\n\n" + targetArgHelp + `

Prints {"ok": true, "action": "` + name + `", "channel_id": …, "ts": …} on success.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, ts, err := cmdutil.ParseMessageTarget(rt, cmd.CommandPath(), args[0], workspace, ts)
			if err != nil {
				return err
			}
			return runAction(cmd.Context(), rt, target, ts, actionResult{Action: name}, do)
		},
	}
	addTargetFlags(cmd, &workspace, &ts)
	return cmd
}

func newRemindCommand(rt *clictx.Context, now func() time.Time, loc *time.Location) *cobra.Command {
	var workspace, ts, in string
	cmd := &cobra.Command{
		Use:   "remind <target>",
		Short: "Set a reminder for a saved message",
		Long: `Set (or move) the reminder of a saved message.

` + targetArgHelp + `

Prints {"ok": true, "action": "remind", "channel_id": …, "ts": …,
"remind_at": <Unix seconds>, "remind_at_local": <RFC 3339>} on success.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, ts, err := cmdutil.ParseMessageTarget(rt, cmd.CommandPath(), args[0], workspace, ts)
			if err != nil {
				return err
			}
			if loc == nil {
				loc = time.Local
			}
			at, err := later.ParseReminderTime(in, now(), loc)
			if err != nil {
				return err
			}
			result := actionResult{
				Action:        "remind",
				RemindAt:      at,
				RemindAtLocal: time.Unix(at, 0).In(loc).Format(time.RFC3339),
			}
			return runAction(cmd.Context(), rt, target, ts, result,
				func(ctx context.Context, c *slackapi.Client, channelID, ts string) error {
					return later.SetReminder(ctx, c, channelID, ts, at)
				})
		},
	}
	cmd.Flags().StringVar(&in, "in", "", "When to remind (required): "+later.ReminderHelp)
	_ = cmd.MarkFlagRequired("in")
	addTargetFlags(cmd, &workspace, &ts)
	return cmd
}

// runAction resolves the target's channel, applies do to the message
// with that ts in it, and prints result.
func runAction(ctx context.Context, rt *clictx.Context, target cmdutil.Target, ts string, result actionResult, do func(ctx context.Context, c *slackapi.Client, channelID, ts string) error) error {
	channelID, err := cmdutil.WithTarget(ctx, rt, target, func(loc cmdutil.Located) (string, error) {
		return loc.ChannelID, do(ctx, loc.Client, loc.ChannelID, ts)
	})
	if err != nil {
		return err
	}
	result.OK, result.ChannelID, result.TS = true, channelID, ts
	return rt.Print(result)
}
