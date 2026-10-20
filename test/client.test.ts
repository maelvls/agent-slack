import { afterEach, describe, expect, mock, test } from "bun:test";
import { SlackApiClient } from "../src/slack/client.ts";

const originalFetch = globalThis.fetch;
const originalSetTimeout = globalThis.setTimeout;

afterEach(() => {
  globalThis.fetch = originalFetch;
  globalThis.setTimeout = originalSetTimeout;
  delete process.env.AGENT_SLACK_RATE_LIMIT_MAX_WAIT_MS;
});

describe("SlackApiClient browser multipart transport", () => {
  test("retries HTTP 429 responses using Retry-After", async () => {
    process.env.AGENT_SLACK_RATE_LIMIT_MAX_WAIT_MS = "30000";
    const responses = [
      new Response(JSON.stringify({ ok: false, error: "ratelimited" }), {
        status: 429,
        headers: { "Content-Type": "application/json", "Retry-After": "2" },
      }),
      new Response(JSON.stringify({ ok: true, file_id: "F12345678" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ];
    const fetchMock = mock(async (_input: string | URL | Request, _init?: RequestInit) => {
      return responses.shift()!;
    });
    const delays: number[] = [];
    globalThis.fetch = fetchMock as unknown as typeof fetch;
    globalThis.setTimeout = ((callback: () => void, delay?: number) => {
      delays.push(delay ?? 0);
      callback();
      return 0;
    }) as unknown as typeof setTimeout;

    const client = new SlackApiClient(
      {
        auth_type: "browser",
        xoxc_token: "xoxc-test",
        xoxd_cookie: "xoxd-test",
      },
      { workspaceUrl: "https://workspace.slack.com" },
    );

    await expect(
      client.apiMultipart("files.createCanvas", {
        title: "Launch plan",
        markdown: "# Launch plan\n",
      }),
    ).resolves.toEqual({ ok: true, file_id: "F12345678" });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(delays).toEqual([2000]);
    for (const call of fetchMock.mock.calls) {
      expect(call[1]?.body).toBeInstanceOf(FormData);
    }
  });
});

describe("SlackApiClient browser rate-limit retries", () => {
  function rateLimitedThenOk(retryAfter: string | undefined) {
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    if (retryAfter !== undefined) {
      headers["Retry-After"] = retryAfter;
    }
    const responses = [
      new Response(JSON.stringify({ ok: false, error: "ratelimited" }), { status: 429, headers }),
      new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ];
    globalThis.fetch = (async () => responses.shift()!) as unknown as typeof fetch;
    const delays: number[] = [];
    globalThis.setTimeout = ((callback: () => void, delay?: number) => {
      delays.push(delay ?? 0);
      callback();
      return 0;
    }) as unknown as typeof setTimeout;
    const client = new SlackApiClient(
      { auth_type: "browser", xoxc_token: "xoxc-test", xoxd_cookie: "xoxd-test" },
      { workspaceUrl: "https://workspace.slack.com" },
    );
    return { client, delays };
  }

  test.each([
    ["missing", undefined],
    ["non-numeric", "Wed, 21 Oct 2026 07:28:00 GMT"],
  ])("waits 5s when Retry-After is %s", async (_label, retryAfter) => {
    const { client, delays } = rateLimitedThenOk(retryAfter);
    await expect(client.api("conversations.history", {})).resolves.toEqual({ ok: true });
    expect(delays).toEqual([5000]);
  });

  test("waits the full Retry-After when it is within the max wait", async () => {
    const { client, delays } = rateLimitedThenOk("45");
    await expect(client.apiMultipart("files.createCanvas", {})).resolves.toEqual({ ok: true });
    expect(delays).toEqual([45000]);
  });
});
