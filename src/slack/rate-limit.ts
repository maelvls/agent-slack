/**
 * Shared 429 handling for both SlackApiClient transports (WebClient for xoxb/xoxp
 * tokens, raw fetch for browser xoxc/xoxd auth).
 */

import { ErrorCode } from "@slack/web-api";
import { extendCommandWatchdog } from "../lib/command-watchdog.ts";

// Slack's "default" rate-limit tier typically asks for waits in the 1-60s range;
// auto-wait up to a minute per retry so callers don't have to opt in for the common case.
const DEFAULT_SLACK_RATE_LIMIT_MAX_WAIT_MS = 60_000;

export const MAX_RATE_LIMIT_RETRIES = 3;

function getSlackRateLimitMaxWaitMs(): number {
  const raw = process.env.AGENT_SLACK_RATE_LIMIT_MAX_WAIT_MS?.trim();
  if (!raw) {
    return DEFAULT_SLACK_RATE_LIMIT_MAX_WAIT_MS;
  }
  const parsed = Number(raw);
  if (!Number.isFinite(parsed) || parsed < 0) {
    return DEFAULT_SLACK_RATE_LIMIT_MAX_WAIT_MS;
  }
  return Math.floor(parsed);
}

// Missing or non-numeric (e.g. HTTP-date) Retry-After falls back to 5s rather than NaN,
// which would slip past the max-wait check and retry immediately.
export function parseRetryAfterSec(header: string | null): number {
  const parsed = Number(header ?? "");
  return header?.trim() && Number.isFinite(parsed) ? parsed : 5;
}

// WebClient only raises a coded RateLimitedError when the 429 carries a numeric
// Retry-After (which Slack always sends); anything else is not retried.
export function webClientRetryAfterSec(error: unknown): number | undefined {
  return typeof error === "object" &&
    error !== null &&
    "code" in error &&
    error.code === ErrorCode.RateLimitedError &&
    "retryAfter" in error &&
    typeof error.retryAfter === "number"
    ? error.retryAfter
    : undefined;
}

function slackRateLimitError(input: {
  method: string;
  retryAfterSec: number;
  maxWaitMs: number;
}): Error {
  return new Error(
    `Slack API call ${input.method} was rate limited; retry-after ${input.retryAfterSec}s exceeds AGENT_SLACK_RATE_LIMIT_MAX_WAIT_MS=${input.maxWaitMs}.`,
  );
}

function pluralSeconds(n: number): string {
  return n === 1 ? "second" : "seconds";
}

function logRateLimitRetry(input: {
  method: string;
  retryAfterSec: number;
  delayMs: number;
}): void {
  const waitSec = Math.round(input.delayMs / 1000);
  console.error(
    `Slack API call ${input.method} was rate limited, and the API is telling us to retry in ` +
      `${input.retryAfterSec} ${pluralSeconds(input.retryAfterSec)}. ` +
      `Waiting ${waitSec} ${pluralSeconds(waitSec)} before retrying...`,
  );
}

/**
 * Sleep out a 429 before the caller retries; throws if Retry-After exceeds `maxWaitMs`
 * (default AGENT_SLACK_RATE_LIMIT_MAX_WAIT_MS; 0 never waits). `timeoutMs` is the
 * per-call API timeout, used to keep the command watchdog clear of the retried call too.
 */
export async function waitOutRateLimit(input: {
  method: string;
  retryAfterSec: number;
  timeoutMs: number;
  maxWaitMs?: number;
}): Promise<void> {
  const { method, retryAfterSec } = input;
  const delayMs = Math.max(retryAfterSec, 1) * 1000;
  const maxWaitMs = input.maxWaitMs ?? getSlackRateLimitMaxWaitMs();
  if (delayMs > maxWaitMs) {
    throw slackRateLimitError({ method, retryAfterSec, maxWaitMs });
  }
  logRateLimitRetry({ method, retryAfterSec, delayMs });
  // The command watchdog doesn't know this wait is intentional; push its deadline
  // out so it doesn't kill the process mid-retry.
  extendCommandWatchdog(delayMs + input.timeoutMs + 5000);
  await new Promise((r) => setTimeout(r, delayMs));
}
