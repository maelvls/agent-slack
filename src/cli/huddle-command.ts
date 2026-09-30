import type { Command } from "commander";
import type { CliContext } from "./context.ts";
import { pruneEmpty } from "../lib/compact-json.ts";
import { parseLimit } from "./message-actions.ts";
import { fetchHuddles, parseHuddleDate } from "../slack/huddles.ts";
import { resolveUsersById, toReferencedUsers } from "../slack/user-cache.ts";

type HuddleListOptions = {
  workspace?: string;
  limit?: string;
  after?: string;
  before?: string;
  cursor?: string;
  missedOnly?: boolean;
  resolveUsers?: boolean;
  refreshUsers?: boolean;
};

export function registerHuddleCommand(input: { program: Command; ctx: CliContext }): void {
  const huddleCmd = input.program
    .command("huddle")
    .description("Read huddle history (Slack's Huddles tab; requires browser auth)");

  huddleCmd
    .command("list", { isDefault: true })
    .description(
      "List huddles you were in or invited to, most recently ended first. Read what was said with `message list <channel_id> --thread-ts <thread_ts>`",
    )
    .option(
      "--workspace <url>",
      "Workspace selector (full URL or unique substring; required if you have multiple workspaces)",
    )
    .option("--limit <n>", "Max huddles to return (default 20)", "20")
    .option("--after <date>", "Only huddles that started on or after YYYY-MM-DD (local time)")
    .option("--before <date>", "Only huddles that started before YYYY-MM-DD (local time)")
    .option("--cursor <cursor>", "Resume from a previous next_cursor")
    .option("--missed-only", "Only huddles you were invited to but did not join")
    .option("--resolve-users", "Resolve user IDs to user profiles")
    .option(
      "--refresh-users",
      "Refresh user profile cache before resolving user IDs (implies --resolve-users)",
    )
    .action(async (options: HuddleListOptions) => {
      try {
        const workspaceUrl = input.ctx.effectiveWorkspaceUrl(options.workspace);
        const limit = parseLimit(options.limit) ?? 20;
        const after = options.after ? parseHuddleDate(options.after, "--after") : undefined;
        const before = options.before ? parseHuddleDate(options.before, "--before") : undefined;

        const payload = await input.ctx.withAutoRefresh({
          workspaceUrl,
          work: async () => {
            const { client, workspace_url } = await input.ctx.getClientForWorkspace(workspaceUrl);
            const result = await fetchHuddles(client, {
              limit,
              after,
              before,
              cursor: options.cursor,
              missedOnly: options.missedOnly,
            });
            if (!options.resolveUsers && !options.refreshUsers) {
              return result;
            }
            const userIds = result.huddles.flatMap((h) =>
              h.created_by ? [h.created_by, ...h.participants] : h.participants,
            );
            const usersById = await resolveUsersById({
              client,
              workspaceUrl: workspace_url ?? workspaceUrl ?? "",
              userIds,
              forceRefresh: Boolean(options.refreshUsers),
            });
            return { ...result, referenced_users: toReferencedUsers(userIds, usersById) };
          },
        });

        console.log(JSON.stringify(pruneEmpty(payload), null, 2));
      } catch (err: unknown) {
        console.error(input.ctx.errorMessage(err));
        process.exitCode = 1;
      }
    });
}
