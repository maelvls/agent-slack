// Package messagecmd implements the "message" command: reading messages
// and threads, and sending, scheduling, editing, deleting and reacting.
package messagecmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/cmd/composecmd"
	"github.com/maelvls/slackcli/internal/cmd/draftcmd"
)

// composeFunc opens the human review editor (composecmd.Compose).
type composeFunc func(ctx context.Context, rt *clictx.Context, target, initialText string, opts composecmd.Options) (map[string]any, error)

// command holds what the subcommands share. Tests replace compose, now
// and downloadsDir.
type command struct {
	rt *clictx.Context
	// compose is what safe mode redirects "send" to.
	compose composeFunc
	now     func() time.Time
	// downloadsDir is where attachments are saved; "" means
	// appinfo.EnsureDownloadsDir().
	downloadsDir string
}

// New returns the "message" command.
func New(rt *clictx.Context) *cobra.Command {
	return newCommand(&command{rt: rt, compose: composecmd.Compose, now: time.Now})
}

const (
	workspaceUsage = "Workspace selector (full URL or unique substring). Needed for #channel/channel-ID/user-ID targets when several workspaces are configured; a Slack URL target that names its workspace takes precedence"
	tsUsage        = `Message ts, e.g. 1770165109.628379 (required when <target> is a channel; redundant with a message URL)`
)

func newCommand(c *command) *cobra.Command {
	var opts readOptions
	cmd := &cobra.Command{
		Use:   "message [<target>]",
		Short: "Read/write Slack messages (token-efficient JSON)",
		Long: fmt.Sprintf(`Read/write Slack messages (token-efficient JSON).

"%[1]s message <target>" is short for "%[1]s message get <target>".

Safe mode (--safe-mode or %[2]s=1) keeps a human in the loop: "send" opens
the compose editor instead of posting, and "edit" and "delete" are refused.
"react" and "scheduled cancel" are NOT covered by safe mode.`, appinfo.Name, appinfo.EnvName("SAFE_MODE")),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			// Without a subcommand this is "get": make sure a typo of a
			// subcommand is not taken for a channel name.
			if err := checkDefaultGet(cmd, args, opts.ts != ""); err != nil {
				return err
			}
			return c.runGet(cmd, args[0], opts)
		},
	}
	// SuggestionsFor needs it; cobra only defaults it on its own path.
	cmd.SuggestionsMinimumDistance = 2
	addGetFlags(cmd, &opts)

	cmd.AddCommand(
		c.newGet(),
		c.newList(),
		c.newSend(),
		c.newEdit(),
		c.newDelete(),
		c.newReact(),
		c.newScheduled(),
		composecmd.New(c.rt),
		draftcmd.New(c.rt),
	)
	return cmd
}

// checkDefaultGet decides whether "message <args>" can be the implicit
// "get": exactly one argument that is a URL, or a channel that comes with
// --ts or does not look like a mistyped subcommand.
func checkDefaultGet(cmd *cobra.Command, args []string, hasTS bool) error {
	first := args[0]
	isURL := strings.Contains(first, "://")
	if isURL && len(args) > 1 {
		return fmt.Errorf(`"%s" accepts exactly one <target>, got %d arguments`, cmd.CommandPath(), len(args))
	}
	suggestions := cmd.SuggestionsFor(first)
	if isURL || (len(args) == 1 && (hasTS || len(suggestions) == 0)) {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "unknown command %q for %q", first, cmd.CommandPath())
	if len(suggestions) > 0 {
		fmt.Fprintf(&b, " (did you mean %s?)", strings.Join(suggestions, ", "))
	}
	fmt.Fprintf(&b, `. Run "%[1]s --help" for the commands; to fetch a message, run "%[1]s <message-url>" or "%[1]s <channel> --ts <ts>"`, cmd.CommandPath())
	return errors.New(b.String())
}
