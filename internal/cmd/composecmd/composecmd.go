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

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/cmd/cmdutil"
	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/channels"
	"github.com/maelvls/slackcli/internal/slack/slacktext"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
	"github.com/maelvls/slackcli/internal/slackid"
)

// Options are the settings of a compose session.
type Options struct {
	// Workspace is the value of the --workspace flag ("" if not given).
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
// Likewise, when no browser can be opened, Compose fails rather than
// print the editor's link where an agent could read it and press Send
// itself: always under safe mode, and otherwise unless stderr is a real
// terminal.
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
		Short: "Open a rich editor in the browser for a human to review and send a message",
		Long: `Open a rich editor in the user's browser, pre-filled with [text], and wait
until a human sends the message from there or cancels. Nothing is posted
unless they press Send.

<target> is one of:
  a Slack message URL       reply in the thread of that message
  #name, name, channel ID   post to that channel (reply with --thread-ts)
  a user ID (U…/W…)         post to the direct message with that user

[text] is the initial message text (Slack mrkdwn); the human may edit it.

Prints {ok, sent, channel_id, ts, thread_ts, permalink, text}, where text is
what the human sent. If they cancel, close the tab, or never open the editor
(30 minutes), prints {ok, cancelled, reason}.

The command does not time out: it waits for the human. Stop it with Ctrl-C.

CI: when the CI environment variable is set there is no browser, so the
editor is skipped and [text] is sent immediately; the output then has
editor: "skipped" and no text. In safe mode the command fails instead: safe
mode never sends without a human.

No browser: when no browser can be opened the command fails, unless it runs
in an interactive terminal, where it prints a one-time link to open by hand.
In safe mode it always fails and the link is never printed: a human must run
the command themselves in a graphical session.`,
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
	cmdutil.AddTargetWorkspaceFlag(cmd, &opts.Workspace)
	cmd.Flags().StringVar(&opts.ThreadTS, "thread-ts", "", "Thread root ts to reply to, for channel targets and user-ID targets; redundant with a message URL target, which already says which thread to reply in and which it must not contradict")
	cmd.Flags().BoolVar(&opts.NoUnfurl, "no-unfurl", false, "Suppress link and media previews")
	// [text] may start with "- ".
	return clictx.NoTimeout(cmdutil.AllowDashText(cmd))
}

// composer is Compose with its environment made explicit, for tests.
type composer struct {
	rt *clictx.Context
	// openBrowser opens the editor URL in the user's browser.
	openBrowser func(ctx context.Context, url string) error
	// humanAtStderr reports whether stderr is an interactive terminal:
	// what is printed there is then most likely read by a human rather
	// than captured by a program. Most likely only: an agent may run the
	// command under a pseudo-terminal, which is why safe mode does not
	// rely on it.
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

// isTerminal reports whether w is a terminal. Being a character device
// is not enough: /dev/null is one.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

func (c *composer) compose(ctx context.Context, targetInput, initialText string, opts Options) (map[string]any, error) {
	rt := c.rt
	target, err := cmdutil.ParseTarget(rt, targetInput, opts.Workspace)
	if err != nil {
		return nil, err
	}
	threadTS, err := cmdutil.ParseTS("--thread-ts", opts.ThreadTS)
	if err != nil {
		return nil, err
	}
	ci := clictx.IsCI()
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

	dest, err := cmdutil.WithTarget(ctx, rt, target, func(loc cmdutil.Located) (destination, error) {
		dest := destination{
			// Pin the workspace: the message goes where the editor says.
			workspace:    cmp.Or(loc.WorkspaceURL, target.Workspace),
			workspaceURL: loc.WorkspaceURL,
			channelID:    loc.ChannelID,
		}
		// A message URL replies in that message's thread, which
		// --thread-ts may repeat but not contradict.
		var err error
		if dest.threadTS, err = target.ThreadTS(ctx, loc.Client, threadTS); err != nil {
			return destination{}, err
		}
		// The editor shows where the message goes.
		if target.Kind == slackurl.TargetChannel {
			if ch := slackid.ParseChannelInput(target.Channel); !ch.IsID {
				dest.channelName = ch.Value
			}
		}
		if !ci && dest.channelName == "" {
			dest.channelName = channels.ResolveName(ctx, loc.Client, dest.channelID)
		}
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
		// The link lets whoever holds it press Send: fail closed rather
		// than hand it to an agent.
		switch {
		case rt.SafeMode():
			// Even on a terminal, which may be a pseudo-terminal an
			// agent runs the command under.
			sess.interrupt()
			sess.stop()
			return nil, fmt.Errorf("safe mode is active and no browser could be opened for the compose editor (%w): nothing was sent, and the editor's link is never printed in safe mode, because whoever holds it can send the message. A human must run `%s message compose <target> [text]` themselves, in a graphical session on a machine with a browser", err, appinfo.Name)
		case !c.humanAtStderr():
			// The link would go to a pipe, a file or a log.
			sess.interrupt()
			sess.stop()
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
