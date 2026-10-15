import { Option, type Command } from "commander";
import type { CliContext } from "./context.ts";
import {
  deleteMessage,
  editMessage,
  handleMessageGet,
  handleMessageList,
  reactOnTarget,
  sendMessage,
  type MessageCommandOptions,
} from "./message-actions.ts";
import { composeMessage } from "./compose-actions.ts";
import { registerScheduledMessageCommand } from "./message-scheduled-command.ts";
import { registerMessageDraftCommand } from "./message-draft-command.ts";
import { isSafeModeEnabled, redirectSendToDraft, safeModeBlockedError } from "./safe-mode.ts";
import { addHiddenOptionAlias, collectOptionValue, pickPositionalOrFlag } from "./options.ts";
import { isSlackTs, parseMsgTarget, resolveTargetArgs } from "./targets.ts";

const CHANNEL_OPTION_DESCRIPTION =
  "Channel (#name, name, or channel ID); alternative to the positional <target>";

type MessageReadOptions = MessageCommandOptions & { channel?: string; thread?: boolean };

/**
 * Normalize `message get/list` arguments: the target may come from `--channel`,
 * and an optional second positional is the message ts (same as `--ts`).
 */
export function resolveMessageReadArgs(input: {
  positionals: (string | undefined)[];
  options: MessageReadOptions;
}): { targetInput: string; options: MessageReadOptions } {
  const {
    target,
    rest: [tsArg],
  } = resolveTargetArgs({ positionals: input.positionals, channel: input.options.channel });
  if (tsArg !== undefined && !isSlackTs(tsArg)) {
    throw new Error(
      `Unexpected argument "${tsArg}": expected a message ts like "1770165109.628379"`,
    );
  }
  const ts = pickPositionalOrFlag({
    positional: tsArg,
    flag: input.options.ts,
    positionalName: "[ts]",
    flagName: "--ts",
  });
  return { targetInput: target, options: { ...input.options, ts } };
}

export function registerMessageCommand(input: { program: Command; ctx: CliContext }): void {
  const safeModeActive = (): boolean =>
    isSafeModeEnabled({ cliFlag: Boolean(input.program.opts().safeMode) });
  const messageCmd = input.program
    .command("message")
    .description("Read/write Slack messages (token-efficient JSON)");

  const getCmd = messageCmd
    .command("get", { isDefault: true })
    .description("Fetch a single Slack message (with thread summary if any)")
    .argument("[target]", "Slack message URL, #channel, or channel ID (or use --channel)")
    .argument("[ts]", "Message ts (same as --ts)")
    .option(
      "--workspace <url>",
      "Workspace selector (full URL or unique substring; needed when using #channel/channel id across multiple workspaces)",
    )
    .option("--channel <channel>", CHANNEL_OPTION_DESCRIPTION)
    .option("--ts <ts>", "Message ts (required when using #channel/channel id)")
    .option("--thread-ts <ts>", "Thread root ts hint (useful for thread permalinks)")
    .option(
      "--max-body-chars <n>",
      "Max content characters to include (default 8000, -1 for unlimited)",
      "8000",
    )
    .option("--include-reactions", "Include reactions + reacting users")
    .option("--resolve-users", "Resolve user IDs to user profiles")
    .option(
      "--refresh-users",
      "Refresh user profile cache before resolving user IDs (implies --resolve-users)",
    );
  addHiddenOptionAlias(getCmd, { flags: "--max-content-chars <n>", target: "maxBodyChars" });
  // `--thread` turns `get` into `list`: fetch the whole thread instead of one message.
  getCmd.addOption(new Option("--thread").hideHelp());
  getCmd.action(async (...args) => {
    const [targetArg, tsArg, rawOptions] = args as [
      string | undefined,
      string | undefined,
      MessageReadOptions,
    ];
    try {
      const { targetInput, options } = resolveMessageReadArgs({
        positionals: [targetArg, tsArg],
        options: rawOptions,
      });
      const payload = options.thread
        ? await handleMessageList({ ctx: input.ctx, targetInput, options })
        : await handleMessageGet({ ctx: input.ctx, targetInput, options });
      console.log(JSON.stringify(payload, null, 2));
    } catch (err: unknown) {
      console.error(input.ctx.errorMessage(err));
      process.exitCode = 1;
    }
  });

  const listCmd = messageCmd
    .command("list")
    .description("List recent channel messages, or fetch a full thread")
    .argument("[target]", "Slack message URL, #channel, or channel ID (or use --channel)")
    .argument("[ts]", "Any message ts in the thread to fetch (same as --ts)")
    .option(
      "--workspace <url>",
      "Workspace selector (full URL or unique substring; needed when using #channel/channel id across multiple workspaces)",
    )
    .option("--channel <channel>", CHANNEL_OPTION_DESCRIPTION)
    .option(
      "--thread-ts <ts>",
      "Thread root ts (lists the thread root and replies instead of channel history)",
    )
    .option("--ts <ts>", "Message ts (resolve message to its thread)")
    .option("--limit <n>", "Max messages to return for channel history (default 25, max 200)")
    .option("--oldest <ts>", "Only messages after this ts (channel history mode)")
    .option("--latest <ts>", "Only messages before this ts (channel history mode)")
    .option(
      "--with-reaction <emoji>",
      "Only include messages with this reaction (repeatable; channel history mode; requires --oldest)",
      collectOptionValue,
      [],
    )
    .option(
      "--without-reaction <emoji>",
      "Only include messages without this reaction (repeatable; channel history mode; requires --oldest)",
      collectOptionValue,
      [],
    )
    .option(
      "--max-body-chars <n>",
      "Max content characters to include (default 8000, -1 for unlimited)",
      "8000",
    )
    .option("--include-reactions", "Include reactions + reacting users")
    .option("--resolve-users", "Resolve user IDs to user profiles")
    .option(
      "--refresh-users",
      "Refresh user profile cache before resolving user IDs (implies --resolve-users)",
    );
  addHiddenOptionAlias(listCmd, { flags: "--max-content-chars <n>", target: "maxBodyChars" });
  listCmd.action(async (...args) => {
    const [targetArg, tsArg, rawOptions] = args as [
      string | undefined,
      string | undefined,
      MessageReadOptions,
    ];
    try {
      const { targetInput, options } = resolveMessageReadArgs({
        positionals: [targetArg, tsArg],
        options: rawOptions,
      });
      const payload = await handleMessageList({ ctx: input.ctx, targetInput, options });
      console.log(JSON.stringify(payload, null, 2));
    } catch (err: unknown) {
      console.error(input.ctx.errorMessage(err));
      process.exitCode = 1;
    }
  });

  messageCmd
    .command("edit")
    .description("Edit a message")
    .argument("[target]", "Slack message URL, #channel, or channel ID (or use --channel)")
    .argument("[text]", "New message text (or use --text)")
    .option(
      "--workspace <url>",
      "Workspace selector (full URL or unique substring; needed when using #channel/channel id across multiple workspaces)",
    )
    .option("--channel <channel>", CHANNEL_OPTION_DESCRIPTION)
    .option("--ts <ts>", "Message ts (required when using #channel/channel id)")
    .option("--text <text>", "New message text; alternative to the positional [text]")
    .option("--blocks <path>", "Read Block Kit blocks JSON from file or '-'")
    .action(async (...args) => {
      const [targetArg, textArg, options] = args as [
        string | undefined,
        string | undefined,
        { workspace?: string; channel?: string; ts?: string; text?: string; blocks?: string },
      ];
      try {
        const {
          target: targetInput,
          rest: [positionalText],
        } = resolveTargetArgs({ positionals: [targetArg, textArg], channel: options.channel });
        const text = pickPositionalOrFlag({
          positional: positionalText,
          flag: options.text,
          positionalName: "[text]",
          flagName: "--text",
        });
        if (text === undefined) {
          throw new Error("Missing <text>: pass the new message text (or --text <text>)");
        }
        if (safeModeActive()) {
          throw safeModeBlockedError("edit");
        }
        const payload = await editMessage({
          ctx: input.ctx,
          targetInput,
          text,
          options,
        });
        console.log(JSON.stringify(payload, null, 2));
      } catch (err: unknown) {
        console.error(input.ctx.errorMessage(err));
        process.exitCode = 1;
      }
    });

  messageCmd
    .command("delete")
    .description("Delete a message")
    .argument("[target]", "Slack message URL, #channel, or channel ID (or use --channel)")
    .option(
      "--workspace <url>",
      "Workspace selector (full URL or unique substring; needed when using #channel/channel id across multiple workspaces)",
    )
    .option("--channel <channel>", CHANNEL_OPTION_DESCRIPTION)
    .option("--ts <ts>", "Message ts (required when using #channel/channel id)")
    .action(async (...args) => {
      const [targetArg, options] = args as [
        string | undefined,
        { workspace?: string; channel?: string; ts?: string },
      ];
      try {
        const { target: targetInput } = resolveTargetArgs({
          positionals: [targetArg],
          channel: options.channel,
        });
        if (safeModeActive()) {
          throw safeModeBlockedError("delete");
        }
        const payload = await deleteMessage({
          ctx: input.ctx,
          targetInput,
          options,
        });
        console.log(JSON.stringify(payload, null, 2));
      } catch (err: unknown) {
        console.error(input.ctx.errorMessage(err));
        process.exitCode = 1;
      }
    });

  messageCmd
    .command("send")
    .description("Send or schedule a message (optionally into a thread)")
    .argument(
      "[target]",
      "Slack message URL, #name/name, channel ID, or user ID (U.../W...) (or use --channel)",
    )
    .argument("[text]", "Message text to post (optional when using --attach; or use --text)")
    .option(
      "--workspace <url>",
      "Workspace selector (full URL or unique substring; needed when using #channel/channel id across multiple workspaces)",
    )
    .option("--channel <channel>", CHANNEL_OPTION_DESCRIPTION)
    .option("--text <text>", "Message text; alternative to the positional [text]")
    .option(
      "--thread-ts <ts>",
      "Thread root ts for channel targets; URL targets derive their thread context",
    )
    .option(
      "--reply-broadcast",
      "Also broadcast this thread reply to the parent channel (requires thread context; use --thread-ts for channel targets; not supported for user-ID/DM targets; cannot be combined with --attach).",
    )
    .option(
      "--attach <path>",
      "Attach a local file path (repeatable); text becomes an initial comment without automatic list conversion",
      collectOptionValue,
      [],
    )
    .option(
      "--blocks <path>",
      "Path to a JSON file containing a Block Kit blocks array. Bypasses automatic markdown-to-rich-text conversion. Use '-' to read from stdin. Cannot be combined with --attach.",
    )
    .option(
      "--schedule <time>",
      "Schedule delivery at an ISO 8601 timestamp with explicit timezone (or Unix timestamp), within 120 days. Cannot be combined with --attach.",
    )
    .option(
      "--schedule-in <duration>",
      "Schedule delivery within 120 days after a duration or future phrase, e.g. 3h or monday 9am. Named phrases use this process's local timezone. Cannot be combined with --attach.",
    )
    .option("--no-unfurl", "Suppress link and media previews. Cannot be combined with --attach.")
    .action(async (...args) => {
      const [targetArg, textArg, options] = args as [
        string | undefined,
        string | undefined,
        {
          workspace?: string;
          channel?: string;
          text?: string;
          threadTs?: string;
          attach?: string[];
          blocks?: string;
          replyBroadcast?: boolean;
          schedule?: string;
          scheduleIn?: string;
          unfurl?: boolean;
        },
      ];
      let targetInput: string;
      let text: string | undefined;
      try {
        const resolved = resolveTargetArgs({
          positionals: [targetArg, textArg],
          channel: options.channel,
        });
        targetInput = resolved.target;
        text = pickPositionalOrFlag({
          positional: resolved.rest[0],
          flag: options.text,
          positionalName: "[text]",
          flagName: "--text",
        });
      } catch (err: unknown) {
        console.error(input.ctx.errorMessage(err));
        process.exitCode = 1;
        return;
      }
      const hasAttach = (options.attach ?? []).length > 0;
      const hasBlocks = options.blocks !== undefined;
      const hasSchedule = options.schedule !== undefined || options.scheduleIn !== undefined;
      if (!text && !hasAttach && !hasBlocks) {
        console.error("Error: <text> is required when no --attach files or --blocks are provided.");
        process.exitCode = 1;
        return;
      }
      if (hasBlocks && hasAttach) {
        console.error("Error: --blocks cannot be combined with --attach.");
        process.exitCode = 1;
        return;
      }
      if (options.replyBroadcast && hasAttach) {
        console.error(
          "Error: --reply-broadcast cannot be combined with --attach (Slack file uploads do not support thread broadcasts).",
        );
        process.exitCode = 1;
        return;
      }
      if (options.schedule !== undefined && options.scheduleIn !== undefined) {
        console.error("Error: --schedule and --schedule-in are mutually exclusive.");
        process.exitCode = 1;
        return;
      }
      if (hasSchedule && hasAttach) {
        console.error(
          "Error: --schedule/--schedule-in cannot be combined with --attach (Slack scheduled messages do not support file uploads).",
        );
        process.exitCode = 1;
        return;
      }
      try {
        const payload = safeModeActive()
          ? await redirectSendToDraft({
              ctx: input.ctx,
              targetInput,
              text: text ?? "",
              options,
            })
          : await sendMessage({
              ctx: input.ctx,
              targetInput,
              text: text ?? "",
              options,
            });
        console.log(JSON.stringify(payload, null, 2));
      } catch (err: unknown) {
        console.error(input.ctx.errorMessage(err));
        process.exitCode = 1;
      }
    });

  registerScheduledMessageCommand({ messageCmd, ctx: input.ctx });
  registerMessageDraftCommand({ messageCmd, ctx: input.ctx });

  messageCmd
    .command("compose")
    .description(
      "Open a send-capable rich editor in the browser; CI skips the editor and immediately sends supplied text",
    )
    .argument("<target>", "Slack message URL, #name/name, or channel id")
    .argument("[text]", "Initial message text (mrkdwn; sent immediately when CI skips the editor)")
    .option(
      "--workspace <url>",
      "Workspace selector (full URL or unique substring; needed when using #channel/channel id across multiple workspaces)",
    )
    .option(
      "--thread-ts <ts>",
      "Thread root ts to post into; overrides the URL-derived thread when supplied",
    )
    .option("--no-unfurl", "Suppress link and media previews")
    .action(async (...args) => {
      const [targetInput, text, options] = args as [
        string,
        string | undefined,
        { workspace?: string; threadTs?: string; unfurl?: boolean },
      ];
      try {
        const payload = await composeMessage({
          ctx: input.ctx,
          targetInput,
          initialText: text,
          options,
        });
        console.log(JSON.stringify(payload, null, 2));
      } catch (err: unknown) {
        console.error(input.ctx.errorMessage(err));
        process.exitCode = 1;
      }
    });

  const reactCmd = messageCmd.command("react").description("Add or remove reactions");

  const registerReact = (spec: { action: "add" | "remove"; desc: string; emojiDesc: string }) =>
    reactCmd
      .command(spec.action)
      .description(spec.desc)
      .argument("[target]", "Slack message URL, #channel, or channel ID (or use --channel)")
      .argument("[emoji]", spec.emojiDesc)
      .option(
        "--workspace <url>",
        "Workspace selector (full URL or unique substring; needed when using #channel/channel id across multiple workspaces)",
      )
      .option("--channel <channel>", CHANNEL_OPTION_DESCRIPTION)
      .option("--ts <ts>", "Message ts (required when using #channel/channel id)")
      .action(async (...args) => {
        const [targetArg, emojiArg, options] = args as [
          string | undefined,
          string | undefined,
          { workspace?: string; channel?: string; ts?: string },
        ];
        try {
          const {
            target: targetInput,
            rest: [emoji],
          } = resolveTargetArgs({ positionals: [targetArg, emojiArg], channel: options.channel });
          if (!emoji?.trim()) {
            throw new Error("Missing <emoji>: pass the reaction emoji (:rocket:, rocket, or 🚀)");
          }
          const payload = await reactOnTarget({
            ctx: input.ctx,
            action: spec.action,
            targetInput,
            emoji,
            options,
          });
          console.log(JSON.stringify(payload, null, 2));
        } catch (err: unknown) {
          console.error(input.ctx.errorMessage(err));
          process.exitCode = 1;
        }
      });

  registerReact({
    action: "add",
    desc: "Add a reaction to a message",
    emojiDesc: "Emoji to react with (:rocket:, rocket, or 🚀)",
  });
  registerReact({
    action: "remove",
    desc: "Remove a reaction from a message",
    emojiDesc: "Emoji to remove (:rocket:, rocket, or 🚀)",
  });
}

/**
 * Hidden `thread [get] <target> [ts]`: agents guess this for reading a thread.
 * It is `message list` restricted to thread mode.
 */
export function registerThreadCommand(input: { program: Command; ctx: CliContext }): void {
  const threadCmd = input.program
    .command("thread", { hidden: true })
    .description("Alias of `message list` for reading a full thread");

  const getCmd = threadCmd
    .command("get", { isDefault: true })
    .description("Fetch a full thread (same as `message list <url>`)")
    .argument("[target]", "Slack message URL, #channel, or channel ID (or use --channel)")
    .argument("[ts]", "Any message ts in the thread (same as --ts)")
    .option(
      "--workspace <url>",
      "Workspace selector (full URL or unique substring; needed when using #channel/channel id across multiple workspaces)",
    )
    .option("--channel <channel>", CHANNEL_OPTION_DESCRIPTION)
    .option("--thread-ts <ts>", "Thread root ts")
    .option("--ts <ts>", "Message ts (resolve message to its thread)")
    .option(
      "--max-body-chars <n>",
      "Max content characters to include (default 8000, -1 for unlimited)",
      "8000",
    )
    .option("--include-reactions", "Include reactions + reacting users")
    .option("--resolve-users", "Resolve user IDs to user profiles")
    .option(
      "--refresh-users",
      "Refresh user profile cache before resolving user IDs (implies --resolve-users)",
    );
  addHiddenOptionAlias(getCmd, { flags: "--max-content-chars <n>", target: "maxBodyChars" });
  getCmd.action(async (...args) => {
    const [targetArg, tsArg, rawOptions] = args as [
      string | undefined,
      string | undefined,
      MessageReadOptions,
    ];
    try {
      const { targetInput, options } = resolveMessageReadArgs({
        positionals: [targetArg, tsArg],
        options: rawOptions,
      });
      if (parseMsgTarget(targetInput).kind !== "url" && !options.ts && !options.threadTs) {
        throw new Error(
          'thread needs a Slack message URL, or a channel plus a ts (e.g. thread C123 "1770165109.628379")',
        );
      }
      const payload = await handleMessageList({ ctx: input.ctx, targetInput, options });
      console.log(JSON.stringify(payload, null, 2));
    } catch (err: unknown) {
      console.error(input.ctx.errorMessage(err));
      process.exitCode = 1;
    }
  });
}
