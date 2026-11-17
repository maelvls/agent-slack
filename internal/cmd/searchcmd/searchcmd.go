// Package searchcmd implements the "search" command.
package searchcmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/cmd/cmdutil"
	"github.com/maelvls/slackcli/internal/slack/search"
)

// flags are the options shared by the three subcommands.
type flags struct {
	workspace       string
	channels        []string
	user            string
	after           string
	before          string
	contentType     string
	limit           int
	maxContentChars int
	noDownload      bool
	resolveUsers    bool
	refreshUsers    bool
}

// New returns the "search" command.
func New(rt *clictx.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search Slack messages and files (token-efficient JSON)",
		Long: fmt.Sprintf(`Search Slack messages and files (token-efficient JSON).

Results are sorted newest first. Message results have channel_id, ts, author,
content (Markdown), files and permalink; file results have title, mimetype,
mode, path (the local download) and permalink. A file that could not be
downloaded keeps its entry, with "error" and a path to a file holding the
reason.

There are two ways of searching:

  Without --channel, <query> goes to Slack's search API and may use Slack's
  search operators (in:#channel, from:@user, has:link, "exact phrase", …).
  This needs a user token or browser credentials: bot tokens (xoxb-) cannot
  search.

  With --channel, the given channels are scanned and matched locally: every
  word of <query> (and every "quoted phrase") must appear, ignoring case.
  This works with any token and does not depend on Slack's index, but only
  sees top-level messages (not thread replies), matches files on their title
  and name only, and can be slow in busy channels: bound it with --after.

At most --limit results of each kind are returned (default %d, max %d). When
more match, the output has "more_messages" / "more_files" with a hint and,
when it helps, the --before date that continues the search with older results.`,
			search.DefaultLimit, search.MaxLimit),
	}
	cmd.AddCommand(
		newKind(rt, search.KindAll, "Search messages and files", "Both searches run concurrently; --limit applies to each."),
		newKind(rt, search.KindMessages, "Search messages", "Files attached to the messages found are downloaded unless --no-download is given."),
		newKind(rt, search.KindFiles, "Search files", "The files found are downloaded unless --no-download is given."),
	)
	return cmd
}

func newKind(rt *clictx.Context, kind search.Kind, short, detail string) *cobra.Command {
	var f flags
	cmd := &cobra.Command{
		Use:   string(kind) + " <query>",
		Short: short,
		Long: short + ". " + detail + `

<query> is a Slack search query, or with --channel words to look for in the
channels scanned; it may be "" when a filter (--user, --after, --before,
--channel) is given. See the help of the parent command for the output, the
two ways of searching and how to get more results than --limit.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), rt, kind, args[0], f)
		},
	}
	fl := cmd.Flags()
	clictx.AddWorkspaceFlag(cmd, &f.workspace)
	fl.StringSliceVar(&f.channels, "channel", nil,
		"Search only these channels (#name, name or ID); repeat the flag or separate channels with commas. "+
			"The channels are scanned and <query> is matched locally instead of using Slack's search API (search operators are then rejected)")
	fl.StringVar(&f.user, "user", "", "Only results posted by this user: user ID (U…/W…), @handle, handle, email address or display name")
	fl.StringVar(&f.after, "after", "",
		"Only results dated after this day, YYYY-MM-DD. Exclusive, like Slack's after: operator: --after 2026-01-01 starts on 2026-01-02. "+
			"Days are in the Slack account's time zone, or with --channel in the local time zone")
	fl.StringVar(&f.before, "before", "",
		"Only results dated before this day, YYYY-MM-DD. Exclusive, like Slack's before: operator: --before 2026-02-01 ends on 2026-01-31")
	fl.StringVar(&f.contentType, "content-type", string(search.ContentAny),
		"Keep only: any (everything), text (messages without files; text/plain files), image, snippet, file (messages with any file; every file)")
	fl.IntVar(&f.limit, "limit", search.DefaultLimit,
		fmt.Sprintf("Maximum number of results of each kind, 1 to %d. When more match, more_messages / more_files in the output say how to get them", search.MaxLimit))
	fl.IntVar(&f.maxContentChars, "max-content-chars", search.DefaultMaxContentChars,
		"Truncate the content of each message to this many characters (-1 for unlimited)")
	fl.BoolVar(&f.noDownload, "no-download", false,
		"Do not download files: results list them without a local path (file results keep their permalink)")
	fl.BoolVar(&f.resolveUsers, "resolve-users", false,
		"Add referenced_users: the profiles of the authors and of the users mentioned in the messages found, keyed by user ID")
	fl.BoolVar(&f.refreshUsers, "refresh-users", false,
		"Like --resolve-users, but fetch the profiles again instead of using the local user cache (implies --resolve-users)")
	return cmd
}

func run(ctx context.Context, rt *clictx.Context, kind search.Kind, query string, f flags) error {
	contentType, err := search.ParseContentType(f.contentType)
	if err != nil {
		return fmt.Errorf("--content-type: %w", err)
	}
	if err := cmdutil.CheckLimit(f.limit, search.MaxLimit, "to go further back, repeat the search with an earlier --before date"); err != nil {
		return err
	}
	if err := cmdutil.CheckMaxChars("--max-content-chars", f.maxContentChars); err != nil {
		return err
	}

	workspace := rt.EffectiveWorkspace(f.workspace)
	if err := rt.AssertWorkspaceForChannelNames(workspace, f.channels...); err != nil {
		return err
	}

	result, err := clictx.WithAutoRefresh(ctx, rt, func() (*search.Result, error) {
		r, err := rt.Client(ctx, workspace)
		if err != nil {
			return nil, err
		}
		return search.Search(ctx, r.Client, search.Options{
			Query:           query,
			Kind:            kind,
			Channels:        f.channels,
			User:            f.user,
			After:           f.after,
			Before:          f.before,
			ContentType:     contentType,
			Limit:           f.limit,
			MaxContentChars: f.maxContentChars,
			NoDownload:      f.noDownload,
			ResolveUsers:    f.resolveUsers || f.refreshUsers,
			RefreshUsers:    f.refreshUsers,
		})
	})
	if err != nil {
		return err
	}
	for _, w := range result.Warnings {
		rt.Hintf("Warning: %s", w)
	}
	return rt.Print(result)
}
