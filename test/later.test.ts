import { describe, expect, test } from "bun:test";
import {
  fetchLaterItems,
  updateLaterMark,
  saveLater,
  removeLater,
  setLaterReminder,
  parseReminderDuration,
} from "../src/slack/later.ts";
import type { SlackApiClient } from "../src/slack/client.ts";
import { Command } from "commander";
import type { CliContext } from "../src/cli/context.ts";
import { registerLaterCommand } from "../src/cli/later-command.ts";

function createClient(responses: Record<string, Record<string, unknown>>) {
  const calls: { method: string; params: Record<string, unknown>; multipart?: boolean }[] = [];
  const client = {
    api: async (method: string, params: Record<string, unknown> = {}) => {
      calls.push({ method, params });
      return responses[method] ?? { ok: true };
    },
    apiMultipart: async (method: string, params: Record<string, unknown> = {}) => {
      calls.push({ method, params, multipart: true });
      return responses[method] ?? { ok: true };
    },
  } as unknown as SlackApiClient;
  return { client, calls };
}

describe("fetchLaterItems", () => {
  test("returns counts only when countsOnly is true", async () => {
    const { client, calls } = createClient({
      "saved.list": {
        ok: true,
        saved_items: [
          {
            item_id: "C123",
            item_type: "message",
            ts: "1.1",
            state: "in_progress",
            date_created: 100,
          },
        ],
        counts: {
          uncompleted_count: 5,
          archived_count: 1,
          completed_count: 10,
          total_count: 16,
        },
      },
    });

    const result = await fetchLaterItems(client, { countsOnly: true });

    expect(calls).toHaveLength(1);
    expect(calls[0]?.method).toBe("saved.list");
    expect(result.counts).toEqual({
      in_progress: 5,
      archived: 1,
      completed: 10,
      total: 16,
    });
    expect(result.items).toHaveLength(0);
  });

  test("filters to message items only", async () => {
    const { client } = createClient({
      "saved.list": {
        ok: true,
        saved_items: [
          {
            item_id: "Sa123",
            item_type: "reminder",
            ts: "",
            state: "in_progress",
            date_created: 100,
          },
          {
            item_id: "C123",
            item_type: "message",
            ts: "1.1",
            state: "in_progress",
            date_created: 200,
          },
        ],
        counts: { uncompleted_count: 2, archived_count: 0, completed_count: 0, total_count: 2 },
      },
      "conversations.info": { ok: true, channel: { name: "general" } },
      "conversations.history": {
        ok: true,
        messages: [{ ts: "1.1", text: "hello", user: "U1" }],
      },
    });

    const result = await fetchLaterItems(client);

    expect(result.items).toHaveLength(1);
    expect(result.items[0]?.channel_id).toBe("C123");
  });

  test("hydrates saved thread replies from conversations.replies", async () => {
    const { client, calls } = createClient({
      "saved.list": {
        ok: true,
        saved_items: [
          { item_id: "C1", item_type: "message", ts: "5.5", state: "in_progress", date_created: 1 },
        ],
        counts: {},
      },
      "conversations.info": { ok: true, channel: { name: "test" } },
      // Channel history skips thread replies; it returns the root instead.
      "conversations.history": { ok: true, messages: [{ ts: "1.1", text: "root" }] },
      "conversations.replies": {
        ok: true,
        messages: [
          { ts: "1.1", text: "root", user: "U1" },
          { ts: "5.5", thread_ts: "1.1", text: "the saved reply", user: "U2" },
        ],
      },
    });

    const result = await fetchLaterItems(client);

    expect(result.items[0]?.message).toEqual({
      author: { user_id: "U2", bot_id: undefined },
      content: "the saved reply",
      thread_ts: "1.1",
      reply_count: undefined,
    });
    expect(calls.find((c) => c.method === "conversations.replies")?.params).toMatchObject({
      channel: "C1",
      ts: "5.5",
    });
  });

  test("filters by state", async () => {
    const { client } = createClient({
      "saved.list": {
        ok: true,
        saved_items: [
          {
            item_id: "C1",
            item_type: "message",
            ts: "1.1",
            state: "in_progress",
            date_created: 100,
          },
          {
            item_id: "C2",
            item_type: "message",
            ts: "2.2",
            state: "completed",
            date_created: 200,
            date_completed: 300,
          },
          { item_id: "C3", item_type: "message", ts: "3.3", state: "archived", date_created: 150 },
        ],
        counts: { uncompleted_count: 1, archived_count: 1, completed_count: 1, total_count: 3 },
      },
      "conversations.info": { ok: true, channel: { name: "test" } },
      "conversations.history": { ok: true, messages: [{ ts: "2.2", text: "done", user: "U1" }] },
    });

    const result = await fetchLaterItems(client, { state: "completed" });

    expect(result.items).toHaveLength(1);
    expect(result.items[0]?.state).toBe("completed");
    expect(result.items[0]?.date_completed).toBe(300);
  });

  test("state 'all' returns all items", async () => {
    const { client } = createClient({
      "saved.list": {
        ok: true,
        saved_items: [
          {
            item_id: "C1",
            item_type: "message",
            ts: "1.1",
            state: "in_progress",
            date_created: 100,
          },
          { item_id: "C2", item_type: "message", ts: "2.2", state: "completed", date_created: 200 },
        ],
        counts: { uncompleted_count: 1, archived_count: 0, completed_count: 1, total_count: 2 },
      },
      "conversations.info": { ok: true, channel: { name: "test" } },
      "conversations.history": { ok: true, messages: [] },
    });

    const result = await fetchLaterItems(client, { state: "all" });

    expect(result.items).toHaveLength(2);
  });

  test("respects limit", async () => {
    const { client } = createClient({
      "saved.list": {
        ok: true,
        saved_items: [
          {
            item_id: "C1",
            item_type: "message",
            ts: "1.1",
            state: "in_progress",
            date_created: 100,
          },
          {
            item_id: "C2",
            item_type: "message",
            ts: "2.2",
            state: "in_progress",
            date_created: 200,
          },
          {
            item_id: "C3",
            item_type: "message",
            ts: "3.3",
            state: "in_progress",
            date_created: 300,
          },
        ],
        counts: { uncompleted_count: 3, archived_count: 0, completed_count: 0, total_count: 3 },
      },
      "conversations.info": { ok: true, channel: { name: "test" } },
      "conversations.history": { ok: true, messages: [] },
    });

    const result = await fetchLaterItems(client, { limit: 2 });

    expect(result.items).toHaveLength(2);
  });

  test("hydrates message content", async () => {
    const { client } = createClient({
      "saved.list": {
        ok: true,
        saved_items: [
          {
            item_id: "C123",
            item_type: "message",
            ts: "1.1",
            state: "in_progress",
            date_created: 100,
          },
        ],
        counts: { uncompleted_count: 1, archived_count: 0, completed_count: 0, total_count: 1 },
      },
      "conversations.info": { ok: true, channel: { name: "general", is_channel: true } },
      "conversations.history": {
        ok: true,
        messages: [
          {
            ts: "1.1",
            text: "Hello world",
            user: "U456",
            thread_ts: "1.0",
            reply_count: 3,
          },
        ],
      },
    });

    const result = await fetchLaterItems(client);

    expect(result.items).toHaveLength(1);
    const item = result.items[0]!;
    expect(item.channel_id).toBe("C123");
    expect(item.channel_name).toBe("general");
    expect(item.message?.author?.user_id).toBe("U456");
    expect(item.message?.content).toBe("Hello world");
    expect(item.message?.thread_ts).toBe("1.0");
    expect(item.message?.reply_count).toBe(3);
  });

  test("resolves DM user display names", async () => {
    const { client } = createClient({
      "saved.list": {
        ok: true,
        saved_items: [
          {
            item_id: "D123",
            item_type: "message",
            ts: "1.1",
            state: "in_progress",
            date_created: 100,
          },
        ],
        counts: { uncompleted_count: 1, archived_count: 0, completed_count: 0, total_count: 1 },
      },
      "conversations.info": { ok: true, channel: { is_im: true, user: "U789" } },
      "conversations.history": { ok: true, messages: [{ ts: "1.1", text: "hi", user: "U789" }] },
      "users.info": {
        ok: true,
        user: { id: "U789", real_name: "Alice Smith", profile: { display_name: "alice" } },
      },
    });

    const result = await fetchLaterItems(client);

    expect(result.items[0]?.channel_name).toBe("alice");
  });

  test("truncates long message content", async () => {
    const longText = "x".repeat(5000);
    const { client } = createClient({
      "saved.list": {
        ok: true,
        saved_items: [
          {
            item_id: "C1",
            item_type: "message",
            ts: "1.1",
            state: "in_progress",
            date_created: 100,
          },
        ],
        counts: { uncompleted_count: 1, archived_count: 0, completed_count: 0, total_count: 1 },
      },
      "conversations.info": { ok: true, channel: { name: "test" } },
      "conversations.history": { ok: true, messages: [{ ts: "1.1", text: longText, user: "U1" }] },
    });

    const result = await fetchLaterItems(client, { maxBodyChars: 100 });

    expect(result.items[0]?.message?.content?.length).toBeLessThanOrEqual(103); // 100 + "\n…"
  });

  test("next_cursor resumes right after the last returned item", async () => {
    // saved.list pages by position: the cursor is the offset of the next item.
    const saved = ["1.1", "2.2", "3.3", "4.4", "5.5"].map((ts) => ({
      item_id: "C123",
      item_type: "message",
      ts,
      state: "in_progress",
      date_created: 100,
    }));
    const client = {
      api: async (method: string, params: Record<string, unknown> = {}) => {
        if (method !== "saved.list") {
          return { ok: true };
        }
        const start = Number(params.cursor ?? 0);
        const end = Math.min(start + Number(params.limit), saved.length);
        return {
          ok: true,
          saved_items: saved.slice(start, end),
          counts: { uncompleted_count: saved.length, total_count: saved.length },
          response_metadata: { next_cursor: end < saved.length ? String(end) : "" },
        };
      },
    } as unknown as SlackApiClient;

    const first = await fetchLaterItems(client, { limit: 2 });
    const second = await fetchLaterItems(client, { limit: 2, cursor: first.next_cursor });

    expect(first.items.map((i) => i.ts)).toEqual(["1.1", "2.2"]);
    expect(second.items.map((i) => i.ts)).toEqual(["3.3", "4.4"]);
    expect(second.counts.total).toBe(5);
  });
});

describe("updateLaterMark", () => {
  test("sends mark=completed via multipart", async () => {
    const { client, calls } = createClient({});

    await updateLaterMark(client, { channelId: "C123", ts: "1.1", mark: "completed" });

    expect(calls).toHaveLength(1);
    expect(calls[0]?.method).toBe("saved.update");
    expect(calls[0]?.multipart).toBe(true);
    expect(calls[0]?.params).toEqual({
      item_id: "C123",
      item_type: "message",
      ts: "1.1",
      mark: "completed",
    });
  });

  test("sends mark=archived via multipart", async () => {
    const { client, calls } = createClient({});

    await updateLaterMark(client, { channelId: "C123", ts: "1.1", mark: "archived" });

    expect(calls[0]?.params.mark).toBe("archived");
    expect(calls[0]?.multipart).toBe(true);
  });

  test("sends mark=uncompleted via multipart", async () => {
    const { client, calls } = createClient({});

    await updateLaterMark(client, { channelId: "C123", ts: "1.1", mark: "uncompleted" });

    expect(calls[0]?.params.mark).toBe("uncompleted");
  });

  test("sends mark=unarchived via multipart", async () => {
    const { client, calls } = createClient({});

    await updateLaterMark(client, { channelId: "C123", ts: "1.1", mark: "unarchived" });

    expect(calls[0]?.params.mark).toBe("unarchived");
  });
});

describe("saveLater", () => {
  test("calls saved.add with correct params", async () => {
    const { client, calls } = createClient({});

    await saveLater(client, { channelId: "C123", ts: "1.1" });

    expect(calls).toHaveLength(1);
    expect(calls[0]?.method).toBe("saved.add");
    expect(calls[0]?.params).toEqual({
      item_id: "C123",
      item_type: "message",
      ts: "1.1",
    });
  });
});

describe("removeLater", () => {
  test("calls saved.delete with correct params", async () => {
    const { client, calls } = createClient({});

    await removeLater(client, { channelId: "C123", ts: "1.1" });

    expect(calls).toHaveLength(1);
    expect(calls[0]?.method).toBe("saved.delete");
    expect(calls[0]?.params).toEqual({
      item_id: "C123",
      item_type: "message",
      ts: "1.1",
    });
  });
});

describe("setLaterReminder", () => {
  test("calls saved.update via multipart with date_due", async () => {
    const { client, calls } = createClient({});

    await setLaterReminder(client, { channelId: "C123", ts: "1.1", dateDue: 1700000000 });

    expect(calls).toHaveLength(1);
    expect(calls[0]?.method).toBe("saved.update");
    expect(calls[0]?.multipart).toBe(true);
    expect(calls[0]?.params).toEqual({
      item_id: "C123",
      item_type: "message",
      ts: "1.1",
      date_due: "1700000000",
    });
  });
});

describe("parseReminderDuration", () => {
  test("parses minutes", () => {
    const now = Math.floor(Date.now() / 1000);
    const result = parseReminderDuration("30m");
    expect(result).toBeGreaterThanOrEqual(now + 30 * 60 - 2);
    expect(result).toBeLessThanOrEqual(now + 30 * 60 + 2);
  });

  test("parses hours", () => {
    const now = Math.floor(Date.now() / 1000);
    const result = parseReminderDuration("2h");
    expect(result).toBeGreaterThanOrEqual(now + 2 * 3600 - 2);
    expect(result).toBeLessThanOrEqual(now + 2 * 3600 + 2);
  });

  test("parses fractional hours", () => {
    const now = Math.floor(Date.now() / 1000);
    const result = parseReminderDuration("1.5h");
    expect(result).toBeGreaterThanOrEqual(now + 1.5 * 3600 - 2);
    expect(result).toBeLessThanOrEqual(now + 1.5 * 3600 + 2);
  });

  test("parses days", () => {
    const now = Math.floor(Date.now() / 1000);
    const result = parseReminderDuration("3d");
    expect(result).toBeGreaterThanOrEqual(now + 3 * 86400 - 2);
    expect(result).toBeLessThanOrEqual(now + 3 * 86400 + 2);
  });

  test("parses 'tomorrow' as next day 9 AM", () => {
    const result = parseReminderDuration("tomorrow");
    const date = new Date(result * 1000);
    expect(date.getHours()).toBe(9);
    expect(date.getMinutes()).toBe(0);

    const tomorrow = new Date();
    tomorrow.setDate(tomorrow.getDate() + 1);
    expect(date.getDate()).toBe(tomorrow.getDate());
  });

  test("parses day names", () => {
    const result = parseReminderDuration("monday");
    const date = new Date(result * 1000);
    expect(date.getHours()).toBe(9);
    expect(date.getDay()).toBe(1); // Monday
  });

  test("passes through unix timestamps", () => {
    const result = parseReminderDuration("1700000000");
    expect(result).toBe(1700000000);
  });

  test("supports verbose unit names", () => {
    const now = Math.floor(Date.now() / 1000);

    const mins = parseReminderDuration("5 minutes");
    expect(mins).toBeGreaterThanOrEqual(now + 5 * 60 - 2);

    const hrs = parseReminderDuration("1 hour");
    expect(hrs).toBeGreaterThanOrEqual(now + 3600 - 2);

    const days = parseReminderDuration("2 days");
    expect(days).toBeGreaterThanOrEqual(now + 2 * 86400 - 2);
  });

  test("throws on invalid input", () => {
    expect(() => parseReminderDuration("banana")).toThrow("Invalid duration");
    expect(() => parseReminderDuration("")).toThrow("Invalid duration");
    expect(() => parseReminderDuration("abc123")).toThrow("Invalid duration");
  });
});

describe("later list command", () => {
  test("--cursor resumes saved.list from a previous next_cursor", async () => {
    const { client, calls } = createClient({
      "saved.list": { ok: true, saved_items: [], counts: { total_count: 0 } },
    });
    const ctx = {
      effectiveWorkspaceUrl: (flag?: string) => flag,
      withAutoRefresh: async <T>(input: { work: () => Promise<T> }) => input.work(),
      getClientForWorkspace: async () => ({ client }),
      errorMessage: (err: unknown) => String(err),
    } as unknown as CliContext;
    const program = new Command();
    registerLaterCommand({ program, ctx });
    const originalLog = console.log;
    console.log = () => {};
    try {
      await program.parseAsync(["later", "list", "--cursor", "page-2"], { from: "user" });
    } finally {
      console.log = originalLog;
    }

    expect(calls[0]).toEqual({ method: "saved.list", params: { limit: 50, cursor: "page-2" } });
  });
});
