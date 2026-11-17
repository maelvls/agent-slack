package messagecmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/cmd/cmdutil"
	"github.com/maelvls/slackcli/internal/slack/messages"
	"github.com/maelvls/slackcli/internal/slack/slacktext"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
	"github.com/maelvls/slackcli/internal/slack/users"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// readOptions are the flags "get" and "list" share.
type readOptions struct {
	workspace        string
	ts               string
	threadTS         string
	maxBodyChars     int
	includeReactions bool
	resolveUsers     bool
	refreshUsers     bool
}

type listOptions struct {
	readOptions
	limit           int
	oldest          string
	latest          string
	withReaction    []string
	withoutReaction []string
}

// getResult is the output of "get".
type getResult struct {
	Message         messages.CompactMessage      `json:"message"`
	Thread          *threadSummary               `json:"thread"`
	ReferencedUsers map[string]users.CompactUser `json:"referenced_users"`
}

// listResult is the output of "list". ChannelID is only set in channel
// history mode.
type listResult struct {
	ChannelID       string                       `json:"channel_id"`
	Messages        []messages.CompactMessage    `json:"messages"`
	ReferencedUsers map[string]users.CompactUser `json:"referenced_users"`
}

func addReadFlags(cmd *cobra.Command, o *readOptions) {
	f := cmd.Flags()
	f.StringVar(&o.workspace, "workspace", "", cmdutil.TargetWorkspaceUsage)
	f.IntVar(&o.maxBodyChars, "max-body-chars", messages.DefaultMaxBodyChars, "Max characters of each message's content to include; longer content is cut (-1 for unlimited)")
	f.BoolVar(&o.includeReactions, "include-reactions", false, "Include reactions and the IDs of the users who reacted")
	f.BoolVar(&o.resolveUsers, "resolve-users", false, `Add "referenced_users": the profiles of the user IDs appearing in the output (cached for 24h)`)
	f.BoolVar(&o.refreshUsers, "refresh-users", false, "Like --resolve-users, but fetch the profiles again instead of using the cache")
}

func addGetFlags(cmd *cobra.Command, o *readOptions) {
	addReadFlags(cmd, o)
	cmd.Flags().StringVar(&o.ts, "ts", "", cmdutil.MessageTSUsage)
	cmd.Flags().StringVar(&o.threadTS, "thread-ts", "", "Thread root ts: where to look when the message is a thread reply (a permalink's ?thread_ts= already says so)")
}

func (c *command) newGet() *cobra.Command {
	var opts readOptions
	cmd := &cobra.Command{
		Use:   "get <target>",
		Short: "Fetch a single Slack message (with thread summary if any)",
		Long: `Fetch a single Slack message (with thread summary if any).

<target> is a Slack message URL, or a channel (#name, name, channel ID or
channel URL) combined with --ts.

Prints {message, thread, referenced_users}. "thread" ({ts, length}) is present
when the message belongs to a thread: use "list" on the same target to read
it. Attached files are downloaded and listed with their local path; a file
that cannot be downloaded is listed with an "error" instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.runGet(cmd, args[0], opts)
		},
	}
	addGetFlags(cmd, &opts)
	return cmd
}

func (c *command) runGet(cmd *cobra.Command, targetInput string, opts readOptions) error {
	ctx := cmd.Context()
	// "message <target>" is "message get <target>".
	target, ts, err := cmdutil.ParseMessageTarget(c.rt, appinfo.Name+" message get", targetInput, opts.workspace, opts.ts)
	if err != nil {
		return err
	}
	threadTS, err := cmdutil.ParseTS("--thread-ts", opts.threadTS)
	if err != nil {
		return err
	}
	// 0 would mean "the default" to messages.CompactOptions.
	if err := cmdutil.CheckMaxChars("--max-body-chars", opts.maxBodyChars); err != nil {
		return err
	}

	result, err := cmdutil.WithTarget(ctx, c.rt, target, func(loc cmdutil.Located) (getResult, error) {
		ref := target.Ref // zero unless the target is a message URL
		ref.WorkspaceURL, ref.ChannelID, ref.MessageTS, ref.Raw = loc.WorkspaceURL, loc.ChannelID, ts, targetInput
		if ref.ThreadTSHint == "" {
			ref.ThreadTSHint = threadTS
		}
		msg, err := messages.FetchMessage(ctx, loc.Client, ref, opts.includeReactions)
		if err != nil {
			return getResult{}, err
		}
		thread, err := getThreadSummary(ctx, loc.Client, loc.ChannelID, msg)
		if err != nil {
			return getResult{}, err
		}
		msgs := []messages.Message{msg}
		compact, referenced, err := c.present(ctx, loc, msgs, opts)
		if err != nil {
			return getResult{}, err
		}
		return getResult{Message: compact[0], Thread: thread, ReferencedUsers: referenced}, nil
	})
	if err != nil {
		return err
	}
	return c.rt.Print(result)
}

// present downloads the files of msgs, converts them to the printed shape
// and resolves the users they reference when asked to.
func (c *command) present(ctx context.Context, loc cmdutil.Located, msgs []messages.Message, opts readOptions) ([]messages.CompactMessage, map[string]users.CompactUser, error) {
	downloads, err := c.downloadMessageFiles(ctx, loc.Client, msgs)
	if err != nil {
		return nil, nil, err
	}
	compact := make([]messages.CompactMessage, len(msgs))
	for i, m := range msgs {
		compact[i] = messages.ToCompact(m, messages.CompactOptions{
			MaxBodyChars:     opts.maxBodyChars,
			IncludeReactions: opts.includeReactions,
			Downloads:        downloads,
		})
	}
	if !opts.resolveUsers && !opts.refreshUsers {
		return compact, nil, nil
	}
	ids := users.CollectReferencedUserIDs(msgs, opts.includeReactions)
	byID, err := users.ResolveByID(ctx, loc.Client, ids, users.ResolveOptions{
		WorkspaceURL: loc.WorkspaceURL,
		ForceRefresh: opts.refreshUsers,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("resolving users: %w", err)
	}
	return compact, users.ToReferencedUsers(ids, byID), nil
}

func (c *command) newList() *cobra.Command {
	var opts listOptions
	cmd := &cobra.Command{
		Use:   "list <target>",
		Short: "List recent channel messages, or fetch a full thread",
		Long: fmt.Sprintf(`List recent channel messages, or fetch a full thread.

  list <channel>                   the most recent top-level messages of the
                                   channel, oldest first (--limit, --oldest,
                                   --latest, --with-reaction, --without-reaction)
  list <message-url>               the whole thread that message belongs to
  list <channel> --ts <ts>         same, for the message with that ts
  list <channel> --thread-ts <ts>  the whole thread with that root

<channel> is #name, name, a channel ID or a channel URL. Channel history does
not include thread replies: a message with "thread_ts" has a thread, list it
to read the replies. At most %d messages are returned per call; to page back,
pass the ts of the oldest message received as --latest.`, messages.MaxHistoryLimit),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.runList(cmd, args[0], opts)
		},
	}
	addReadFlags(cmd, &opts.readOptions)
	f := cmd.Flags()
	f.StringVar(&opts.threadTS, "thread-ts", "", "Thread root ts: list that thread (root and replies) instead of the channel history")
	f.StringVar(&opts.ts, "ts", "", "Message ts: list the thread that message belongs to")
	f.IntVar(&opts.limit, "limit", messages.DefaultHistoryLimit, fmt.Sprintf("Max messages to return (channel history mode; 1 to %d)", messages.MaxHistoryLimit))
	f.StringVar(&opts.oldest, "oldest", "", "Only messages after this ts or Unix time, exclusive (channel history mode)")
	f.StringVar(&opts.latest, "latest", "", "Only messages before this ts or Unix time, exclusive (channel history mode)")
	f.StringArrayVar(&opts.withReaction, "with-reaction", nil, "Only messages carrying this reaction (:eyes:, eyes or 👀; repeatable, all must be present; channel history mode; requires --oldest)")
	f.StringArrayVar(&opts.withoutReaction, "without-reaction", nil, "Only messages not carrying this reaction (repeatable; channel history mode; requires --oldest)")
	return cmd
}

// historyOnlyFlags only make sense when listing a channel's history.
var historyOnlyFlags = []string{"limit", "oldest", "latest", "with-reaction", "without-reaction"}

// parseReactionFilters normalises repeatable reaction flags to bare
// reaction names, without duplicates.
func parseReactionFilters(flag string, raw []string) ([]string, error) {
	var out []string
	for _, v := range raw {
		name, err := slacktext.NormalizeReactionName(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", flag, err)
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out, nil
}

// requireOldestForReactionFilters refuses reaction filters without
// --oldest: they are applied client-side, so an unbounded scan would walk
// the whole channel.
func requireOldestForReactionFilters(oldest string, hasFilters bool) error {
	if hasFilters && oldest == "" {
		return errors.New(`reaction filters require --oldest "<seconds>.<micros>" to bound scan size. Example: --oldest "1770165109.628379"`)
	}
	return nil
}

func (c *command) runList(cmd *cobra.Command, targetInput string, opts listOptions) error {
	ctx := cmd.Context()
	target, err := cmdutil.ParseTarget(c.rt, targetInput, opts.workspace)
	if err != nil {
		return err
	}
	if err := target.NoUser(cmd.CommandPath()); err != nil {
		return err
	}
	if err := cmdutil.CheckMaxChars("--max-body-chars", opts.maxBodyChars); err != nil {
		return err
	}
	threadTS, err := cmdutil.ParseTS("--thread-ts", opts.threadTS)
	if err != nil {
		return err
	}

	// Which thread, if any? FetchThread accepts the ts of a reply too, so
	// the message itself need not be fetched first.
	rootTS := threadTS
	if target.Kind == slackurl.TargetURL {
		if _, err := target.MessageTS(opts.ts); err != nil {
			return err
		}
		if rootTS == "" {
			rootTS = cmp.Or(target.Ref.ThreadTSHint, target.Ref.MessageTS)
		}
	} else if rootTS == "" {
		if rootTS, err = cmdutil.ParseTS("--ts", opts.ts); err != nil {
			return err
		}
	}

	if rootTS != "" {
		for _, name := range historyOnlyFlags {
			if cmd.Flags().Changed(name) {
				return fmt.Errorf("--%s only applies to channel history, but this lists a thread (message URL, --ts or --thread-ts): drop --%[1]s, or pass only the channel", name)
			}
		}
		return c.listThread(ctx, target, rootTS, opts.readOptions)
	}
	return c.listHistory(ctx, target, opts)
}

func (c *command) listThread(ctx context.Context, target cmdutil.Target, rootTS string, opts readOptions) error {
	result, err := cmdutil.WithTarget(ctx, c.rt, target, func(loc cmdutil.Located) (listResult, error) {
		msgs, err := messages.FetchThread(ctx, loc.Client, loc.ChannelID, rootTS, opts.includeReactions)
		if slackapi.IsErrorCode(err, "thread_not_found") {
			return listResult{}, fmt.Errorf("no message with ts %s in channel %s: it may have been deleted, or the ts or URL is wrong or truncated (quote URLs containing \"&\"): %w", rootTS, loc.ChannelID, err)
		}
		if err != nil {
			return listResult{}, err
		}
		compact, referenced, err := c.present(ctx, loc, msgs, opts)
		if err != nil {
			return listResult{}, err
		}
		// Every message of a thread shares channel and root: not repeated.
		for i := range compact {
			compact[i].ChannelID, compact[i].ThreadTS = "", ""
		}
		return listResult{Messages: compact, ReferencedUsers: referenced}, nil
	})
	if err != nil {
		return err
	}
	return cmdutil.PrintList(c.rt, result, "messages")
}

func (c *command) listHistory(ctx context.Context, target cmdutil.Target, opts listOptions) error {
	if err := cmdutil.CheckLimit(opts.limit, messages.MaxHistoryLimit, "page further back with --latest"); err != nil {
		return err
	}
	oldest, err := parseHistoryBound("--oldest", opts.oldest)
	if err != nil {
		return err
	}
	latest, err := parseHistoryBound("--latest", opts.latest)
	if err != nil {
		return err
	}
	with, err := parseReactionFilters("--with-reaction", opts.withReaction)
	if err != nil {
		return err
	}
	without, err := parseReactionFilters("--without-reaction", opts.withoutReaction)
	if err != nil {
		return err
	}
	for _, name := range with {
		if slices.Contains(without, name) {
			return fmt.Errorf("reaction %q is in both --with-reaction and --without-reaction: no message can match", name)
		}
	}
	if err := requireOldestForReactionFilters(oldest, len(with)+len(without) > 0); err != nil {
		return err
	}

	result, err := cmdutil.WithTarget(ctx, c.rt, target, func(loc cmdutil.Located) (listResult, error) {
		msgs, err := messages.FetchChannelHistory(ctx, loc.Client, loc.ChannelID, messages.HistoryOptions{
			Limit:            opts.limit,
			Latest:           latest,
			Oldest:           oldest,
			IncludeReactions: opts.includeReactions,
			WithReactions:    with,
			WithoutReactions: without,
		})
		if err != nil {
			return listResult{}, err
		}
		compact, referenced, err := c.present(ctx, loc, msgs, opts.readOptions)
		if err != nil {
			return listResult{}, err
		}
		return listResult{ChannelID: loc.ChannelID, Messages: compact, ReferencedUsers: referenced}, nil
	})
	if err != nil {
		return err
	}
	if len(result.Messages) == 0 {
		c.rt.Hintf("No messages matched in %s.", describeRange(oldest, latest))
	}
	return cmdutil.PrintList(c.rt, result, "messages")
}

var historyBoundRe = regexp.MustCompile(`^\d+(\.\d+)?$`)

// parseHistoryBound validates --oldest / --latest: a message ts or a Unix
// time in seconds.
func parseHistoryBound(flag, raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v != "" && !historyBoundRe.MatchString(v) {
		return "", fmt.Errorf("invalid %s value %q: expected a Slack ts (1770165109.628379) or a Unix time in seconds", flag, raw)
	}
	return v, nil
}

// describeRange words the --oldest/--latest window for the "no messages"
// hint, which says why "messages" is empty.
func describeRange(oldest, latest string) string {
	var parts []string
	if oldest != "" {
		parts = append(parts, "after "+oldest)
	}
	if latest != "" {
		parts = append(parts, "before "+latest)
	}
	if len(parts) == 0 {
		return "the channel"
	}
	return "the channel " + strings.Join(parts, " and ")
}
