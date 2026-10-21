import { describe, expect, test } from "bun:test";
import {
  parseWatchDuration,
  isTransientWatchError,
  retryWhileTransient,
  rtmEventInScope,
  waitForNewMessages,
} from "../src/cli/message-watch.ts";
import { rtmSocketUrl, toRtmMessageEvent } from "../src/slack/rtm.ts";
import type { SlackApiClient, SlackAuth } from "../src/slack/client.ts";
import type { SlackMessageSummary } from "../src/slack/messages.ts";

describe("rtmSocketUrl", () => {
  const browser: SlackAuth = { auth_type: "browser", xoxc_token: "xoxc-1-2", xoxd_cookie: "d" };
  const failing = (message: string) =>
    ({
      api: async () => {
        throw new Error(message);
      },
    }) as unknown as SlackApiClient;

  test("uses the URL rtm.connect returns", async () => {
    const client = {
      api: async () => ({ url: "wss://rtm.example/abc" }),
    } as unknown as SlackApiClient;
    expect(await rtmSocketUrl(client, browser)).toBe("wss://rtm.example/abc");
  });

  test("on Enterprise Grid, browser auth connects like the web client", async () => {
    expect(await rtmSocketUrl(failing("enterprise_is_restricted"), browser)).toBe(
      "wss://wss-primary.slack.com/?token=xoxc-1-2",
    );
  });

  test("other failures, and non-browser tokens, still throw", async () => {
    await expect(rtmSocketUrl(failing("not_allowed_token_type"), browser)).rejects.toThrow(
      "not_allowed_token_type",
    );
    await expect(
      rtmSocketUrl(failing("enterprise_is_restricted"), { auth_type: "standard", token: "xoxp-1" }),
    ).rejects.toThrow("enterprise_is_restricted");
  });
});

function msg(ts: string, user = "U_OTHER", thread_ts?: string): SlackMessageSummary {
  return { channel_id: "C1", ts, user, thread_ts, text: ts, markdown: ts };
}

describe("parseWatchDuration", () => {
  test("parses units and bare seconds", () => {
    expect(parseWatchDuration("90s", "--timeout")).toBe(90_000);
    expect(parseWatchDuration("10m", "--timeout")).toBe(600_000);
    expect(parseWatchDuration("1.5h", "--timeout")).toBe(5_400_000);
    expect(parseWatchDuration("45", "--timeout")).toBe(45_000);
    expect(parseWatchDuration("250ms", "--settle")).toBe(250);
  });

  test("rejects garbage with the flag name", () => {
    expect(() => parseWatchDuration("soon", "--timeout")).toThrow("--timeout");
  });
});

describe("rtmEventInScope", () => {
  const thread = { channelId: "C1", threadTs: "100.000001", topLevel: false };

  test("matches replies in the watched thread only", () => {
    expect(rtmEventInScope({ channel: "C1", ts: "101.0", thread_ts: "100.000001" }, thread)).toBe(
      true,
    );
    expect(rtmEventInScope({ channel: "C1", ts: "101.0", thread_ts: "99.0" }, thread)).toBe(false);
    expect(rtmEventInScope({ channel: "C1", ts: "101.0" }, thread)).toBe(false);
    expect(rtmEventInScope({ channel: "C2", ts: "101.0", thread_ts: "100.000001" }, thread)).toBe(
      false,
    );
  });

  test("ignores edits, deletes and reply-count bumps", () => {
    for (const subtype of ["message_changed", "message_deleted", "message_replied"]) {
      expect(rtmEventInScope({ channel: "C1", thread_ts: "100.000001", subtype }, thread)).toBe(
        false,
      );
    }
  });

  test("top-level scope accepts new posts but not other threads' replies", () => {
    const dm = { channelId: "D1", threadTs: "100.000001", topLevel: true };
    expect(rtmEventInScope({ channel: "D1", ts: "101.0" }, dm)).toBe(true);
    expect(rtmEventInScope({ channel: "D1", ts: "101.0", thread_ts: "50.0" }, dm)).toBe(false);
    expect(rtmEventInScope({ channel: "D1", ts: "101.0", thread_ts: "100.000001" }, dm)).toBe(true);
  });
});

describe("toRtmMessageEvent", () => {
  test("keeps message events, drops everything else", () => {
    expect(
      toRtmMessageEvent({
        type: "message",
        channel: "C1",
        ts: "1.2",
        thread_ts: "1.0",
        user: "U1",
      }),
    ).toEqual({ channel: "C1", ts: "1.2", thread_ts: "1.0", user: "U1", subtype: undefined });
    expect(toRtmMessageEvent({ type: "user_typing", channel: "C1" })).toBeNull();
    expect(toRtmMessageEvent({ type: "message" })).toBeNull();
    expect(toRtmMessageEvent(null)).toBeNull();
  });
});

describe("waitForNewMessages", () => {
  const base = { settleMs: 0, pollIntervalMs: 10, timeoutMs: 1000 };

  test("returns immediately when a reply already exists after `after` (catch-up)", async () => {
    let calls = 0;
    const result = await waitForNewMessages({
      ...base,
      after: "100.0",
      fetchNew: async () => {
        calls++;
        return [msg("99.0"), msg("100.0"), msg("101.0")];
      },
      accept: () => true,
    });
    expect(result).toEqual({ messages: [msg("101.0")], timedOut: false });
    expect(calls).toBe(1);
  });

  test("ignores rejected messages (e.g. our own) and keeps waiting", async () => {
    let calls = 0;
    const result = await waitForNewMessages({
      ...base,
      after: "100.0",
      fetchNew: async () => {
        calls++;
        return calls < 3 ? [msg("101.0", "U_SELF")] : [msg("101.0", "U_SELF"), msg("102.0")];
      },
      accept: (m) => m.user !== "U_SELF",
    });
    expect(result.messages.map((m) => m.ts)).toEqual(["102.0"]);
    expect(calls).toBe(3);
  });

  test("times out with no messages", async () => {
    const result = await waitForNewMessages({
      ...base,
      timeoutMs: 40,
      after: "100.0",
      fetchNew: async () => [],
      accept: () => true,
    });
    expect(result).toEqual({ messages: [], timedOut: true });
  });

  test("a push trigger wakes the loop without waiting for the poll interval", async () => {
    let arrived = false;
    let unsubscribed = false;
    const started = Date.now();
    const result = await waitForNewMessages({
      ...base,
      pollIntervalMs: 60_000,
      livePollIntervalMs: 60_000,
      timeoutMs: 5000,
      after: "100.0",
      fetchNew: async () => (arrived ? [msg("101.0")] : []),
      accept: () => true,
      subscribe: async ({ trigger, setLive }) => {
        setLive(true);
        setTimeout(() => {
          arrived = true;
          trigger();
        }, 20);
        return () => {
          unsubscribed = true;
        };
      },
    });
    expect(result.messages.map((m) => m.ts)).toEqual(["101.0"]);
    expect(Date.now() - started).toBeLessThan(2000);
    expect(unsubscribed).toBe(true);
  });

  test("falls back to polling when subscribe fails", async () => {
    const logs: string[] = [];
    let calls = 0;
    const result = await waitForNewMessages({
      ...base,
      after: "100.0",
      fetchNew: async () => (++calls >= 2 ? [msg("101.0")] : []),
      accept: () => true,
      subscribe: async () => {
        throw new Error("not_allowed_token_type");
      },
      log: (m) => logs.push(m),
    });
    expect(result.messages.map((m) => m.ts)).toEqual(["101.0"]);
    expect(logs[0]).toContain("not_allowed_token_type");
  });

  test("settle window collects follow-up messages", async () => {
    let calls = 0;
    const result = await waitForNewMessages({
      ...base,
      settleMs: 20,
      after: "100.0",
      fetchNew: async () => (++calls === 1 ? [msg("101.0")] : [msg("101.0"), msg("102.0")]),
      accept: () => true,
    });
    expect(result.messages.map((m) => m.ts)).toEqual(["101.0", "102.0"]);
  });
});

describe("isTransientWatchError", () => {
  test("network loss, timeouts, 5xx and rate limits are transient", () => {
    expect(isTransientWatchError(new TypeError("fetch failed"))).toBe(true);
    expect(
      isTransientWatchError(
        new Error("Unable to connect. Is the computer able to access the url?"),
      ),
    ).toBe(true);
    expect(isTransientWatchError(new Error("Slack API call conversations.replies timed out"))).toBe(
      true,
    );
    expect(isTransientWatchError(new Error("Slack HTTP 503 calling conversations.history"))).toBe(
      true,
    );
    expect(isTransientWatchError(new Error("ratelimited"))).toBe(true);
    expect(isTransientWatchError({ code: "slack_webapi_request_error" })).toBe(true);
  });

  test("Slack errors that won't fix themselves are not", () => {
    expect(isTransientWatchError(new Error("invalid_auth"))).toBe(false);
    expect(isTransientWatchError(new Error("channel_not_found"))).toBe(false);
    expect(isTransientWatchError(new Error("Slack HTTP 404 calling conversations.info"))).toBe(
      false,
    );
    expect(
      isTransientWatchError({
        code: "slack_webapi_platform_error",
        data: { error: "token_revoked" },
      }),
    ).toBe(false);
  });
});

describe("waitForNewMessages offline", () => {
  const base = { settleMs: 0, pollIntervalMs: 10, timeoutMs: 1000 };

  test("keeps polling through network errors (e.g. laptop asleep)", async () => {
    let calls = 0;
    const logs: string[] = [];
    const result = await waitForNewMessages({
      ...base,
      after: "100.0",
      fetchNew: async () => {
        calls++;
        if (calls < 4) {
          throw new TypeError("fetch failed");
        }
        return [msg("101.0")];
      },
      accept: () => true,
      log: (m) => logs.push(m),
    });
    expect(result).toEqual({ messages: [msg("101.0")], timedOut: false });
    expect(calls).toBe(4);
    // One warning per outage, not one per attempt.
    expect(logs.filter((l) => l.includes("fetch failed"))).toHaveLength(1);
  });

  test("times out normally when the network never comes back", async () => {
    const result = await waitForNewMessages({
      ...base,
      timeoutMs: 50,
      after: "100.0",
      fetchNew: async () => {
        throw new TypeError("fetch failed");
      },
      accept: () => true,
    });
    expect(result).toEqual({ messages: [], timedOut: true });
  });

  test("still fails fast on errors that won't fix themselves", async () => {
    await expect(
      waitForNewMessages({
        ...base,
        after: "100.0",
        fetchNew: async () => {
          throw new Error("invalid_auth");
        },
        accept: () => true,
      }),
    ).rejects.toThrow("invalid_auth");
  });
});

describe("retryWhileTransient", () => {
  test("retries transient failures, then returns", async () => {
    let calls = 0;
    const value = await retryWhileTransient({
      what: "lookup",
      deadline: Date.now() + 1000,
      intervalMs: 5,
      fn: async () => {
        if (++calls < 3) {
          throw new TypeError("fetch failed");
        }
        return "ok";
      },
    });
    expect(value).toBe("ok");
    expect(calls).toBe(3);
  });

  test("gives up at the deadline and rethrows", async () => {
    await expect(
      retryWhileTransient({
        what: "lookup",
        deadline: Date.now() + 20,
        intervalMs: 5,
        fn: async () => {
          throw new TypeError("fetch failed");
        },
      }),
    ).rejects.toThrow("fetch failed");
  });
});
