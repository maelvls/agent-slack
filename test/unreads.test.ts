import { describe, expect, test } from "bun:test";
import type { SlackApiClient } from "../src/slack/client.ts";
import { fetchUnreads } from "../src/slack/unreads.ts";
import { isRestrictedError } from "../src/slack/users-counts.ts";

type Call = { method: string; params: Record<string, unknown> };

function gridClient(calls: Call[]) {
  return {
    api: async (method: string, params: Record<string, unknown> = {}) => {
      calls.push({ method, params });
      switch (method) {
        case "client.counts":
          throw new Error("team_is_restricted");
        case "users.counts":
          return {
            channels: [
              { id: "C1", name: "quiet", unread_count_display: 0, mention_count_display: 0 },
              { id: "C2", name: "busy", unread_count_display: 3, mention_count_display: 0 },
              {
                id: "C3",
                name: "muted",
                is_muted: true,
                unread_count_display: 9,
                mention_count_display: 0,
              },
              {
                id: "C4",
                name: "muted-mention",
                is_muted: true,
                unread_count_display: 2,
                mention_count_display: 1,
              },
              {
                id: "C5",
                name: "archived",
                is_archived: true,
                unread_count_display: 4,
                mention_count_display: 0,
              },
            ],
            groups: [
              { id: "G1", is_mpim: true, unread_count_display: 1, mention_count_display: 0 },
            ],
            ims: [
              { id: "D1", user_id: "U1", dm_count: 2 },
              { id: "D2", user_id: "U2", dm_count: 0 },
            ],
          };
        case "conversations.info": {
          const id = params.channel as string;
          return {
            channel: {
              id,
              name: id.startsWith("D") ? undefined : `name-${id}`,
              is_im: id.startsWith("D"),
              is_mpim: id === "G1",
              user: id === "D1" ? "U1" : undefined,
              last_read: "100.000000",
            },
          };
        }
        case "users.info":
          return { user: { id: params.user, name: "alice", profile: { display_name: "Alice" } } };
        case "conversations.history":
          return { messages: [{ ts: "101.000000", user: "U1", text: "hi" }] };
        default:
          throw new Error(`unexpected method: ${method}`);
      }
    },
  } as unknown as SlackApiClient;
}

describe("fetchUnreads on Enterprise Grid", () => {
  test("falls back to users.counts and reads last_read from conversations.info", async () => {
    const calls: Call[] = [];
    const result = await fetchUnreads(gridClient(calls));

    expect(result.threads).toBeNull();
    const byId = Object.fromEntries(result.channels.map((c) => [c.channel_id, c]));
    expect(Object.keys(byId).sort()).toEqual(["C2", "C4", "D1", "G1"]);
    expect(byId.D1).toMatchObject({ channel_type: "dm", channel_name: "Alice", unread_count: 2 });
    expect(byId.G1?.channel_type).toBe("mpim");
    expect(byId.C4?.mention_count).toBe(1);
    expect(byId.C2?.messages?.[0]?.content).toBe("hi");

    const history = calls.filter((c) => c.method === "conversations.history");
    expect(history).toHaveLength(4);
    expect(history.every((c) => c.params.oldest === "100.000000")).toBe(true);
  });

  test("counts-only skips history", async () => {
    const calls: Call[] = [];
    const result = await fetchUnreads(gridClient(calls), { includeMessages: false });
    expect(result.channels).toHaveLength(4);
    expect(calls.some((c) => c.method === "conversations.history")).toBe(false);
  });

  test("rethrows non-restricted errors", async () => {
    const client = {
      api: async () => {
        throw new Error("invalid_auth");
      },
    } as unknown as SlackApiClient;
    await expect(fetchUnreads(client)).rejects.toThrow("invalid_auth");
  });

  test("isRestrictedError matches *_is_restricted codes from either client", () => {
    expect(isRestrictedError(new Error("team_is_restricted"))).toBe(true);
    expect(isRestrictedError(new Error("enterprise_is_restricted"))).toBe(true);
    const webApiError = Object.assign(new Error("An API error occurred"), {
      data: { error: "team_is_restricted" },
    });
    expect(isRestrictedError(webApiError)).toBe(true);
    expect(isRestrictedError(new Error("not_authed"))).toBe(false);
  });
});
