import { afterEach, describe, expect, mock, test } from "bun:test";
import { getWebClientFetch, SlackApiClient } from "../src/slack/client.ts";
import { SlackRateLimitError } from "../src/slack/rate-limit.ts";

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
  function rateLimitedThenOk(retryAfter: string | undefined, rateLimitedCount = 1) {
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    if (retryAfter !== undefined) {
      headers["Retry-After"] = retryAfter;
    }
    const responses = [
      ...Array.from(
        { length: rateLimitedCount },
        () =>
          new Response(JSON.stringify({ ok: false, error: "ratelimited" }), {
            status: 429,
            headers,
          }),
      ),
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

  test("maxRateLimitWaitMs: 0 throws instead of waiting", async () => {
    const { client, delays } = rateLimitedThenOk("1");
    const call = client.api("conversations.list", {}, { maxRateLimitWaitMs: 0 });
    await expect(call).rejects.toThrow("retry-after 1s exceeds");
    await expect(call).rejects.toBeInstanceOf(SlackRateLimitError);
    expect(delays).toEqual([]);
  });

  test("throws SlackRateLimitError once the retries are spent", async () => {
    const { client, delays } = rateLimitedThenOk("1", 4);
    const call = client.api("conversations.list", {});
    await expect(call).rejects.toThrow("Slack HTTP 429 calling conversations.list");
    await expect(call).rejects.toBeInstanceOf(SlackRateLimitError);
    expect(delays).toEqual([1000, 1000, 1000]);
  });

  test("other HTTP errors are not SlackRateLimitError", async () => {
    globalThis.fetch = (async () =>
      new Response("oops", { status: 500 })) as unknown as typeof fetch;
    const client = new SlackApiClient(
      { auth_type: "browser", xoxc_token: "xoxc-test", xoxd_cookie: "xoxd-test" },
      { workspaceUrl: "https://workspace.slack.com" },
    );
    const error = await client.api("conversations.list", {}).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(Error);
    expect(error).not.toBeInstanceOf(SlackRateLimitError);
    expect((error as Error).message).toBe("Slack HTTP 500 calling conversations.list");
  });
});

describe("SlackApiClient standard-token rate-limit retries", () => {
  function rateLimited(retryAfter: string | undefined): Response {
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    if (retryAfter !== undefined) {
      headers["Retry-After"] = retryAfter;
    }
    return new Response(JSON.stringify({ ok: false, error: "ratelimited" }), {
      status: 429,
      headers,
    });
  }

  function ok(): Response {
    return new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }

  function standardClient(responses: Response[]) {
    const fetchMock = mock(async (_input: string | URL | Request, _init?: RequestInit) => {
      return responses.shift()!;
    });
    globalThis.fetch = fetchMock as unknown as typeof fetch;
    const delays: number[] = [];
    globalThis.setTimeout = ((callback: () => void, delay?: number) => {
      delays.push(delay ?? 0);
      callback();
      return 0;
    }) as unknown as typeof setTimeout;
    // WebClient captures globalThis.fetch at construction, so build it after mocking.
    const client = new SlackApiClient({ auth_type: "standard", token: "xoxb-test" });
    return { client, fetchMock, delays };
  }

  test("waits out Retry-After and retries instead of throwing", async () => {
    const { client, fetchMock, delays } = standardClient([rateLimited("2"), ok()]);
    await expect(client.api("conversations.list", {})).resolves.toMatchObject({ ok: true });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(delays).toContain(2000);
  });

  test("covers apiMultipart, which delegates to the standard path", async () => {
    const { client, fetchMock } = standardClient([rateLimited("1"), ok()]);
    await expect(client.apiMultipart("saved.update", {})).resolves.toMatchObject({ ok: true });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  test.each([
    ["missing", undefined],
    ["non-numeric", "Wed, 21 Oct 2026 07:28:00 GMT"],
  ])("waits 5s and retries when Retry-After is %s", async (_label, retryAfter) => {
    const { client, fetchMock, delays } = standardClient([rateLimited(retryAfter), ok()]);
    await expect(client.api("conversations.list", {})).resolves.toMatchObject({ ok: true });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(delays).toContain(5000);
  });

  test("throws SlackRateLimitError when Retry-After exceeds the max wait", async () => {
    process.env.AGENT_SLACK_RATE_LIMIT_MAX_WAIT_MS = "10000";
    try {
      const { client, fetchMock } = standardClient([rateLimited("30"), ok()]);
      const call = client.api("conversations.list", {});
      await expect(call).rejects.toThrow(
        "retry-after 30s exceeds AGENT_SLACK_RATE_LIMIT_MAX_WAIT_MS=10000",
      );
      await expect(call).rejects.toBeInstanceOf(SlackRateLimitError);
      expect(fetchMock).toHaveBeenCalledTimes(1);
    } finally {
      delete process.env.AGENT_SLACK_RATE_LIMIT_MAX_WAIT_MS;
    }
  });

  test("maxRateLimitWaitMs: 0 throws SlackRateLimitError instead of waiting", async () => {
    const { client, fetchMock, delays } = standardClient([rateLimited("1"), ok()]);
    const call = client.api("conversations.list", {}, { maxRateLimitWaitMs: 0 });
    await expect(call).rejects.toThrow("retry-after 1s exceeds");
    await expect(call).rejects.toBeInstanceOf(SlackRateLimitError);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(delays).not.toContain(1000);
  });

  test("gives up after 3 retries with SlackRateLimitError", async () => {
    const { client, fetchMock } = standardClient([
      rateLimited("1"),
      rateLimited("1"),
      rateLimited("1"),
      rateLimited("1"),
      ok(),
    ]);
    const error = await client.api("conversations.list", {}).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(SlackRateLimitError);
    expect((error as SlackRateLimitError).message).toBe(
      "Slack API call conversations.list was still rate limited after 3 retries.",
    );
    expect((error as SlackRateLimitError).retryAfterSec).toBe(1);
    expect(fetchMock).toHaveBeenCalledTimes(4);
  });
});

describe("getWebClientFetch", () => {
  test("leaves non-429 responses and parseable Retry-After untouched", async () => {
    const plain = new Response("{}", { status: 200 });
    const limited = new Response("{}", { status: 429, headers: { "Retry-After": "7" } });
    const queue = [plain, limited];
    const wrapped = getWebClientFetch((async () => queue.shift()!) as unknown as typeof fetch);
    expect(await wrapped("https://slack.com/api/x")).toBe(plain);
    expect(await wrapped("https://slack.com/api/x")).toBe(limited);
  });

  test("fills in Retry-After: 5 on a 429 without a parseable one", async () => {
    const wrapped = getWebClientFetch(
      (async () =>
        new Response('{"ok":false}', {
          status: 429,
          statusText: "Too Many Requests",
        })) as unknown as typeof fetch,
    );
    const response = await wrapped("https://slack.com/api/x");
    expect(response.status).toBe(429);
    expect(response.statusText).toBe("Too Many Requests");
    expect(response.headers.get("Retry-After")).toBe("5");
    expect(await (response as Response).text()).toBe('{"ok":false}');
  });
});
