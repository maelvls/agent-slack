import type { SlackApiClient, SlackAuth } from "./client.ts";
import { getKeychainTlsOption } from "../lib/keychain-ca.ts";
import { getString, isRecord } from "../lib/object-type-guards.ts";
import { getUserAgent } from "../lib/version.ts";

/** The fields of an RTM `message` event that matter for deciding whether to re-fetch. */
export type RtmMessageEvent = {
  channel: string;
  ts?: string;
  thread_ts?: string;
  user?: string;
  subtype?: string;
};

export type RtmConnection = { close: () => void };

const RTM_PING_INTERVAL_MS = 30_000;

function parseJson(raw: string): unknown {
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
}

export function toRtmMessageEvent(data: unknown): RtmMessageEvent | null {
  if (!isRecord(data) || data.type !== "message") {
    return null;
  }
  const channel = getString(data.channel);
  if (!channel) {
    return null;
  }
  return {
    channel,
    ts: getString(data.ts),
    thread_ts: getString(data.thread_ts),
    user: getString(data.user),
    subtype: getString(data.subtype),
  };
}

/**
 * Open a Slack RTM websocket (the push channel the Slack clients use). Works with
 * browser auth (xoxc/xoxd) and classic tokens; newer app tokens get
 * `not_allowed_token_type` from `rtm.connect`, which the caller should treat as
 * "fall back to polling".
 *
 * `onReady` fires on every successful `hello`; `onClose` fires once when the socket
 * goes away (network drop, `goodbye`, server restart). Reconnecting is up to the caller.
 */
export async function openRtm(input: {
  client: SlackApiClient;
  auth: SlackAuth;
  onMessage: (event: RtmMessageEvent) => void;
  onReady: () => void;
  onClose: (reason: string) => void;
}): Promise<RtmConnection> {
  if (typeof WebSocket === "undefined") {
    throw new Error("WebSocket is not available in this runtime");
  }
  const resp = await input.client.api("rtm.connect", {});
  const url = getString(resp.url);
  if (!url) {
    throw new Error("rtm.connect returned no websocket url");
  }

  const headers: Record<string, string> = {
    Origin: "https://app.slack.com",
    "User-Agent": getUserAgent(),
  };
  if (input.auth.auth_type === "browser") {
    headers.Cookie = `d=${encodeURIComponent(input.auth.xoxd_cookie)}`;
  }
  // Bun and Node (undici) both accept a non-standard init object with headers/tls.
  const WebSocketWithInit = WebSocket as unknown as new (
    url: string,
    init: Record<string, unknown>,
  ) => WebSocket;
  const ws = new WebSocketWithInit(url, { headers, ...getKeychainTlsOption() });

  let closed = false;
  let pingId = 0;
  const pinger = setInterval(() => {
    if (ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ id: ++pingId, type: "ping" }));
    }
  }, RTM_PING_INTERVAL_MS);

  const finish = (reason: string): void => {
    if (closed) {
      return;
    }
    closed = true;
    clearInterval(pinger);
    try {
      ws.close();
    } catch {
      // already closing
    }
    input.onClose(reason);
  };

  ws.onmessage = (e: MessageEvent) => {
    const data = parseJson(typeof e.data === "string" ? e.data : String(e.data));
    const type = isRecord(data) ? data.type : undefined;
    if (type === "hello") {
      input.onReady();
      return;
    }
    if (type === "goodbye") {
      finish("goodbye");
      return;
    }
    const event = toRtmMessageEvent(data);
    if (event) {
      input.onMessage(event);
    }
  };
  ws.onerror = (e: Event) => {
    const message = isRecord(e) && typeof e.message === "string" ? e.message : "error";
    finish(message);
  };
  ws.onclose = (e: CloseEvent) => {
    finish(`closed (${e.code}${e.reason ? `: ${e.reason}` : ""})`);
  };

  return {
    close: () => {
      if (closed) {
        return;
      }
      closed = true;
      clearInterval(pinger);
      ws.close();
    },
  };
}
