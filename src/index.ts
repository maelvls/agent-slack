import { Command } from "commander";
import { installProxyDispatcher } from "./lib/proxy.ts";
import {
  getCommandTimeoutMs,
  shouldStartCommandWatchdog,
  startCommandWatchdog,
} from "./lib/command-watchdog.ts";
import { getPackageVersion } from "./lib/version.ts";
import { createCliContext } from "./cli/context.ts";
import { registerAuthCommand } from "./cli/auth-command.ts";
import { registerCanvasCommand } from "./cli/canvas-command.ts";
import { registerMessageCommand, registerThreadCommand } from "./cli/message-command.ts";
import { registerSearchCommand } from "./cli/search-command.ts";
import { registerLaterCommand } from "./cli/later-command.ts";
import { registerHuddleCommand } from "./cli/huddle-command.ts";
import { registerUnreadsCommand } from "./cli/unreads-command.ts";
import { registerUpdateCommand } from "./cli/update-command.ts";
import { registerUserCommand } from "./cli/user-command.ts";
import { registerChannelCommand } from "./cli/channel-command.ts";
import { registerWorkflowCommand } from "./cli/workflow-command.ts";
import { backgroundUpdateCheck } from "./lib/update.ts";

installProxyDispatcher();

const program = new Command();

function maybeStartCommandWatchdog(args: string[]): void {
  if (!shouldStartCommandWatchdog(args)) {
    return;
  }
  startCommandWatchdog(getCommandTimeoutMs());
}

program
  .name("agent-slack")
  .description("Slack automation CLI for AI agents")
  .version(getPackageVersion())
  // Commander's default, made explicit: subcommands inherit it, and agents
  // recover from a typo far faster with a "(Did you mean ...?)" hint.
  .showSuggestionAfterError(true)
  .option(
    "--safe-mode",
    'Human-in-the-loop enforcement: redirect "message send" to the draft editor and block "message edit"/"message delete" (also: AGENT_SLACK_SAFE_MODE=1)',
  );

maybeStartCommandWatchdog(process.argv.slice(2));

const ctx = createCliContext();

registerAuthCommand({ program, ctx });
registerMessageCommand({ program, ctx });
registerThreadCommand({ program, ctx });
registerCanvasCommand({ program, ctx });
registerSearchCommand({ program, ctx });
registerLaterCommand({ program, ctx });
registerHuddleCommand({ program, ctx });
registerUnreadsCommand({ program, ctx });
registerUpdateCommand({ program });
registerUserCommand({ program, ctx });
registerChannelCommand({ program, ctx });
registerWorkflowCommand({ program, ctx });

program.parse(process.argv);
if (!process.argv.slice(2).length) {
  program.outputHelp();
}

// Fire-and-forget background update check (throttled to once/24h, stderr only).
// Skip for the update command itself to avoid duplicate output.
const [subcommand] = process.argv.slice(2);
if (subcommand && subcommand !== "update") {
  backgroundUpdateCheck();
}
