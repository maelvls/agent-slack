import { describe, expect, test } from "bun:test";
import { fetchHuddles, parseHuddleDate } from "../src/slack/huddles.ts";
import type { SlackApiClient } from "../src/slack/client.ts";

function rawHuddle(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: "R1",
    name: "",
    created_by: "U1",
    date_start: 1000,
    date_end: 1600,
    participants: [],
    participant_history: ["U1", "U2"],
    channels: ["D1"],
    has_ended: true,
    huddle_link: "https://app.slack.com/huddle/T1/D1",
    thread_root_ts: "1000.000100",
    channel_type: "dm",
    background_id: "GRADIENT_01",
    ...overrides,
  };
}

function createClient(pages: Record<string, unknown>[]) {
  const calls: Record<string, unknown>[] = [];
  const client = {
    api: async (_method: string, params: Record<string, unknown> = {}) => {
      calls.push(params);
      return pages[calls.length - 1] ?? { ok: true, huddles: [] };
    },
  } as unknown as SlackApiClient;
  return { client, calls };
}

describe("fetchHuddles", () => {
  test("compacts huddles and drops UI-only fields", async () => {
    const { client } = createClient([{ ok: true, huddles: [rawHuddle()] }]);

    const result = await fetchHuddles(client);

    expect(result.huddles).toEqual([
      {
        id: "R1",
        channel_id: "D1",
        channel_type: "dm",
        name: undefined,
        created_by: "U1",
        participants: ["U1", "U2"],
        date_start: 1000,
        date_end: 1600,
        duration_seconds: 600,
        active: undefined,
        thread_ts: "1000.000100",
        huddle_link: "https://app.slack.com/huddle/T1/D1",
      },
    ]);
    expect(result.next_cursor).toBeUndefined();
  });

  test("marks ongoing huddles active without an end or duration", async () => {
    const { client } = createClient([
      {
        ok: true,
        huddles: [rawHuddle({ has_ended: false, date_end: 0, participants: ["U3"] })],
      },
    ]);

    const [huddle] = (await fetchHuddles(client)).huddles;

    expect(huddle?.active).toBe(true);
    expect(huddle?.date_end).toBeUndefined();
    expect(huddle?.duration_seconds).toBeUndefined();
    expect(huddle?.participants).toEqual(["U1", "U2", "U3"]);
  });

  test("pages with the cursor, requesting only what is still needed", async () => {
    const { client, calls } = createClient([
      {
        ok: true,
        huddles: [rawHuddle({ id: "R1" }), rawHuddle({ id: "R2" })],
        response_metadata: { next_cursor: "c1" },
      },
      {
        ok: true,
        huddles: [rawHuddle({ id: "R3" })],
        response_metadata: { next_cursor: "c2" },
      },
    ]);

    const result = await fetchHuddles(client, { limit: 3 });

    expect(calls).toEqual([
      { limit: 3, cursor: undefined, missed_huddles_only: undefined },
      { limit: 1, cursor: "c1", missed_huddles_only: undefined },
    ]);
    expect(result.huddles.map((h) => h.id)).toEqual(["R1", "R2", "R3"]);
    expect(result.next_cursor).toBe("c2");
  });

  test("caps page size at 100 and forwards --missed-only", async () => {
    const { client, calls } = createClient([{ ok: true, huddles: [] }]);

    await fetchHuddles(client, { limit: 500, missedOnly: true, cursor: "start" });

    expect(calls).toEqual([{ limit: 100, cursor: "start", missed_huddles_only: true }]);
  });

  test("filters by start date and stops paging once past --after", async () => {
    const { client, calls } = createClient([
      {
        ok: true,
        huddles: [
          rawHuddle({ id: "too-new", date_start: 5000, date_end: 5100 }),
          rawHuddle({ id: "kept", date_start: 3000, date_end: 3100 }),
          rawHuddle({ id: "too-old", date_start: 1000, date_end: 1100 }),
        ],
        response_metadata: { next_cursor: "more" },
      },
    ]);

    const result = await fetchHuddles(client, { after: 2000, before: 4000 });

    expect(calls).toHaveLength(1);
    expect(result.huddles.map((h) => h.id)).toEqual(["kept"]);
    expect(result.next_cursor).toBeUndefined();
  });
});

describe("parseHuddleDate", () => {
  test("parses YYYY-MM-DD as local midnight", () => {
    expect(parseHuddleDate("2026-09-28", "--after")).toBe(new Date(2026, 8, 28).getTime() / 1000);
  });

  test("rejects malformed and impossible dates", () => {
    expect(() => parseHuddleDate("28/09/2026", "--after")).toThrow("expected YYYY-MM-DD");
    expect(() => parseHuddleDate("2026-02-30", "--before")).toThrow("not a real date");
  });
});
