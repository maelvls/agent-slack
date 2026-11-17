package messagecmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/cmd/cmdutil"
	"github.com/maelvls/slackcli/internal/slack/slacktext"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// okResult is the output of commands that have nothing else to report.
type okResult struct {
	OK bool `json:"ok"`
}

const messageTargetLong = `<target> is a Slack message URL, or a channel (#name, name, channel ID or
channel URL) combined with --ts.`

func (c *command) newEdit() *cobra.Command {
	var workspace, tsFlag, blocksPath string
	cmd := &cobra.Command{
		Use:   "edit <target> <text> [flags]",
		Short: "Edit a message",
		Long: fmt.Sprintf(`Edit one of your own messages: <text> replaces its whole content.

%s

<text> is Slack mrkdwn, converted as by "send". With --blocks, pass "" as
<text> to send the blocks alone. Prints {"ok": true}.

Refused in safe mode (--safe-mode or %s=1).`, messageTargetLong, appinfo.EnvName("SAFE_MODE")),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.rt.CheckSafeMode("message edit"); err != nil {
				return err
			}
			ctx := cmd.Context()
			target, ts, err := cmdutil.ParseMessageTarget(c.rt, cmd.CommandPath(), args[0], workspace, tsFlag)
			if err != nil {
				return err
			}
			text := args[1]
			hasBlocks := cmd.Flags().Changed("blocks")
			switch {
			case hasBlocks && strings.TrimSpace(blocksPath) == "":
				return errors.New(`--blocks needs a file path (or "-" for stdin), got an empty value`)
			case strings.TrimSpace(text) == "" && !hasBlocks:
				return fmt.Errorf(`<text> is empty: pass the new content of the message (to remove a message, use "%s message delete")`, appinfo.Name)
			}
			params := map[string]any{"ts": ts, "text": slacktext.FormatOutboundText(text)}
			if hasBlocks {
				blocks, err := loadBlocks(cmd.InOrStdin(), blocksPath)
				if err != nil {
					return err
				}
				params["blocks"] = blocks
			} else if blocks := slacktext.TextToRichTextBlocks(text); blocks != nil {
				params["blocks"] = blocks
			}

			_, err = cmdutil.WithTarget(ctx, c.rt, target, func(loc cmdutil.Located) (struct{}, error) {
				params["channel"] = loc.ChannelID
				_, err := loc.Client.API(ctx, "chat.update", params)
				return struct{}{}, explainMutationError(err, "edit", loc.ChannelID, ts)
			})
			if err != nil {
				return err
			}
			return c.rt.Print(okResult{OK: true})
		},
	}
	cmdutil.AddTargetWorkspaceFlag(cmd, &workspace)
	cmd.Flags().StringVar(&tsFlag, "ts", "", cmdutil.MessageTSUsage)
	cmd.Flags().StringVar(&blocksPath, "blocks", "", blocksUsage)
	// <text> may start with "- ".
	return cmdutil.AllowDashText(cmd)
}

func (c *command) newDelete() *cobra.Command {
	var workspace, tsFlag string
	cmd := &cobra.Command{
		Use:   "delete <target>",
		Short: "Delete a message",
		Long: fmt.Sprintf(`Delete one of your own messages. This cannot be undone.

%s

Prints {"ok": true}. Refused in safe mode (--safe-mode or %s=1).`, messageTargetLong, appinfo.EnvName("SAFE_MODE")),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := c.rt.CheckSafeMode("message delete"); err != nil {
				return err
			}
			ctx := cmd.Context()
			target, ts, err := cmdutil.ParseMessageTarget(c.rt, cmd.CommandPath(), args[0], workspace, tsFlag)
			if err != nil {
				return err
			}
			_, err = cmdutil.WithTarget(ctx, c.rt, target, func(loc cmdutil.Located) (struct{}, error) {
				_, err := loc.Client.API(ctx, "chat.delete", map[string]any{"channel": loc.ChannelID, "ts": ts})
				return struct{}{}, explainMutationError(err, "delete", loc.ChannelID, ts)
			})
			if err != nil {
				return err
			}
			return c.rt.Print(okResult{OK: true})
		},
	}
	cmdutil.AddTargetWorkspaceFlag(cmd, &workspace)
	cmd.Flags().StringVar(&tsFlag, "ts", "", cmdutil.MessageTSUsage)
	return cmd
}

// explainMutationError words the refusals of chat.update / chat.delete.
// action is "edit" or "delete".
func explainMutationError(err error, action, channelID, ts string) error {
	switch {
	case err == nil:
		return nil
	case slackapi.IsErrorCode(err, "cant_update_message", "cant_delete_message"):
		return fmt.Errorf("Slack refused to %[1]s message %[2]s in %[3]s: you can only %[1]s your own messages (and a workspace setting may forbid even that): %[4]w", action, ts, channelID, err)
	case slackapi.IsErrorCode(err, "edit_window_closed"):
		return fmt.Errorf("message %s in %s can no longer be edited: the workspace's editing window has passed: %w", ts, channelID, err)
	case slackapi.IsErrorCode(err, "message_not_found"):
		return fmt.Errorf("no message with ts %s in %s: it may already be deleted, or the ts or channel is wrong: %w", ts, channelID, err)
	}
	return fmt.Errorf("%s of message %s in %s failed: %w", action, ts, channelID, err)
}

func (c *command) newReact() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "react",
		Short: "Add or remove reactions",
		Long: `Add or remove your reactions on a message.

Reactions are not covered by safe mode: they are applied immediately.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		c.newReactAction("add", "reactions.add", "Add a reaction to a message", "Emoji to react with (:rocket:, rocket, or 🚀)"),
		c.newReactAction("remove", "reactions.remove", "Remove your reaction from a message", "Emoji to remove (:rocket:, rocket, or 🚀)"),
	)
	return cmd
}

func (c *command) newReactAction(action, method, short, emojiHelp string) *cobra.Command {
	var workspace, tsFlag string
	cmd := &cobra.Command{
		Use:   action + " <target> <emoji>",
		Short: short,
		Long: fmt.Sprintf(`%s.

%s

<emoji>: %s; custom workspace emoji by name. Write the "-1"
emoji as :-1: (a bare -1 is read as a flag).

Prints {"ok": true}. Not covered by safe mode.`, short, messageTargetLong, emojiHelp),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			target, ts, err := cmdutil.ParseMessageTarget(c.rt, cmd.CommandPath(), args[0], workspace, tsFlag)
			if err != nil {
				return err
			}
			name, err := slacktext.NormalizeReactionName(args[1])
			if err != nil {
				return err
			}
			_, err = cmdutil.WithTarget(ctx, c.rt, target, func(loc cmdutil.Located) (struct{}, error) {
				_, err := loc.Client.API(ctx, method, map[string]any{
					"channel":   loc.ChannelID,
					"timestamp": ts,
					"name":      name,
				})
				switch {
				case slackapi.IsErrorCode(err, "already_reacted"):
					err = fmt.Errorf("you already reacted with :%s: to message %s: %w", name, ts, err)
				case slackapi.IsErrorCode(err, "no_reaction"):
					err = fmt.Errorf("you have no :%s: reaction on message %s to remove: %w", name, ts, err)
				case slackapi.IsErrorCode(err, "invalid_name"):
					err = fmt.Errorf("Slack does not know the emoji :%s: (custom emoji must exist in this workspace): %w", name, err)
				}
				return struct{}{}, err
			})
			if err != nil {
				return err
			}
			return c.rt.Print(okResult{OK: true})
		},
	}
	cmdutil.AddTargetWorkspaceFlag(cmd, &workspace)
	cmd.Flags().StringVar(&tsFlag, "ts", "", cmdutil.MessageTSUsage)
	return cmd
}
