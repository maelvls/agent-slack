import type { Command } from "commander";
import type { CliContext } from "./context.ts";
import { pruneEmpty } from "../lib/compact-json.ts";
import { getString } from "../lib/object-type-guards.ts";
import { getDmChannelForUsers, getUser, listUsers, resolveUserId } from "../slack/users.ts";
import { collectOrgChartUserIds, fetchOrgChart } from "../slack/org-chart.ts";
import { resolveUsersById, toReferencedUsers } from "../slack/user-cache.ts";

export function registerUserCommand(input: { program: Command; ctx: CliContext }): void {
  const userCmd = input.program.command("user").description("Workspace user directory");

  userCmd
    .command("list")
    .description("List users in the workspace")
    .option(
      "--workspace <url>",
      "Workspace selector (full URL or unique substring; required if you have multiple workspaces)",
    )
    .option("--limit <n>", "Max users (default 200)", "200")
    .option("--cursor <cursor>", "Pagination cursor")
    .option("--include-bots", "Include bot users")
    .action(async (...args) => {
      const [options] = args as [
        { workspace?: string; limit: string; cursor?: string; includeBots?: boolean },
      ];
      try {
        const workspaceUrl = input.ctx.effectiveWorkspaceUrl(options.workspace);
        const payload = await input.ctx.withAutoRefresh({
          workspaceUrl,
          work: async () => {
            const { client } = await input.ctx.getClientForWorkspace(workspaceUrl);
            const limit = Number.parseInt(options.limit, 10);
            return await listUsers(client, {
              limit,
              cursor: options.cursor,
              includeBots: Boolean(options.includeBots),
            });
          },
        });
        console.log(JSON.stringify(pruneEmpty(payload), null, 2));
      } catch (err: unknown) {
        console.error(input.ctx.errorMessage(err));
        process.exitCode = 1;
      }
    });

  userCmd
    .command("get")
    .description("Get a single workspace user")
    .argument("<user>", "User ID (U.../W...) or @handle/handle")
    .option(
      "--workspace <url>",
      "Workspace selector (full URL or unique substring; required if you have multiple workspaces)",
    )
    .action(async (...args) => {
      const [user, options] = args as [string, { workspace?: string }];
      try {
        const workspaceUrl = input.ctx.effectiveWorkspaceUrl(options.workspace);
        const payload = await input.ctx.withAutoRefresh({
          workspaceUrl,
          work: async () => {
            const { client } = await input.ctx.getClientForWorkspace(workspaceUrl);
            return await getUser(client, user);
          },
        });
        console.log(JSON.stringify(pruneEmpty(payload), null, 2));
      } catch (err: unknown) {
        console.error(input.ctx.errorMessage(err));
        process.exitCode = 1;
      }
    });

  userCmd
    .command("org-chart")
    .description(
      "Show a user's manager chain (nearest first), peers, and direct reports (requires browser auth)",
    )
    .argument("[user]", "User ID (U.../W...), @handle/handle, or email (default: you)")
    .option(
      "--workspace <url>",
      "Workspace selector (full URL or unique substring; required if you have multiple workspaces)",
    )
    .option("--all-peers", "Also list each manager's peers (the wider org at every level)")
    .option("--resolve-users", "Resolve user IDs to user profiles")
    .option(
      "--refresh-users",
      "Refresh user profile cache before resolving user IDs (implies --resolve-users)",
    )
    .action(async (...args) => {
      const [user, options] = args as [
        string | undefined,
        { workspace?: string; allPeers?: boolean; resolveUsers?: boolean; refreshUsers?: boolean },
      ];
      try {
        const workspaceUrl = input.ctx.effectiveWorkspaceUrl(options.workspace);
        const payload = await input.ctx.withAutoRefresh({
          workspaceUrl,
          work: async () => {
            const { client, workspace_url } = await input.ctx.getClientForWorkspace(workspaceUrl);
            const userId = user?.trim()
              ? await resolveUserId(client, user)
              : getString((await client.api("auth.test")).user_id);
            if (!userId) {
              throw new Error(`Could not resolve user: ${user ?? "(current user)"}`);
            }
            const chart = await fetchOrgChart(client, { userId, allPeers: options.allPeers });
            if (!options.resolveUsers && !options.refreshUsers) {
              return chart;
            }
            const userIds = collectOrgChartUserIds(chart);
            const usersById = await resolveUsersById({
              client,
              workspaceUrl: workspace_url ?? workspaceUrl ?? "",
              userIds,
              forceRefresh: Boolean(options.refreshUsers),
            });
            return { ...chart, referenced_users: toReferencedUsers(userIds, usersById) };
          },
        });
        console.log(JSON.stringify(pruneEmpty(payload), null, 2));
      } catch (err: unknown) {
        console.error(input.ctx.errorMessage(err));
        process.exitCode = 1;
      }
    });

  userCmd
    .command("dm-open")
    .description("Open or get a DM / group DM channel")
    .argument("<users...>", "One to 8 other user IDs (U.../W...) or @handles; caller is implicit")
    .option("--workspace <url>", "Workspace URL (required if you have multiple workspaces)")
    .action(async (...args) => {
      const [users, options] = args as [string[], { workspace?: string }];
      try {
        const workspaceUrl = input.ctx.effectiveWorkspaceUrl(options.workspace);
        const payload = await input.ctx.withAutoRefresh({
          workspaceUrl,
          work: async () => {
            const { client } = await input.ctx.getClientForWorkspace(workspaceUrl);
            return await getDmChannelForUsers(client, users);
          },
        });
        console.log(JSON.stringify(pruneEmpty(payload), null, 2));
      } catch (err: unknown) {
        console.error(input.ctx.errorMessage(err));
        process.exitCode = 1;
      }
    });
}
