import { type FetchFunction, WebClient } from "@slack/web-api";
import { getKeychainTlsOption } from "../lib/keychain-ca.ts";
import { getUserAgent } from "../lib/version.ts";
import {
  MAX_RATE_LIMIT_RETRIES,
  parseRetryAfterSec,
  SlackRateLimitError,
  waitOutRateLimit,
  webClientRetryAfterSec,
  withDefaultRetryAfter,
} from "./rate-limit.ts";

export type SlackAuth =
  | { auth_type: "standard"; token: string }
  | { auth_type: "browser"; xoxc_token: string; xoxd_cookie: string };

const DEFAULT_SLACK_API_TIMEOUT_MS = 20_000;

function getSlackApiTimeoutMs(): number {
  const raw =
    process.env.AGENT_SLACK_API_TIMEOUT_MS?.trim() || process.env.SLACK_API_TIMEOUT_MS?.trim();
  if (!raw) {
    return DEFAULT_SLACK_API_TIMEOUT_MS;
  }
  const parsed = Number(raw);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return DEFAULT_SLACK_API_TIMEOUT_MS;
  }
  return Math.floor(parsed);
}

function slackApiTimeoutError(method: string, timeoutMs: number): Error {
  return new Error(
    `Slack API call ${method} timed out after ${timeoutMs}ms. Set AGENT_SLACK_API_TIMEOUT_MS to adjust.`,
  );
}

function timeoutSignal(timeoutMs: number): AbortSignal | undefined {
  return typeof AbortSignal !== "undefined" && typeof AbortSignal.timeout === "function"
    ? AbortSignal.timeout(timeoutMs)
    : undefined;
}

function isAbortOrTimeoutError(error: unknown): boolean {
  if (!isRecord(error)) {
    return false;
  }
  const name = typeof error.name === "string" ? error.name : "";
  const code = typeof error.code === "string" ? error.code : "";
  return (
    name === "AbortError" ||
    name === "TimeoutError" ||
    code === "ABORT_ERR" ||
    code === "ECONNABORTED"
  );
}

/**
 * WebClient's fetch: `globalThis.fetch` already honors HTTPS_PROXY (natively under Bun,
 * via installProxyDispatcher under Node), so this only threads the Bun keychain CA
 * through and gives every 429 a Retry-After WebClient can parse.
 */
export function getWebClientFetch(baseFetch: typeof fetch = fetch): FetchFunction {
  const tlsOption = getKeychainTlsOption();
  return async (input, init) =>
    withDefaultRetryAfter(await baseFetch(input, { ...init, ...tlsOption }));
}

function httpError(method: string, status: number): Error {
  const message = `Slack HTTP ${status} calling ${method}`;
  // Only reached for a 429 once the retries are spent.
  return status === 429
    ? new SlackRateLimitError({ method, retryAfterSec: 0, message })
    : new Error(message);
}

export class SlackApiClient {
  private auth: SlackAuth;
  private web?: WebClient;
  private workspaceUrl?: string;
  private edgeTeamId?: Promise<string>;

  constructor(auth: SlackAuth, options?: { workspaceUrl?: string }) {
    this.auth = auth;
    this.workspaceUrl = options?.workspaceUrl;
    if (auth.auth_type === "standard") {
      // Rate limits are rejected rather than retried inside WebClient so standardApi()
      // can apply the same bounded, logged wait as the browser path.
      this.web = new WebClient(auth.token, {
        timeout: getSlackApiTimeoutMs(),
        retryConfig: { retries: 0 },
        rejectRateLimitedCalls: true,
        fetch: getWebClientFetch(),
      });
    }
  }

  getWorkspaceUrl(): string | undefined {
    return this.workspaceUrl;
  }

  /**
   * Call Slack's edge API (edgeapi.slack.com), which the web client uses for fast
   * directory lookups such as `users/search`. Requires browser auth (xoxc/xoxd).
   */
  async edgeApi(path: string, body: Record<string, unknown>): Promise<Record<string, unknown>> {
    const { auth } = this;
    if (auth.auth_type !== "browser") {
      throw new Error("Slack edge API requires browser auth");
    }
    const teamId = await this.getEdgeTeamId();
    const url = `https://edgeapi.slack.com/cache/${teamId}/${path.replace(/^\//, "")}`;
    const timeoutMs = getSlackApiTimeoutMs();
    let response: Response;
    try {
      response = await fetch(url, {
        method: "POST",
        headers: {
          Cookie: `d=${encodeURIComponent(auth.xoxd_cookie)}`,
          "Content-Type": "application/json",
          Origin: "https://app.slack.com",
          "User-Agent": getUserAgent(),
        },
        body: JSON.stringify({ token: auth.xoxc_token, ...body }),
        signal: timeoutSignal(timeoutMs),
        ...getKeychainTlsOption(),
      });
    } catch (error) {
      if (isAbortOrTimeoutError(error)) {
        throw slackApiTimeoutError(`edge ${path}`, timeoutMs);
      }
      throw error;
    }

    const data: unknown = await response.json().catch(() => ({}));
    if (!response.ok) {
      throw new Error(`Slack HTTP ${response.status} calling edge ${path}`);
    }
    if (!isRecord(data) || data.ok !== true) {
      const error = isRecord(data) && typeof data.error === "string" ? data.error : null;
      throw new Error(error || `Slack API error calling edge ${path}`);
    }
    return data;
  }

  // On Enterprise Grid the edge API is keyed by the enterprise ID; the team ID
  // returns invalid_auth there.
  private getEdgeTeamId(): Promise<string> {
    this.edgeTeamId ??= (async () => {
      const resp = await this.api("auth.test");
      const id =
        (typeof resp.enterprise_id === "string" && resp.enterprise_id) ||
        (typeof resp.team_id === "string" && resp.team_id);
      if (!id) {
        throw new Error("auth.test returned no team_id");
      }
      return id;
    })();
    return this.edgeTeamId;
  }

  /**
   * Call a Slack API method using multipart/form-data encoding.
   * Some internal Slack APIs (e.g. saved.update) require multipart encoding
   * and silently ignore parameters sent as application/x-www-form-urlencoded.
   */
  async apiMultipart(
    method: string,
    params: Record<string, unknown> = {},
  ): Promise<Record<string, unknown>> {
    if (this.auth.auth_type === "standard") {
      // Standard tokens can use the normal API path
      return this.api(method, params);
    }
    if (!this.workspaceUrl) {
      throw new Error("Browser auth requires workspace URL.");
    }
    const auth = this.auth as Extract<SlackAuth, { auth_type: "browser" }>;
    return this.browserApiMultipart({
      workspaceUrl: this.workspaceUrl,
      auth,
      method,
      params,
    });
  }

  private async browserApiMultipart(input: {
    workspaceUrl: string;
    auth: Extract<SlackAuth, { auth_type: "browser" }>;
    method: string;
    params: Record<string, unknown>;
    attempt?: number;
  }): Promise<Record<string, unknown>> {
    const attempt = input.attempt ?? 0;
    const url = `${input.workspaceUrl.replace(/\/$/, "")}/api/${input.method}`;
    const fd = new FormData();
    fd.append("token", input.auth.xoxc_token);
    for (const [k, v] of Object.entries(input.params)) {
      if (v !== undefined) {
        fd.append(k, typeof v === "object" ? JSON.stringify(v) : String(v));
      }
    }
    const timeoutMs = getSlackApiTimeoutMs();
    let response: Response;
    try {
      response = await fetch(url, {
        method: "POST",
        headers: {
          Cookie: `d=${encodeURIComponent(input.auth.xoxd_cookie)}`,
          Origin: "https://app.slack.com",
          "User-Agent": getUserAgent(),
        },
        body: fd,
        signal: timeoutSignal(timeoutMs),
        ...getKeychainTlsOption(),
      });
    } catch (error) {
      if (isAbortOrTimeoutError(error)) {
        throw slackApiTimeoutError(input.method, timeoutMs);
      }
      throw error;
    }

    if (response.status === 429 && attempt < MAX_RATE_LIMIT_RETRIES) {
      await waitOutRateLimit({
        method: input.method,
        retryAfterSec: parseRetryAfterSec(response.headers.get("Retry-After")),
        timeoutMs,
      });
      return this.browserApiMultipart({
        ...input,
        attempt: attempt + 1,
      });
    }

    const data: unknown = await response.json().catch(() => ({}));
    if (!response.ok) {
      throw httpError(input.method, response.status);
    }
    if (!isRecord(data) || data.ok !== true) {
      const error = isRecord(data) && typeof data.error === "string" ? data.error : null;
      throw new Error(error || `Slack API error calling ${input.method}`);
    }
    return data;
  }

  /**
   * `options.maxRateLimitWaitMs` overrides AGENT_SLACK_RATE_LIMIT_MAX_WAIT_MS for
   * this call; 0 makes a 429 throw instead of waiting.
   */
  async api(
    method: string,
    params: Record<string, unknown> = {},
    options?: { maxRateLimitWaitMs?: number },
  ): Promise<Record<string, unknown>> {
    const maxRateLimitWaitMs = options?.maxRateLimitWaitMs;
    if (this.auth.auth_type === "standard") {
      if (!this.web) {
        throw new Error("WebClient not initialized");
      }
      return this.standardApi({ web: this.web, method, params, maxRateLimitWaitMs });
    }

    if (!this.workspaceUrl) {
      throw new Error(
        "Browser auth requires workspace URL. Provide --workspace-url or set SLACK_WORKSPACE_URL, or call via a Slack message URL.",
      );
    }
    const { auth } = this;
    if (auth.auth_type !== "browser") {
      throw new Error("Browser API requires browser auth");
    }
    return this.browserApi({
      workspaceUrl: this.workspaceUrl,
      auth,
      method,
      params,
      maxRateLimitWaitMs,
    });
  }

  private async standardApi(input: {
    web: WebClient;
    method: string;
    params: Record<string, unknown>;
    maxRateLimitWaitMs?: number;
    attempt?: number;
  }): Promise<Record<string, unknown>> {
    const attempt = input.attempt ?? 0;
    try {
      return (await input.web.apiCall(input.method, input.params)) as unknown as Record<
        string,
        unknown
      >;
    } catch (error) {
      const retryAfterSec = webClientRetryAfterSec(error);
      if (retryAfterSec === undefined) {
        throw error;
      }
      if (attempt >= MAX_RATE_LIMIT_RETRIES) {
        throw new SlackRateLimitError({
          method: input.method,
          retryAfterSec,
          message: `Slack API call ${input.method} was still rate limited after ${MAX_RATE_LIMIT_RETRIES} retries.`,
        });
      }
      await waitOutRateLimit({
        method: input.method,
        retryAfterSec,
        timeoutMs: getSlackApiTimeoutMs(),
        maxWaitMs: input.maxRateLimitWaitMs,
      });
      return this.standardApi({ ...input, attempt: attempt + 1 });
    }
  }

  private async browserApi(input: {
    workspaceUrl: string;
    auth: Extract<SlackAuth, { auth_type: "browser" }>;
    method: string;
    params: Record<string, unknown>;
    maxRateLimitWaitMs?: number;
    attempt?: number;
  }): Promise<Record<string, unknown>> {
    const attempt = input.attempt ?? 0;
    const url = `${input.workspaceUrl.replace(/\/$/, "")}/api/${input.method}`;
    const cleanedEntries = Object.entries(input.params)
      .filter(([, v]) => v !== undefined)
      .map(([k, v]) => [k, typeof v === "object" ? JSON.stringify(v) : String(v)]);
    const formBody = new URLSearchParams({
      token: input.auth.xoxc_token,
      ...Object.fromEntries(cleanedEntries),
    });
    const timeoutMs = getSlackApiTimeoutMs();
    let response: Response;
    try {
      response = await fetch(url, {
        method: "POST",
        headers: {
          Cookie: `d=${encodeURIComponent(input.auth.xoxd_cookie)}`,
          "Content-Type": "application/x-www-form-urlencoded",
          Origin: "https://app.slack.com",
          "User-Agent": getUserAgent(),
        },
        body: formBody,
        signal: timeoutSignal(timeoutMs),
        ...getKeychainTlsOption(),
      });
    } catch (error) {
      if (isAbortOrTimeoutError(error)) {
        throw slackApiTimeoutError(input.method, timeoutMs);
      }
      throw error;
    }

    if (response.status === 429 && attempt < MAX_RATE_LIMIT_RETRIES) {
      await waitOutRateLimit({
        method: input.method,
        retryAfterSec: parseRetryAfterSec(response.headers.get("Retry-After")),
        timeoutMs,
        maxWaitMs: input.maxRateLimitWaitMs,
      });
      return this.browserApi({
        ...input,
        attempt: attempt + 1,
      });
    }

    const data: unknown = await response.json().catch(() => ({}));
    if (!response.ok) {
      throw httpError(input.method, response.status);
    }
    if (!isRecord(data) || data.ok !== true) {
      const error = isRecord(data) && typeof data.error === "string" ? data.error : null;
      throw new Error(error || `Slack API error calling ${input.method}`);
    }
    return data;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
