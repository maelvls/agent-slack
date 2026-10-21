import { Option, type Command } from "commander";
import type { CliContext } from "./context.ts";
import { stopCommandWatchdog } from "../lib/command-watchdog.ts";
import {
  handleMessageWatch,
  WATCH_TIMEOUT_EXIT_CODE,
  type MessageWatchOptions,
} from "./message-watch.ts";
import { pickPositionalOrFlag } from "./options.ts";
import { isSlackTs, resolveTargetArgs } from "./targets.ts";

export function registerMessageWatchCommand(input: { messageCmd: Command; ctx: CliContext }): void {
  const watchCmd = input.messageCmd
    .command("watch")
    .description(
      "Block until a new message arrives, then print the new messages and exit (exit code 2 on --timeout). Uses Slack's live websocket, falling back to polling.",
    )
    .argument(
      "[target]",
      "Message URL (watches its thread), #channel, channel ID, or user ID (DM) (or use --channel)",
    )
    .argument("[ts]", "Message ts whose thread to watch (same as --ts)")
    .option(
      "--workspace <url>",
      "Workspace selector (full URL or unique substring; needed when using #channel/channel id across multiple workspaces)",
    )
    .option(
      "--channel <channel>",
      "Channel (#name, name, or channel ID); alternative to the positional <target>",
    )
    .option("--ts <ts>", "Message ts whose thread to watch")
    .option("--thread-ts <ts>", "Thread root ts to watch (skips resolving it from --ts)")
    .option(
      "--after <ts>",
      "Only report messages newer than this ts (default: the target message, or now for a bare channel). Replies already posted after it return immediately.",
    )
    .option("--timeout <duration>", "Give up after this long, e.g. 90s, 10m, 2h", "30m")
    .option(
      "--settle <duration>",
      "After the first new message, wait this long to collect follow-ups",
      "3s",
    )
    .option(
      "--poll-interval <duration>",
      "Polling interval when the live websocket is unavailable (min 5s)",
      "15s",
    )
    .option(
      "--exclude-self",
      "Ignore messages from your own account. Off by default: a human using the same account may answer.",
    )
    .option(
      "--max-body-chars <n>",
      "Max content characters to include (default 8000, -1 for unlimited)",
      "8000",
    )
    .option("--resolve-users", "Resolve user IDs to user profiles");
  watchCmd.addOption(new Option("--no-rtm", "Poll only; don't open a websocket").hideHelp());
  // Own messages used to be excluded by default; keep the old opt-in flag as a no-op.
  watchCmd.addOption(new Option("--include-self").hideHelp());
  watchCmd.action(async (...args) => {
    const [targetArg, tsArg, options] = args as [
      string | undefined,
      string | undefined,
      MessageWatchOptions & { rtm?: boolean },
    ];
    try {
      const {
        target: targetInput,
        rest: [positionalTs],
      } = resolveTargetArgs({ positionals: [targetArg, tsArg], channel: options.channel });
      if (positionalTs !== undefined && !isSlackTs(positionalTs)) {
        throw new Error(
          `Unexpected argument "${positionalTs}": expected a message ts like "1770165109.628379"`,
        );
      }
      const ts = pickPositionalOrFlag({
        positional: positionalTs,
        flag: options.ts,
        positionalName: "[ts]",
        flagName: "--ts",
      });
      // Long-running by design; --timeout bounds it instead of the watchdog.
      stopCommandWatchdog();
      const { payload, timedOut } = await handleMessageWatch({
        ctx: input.ctx,
        targetInput,
        options: { ...options, ts, noRtm: options.rtm === false },
      });
      console.log(JSON.stringify(payload, null, 2));
      if (timedOut) {
        process.exitCode = WATCH_TIMEOUT_EXIT_CODE;
      }
    } catch (err: unknown) {
      console.error(input.ctx.errorMessage(err));
      process.exitCode = 1;
    }
  });
}
