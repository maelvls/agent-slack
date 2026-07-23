/**
 * Kills the process if a command runs away (hung network call, stuck prompt, etc).
 * Callers that are about to perform a legitimate, bounded wait (e.g. sleeping through
 * a Slack rate-limit retry-after) should call extendCommandWatchdog() first so the
 * watchdog doesn't fire mid-wait.
 */

const DEFAULT_COMMAND_TIMEOUT_MS = 30_000;

let timer: ReturnType<typeof setTimeout> | undefined;
let armedTimeoutMs = DEFAULT_COMMAND_TIMEOUT_MS;

export function getCommandTimeoutMs(): number {
  const raw = process.env.AGENT_SLACK_COMMAND_TIMEOUT_MS?.trim();
  if (!raw) {
    return DEFAULT_COMMAND_TIMEOUT_MS;
  }
  const parsed = Number(raw);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return DEFAULT_COMMAND_TIMEOUT_MS;
  }
  return Math.floor(parsed);
}

function arm(timeoutMs: number): void {
  if (timer) {
    clearTimeout(timer);
  }
  armedTimeoutMs = timeoutMs;
  timer = setTimeout(() => {
    console.error(
      `agent-slack command timed out after ${armedTimeoutMs}ms. Set AGENT_SLACK_COMMAND_TIMEOUT_MS to adjust.`,
    );
    process.exit(124);
  }, timeoutMs);
  (timer as { unref?: () => void }).unref?.();
}

export function startCommandWatchdog(timeoutMs: number): void {
  arm(timeoutMs);
}

/**
 * Push the watchdog deadline out to at least `minMs` from now. Used before an
 * intentional wait (e.g. a rate-limit retry) so it isn't mistaken for a hang.
 */
export function extendCommandWatchdog(minMs: number): void {
  if (!timer) {
    return;
  }
  arm(Math.max(minMs, armedTimeoutMs));
}
