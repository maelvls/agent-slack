// Package composecmd implements "message compose": a local, browser-based
// editor in which a human reviews, edits and sends a message.
package composecmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/channels"
	"github.com/maelvls/slackcli/internal/slack/messages"
	"github.com/maelvls/slackcli/internal/slack/slacktext"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
	"github.com/maelvls/slackcli/internal/slackapi"
	"github.com/maelvls/slackcli/internal/slackid"
)

// Options are the settings of a compose session.
type Options struct {
	// Workspace is the effective --workspace selector ("" for default).
	Workspace string
	// ThreadTS, when set, makes the message a reply in that thread.
	ThreadTS string
	// NoUnfurl suppresses link and media previews.
	NoUnfurl bool
}

// Compose opens the editor for target (message URL, #channel, channel ID,
// or user ID for a DM) pre-filled with initialText and blocks until the
// human sends or cancels. The returned value is the JSON payload to print:
//
//	{ok, sent, channel_id, ts, thread_ts, permalink, text}  the human sent `text`
//	{ok, cancelled, reason}                                 nothing was sent
//
// It is also what safe mode redirects "message send" to; callers should
// have disabled the command timeout (Compose does so again, defensively).
//
// The editor runs on a loopback HTTP server that only obeys the page it
// served to the user's browser (see session). The message is posted with
// chat.postMessage, formatted exactly like "message send" formats its
// text; it is posted at most once.
//
// When the CI environment variable is set (to anything but "", "0" or
// "false") there is no browser to open, and what Compose does depends on
// safe mode:
//
//   - safe mode off: the editor is skipped and initialText is sent as is
//     (an error if it is blank); the payload is {ok, sent, editor:
//     "skipped", channel_id, ts, thread_ts, permalink}. This is the
//     documented behaviour of "message compose", and gives nothing that
//     "message send" does not;
//   - safe mode on (rt.SafeMode()): Compose fails before contacting Slack.
//     Under safe mode nothing is ever sent without a human pressing Send
//     in the editor, whatever the environment says.
//
// Likewise, when no browser can be opened and stderr is not a terminal,
// Compose fails rather than print the editor's link where an agent could
// read it and press Send itself.
func Compose(ctx context.Context, rt *clictx.Context, target, initialText string, opts Options) (map[string]any, error) {
	return newComposer(rt).compose(ctx, target, initialText, opts)
}

// New returns the "compose" command, to be added under "message".
func New(rt *clictx.Context) *cobra.Command {
	return newCommand(newComposer(rt))
}

func newCommand(c *composer) *cobra.Command {
	var opts Options
	cmd := &cobra.Command{
		Use:   "compose <target> [text]",
		Short: "Open a send-capable rich editor in the browser; CI skips the editor and immediately sends supplied text",
		Long: "Open a rich editor in the user's browser, pre-filled with [text], and wait until a human sends the " +
			"message from there or cancels. Nothing is posted unless they press Send; what they sent (they may " +
			"have edited the text) is printed as {ok, sent, channel_id, ts, thread_ts, permalink, text}. If they " +
			"cancel, close the tab, or never open the editor (30 minutes), the result is {ok, cancelled, reason}.\n\n" +
			"<target> is a Slack message URL (replies in that message's thread), #name/name, a channel id, or a " +
			"user id (DM).\n" +
			"[text] is the initial message text (mrkdwn); it is sent immediately when CI skips the editor. " +
			`Text that starts with "-" must come after "--".` + "\n\n" +
			"The command does not time out: it waits for the human. Stop it with Ctrl-C.\n\n" +
			"CI: when the CI environment variable is set there is no browser, so the editor is skipped and [text] " +
			"is sent immediately — except in safe mode, where the command fails instead: safe mode never sends " +
			"without a human.\n" +
			"No browser: the command fails, unless it runs in an interactive terminal, where it prints a one-time " +
			"link to open by hand.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := ""
			if len(args) > 1 {
				text = args[1]
			}
			out, err := c.compose(cmd.Context(), args[0], text, opts)
			if err != nil {
				return err
			}
			return c.rt.Print(out)
		},
	}
	cmd.Flags().StringVar(&opts.Workspace, "workspace", "", "Workspace selector (full URL or unique substring; needed when using #channel/channel id across multiple workspaces)")
	cmd.Flags().StringVar(&opts.ThreadTS, "thread-ts", "", "Thread root ts to post into; overrides the URL-derived thread when supplied")
	cmd.Flags().BoolVar(&opts.NoUnfurl, "no-unfurl", false, "Suppress link and media previews")
	return clictx.NoTimeout(cmd)
}

// composer is Compose with its environment made explicit, for tests.
type composer struct {
	rt *clictx.Context
	// openBrowser opens the editor URL in the user's browser.
	openBrowser func(ctx context.Context, url string) error
	// humanAtStderr reports whether stderr is an interactive terminal,
	// i.e. whether what is printed there is read by a human rather than
	// captured by a program.
	humanAtStderr func() bool
	timeouts      timeouts
}

func newComposer(rt *clictx.Context) *composer {
	return &composer{
		rt:            rt,
		openBrowser:   openBrowser,
		humanAtStderr: func() bool { return isTerminal(rt.Stderr) },
		timeouts:      defaultTimeouts,
	}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// inCI reports whether the CI environment variable says this is an
// automated run.
func inCI() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CI"))) {
	case "", "0", "false":
		return false
	}
	return true
}

func (c *composer) compose(ctx context.Context, targetInput, initialText string, opts Options) (map[string]any, error) {
	rt := c.rt
	target, err := slackurl.ParseTarget(targetInput)
	if err != nil {
		return nil, err
	}
	if opts.ThreadTS != "" && !slackurl.IsTS(opts.ThreadTS) {
		return nil, fmt.Errorf("invalid thread ts %q: expected a Slack message ts such as 1770165109.628379", opts.ThreadTS)
	}
	ci := inCI()
	if ci {
		// The one rule that must hold whatever the caller checked: safe
		// mode never sends without a human.
		if rt.SafeMode() {
			return nil, fmt.Errorf("safe mode is active and the compose editor is unavailable in CI (the CI environment variable is set): refusing to send without human review. Unset CI to review the message in the editor, or disable safe mode (--safe-mode / %s) to send", appinfo.EnvName("SAFE_MODE"))
		}
		if strings.TrimSpace(initialText) == "" {
			return nil, errors.New("in CI the compose editor is skipped and the text is sent as is: pass the message text (the CI environment variable is set)")
		}
	}

	workspace := target.WorkspaceURL
	if workspace == "" {
		workspace = rt.EffectiveWorkspace(opts.Workspace)
		if target.Kind == slackurl.TargetChannel {
			if err := rt.AssertWorkspaceForChannelNames(workspace, target.Channel); err != nil {
				return nil, err
			}
		}
	}
	if target.Kind == slackurl.TargetURL {
		if hint := slackurl.TruncatedURLHint(target.Ref); hint != "" {
			rt.Hintf("%s", hint)
		}
	}

	dest, err := clictx.WithAutoRefresh(ctx, rt, func() (destination, error) {
		r, err := rt.Client(ctx, workspace)
		if err != nil {
			return destination{}, err
		}
		dest, err := resolveDestination(ctx, r.Client, target, opts.ThreadTS, !ci)
		if err != nil {
			return destination{}, err
		}
		// Pin the workspace: the message goes where the editor says.
		dest.workspace, dest.workspaceURL = cmp.Or(r.WorkspaceURL, workspace), r.WorkspaceURL
		return dest, nil
	})
	if err != nil {
		return nil, err
	}
	send := func(ctx context.Context, text string) (sentMessage, error) {
		return c.send(ctx, dest, text, opts.NoUnfurl)
	}

	if ci {
		sent, err := send(ctx, initialText)
		if err != nil {
			return nil, err
		}
		out := sentPayload(sent)
		out["editor"] = "skipped"
		return out, nil
	}

	rt.DisableTimeout()
	sess, err := startSession(ctx, c.page(dest, initialText), send, c.timeouts)
	if err != nil {
		return nil, err
	}
	if err := c.openBrowser(ctx, sess.url()); err != nil {
		if !c.humanAtStderr() {
			sess.interrupt()
			sess.stop()
			// The link lets whoever holds it press Send. Here it would
			// go to a pipe or a log, which an agent reads: fail closed.
			return nil, fmt.Errorf("could not open a browser for the compose editor (%w), and the editor's link is not printed because stderr is not a terminal: a human must review the message. Run `%s message compose` yourself in an interactive terminal (it prints the link there), on a machine with a browser", err, appinfo.Name)
		}
		rt.Hintf("Could not open a browser (%v).\nOpen this one-time link yourself, in a browser on this machine (over SSH, forward the port first):\n  %s", err, sess.url())
	}
	rt.Hintf("Compose editor for %s is open in the browser (%s). Waiting for a human to send or cancel there; Ctrl-C aborts.",
		dest.label(), sess.origin())

	out, err := sess.wait(ctx)
	if err != nil {
		return nil, err
	}
	if out.Sent == nil {
		if out.Reason != endCancelled {
			rt.Hintf("Nothing was sent: %s.", out.Reason)
		}
		return map[string]any{"ok": true, "cancelled": true, "reason": string(out.Reason)}, nil
	}
	payload := sentPayload(*out.Sent)
	payload["text"] = out.Text
	return payload, nil
}

func sentPayload(sent sentMessage) map[string]any {
	return map[string]any{
		"ok":         true,
		"sent":       true,
		"channel_id": sent.ChannelID,
		"ts":         sent.TS,
		"thread_ts":  sent.ThreadTS,
		"permalink":  sent.Permalink,
	}
}

// destination is where the composed message goes.
type destination struct {
	// workspace is the selector to get a client with; workspaceURL is the
	// workspace origin ("" when unknown: standard token, no workspace).
	workspace    string
	workspaceURL string
	channelID    string
	// channelName is for display only.
	channelName string
	threadTS    string
}

func (d destination) isDM() bool { return strings.HasPrefix(d.channelID, "D") }

func (d destination) label() string {
	if d.isDM() {
		return "the DM with " + d.channelName
	}
	return "#" + d.channelName
}

// resolveDestination turns a target into a channel and thread. A message
// URL replies in that message's thread, unless threadTS (--thread-ts)
// says otherwise; a user ID is the DM with that user. withName also looks
// up the display name.
func resolveDestination(ctx context.Context, client *slackapi.Client, target slackurl.Target, threadTS string, withName bool) (destination, error) {
	dest := destination{threadTS: threadTS}
	var err error
	switch target.Kind {
	case slackurl.TargetURL:
		dest.channelID = target.Ref.ChannelID
		if threadTS == "" {
			msg, err := messages.FetchMessage(ctx, client, target.Ref, false)
			if err != nil {
				return destination{}, err
			}
			dest.threadTS = cmp.Or(msg.ThreadTS, msg.TS)
		}
	case slackurl.TargetUser:
		if dest.channelID, err = channels.OpenDM(ctx, client, target.UserID); err != nil {
			return destination{}, err
		}
	default:
		if dest.channelID, err = channels.ResolveID(ctx, client, target.Channel); err != nil {
			return destination{}, err
		}
		if ch := slackid.ParseChannelInput(target.Channel); !ch.IsID {
			dest.channelName = ch.Value
		}
	}
	if withName && dest.channelName == "" {
		dest.channelName = channels.ResolveName(ctx, client, dest.channelID)
	}
	return dest, nil
}

// page describes the session to the editor page.
func (c *composer) page(dest destination, initialText string) pageConfig {
	page := pageConfig{
		AppName:       appinfo.Name,
		ChannelName:   dest.channelName,
		IsDM:          dest.isDM(),
		WorkspaceName: workspaceName(dest.workspaceURL),
		ThreadTS:      dest.threadTS,
		InitialText:   initialText,
	}
	if dest.threadTS != "" && dest.workspaceURL != "" {
		page.ThreadURL = slackurl.BuildMessageURL(dest.workspaceURL, dest.channelID, dest.threadTS, "")
	}
	return page
}

// workspaceName is the short name of a workspace URL: "acme" for
// https://acme.slack.com, the host for anything else.
func workspaceName(workspaceURL string) string {
	u, err := url.Parse(workspaceURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := u.Hostname()
	if name, ok := strings.CutSuffix(host, ".slack.com"); ok && name != "" {
		return name
	}
	return host
}

// send posts text to dest with chat.postMessage, formatted the way
// "message send" formats its text: mentions and special characters are
// encoded, and lists are sent as rich text so that Slack renders them.
// The client is obtained anew: credentials may have expired while the
// human was reviewing.
func (c *composer) send(ctx context.Context, dest destination, text string, noUnfurl bool) (sentMessage, error) {
	params := map[string]any{
		"channel": dest.channelID,
		"text":    slacktext.FormatOutboundText(text),
	}
	if dest.threadTS != "" {
		params["thread_ts"] = dest.threadTS
	}
	if blocks := slacktext.TextToRichTextBlocks(text); blocks != nil {
		params["blocks"] = blocks
	}
	if noUnfurl {
		params["unfurl_links"] = false
		params["unfurl_media"] = false
	}
	return clictx.WithAutoRefresh(ctx, c.rt, func() (sentMessage, error) {
		r, err := c.rt.Client(ctx, dest.workspace)
		if err != nil {
			return sentMessage{}, err
		}
		resp, err := r.Client.API(ctx, "chat.postMessage", params)
		if err != nil {
			return sentMessage{}, err
		}
		sent := sentMessage{
			ChannelID: cmp.Or(jsonx.Str(resp["channel"]), dest.channelID),
			TS:        jsonx.Str(resp["ts"]),
			ThreadTS:  dest.threadTS,
		}
		if workspaceURL := cmp.Or(r.WorkspaceURL, dest.workspaceURL); workspaceURL != "" && sent.TS != "" {
			sent.Permalink = slackurl.BuildMessageURL(workspaceURL, sent.ChannelID, sent.TS, dest.threadTS)
		}
		return sent, nil
	})
}
