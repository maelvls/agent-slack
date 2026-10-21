import { describe, expect, test } from "bun:test";
import { fetchMessage, fetchThread } from "../src/slack/messages.ts";

describe("fetchThread", () => {
  test("returns the thread root and replies in chronological order", async () => {
    const calls: { method: string; params: Record<string, unknown> }[] = [];
    const client = {
      api: async (method: string, params: Record<string, unknown>) => {
        calls.push({ method, params });
        return {
          messages: [
            { ts: "2.000002", thread_ts: "1.000001", text: "reply", user: "U22222222" },
            { ts: "1.000001", text: "root", user: "U11111111" },
          ],
        };
      },
    };

    const messages = await fetchThread(client as never, {
      channelId: "C12345678",
      threadTs: "1.000001",
    });

    expect(calls).toEqual([
      {
        method: "conversations.replies",
        params: {
          channel: "C12345678",
          ts: "1.000001",
          limit: 200,
          cursor: undefined,
          include_all_metadata: undefined,
        },
      },
    ]);
    expect(messages.map(({ ts, text }) => ({ ts, text }))).toEqual([
      { ts: "1.000001", text: "root" },
      { ts: "2.000002", text: "reply" },
    ]);
  });

  test("drops the root that conversations.replies repeats on every page", async () => {
    const root = { ts: "1.000001", text: "root", user: "U11111111" };
    const pages: Record<string, { messages: unknown[]; next?: string }> = {
      first: { messages: [root, { ts: "2.000002", text: "a" }], next: "page-2" },
      "page-2": { messages: [root, { ts: "3.000003", text: "b" }] },
    };
    const client = {
      api: async (_method: string, params: Record<string, unknown>) => {
        const page = pages[(params.cursor as string | undefined) ?? "first"]!;
        return { messages: page.messages, response_metadata: { next_cursor: page.next ?? "" } };
      },
    };

    const messages = await fetchThread(client as never, {
      channelId: "C12345678",
      threadTs: "1.000001",
    });

    expect(messages.map((m) => m.ts)).toEqual(["1.000001", "2.000002", "3.000003"]);
  });
});

describe("fetchMessage", () => {
  test("finds a thread reply without a thread_ts hint", async () => {
    const calls: { method: string; params: Record<string, unknown> }[] = [];
    const client = {
      api: async (method: string, params: Record<string, unknown>) => {
        calls.push({ method, params });
        if (method === "conversations.history") {
          return { messages: [{ ts: "1.000001", text: "root" }] };
        }
        return {
          messages: [
            { ts: "1.000001", text: "root" },
            { ts: "5.000005", thread_ts: "1.000001", text: "reply" },
          ],
        };
      },
    };

    const msg = await fetchMessage(client as never, {
      ref: {
        workspace_url: "https://workspace.slack.com",
        channel_id: "C12345678",
        message_ts: "5.000005",
        raw: "C12345678:5.000005",
      },
    });

    expect(msg.text).toBe("reply");
    expect(msg.thread_ts).toBe("1.000001");
    expect(calls.map((c) => c.method)).toEqual(["conversations.history", "conversations.replies"]);
  });
});
