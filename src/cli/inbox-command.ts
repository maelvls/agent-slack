import type { Command } from "commander";
import type { CliContext } from "./context.ts";
import { fetchInbox, parseSince } from "../slack/inbox.ts";
import { parseLimit, parseMaxChars } from "./message-actions.ts";

export function registerInboxCommand(input: { program: Command; ctx: CliContext }): void {
  input.program
    .command("inbox")
    .description(
      "Show messages waiting on you: DMs and group DMs whose newest message isn't yours, @mentions you haven't answered, and new replies in threads you've posted in",
    )
    .option("--workspace <url>", "Workspace URL (defaults to your configured workspace)")
    .option("--since <when>", "Window start: Nd, Nh, or YYYY-MM-DD (UTC) (default 3d)", "3d")
    .option(
      "--max-body-chars <n>",
      "Max text characters per item (default 300, -1 for unlimited)",
      "300",
    )
    .option(
      "--max-conversations <n>",
      "Max conversations/threads to inspect; sets truncated:true when hit (default 60)",
      "60",
    )
    .option("--no-threads", "Skip new replies in threads you've posted in")
    .action(
      async (options: {
        workspace?: string;
        since: string;
        maxBodyChars: string;
        maxConversations: string;
        threads: boolean;
      }) => {
        try {
          const workspaceUrl = input.ctx.effectiveWorkspaceUrl(options.workspace);
          const since = parseSince(options.since);
          const maxBodyChars = parseMaxChars(options.maxBodyChars, "--max-body-chars");
          const maxConversations =
            parseLimit(options.maxConversations, "--max-conversations") ?? 60;

          const payload = await input.ctx.withAutoRefresh({
            workspaceUrl,
            work: async () => {
              const { client } = await input.ctx.getClientForWorkspace(workspaceUrl);
              return fetchInbox(client, {
                since,
                maxBodyChars,
                maxConversations,
                includeThreads: options.threads,
              });
            },
          });

          // No pruneEmpty: this shape is a contract; undefined fields are simply omitted.
          console.log(JSON.stringify(payload, null, 2));
        } catch (err: unknown) {
          console.error(input.ctx.errorMessage(err));
          process.exitCode = 1;
        }
      },
    );
}
