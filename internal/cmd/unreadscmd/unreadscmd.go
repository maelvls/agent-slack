// Package unreadscmd implements the "unreads" command.
package unreadscmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/unreads"
)

// New returns the "unreads" command.
func New(rt *clictx.Context) *cobra.Command {
	var (
		workspace                 string
		maxMessages, maxBodyChars int
		countsOnly, includeSystem bool
	)
	cmd := &cobra.Command{
		Use:   "unreads",
		Short: "Show all unread messages across channels, DMs, and threads",
		Long: `Show every conversation with unread messages, with the unread messages.

Prints JSON:
  channels  conversations with unreads, most mentions first, each with
            channel_id, channel_name, channel_type (channel, dm or mpim),
            mention_count, unread_count and messages (oldest first: ts,
            author, content as Markdown, thread_ts, reply_count).
            unread_count is the number of unread messages fetched, system
            messages excluded; it is a lower bound when
            messages_truncated is true (more unread messages than
            --max-messages: the newest are shown), and absent with
            --counts-only, since Slack only says that there are unreads.
            messages_error is set when the messages of a conversation
            could not be read.
  threads   {has_unreads, mention_count}, present when threads have unread
            replies (the replies themselves are not listed).

Nothing is marked as read. This command uses Slack's internal client.counts
API: it needs browser session credentials (xoxc- token and "d" cookie), not
a bot or user token.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if maxMessages < 1 || maxMessages > unreads.MaxMessagesLimit {
				return fmt.Errorf("invalid --max-messages value %d: must be between 1 and %d (use --counts-only to fetch no messages)", maxMessages, unreads.MaxMessagesLimit)
			}
			if maxBodyChars < -1 || maxBodyChars == 0 {
				return fmt.Errorf("invalid --max-body-chars value %d: must be a positive integer, or -1 for unlimited", maxBodyChars)
			}
			ctx := cmd.Context()
			ws := rt.EffectiveWorkspace(workspace)
			payload, err := clictx.WithAutoRefresh(ctx, rt, func() (unreads.Result, error) {
				r, err := rt.Client(ctx, ws)
				if err != nil {
					return unreads.Result{}, err
				}
				return unreads.Fetch(ctx, r.Client, unreads.Options{
					CountsOnly:    countsOnly,
					MaxMessages:   maxMessages,
					MaxBodyChars:  maxBodyChars,
					IncludeSystem: includeSystem,
				})
			})
			if err != nil {
				return err
			}
			return rt.Print(payload)
		},
	}
	clictx.AddWorkspaceFlag(cmd, &workspace)
	cmd.Flags().BoolVar(&countsOnly, "counts-only", false, "Only list the conversations with unreads and their mention counts; do not fetch message content")
	cmd.Flags().IntVar(&maxMessages, "max-messages", unreads.DefaultMaxMessages, fmt.Sprintf("Max unread messages to fetch per conversation, newest first (1-%d)", unreads.MaxMessagesLimit))
	cmd.Flags().IntVar(&maxBodyChars, "max-body-chars", unreads.DefaultMaxBodyChars, "Max content characters per message (-1 for unlimited)")
	cmd.Flags().BoolVar(&includeSystem, "include-system", false, "Include system messages (joins, leaves, topic changes, etc.)")
	return cmd
}
