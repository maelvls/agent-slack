// Package draftcmd implements "message draft": Slack-native drafts, which
// appear in the user's own Slack client for them to review and send.
package draftcmd

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/cmd/cmdutil"
)

const (
	browserAuthNote = "Drafts need browser auth (xoxc token + xoxd cookie, e.g. from auth import-desktop): Slack refuses bot and user tokens here."
)

// New returns the "draft" command, to be added under "message".
func New(rt *clictx.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "draft",
		Short: "Manage Slack-native message drafts (appear in the user's Slack client; requires browser auth)",
		Long: "Manage Slack-native message drafts. A draft is not posted: it shows up in the user's Slack client " +
			"(Drafts & sent, and the composer of its channel), where they can edit and send it.\n\n" +
			browserAuthNote + "\n\n" +
			fmt.Sprintf("Note: the browser compose editor moved to `%s message compose <target> [text]`.", appinfo.Name),
		// `message draft <target> [text]` used to open the browser editor:
		// point old invocations at `message compose` rather than leave a
		// bare "unknown command".
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}
			return fmt.Errorf("%s is not a 'message draft' subcommand (expected: list, create, update, delete).\n"+
				"The browser compose editor moved to `message compose`:\n  %s message compose %s [text]",
				strconv.Quote(args[0]), appinfo.Name, strconv.Quote(args[0]))
		},
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newList(rt), newCreate(rt), newUpdate(rt), newDelete(rt))
	return cmd
}

func newList(rt *clictx.Context) *cobra.Command {
	var opts listOptions
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List unsent message drafts",
		Long: "List the user's unsent drafts: id, text, destinations (with channel names), last_updated_ts, " +
			"scheduled send time and attached file ids.\n\n" + browserAuthNote,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out, err := list(cmd.Context(), rt, opts)
			if err != nil {
				return err
			}
			return rt.Print(out)
		},
	}
	clictx.AddWorkspaceFlag(cmd, &opts.workspace)
	cmd.Flags().StringVar(&opts.limit, "limit", "", "Max drafts to return (positive integer; default: all that Slack returns)")
	cmd.Flags().BoolVar(&opts.all, "all", false, "Include sent/inactive drafts")
	return cmd
}

func newCreate(rt *clictx.Context) *cobra.Command {
	var opts createOptions
	cmd := &cobra.Command{
		Use:   "create <target> <text>",
		Short: "Create a Slack-native draft addressed to a channel, DM, or thread",
		Long: "Create a Slack-native draft addressed to a channel, DM, or thread. Nothing is posted: the draft waits " +
			"in the user's Slack client.\n\n" +
			"<target> is a Slack message URL (drafts a reply in that message's thread), #name/name, a channel id, " +
			"or a user id (drafts a DM).\n" +
			"<text> is the draft text in Slack mrkdwn (*bold*, _italic_, `code`, lists, > quotes, <url|label>).\n\n" +
			browserAuthNote,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := create(cmd.Context(), rt, args[0], args[1], opts)
			if err != nil {
				return err
			}
			return rt.Print(out)
		},
	}
	cmdutil.AddTargetWorkspaceFlag(cmd, &opts.workspace)
	cmd.Flags().StringVar(&opts.threadTS, "thread-ts", "", "Thread root ts to draft a reply into (optional; overrides the thread of a message URL target)")
	cmd.Flags().BoolVar(&opts.broadcast, "broadcast", false, "Also send the thread reply to the channel when posted (requires thread context; not for DMs)")
	cmd.Flags().StringArrayVar(&opts.attach, "attach", nil, "Attach a local file to the draft (repeatable)")
	// <text> may start with "- ".
	return cmdutil.AllowDashText(cmd)
}

func newUpdate(rt *clictx.Context) *cobra.Command {
	var (
		opts                   updateOptions
		broadcast, noBroadcast bool
	)
	cmd := &cobra.Command{
		Use:   "update <draft-id> <text>",
		Short: "Replace a draft's text (and optionally re-address it)",
		Long: "Replace a draft's text, keeping its destination, thread, broadcast flag and attached files unless " +
			"flags say otherwise.\n\n" +
			"<draft-id> is the id returned by draft list/create. <text> is the new draft text (mrkdwn); it replaces " +
			"the existing body.\n\n" +
			"--attach ADDS files to those already attached; it never removes any (remove attachments in the Slack " +
			"client). Changing the thread or the channel resets the broadcast flag to what the flags ask for.\n" +
			"Drafts with a scheduled send time, and (without --channel) drafts addressed to several destinations, " +
			"are refused: rewriting them would silently drop the schedule or the extra recipients.\n\n" + browserAuthNote,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case cmd.Flags().Changed("no-broadcast") && noBroadcast:
				off := false
				opts.broadcast = &off
			case cmd.Flags().Changed("broadcast"):
				opts.broadcast = &broadcast
			}
			out, err := update(cmd.Context(), rt, args[0], args[1], opts)
			if err != nil {
				return err
			}
			return rt.Print(out)
		},
	}
	cmdutil.AddTargetWorkspaceFlag(cmd, &opts.workspace)
	cmd.Flags().StringVar(&opts.channel, "channel", "", "Re-address the draft to a different channel (#name or id), DM (user id) or thread (message URL)")
	cmd.Flags().StringVar(&opts.threadTS, "thread-ts", "", "Thread root ts to draft a reply into (optional)")
	cmd.Flags().BoolVar(&broadcast, "broadcast", false, "Also send the thread reply to the channel when posted (requires thread context; not for DMs)")
	cmd.Flags().BoolVar(&noBroadcast, "no-broadcast", false, "Clear an inherited broadcast flag (keeps the thread)")
	cmd.MarkFlagsMutuallyExclusive("broadcast", "no-broadcast")
	cmd.Flags().StringArrayVar(&opts.attach, "attach", nil, "Attach one more local file to the draft, in addition to the files it already has (repeatable)")
	cmd.Flags().StringVar(&opts.lastUpdatedTS, "last-updated-ts", "",
		"The draft's last_updated_ts as you last read it: the update is refused if the draft changed since (default: the current value, i.e. no conflict detection)")
	// <text> may start with "- ".
	return cmdutil.AllowDashText(cmd)
}

func newDelete(rt *clictx.Context) *cobra.Command {
	var opts deleteOptions
	cmd := &cobra.Command{
		Use:   "delete <draft-id>",
		Short: "Delete a draft",
		Long:  "Delete a draft. <draft-id> is the id returned by draft list/create.\n\n" + browserAuthNote,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := remove(cmd.Context(), rt, args[0], opts)
			if err != nil {
				return err
			}
			return rt.Print(out)
		},
	}
	clictx.AddWorkspaceFlag(cmd, &opts.workspace)
	cmd.Flags().StringVar(&opts.lastUpdatedTS, "last-updated-ts", "",
		"The draft's last_updated_ts as you last read it, for Slack's conflict detection (default: looked up, i.e. delete whatever the draft now is)")
	return cmd
}
