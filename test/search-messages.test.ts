import { afterEach, describe, expect, mock, spyOn, test } from "bun:test";
import { searchMessagesInChannelsFallback } from "../src/slack/search-messages.ts";
import { startCommandWatchdog, stopCommandWatchdog } from "../src/lib/command-watchdog.ts";

type HistoryPage = { messages: Record<string, unknown>[]; next_cursor?: string };

function historyClient(pages: Record<string, HistoryPage>, options: { delayMs?: number } = {}) {
  const calls: Record<string, unknown>[] = [];
  const client = {
    api: async (method: string, params: Record<string, unknown>) => {
      if (method !== "conversations.history") {
        throw new Error(`Unexpected API method: ${method}`);
      }
      calls.push(params);
      if (options.delayMs) {
        await new Promise((r) => setTimeout(r, options.delayMs));
      }
      const page = pages[(params.cursor as string | undefined) ?? "first"] ?? { messages: [] };
      return {
        ok: true,
        messages: page.messages,
        has_more: Boolean(page.next_cursor),
        response_metadata: { next_cursor: page.next_cursor ?? "" },
      };
    },
  };
  return { client: client as never, calls, cursors: () => calls.map((c) => c.cursor) };
}

function endlessHay(): Record<string, HistoryPage> {
  return new Proxy({} as Record<string, HistoryPage>, {
    get: () => ({ messages: [{ ts: "1770000300.000000", text: "hay" }], next_cursor: "again" }),
  });
}

function search(
  client: never,
  extra: { after?: string; before?: string; channels?: string[] } = {},
) {
  return searchMessagesInChannelsFallback(client, {
    auth: { auth_type: "standard", token: "xoxb-test" },
    query: "needle",
    channels: ["C12345678"],
    limit: 10,
    maxContentChars: 4000,
    contentType: "any",
    download: false,
    ...extra,
  });
}

describe("searchMessagesInChannelsFallback", () => {
  afterEach(() => {
    mock.restore();
    stopCommandWatchdog();
  });

  test("follows the history cursor past the first page", async () => {
    const { client, cursors } = historyClient({
      first: { messages: [{ ts: "1770000300.000000", text: "hay" }], next_cursor: "page-2" },
      "page-2": { messages: [{ ts: "1770000200.000000", text: "the needle" }] },
    });

    const result = await search(client);

    expect(cursors()).toEqual([undefined, "page-2"]);
    expect(result.messages.map((m) => m.ts)).toEqual(["1770000200.000000"]);
    expect(result.truncated_channels).toBeUndefined();
  });

  test("caps the pages scanned per channel and reports truncated channels", async () => {
    const { client, cursors } = historyClient(endlessHay());

    const result = await search(client, { channels: ["C12345678", "C87654321"] });

    expect(cursors()).toHaveLength(20);
    expect(result.truncated_channels).toEqual(["C12345678", "C87654321"]);
  });

  test("passes --after/--before to Slack as inclusive oldest/latest bounds", async () => {
    const { client, calls } = historyClient({});

    await search(client, { after: "2026-01-01", before: "2026-01-31" });

    expect(calls[0]).toMatchObject({
      oldest: String(Date.parse("2026-01-01T00:00:00Z") / 1000),
      latest: String(Math.floor(Date.parse("2026-01-31T23:59:59.999Z") / 1000)),
      inclusive: true,
    });
  });

  test("stops paging once messages are older than --after", async () => {
    const { client, cursors } = historyClient({
      first: { messages: [{ ts: "1000000000.000000", text: "old needle" }], next_cursor: "page-2" },
      "page-2": { messages: [{ ts: "999999999.000000", text: "older needle" }] },
    });

    const result = await search(client, { after: "2026-01-01" });

    expect(cursors()).toEqual([undefined]);
    expect(result.messages).toEqual([]);
  });

  test("keeps the command watchdog from firing while pages keep arriving", async () => {
    const exit = spyOn(process, "exit").mockImplementation((() => {}) as never);
    process.env.AGENT_SLACK_COMMAND_TIMEOUT_MS = "40";
    try {
      startCommandWatchdog(40);
      // 4 pages x 25ms outlasts one 40ms watchdog period; each page re-arms it.
      const { client } = historyClient(
        {
          first: { messages: [{ ts: "4.0", text: "hay" }], next_cursor: "2" },
          "2": { messages: [{ ts: "3.0", text: "hay" }], next_cursor: "3" },
          "3": { messages: [{ ts: "2.0", text: "hay" }], next_cursor: "4" },
          "4": { messages: [{ ts: "1.0", text: "hay" }] },
        },
        { delayMs: 25 },
      );

      await search(client);
    } finally {
      delete process.env.AGENT_SLACK_COMMAND_TIMEOUT_MS;
    }

    expect(exit).not.toHaveBeenCalled();
  });
});
