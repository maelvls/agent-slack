import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { SlackApiClient } from "../src/slack/client.ts";
import { fetchInbox, parseSince } from "../src/slack/inbox.ts";

type Msg = Record<string, unknown>;
type Call = { method: string; params: Record<string, unknown> };

const ME = "UMEMEMEME";
const SINCE = new Date(1000 * 1000); // ts 1000
const BASE = "https://acme.slack.com";

function ts(n: number): string {
  return n.toFixed(6);
}

function msg(user: string, at: number, extra: Msg = {}): Msg {
  return { type: "message", user, ts: ts(at), text: `from ${user} at ${at}`, ...extra };
}

function match(channel: Msg, user: string, at: number, threadTs?: number): Msg {
  const id = channel.id as string;
  const link = `${BASE}/archives/${id}/p${ts(at).replace(".", "")}`;
  return {
    channel,
    user,
    ts: ts(at),
    permalink: threadTs === undefined ? link : `${link}?thread_ts=${ts(threadTs)}&cid=${id}`,
  };
}

const im = (id: string, other: string) => ({ id, is_im: true, name: other });
const chan = (id: string, name: string) => ({ id, name, is_channel: true });

type World = {
  to: Msg[];
  mentions: Msg[];
  mine: Msg[];
  history: Record<string, Msg[]>;
  replies: Record<string, Msg[]>;
  pages?: number;
};

function fakeClient(world: World, calls: Call[]): SlackApiClient {
  return {
    api: async (method: string, params: Record<string, unknown> = {}) => {
      calls.push({ method, params });
      switch (method) {
        case "auth.test":
          return { user_id: ME, url: `${BASE}/` };
        case "search.messages": {
          const q = params.query as string;
          const matches = q.startsWith("to:")
            ? world.to
            : q.startsWith("is:thread")
              ? world.mine
              : world.mentions;
          return { messages: { matches, paging: { pages: world.pages ?? 1 } } };
        }
        case "conversations.history": {
          const oldest = Number(params.oldest ?? 0);
          const msgs = (world.history[params.channel as string] ?? []).filter((m) =>
            params.inclusive ? Number(m.ts) >= oldest : Number(m.ts) > oldest,
          );
          return { messages: [...msgs].reverse().slice(0, Number(params.limit ?? 100)) };
        }
        case "conversations.replies": {
          const key = `${params.channel as string}:${params.ts as string}`;
          const msgs = world.replies[key];
          if (!msgs) {
            throw new Error("thread_not_found");
          }
          return { messages: msgs, has_more: false };
        }
        case "users.info": {
          const id = params.user as string;
          return { ok: true, user: { id, name: `name-${id}`, profile: {} } };
        }
        default:
          throw new Error(`unexpected method: ${method}`);
      }
    },
  } as unknown as SlackApiClient;
}

function buildWorld(): World {
  const D1 = im("D1", "U00000001");
  const D2 = im("D2", "U00000002");
  const D3 = im("D3", "U00000003");
  const D4 = im("D4", "U00000004");
  const DSELF = im("DSELF", ME);
  const C1 = chan("C1", "answered-in-thread");
  const C2 = chan("C2", "answered-in-channel");
  const C3 = chan("C3", "unanswered");
  const C4 = chan("C4", "my-thread");
  const C5 = chan("C5", "dedupe");
  return {
    to: [
      match(D1, "U00000001", 1003),
      match(D2, "U00000002", 1001),
      match(D3, "U00000003", 1001),
      match(DSELF, ME, 1001),
      match(im("DOLD", "U00000008"), "U00000008", 500), // before --since
    ],
    mentions: [
      match(C1, "U00000004", 1001, 1000.5),
      match(C2, "U00000005", 1001),
      match(C3, "U00000005", 1010),
      match(C5, "U00000007", 1003, 1000),
      match(D4, "U00000004", 1004), // mention in a DM: reported as dm
    ],
    mine: [match(C4, ME, 1001, 1000), match(C5, ME, 1001, 1000)],
    history: {
      D1: [msg("U00000001", 1001), msg(ME, 1001.5), msg("U00000001", 1002), msg("U00000001", 1003)],
      D2: [msg("U00000002", 1001), msg(ME, 1002)],
      D3: [msg("U00000003", 1001, { reactions: [{ name: "eyes", users: [ME], count: 1 }] })],
      D4: [msg("U00000004", 1004, { text: "hey <@UMEMEMEME>" })],
      C2: [msg("U00000005", 1001), msg(ME, 1005)],
      C3: [msg("U00000005", 1010), msg("U00000006", 1011)],
    },
    replies: {
      "C1:1000.500000": [msg("U00000004", 1000.5), msg("U00000004", 1001), msg(ME, 1002)],
      "C2:1001.000000": [msg("U00000005", 1001)],
      "C3:1010.000000": [msg("U00000005", 1010, { text: "a".repeat(400) })],
      "C4:1000.000000": [
        msg("U00000006", 999),
        msg(ME, 1001),
        msg("U00000006", 1002),
        msg("UBOT", 1003, { bot_id: "B1" }),
        msg("USLACKBOT", 1004),
        msg("U00000006", 1005, { subtype: "channel_join" }),
      ],
      "C5:1000.000000": [msg("U00000007", 999), msg(ME, 1001), msg("U00000007", 1003)],
    },
  };
}

let runtimeDir = "";
const originalXdg = process.env.XDG_RUNTIME_DIR;

beforeAll(() => {
  runtimeDir = mkdtempSync(join(tmpdir(), "agent-slack-inbox-"));
  process.env.XDG_RUNTIME_DIR = runtimeDir;
});

afterAll(() => {
  if (originalXdg === undefined) {
    delete process.env.XDG_RUNTIME_DIR;
  } else {
    process.env.XDG_RUNTIME_DIR = originalXdg;
  }
  rmSync(runtimeDir, { recursive: true, force: true });
});

describe("fetchInbox", () => {
  test("reports only conversations and threads waiting on me", async () => {
    const calls: Call[] = [];
    const result = await fetchInbox(fakeClient(buildWorld(), calls), { since: SINCE });

    expect(result.user_id).toBe(ME);
    expect(result.since).toBe(SINCE.toISOString());
    expect(result.truncated).toBe(false);
    // Sorted by ts descending. Not here: D2 (I replied), D3 (I reacted),
    // C1 (answered in thread), C2 (answered in channel), self-DM, DOLD.
    expect(result.items.map((i) => [i.kind, i.channel_id])).toEqual([
      ["mention", "C3"],
      ["dm", "D4"],
      ["dm", "D1"],
      ["mention", "C5"],
      ["thread", "C4"],
    ]);

    const byChannel = Object.fromEntries(result.items.map((i) => [i.channel_id, i]));
    expect(byChannel.D1).toEqual({
      kind: "dm",
      channel_id: "D1",
      channel_name: "name-U00000001",
      ts: ts(1003),
      first_ts: ts(1002),
      count: 2,
      author: { user_id: "U00000001", name: "name-U00000001" },
      text: "from U00000001 at 1003",
      permalink: `${BASE}/archives/D1/p1003000000`,
    });
    expect(byChannel.D1?.thread_ts).toBeUndefined();

    // Top-level mention nobody answered: thread_ts is the message itself, text truncated.
    expect(byChannel.C3).toMatchObject({
      channel_name: "unanswered",
      thread_ts: ts(1010),
      ts: ts(1010),
      count: 1,
      author: { user_id: "U00000005", name: "name-U00000005" },
      permalink: `${BASE}/archives/C3/p1010000000`,
    });
    expect(byChannel.C3?.text).toBe(`${"a".repeat(300)}...`);

    // Bots, Slackbot, and joins are not "others"; the pre-window root doesn't count.
    expect(byChannel.C4).toMatchObject({
      kind: "thread",
      thread_ts: ts(1000),
      ts: ts(1002),
      first_ts: ts(1002),
      count: 1,
      permalink: `${BASE}/archives/C4/p1002000000?thread_ts=${ts(1000)}&cid=C4`,
    });

    // Same thread found as a mention and as one of mine: a single mention item.
    expect(result.items.filter((i) => i.channel_id === "C5")).toHaveLength(1);

    // The self-DM and out-of-window DM are never fetched.
    const fetched = calls
      .filter((c) => c.method === "conversations.history")
      .map((c) => c.params.channel);
    expect(fetched).not.toContain("DSELF");
    expect(fetched).not.toContain("DOLD");

    // Search window starts before --since and is sorted newest first.
    const searches = calls.filter((c) => c.method === "search.messages");
    expect(searches.map((c) => c.params.query)).toEqual([
      "to:<@UMEMEMEME> after:1969-12-30",
      "<@UMEMEMEME> after:1969-12-30",
      "is:thread from:<@UMEMEMEME> after:1969-12-30",
    ]);
    expect(searches.every((c) => c.params.sort === "timestamp")).toBe(true);
  });

  test("--no-threads skips threads I only posted in", async () => {
    const calls: Call[] = [];
    const result = await fetchInbox(fakeClient(buildWorld(), calls), {
      since: SINCE,
      includeThreads: false,
    });
    expect(result.items.some((i) => i.kind === "thread")).toBe(false);
    expect(
      calls.some(
        (c) => c.method === "search.messages" && String(c.params.query).startsWith("is:thread"),
      ),
    ).toBe(false);
  });

  test("max-conversations caps inspections and sets truncated", async () => {
    const calls: Call[] = [];
    const result = await fetchInbox(fakeClient(buildWorld(), calls), {
      since: SINCE,
      maxConversations: 1,
    });
    expect(result.truncated).toBe(true);
    const inspections = calls.filter(
      (c) => c.method === "conversations.history" || c.method === "conversations.replies",
    );
    // The newest candidate (C3) plus its channel-answer check.
    expect(inspections.map((c) => c.method)).toEqual([
      "conversations.replies",
      "conversations.history",
    ]);
    expect(result.items.map((i) => i.channel_id)).toEqual(["C3"]);
  });

  test("a capped search marks the result truncated", async () => {
    const world = { ...buildWorld(), pages: 5 };
    const calls: Call[] = [];
    const result = await fetchInbox(fakeClient(world, calls), { since: SINCE });
    expect(result.truncated).toBe(true);
    expect(calls.filter((c) => c.method === "search.messages")).toHaveLength(6);
  });

  test("items is an empty array when nothing waits", async () => {
    const world: World = { to: [], mentions: [], mine: [], history: {}, replies: {} };
    const result = await fetchInbox(fakeClient(world, []), { since: SINCE });
    expect(result.items).toEqual([]);
    expect(result.truncated).toBe(false);
  });
});

describe("parseSince", () => {
  const now = new Date("2026-11-10T12:00:00.000Z");

  test("accepts days, hours, and dates", () => {
    expect(parseSince("3d", now).toISOString()).toBe("2026-11-07T12:00:00.000Z");
    expect(parseSince("6h", now).toISOString()).toBe("2026-11-10T06:00:00.000Z");
    expect(parseSince("2026-11-07", now).toISOString()).toBe("2026-11-07T00:00:00.000Z");
  });

  test("rejects anything else", () => {
    expect(() => parseSince("3 days", now)).toThrow("Invalid --since");
    expect(() => parseSince("2026-13-45", now)).toThrow("Invalid --since");
  });
});
