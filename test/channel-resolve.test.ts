import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { normalizeChannelInput, resolveChannelId } from "../src/slack/channels.ts";
import type { SlackApiClient } from "../src/slack/client.ts";

const WORKSPACE = "https://acme.slack.com";

type Handler = (
  method: string,
  params: Record<string, unknown>,
  options?: { maxRateLimitWaitMs?: number },
) => Record<string, unknown>;

// null = a client with no known workspace (undefined would pick the default).
function createClient(handler: Handler, workspaceUrl: string | null = WORKSPACE) {
  const calls: { method: string; params: Record<string, unknown>; options?: unknown }[] = [];
  const client = {
    getWorkspaceUrl: () => workspaceUrl ?? undefined,
    api: async (
      method: string,
      params: Record<string, unknown>,
      options?: { maxRateLimitWaitMs?: number },
    ) => {
      calls.push({ method, params, options });
      return handler(method, params, options);
    },
  } as unknown as SlackApiClient;
  return { client, calls };
}

const noSearchHits: Handler = (method) => {
  if (method === "search.messages") {
    return { messages: { matches: [] } };
  }
  throw new Error(`unexpected method: ${method}`);
};

describe("resolveChannelId", () => {
  let appDir: string;
  const originalXdg = process.env.XDG_RUNTIME_DIR;

  beforeEach(() => {
    appDir = mkdtempSync(join(tmpdir(), "agent-slack-channel-cache-"));
    process.env.XDG_RUNTIME_DIR = appDir;
  });

  afterEach(() => {
    if (originalXdg === undefined) {
      delete process.env.XDG_RUNTIME_DIR;
    } else {
      process.env.XDG_RUNTIME_DIR = originalXdg;
    }
    rmSync(appDir, { recursive: true, force: true });
  });

  test("channel URLs resolve to their ID without any API call", async () => {
    const { client, calls } = createClient(noSearchHits);
    expect(normalizeChannelInput(`${WORKSPACE}/archives/D0A8RUY2JG0`)).toEqual({
      kind: "id",
      value: "D0A8RUY2JG0",
    });
    expect(await resolveChannelId(client, `${WORKSPACE}/archives/D0A8RUY2JG0`)).toBe("D0A8RUY2JG0");
    expect(calls).toHaveLength(0);
  });

  test("a search hit is cached for the next lookup", async () => {
    const first = createClient((method) => {
      if (method === "search.messages") {
        return { messages: { matches: [{ channel: { id: "C0000000001" } }] } };
      }
      throw new Error(`unexpected method: ${method}`);
    });
    expect(await resolveChannelId(first.client, "#general")).toBe("C0000000001");
    expect(first.calls).toHaveLength(1);

    const second = createClient(noSearchHits);
    expect(await resolveChannelId(second.client, "#general")).toBe("C0000000001");
    expect(second.calls).toHaveLength(0);
  });

  test("the cache is per workspace", async () => {
    const first = createClient((method) => {
      if (method === "search.messages") {
        return { messages: { matches: [{ channel: { id: "C0000000001" } }] } };
      }
      throw new Error(`unexpected method: ${method}`);
    });
    await resolveChannelId(first.client, "#general");

    const other = createClient((method) => {
      if (method === "search.messages") {
        return { messages: { matches: [{ channel: { id: "C0000000009" } }] } };
      }
      throw new Error(`unexpected method: ${method}`);
    }, "https://other.slack.com");
    expect(await resolveChannelId(other.client, "#general")).toBe("C0000000009");
    expect(other.calls).toHaveLength(1);
  });

  test("the conversations.list fallback caches every channel it pages through", async () => {
    const first = createClient((method, params) => {
      if (method === "search.messages") {
        return { messages: { matches: [] } };
      }
      if (method === "conversations.list" && !params.cursor) {
        return {
          channels: [
            { id: "C0000000001", name: "general" },
            { id: "C0000000002", name: "random" },
          ],
          response_metadata: { next_cursor: "page2" },
        };
      }
      if (method === "conversations.list") {
        return { channels: [{ id: "C0000000003", name: "target" }] };
      }
      throw new Error(`unexpected method: ${method}`);
    });
    expect(await resolveChannelId(first.client, "#target")).toBe("C0000000003");
    const listCalls = first.calls.filter((c) => c.method === "conversations.list");
    expect(listCalls).toHaveLength(2);
    expect(listCalls[0]?.options).toEqual({ maxRateLimitWaitMs: 0 });

    const second = createClient(noSearchHits);
    expect(await resolveChannelId(second.client, "#random")).toBe("C0000000002");
    expect(second.calls).toHaveLength(0);
  });

  test("a rate-limited conversations.list fails fast and keeps what it saw", async () => {
    const { client } = createClient((method, params) => {
      if (method === "search.messages") {
        return { messages: { matches: [] } };
      }
      if (method === "conversations.list" && !params.cursor) {
        return {
          channels: [{ id: "C0000000002", name: "random" }],
          response_metadata: { next_cursor: "page2" },
        };
      }
      throw new Error(
        "Slack API call conversations.list was rate limited; retry-after 30s exceeds AGENT_SLACK_RATE_LIMIT_MAX_WAIT_MS=0.",
      );
    });
    await expect(resolveChannelId(client, "#nope")).rejects.toThrow(
      /Pass the channel ID .* or a channel URL instead/,
    );

    const second = createClient(noSearchHits);
    expect(await resolveChannelId(second.client, "#random")).toBe("C0000000002");
  });

  test("without a known workspace nothing is cached", async () => {
    const hit: Handler = (method) => {
      if (method === "search.messages") {
        return { messages: { matches: [{ channel: { id: "C0000000001" } }] } };
      }
      throw new Error(`unexpected method: ${method}`);
    };
    await resolveChannelId(createClient(hit, null).client, "#general");
    const second = createClient(hit, null);
    await resolveChannelId(second.client, "#general");
    expect(second.calls).toHaveLength(1);
  });
});
