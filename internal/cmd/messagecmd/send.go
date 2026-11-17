package messagecmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/messages"
	"github.com/maelvls/slackcli/internal/slack/scheduled"
	"github.com/maelvls/slackcli/internal/slack/slacktext"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
	"github.com/maelvls/slackcli/internal/slack/upload"
	"github.com/maelvls/slackcli/internal/slackapi"
)

type sendOptions struct {
	workspace      string
	threadTS       string
	replyBroadcast bool
	attach         []string
	blocks         string
	schedule       string
	scheduleIn     string
	noUnfurl       bool
}

// sendResult is the output of "send": ts and permalink for a posted
// message, scheduled_message_id and post_at for a scheduled one, files for
// an upload (Slack does not say which message carries them).
type sendResult struct {
	OK                 bool          `json:"ok"`
	ChannelID          string        `json:"channel_id"`
	TS                 string        `json:"ts"`
	ScheduledMessageID string        `json:"scheduled_message_id"`
	PostAt             int64         `json:"post_at,omitempty"`
	ThreadTS           string        `json:"thread_ts"`
	Permalink          string        `json:"permalink"`
	Files              []upload.File `json:"files"`
}

func (c *command) newSend() *cobra.Command {
	var opts sendOptions
	cmd := &cobra.Command{
		Use:   "send <target> [text] [flags]",
		Short: "Send or schedule a message (optionally into a thread)",
		Long: fmt.Sprintf(`Send or schedule a message (optionally into a thread).

<target> is one of:
  a Slack message URL       reply in the thread of that message
  #name, name, channel ID   post to that channel (reply with --thread-ts)
  a user ID (U…/W…)         post to the direct message with that user

[text] is Slack mrkdwn: *bold*, _italic_, `+"`code`"+`, <https://example.com|label>,
<@U123> or @U123 mentions, @here/@channel. Bullet and numbered lists are sent
as real Slack lists. It is required unless --attach or --blocks is given.

Prints {ok, channel_id, ts, thread_ts, permalink}; with --schedule/--schedule-in
{ok, channel_id, scheduled_message_id, post_at, thread_ts}; with --attach
{ok, channel_id, thread_ts, files}.

In safe mode (--safe-mode or %[1]s=1) nothing is posted: the compose editor
opens with the text for a human to review and send. --attach, --blocks,
--schedule, --schedule-in and --reply-broadcast are refused in safe mode, and
so is running in CI, where no editor can open.`, appinfo.EnvName("SAFE_MODE")),
		// Flags are parsed by parseArgsWithDashText, so that [text] may
		// start with "- ".
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, raw []string) error {
			args, ok, err := parseArgsWithDashText(cmd, raw)
			if !ok {
				return err
			}
			if err := cobra.RangeArgs(1, 2)(cmd, args); err != nil {
				return err
			}
			text := ""
			if len(args) == 2 {
				text = args[1]
			}
			return c.runSend(cmd, args[0], text, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.workspace, "workspace", "", workspaceUsage)
	f.StringVar(&opts.threadTS, "thread-ts", "", "Thread root ts to reply to, for channel targets and user-ID targets; refused with a message URL target, which already says which thread to reply in")
	f.BoolVar(&opts.replyBroadcast, "reply-broadcast", false, "Also show this thread reply in the channel (requires --thread-ts for channel targets; a message URL target supplies the thread; not supported for user-ID/DM targets; cannot be combined with --attach)")
	f.StringArrayVar(&opts.attach, "attach", nil, "Attach a local file (repeatable; all files go in one message); [text] becomes the files' comment, sent as plain mrkdwn without automatic list conversion. Cannot be combined with --blocks, --schedule, --schedule-in, --reply-broadcast or --no-unfurl")
	f.StringVar(&opts.blocks, "blocks", "", blocksUsage+". Cannot be combined with --attach")
	f.StringVar(&opts.schedule, "schedule", "", "Send later, at "+scheduled.AtHelp+"; within 120 days. Cannot be combined with --schedule-in or --attach")
	f.StringVar(&opts.scheduleIn, "schedule-in", "", "Send later, after "+scheduled.InHelp+" of this process; within 120 days. Cannot be combined with --schedule or --attach")
	f.BoolVar(&opts.noUnfurl, "no-unfurl", false, "Suppress link and media previews. Cannot be combined with --attach")
	return cmd
}

// normalizeAttachPaths trims and de-duplicates --attach values, keeping
// their order.
func normalizeAttachPaths(raw []string) []string {
	var out []string
	for _, p := range raw {
		if p = strings.TrimSpace(p); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

func (c *command) runSend(cmd *cobra.Command, targetInput, text string, opts sendOptions) error {
	ctx := cmd.Context()
	changed := cmd.Flags().Changed

	target, err := slackurl.ParseTarget(targetInput)
	if err != nil {
		return err
	}
	attach := normalizeAttachPaths(opts.attach)
	hasAttach, hasBlocks := len(attach) > 0, changed("blocks")
	hasSchedule, hasScheduleIn := changed("schedule"), changed("schedule-in")

	// Everything that can be decided from the command line is checked
	// before anything is read, resolved or sent.
	switch {
	case changed("attach") && !hasAttach:
		return errors.New("--attach needs a file path, got an empty value")
	case hasBlocks && strings.TrimSpace(opts.blocks) == "":
		return errors.New(`--blocks needs a file path (or "-" for stdin), got an empty value`)
	case hasSchedule && strings.TrimSpace(opts.schedule) == "":
		return errors.New("--schedule needs a time, got an empty value: pass " + scheduled.AtHelp)
	case hasScheduleIn && strings.TrimSpace(opts.scheduleIn) == "":
		return errors.New("--schedule-in needs a duration or a day, got an empty value: pass " + scheduled.InHelp)
	case strings.TrimSpace(text) == "" && !hasAttach && !hasBlocks:
		return errors.New("<text> is required when no --attach files or --blocks are provided")
	}
	threadTS, err := parseTS("--thread-ts", opts.threadTS)
	if err != nil {
		return err
	}

	if c.rt.SafeMode() {
		payload, err := c.redirectSendToCompose(ctx, targetInput, text, opts, redirectFlags{
			attach: hasAttach, blocks: hasBlocks, schedule: hasSchedule, scheduleIn: hasScheduleIn,
		})
		if err != nil {
			return err
		}
		return c.rt.Print(payload)
	}

	switch {
	case hasBlocks && hasAttach:
		return errors.New("--blocks cannot be combined with --attach (Slack file uploads only take a plain-text comment)")
	case opts.replyBroadcast && hasAttach:
		return errors.New("--reply-broadcast cannot be combined with --attach (Slack file uploads do not support thread broadcasts)")
	case hasSchedule && hasScheduleIn:
		return errors.New("--schedule and --schedule-in are mutually exclusive")
	case (hasSchedule || hasScheduleIn) && hasAttach:
		return errors.New("--schedule/--schedule-in cannot be combined with --attach (Slack scheduled messages do not support file uploads)")
	case opts.noUnfurl && hasAttach:
		return errors.New("--no-unfurl cannot be combined with --attach (Slack file uploads do not accept unfurl parameters)")
	}
	switch target.Kind {
	case slackurl.TargetURL:
		if threadTS != "" {
			return errors.New("--thread-ts cannot be combined with a message URL target: the reply goes to the thread of that message. Drop --thread-ts, or pass the channel instead of the URL")
		}
	case slackurl.TargetUser:
		if opts.replyBroadcast {
			return errors.New("--reply-broadcast is not supported for DM targets")
		}
	default:
		if opts.replyBroadcast && threadTS == "" {
			return errors.New("--reply-broadcast requires --thread-ts for channel targets")
		}
	}

	msg := outgoing{
		text:           slacktext.FormatOutboundText(text),
		threadTS:       threadTS,
		replyBroadcast: opts.replyBroadcast,
		attach:         attach,
		noUnfurl:       opts.noUnfurl,
	}
	msg.postAt, _, err = scheduled.ResolvePostAt(opts.schedule, opts.scheduleIn, c.now(), nil)
	if err != nil {
		return err
	}
	if hasAttach {
		// A typo in a path must fail before a channel is resolved or a DM
		// opened.
		if err := upload.Check(attach); err != nil {
			return err
		}
	}
	if hasBlocks {
		if msg.blocks, err = loadBlocks(cmd.InOrStdin(), opts.blocks); err != nil {
			return err
		}
	} else if text != "" {
		msg.blocks = slacktext.TextToRichTextBlocks(text)
	}

	result, err := withTarget(ctx, c, target, opts.workspace, func(loc located) (sendResult, error) {
		msg := msg
		if target.Kind == slackurl.TargetURL {
			// Reply in the thread the linked message belongs to (or start
			// one under it).
			ref := target.Ref
			ref.WorkspaceURL = loc.WorkspaceURL
			linked, err := messages.FetchMessage(ctx, loc.Client, ref, false)
			if err != nil {
				return sendResult{}, err
			}
			msg.threadTS = cmp.Or(linked.ThreadTS, linked.TS)
		}
		return c.sendToChannel(ctx, loc, msg)
	})
	if err != nil {
		return err
	}
	return c.rt.Print(result)
}

// outgoing is a message ready to be sent to a resolved conversation.
type outgoing struct {
	// text is escaped mrkdwn (slacktext.FormatOutboundText).
	text string
	// blocks is nil when the text alone is enough.
	blocks         []any
	threadTS       string
	replyBroadcast bool
	attach         []string
	// postAt, when non-zero, schedules the message (Unix seconds).
	postAt   int64
	noUnfurl bool
}

// chatParams are the parameters chat.postMessage and chat.scheduleMessage
// share.
func (m outgoing) chatParams(channelID string) map[string]any {
	params := map[string]any{"channel": channelID, "text": m.text}
	if m.threadTS != "" {
		params["thread_ts"] = m.threadTS
		if m.replyBroadcast {
			params["reply_broadcast"] = true
		}
	}
	if m.blocks != nil {
		params["blocks"] = m.blocks
	}
	if m.noUnfurl {
		params["unfurl_links"], params["unfurl_media"] = false, false
	}
	return params
}

func (c *command) sendToChannel(ctx context.Context, loc located, m outgoing) (sendResult, error) {
	result := sendResult{OK: true, ChannelID: loc.channelID, ThreadTS: m.threadTS}

	switch {
	case m.postAt != 0:
		params := m.chatParams(loc.channelID)
		params["post_at"] = m.postAt
		resp, err := loc.Client.API(ctx, "chat.scheduleMessage", params)
		if err != nil {
			return sendResult{}, explainSendError(err, loc.channelID)
		}
		result.ChannelID = cmp.Or(jsonx.Str(resp["channel"]), loc.channelID)
		result.ScheduledMessageID = jsonx.Str(resp["scheduled_message_id"])
		result.PostAt = cmp.Or(responseInt(resp["post_at"]), m.postAt)
		if result.ScheduledMessageID == "" {
			c.rt.Hintf(`Warning: Slack accepted the scheduled message but returned no scheduled_message_id; find it with "%s message scheduled list".`, appinfo.Name)
		}

	case len(m.attach) == 0:
		resp, err := loc.Client.API(ctx, "chat.postMessage", m.chatParams(loc.channelID))
		if err != nil {
			return sendResult{}, explainSendError(err, loc.channelID)
		}
		result.ChannelID = cmp.Or(jsonx.Str(resp["channel"]), loc.channelID)
		result.TS = jsonx.Str(resp["ts"])
		if loc.WorkspaceURL != "" && result.TS != "" {
			result.Permalink = slackurl.BuildMessageURL(loc.WorkspaceURL, result.ChannelID, result.TS, m.threadTS)
		}

	default:
		if m.blocks != nil {
			c.rt.Hintf("Warning: rich text formatting is not supported with file attachments; sending as plain text.")
		}
		uploaded, err := upload.ToChannel(ctx, loc.Client, upload.Destination{
			ChannelID:      loc.channelID,
			ThreadTS:       m.threadTS,
			InitialComment: m.text,
		}, m.attach)
		if err != nil {
			return sendResult{}, explainSendError(err, loc.channelID)
		}
		result.Files = uploaded
	}
	return result, nil
}

// responseInt reads an integer Slack may send as a number or as a string.
func responseInt(v any) int64 {
	if n, ok := jsonx.Num(v); ok {
		return int64(n)
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(jsonx.Str(v)), 10, 64)
	return n
}

// explainSendError adds what to do about the Slack errors an agent is
// most likely to hit when posting.
func explainSendError(err error, channelID string) error {
	switch {
	case slackapi.IsErrorCode(err, "not_in_channel"):
		return fmt.Errorf("you are not a member of %s: join the channel first: %w", channelID, err)
	case slackapi.IsErrorCode(err, "is_archived"):
		return fmt.Errorf("channel %s is archived and cannot be posted to: %w", channelID, err)
	case slackapi.IsErrorCode(err, "thread_not_found"):
		return fmt.Errorf("the thread to reply to does not exist in %s: check --thread-ts (it must be the ts of a message of that channel): %w", channelID, err)
	case slackapi.IsErrorCode(err, "msg_too_long"):
		return fmt.Errorf("the message is too long for Slack (40,000 characters at most): split it, or send the content as a file with --attach: %w", err)
	case slackapi.IsErrorCode(err, "invalid_blocks", "invalid_blocks_format"):
		return fmt.Errorf("Slack rejected the blocks: check them against the Block Kit reference: %w", err)
	}
	return err
}
