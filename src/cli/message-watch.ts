import type { CliContext } from "./context.ts";
import { parseMsgTarget, targetWorkspaceUrl } from "./targets.ts";
import { resolveChannelId, openDmChannel } from "../slack/channels.ts";
import {
  fetchChannelHistory,
  fetchMessage,
  fetchThread,
  toCompactMessage,
  type SlackMessageSummary,
} from "../slack/messages.ts";
import { openRtm, type RtmConnection, type RtmMessageEvent } from "../slack/rtm.ts";
import { pruneEmpty } from "../lib/compact-json.ts";
import { getString, isRecord } from "../lib/object-type-guards.ts";
import { downloadMessageFiles } from "./message-file-downloads.ts";
import { toThreadListMessage } from "./message-thread-info.ts";
import {
  collectReferencedUserIds,
  resolveUsersById,
  toReferencedUsers,
} from "../slack/user-cache.ts";

export type MessageWatchOptions = {
  workspace?: string;
  channel?: string;
  ts?: string;
  threadTs?: string;
  after?: string;
  timeout?: string;
  settle?: string;
  pollInterval?: string;
  excludeSelf?: boolean;
  noRtm?: boolean;
  maxBodyChars: string;
  resolveUsers?: boolean;
};

/** What to watch: replies in one thread, top-level channel messages, or both. */
export type WatchScope = {
  channelId: string;
  threadTs?: string;
  topLevel: boolean;
};

export const WATCH_TIMEOUT_EXIT_CODE = 2;

/** Parse `90s`, `10m`, `1.5h`, or a bare number of seconds into milliseconds. */
export function parseWatchDuration(raw: string, flag: string): number {
  const match = raw
    .trim()
    .toLowerCase()
    .match(/^(\d+(?:\.\d+)?)\s*(ms|s|sec|secs|seconds?|m|min|mins|minutes?|h|hr|hrs|hours?)?$/);
  if (!match) {
    throw new Error(`Invalid ${flag} "${raw}": expected a duration like 90s, 10m, or 1h`);
  }
  const amount = Number.parseFloat(match[1]!);
  const unit = match[2] ?? "s";
  if (unit === "ms") {
    return Math.round(amount);
  }
  const multiplier = unit.startsWith("h") ? 3_600_000 : unit.startsWith("m") ? 60_000 : 1000;
  return Math.round(amount * multiplier);
}

// Slack error codes that clear up on their own, unlike invalid_auth or channel_not_found.
const RETRYABLE_SLACK_ERRORS = new Set([
  "ratelimited",
  "internal_error",
  "fatal_error",
  "service_unavailable",
  "request_timeout",
]);

function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/**
 * Whether a failed Slack call is worth retrying while watching: no network (laptop
 * asleep, DNS down, VPN switching gateways), timeouts, rate limits and 5xx. A watch
 * can run for hours, so it rides these out instead of exiting.
 */
export function isTransientWatchError(err: unknown): boolean {
  if (isRecord(err) && typeof err.code === "string" && err.code.startsWith("slack_webapi_")) {
    if (err.code === "slack_webapi_platform_error") {
      const data = isRecord(err.data) ? err.data : undefined;
      return RETRYABLE_SLACK_ERRORS.has(getString(data?.error) ?? "");
    }
    if (err.code === "slack_webapi_http_error") {
      const status = typeof err.statusCode === "number" ? err.statusCode : 0;
      return status >= 500 || status === 429;
    }
    return true; // request errors (network) and rate limits
  }
  const message = errorText(err);
  const http = message.match(/^Slack HTTP (\d{3})/);
  if (http) {
    const status = Number(http[1]);
    return status >= 500 || status === 429;
  }
  if (/^[a-z][a-z0-9_]*$/.test(message)) {
    return RETRYABLE_SLACK_ERRORS.has(message);
  }
  return true;
}

/** Run `fn`, retrying transient failures every `intervalMs` until `deadline`. */
export async function retryWhileTransient<T>(input: {
  what: string;
  fn: () => Promise<T>;
  deadline: number;
  intervalMs: number;
  log?: (message: string) => void;
}): Promise<T> {
  let warned = false;
  for (;;) {
    try {
      return await input.fn();
    } catch (err) {
      const remaining = input.deadline - Date.now();
      if (!isTransientWatchError(err) || remaining <= 0) {
        throw err;
      }
      if (!warned) {
        warned = true;
        input.log?.(
          `${input.what} failed (${errorText(err)}); retrying every ${Math.round(input.intervalMs / 1000)}s until the timeout.`,
        );
      }
      await new Promise((r) => setTimeout(r, Math.min(input.intervalMs, remaining)));
    }
  }
}

function tsAfter(a: string, b: string): boolean {
  return Number.parseFloat(a) > Number.parseFloat(b);
}

function nowTs(): string {
  return (Date.now() / 1000).toFixed(6);
}

/** Whether a live RTM event could be a new message in scope (cheap pre-filter before re-fetching). */
export function rtmEventInScope(event: RtmMessageEvent, scope: WatchScope): boolean {
  if (event.channel !== scope.channelId) {
    return false;
  }
  // Edits, deletes and the parent's reply-count bump aren't new messages.
  if (
    event.subtype === "message_changed" ||
    event.subtype === "message_deleted" ||
    event.subtype === "message_replied"
  ) {
    return false;
  }
  const isReply = Boolean(event.thread_ts && event.thread_ts !== event.ts);
  if (scope.threadTs && event.thread_ts === scope.threadTs) {
    return true;
  }
  return scope.topLevel && !isReply;
}

/**
 * Wait until `fetchNew` returns messages newer than `after` that pass `accept`.
 * `subscribe` wires a push channel (RTM) that calls `trigger` when something may
 * have arrived; while it's unavailable we fall back to polling every `pollIntervalMs`.
 * Every (re)connect triggers a catch-up fetch, so nothing posted between the
 * caller's last read and the socket coming up is missed. Transient fetch failures
 * (e.g. the laptop is asleep) are retried every `pollIntervalMs` until the timeout.
 */
export async function waitForNewMessages(input: {
  after: string;
  fetchNew: (after: string) => Promise<SlackMessageSummary[]>;
  accept: (m: SlackMessageSummary) => boolean;
  subscribe?: (handlers: {
    trigger: () => void;
    setLive: (live: boolean) => void;
  }) => Promise<() => void>;
  timeoutMs: number;
  settleMs: number;
  pollIntervalMs: number;
  livePollIntervalMs?: number;
  log?: (message: string) => void;
}): Promise<{ messages: SlackMessageSummary[]; timedOut: boolean }> {
  const livePollIntervalMs = input.livePollIntervalMs ?? 60_000;
  let live = false;
  let wake: (() => void) | undefined;
  let pending = true; // start with a catch-up fetch
  const trigger = (): void => {
    pending = true;
    wake?.();
  };

  let unsubscribe: (() => void) | undefined;
  if (input.subscribe) {
    try {
      unsubscribe = await input.subscribe({
        trigger,
        setLive: (value) => {
          live = value;
          if (value) {
            trigger();
          }
        },
      });
    } catch (err) {
      input.log?.(
        `Live updates unavailable (${err instanceof Error ? err.message : String(err)}); polling every ${Math.round(input.pollIntervalMs / 1000)}s.`,
      );
    }
  }

  const deadline = Date.now() + input.timeoutMs;
  let offline = false;
  // undefined means the fetch failed transiently; other errors still throw.
  const fetchMatching = async (): Promise<SlackMessageSummary[] | undefined> => {
    let fetched: SlackMessageSummary[];
    try {
      fetched = await input.fetchNew(input.after);
    } catch (err) {
      if (!isTransientWatchError(err)) {
        throw err;
      }
      if (!offline) {
        offline = true;
        input.log?.(
          `Fetching new messages failed (${errorText(err)}); retrying every ${Math.round(input.pollIntervalMs / 1000)}s until the timeout.`,
        );
      }
      return undefined;
    }
    if (offline) {
      offline = false;
      input.log?.("Fetching new messages works again.");
    }
    return fetched.filter((m) => tsAfter(m.ts, input.after) && input.accept(m));
  };

  try {
    for (;;) {
      if (pending) {
        pending = false;
        const found = await fetchMatching();
        if (found && found.length > 0) {
          // People often send a reply as several quick messages; wait briefly to gather them.
          const settle = Math.min(input.settleMs, Math.max(0, deadline - Date.now()));
          if (settle > 0) {
            await new Promise((r) => setTimeout(r, settle));
            const settled = await fetchMatching();
            return { messages: settled && settled.length > 0 ? settled : found, timedOut: false };
          }
          return { messages: found, timedOut: false };
        }
      }
      const remaining = deadline - Date.now();
      if (remaining <= 0) {
        return { messages: [], timedOut: true };
      }
      const interval = live && !offline ? livePollIntervalMs : input.pollIntervalMs;
      await new Promise<void>((resolve) => {
        const timer = setTimeout(
          () => {
            pending = true;
            resolve();
          },
          Math.min(interval, remaining),
        );
        wake = () => {
          clearTimeout(timer);
          resolve();
        };
      });
      wake = undefined;
    }
  } finally {
    unsubscribe?.();
  }
}

/** Keep an RTM socket open for the duration of a watch, reconnecting with backoff. */
function subscribeRtm(input: {
  ctx: CliContext;
  workspaceUrl: string | undefined;
  scope: WatchScope;
  log: (message: string) => void;
}): Parameters<typeof waitForNewMessages>[0]["subscribe"] {
  return async ({ trigger, setLive }) => {
    const { client, auth } = await input.ctx.getClientForWorkspace(input.workspaceUrl);
    let stopped = false;
    let conn: RtmConnection | undefined;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let failures = 0;

    const connect = async (): Promise<void> => {
      conn = await openRtm({
        client,
        auth,
        onReady: () => {
          failures = 0;
          setLive(true);
        },
        onMessage: (event) => {
          if (rtmEventInScope(event, input.scope)) {
            trigger();
          }
        },
        onClose: (reason) => {
          setLive(false);
          if (stopped) {
            return;
          }
          const delayMs = Math.min(60_000, 2000 * 2 ** failures++);
          input.log(`Live connection lost (${reason}); reconnecting in ${delayMs / 1000}s.`);
          retryTimer = setTimeout(() => {
            connect().catch((err: unknown) => {
              input.log(
                `Reconnect failed (${err instanceof Error ? err.message : String(err)}); polling until the next attempt.`,
              );
              conn = undefined;
              retryTimer = setTimeout(() => void connect().catch(() => {}), 60_000);
            });
          }, delayMs);
        },
      });
    };

    await connect();
    return () => {
      stopped = true;
      clearTimeout(retryTimer);
      conn?.close();
    };
  };
}

async function resolveWatchScope(input: {
  ctx: CliContext;
  targetInput: string;
  options: MessageWatchOptions;
}): Promise<{ scope: WatchScope; after: string; workspaceUrl: string | undefined }> {
  const target = parseMsgTarget(input.targetInput);
  const flagWorkspace = input.ctx.effectiveWorkspaceUrl(input.options.workspace);
  const workspaceUrl = targetWorkspaceUrl(target) ?? flagWorkspace;
  const { client, workspace_url } = await input.ctx.getClientForWorkspace(workspaceUrl);

  let channelId: string;
  let messageTs: string | undefined;
  let threadTsHint = input.options.threadTs?.trim() || undefined;
  if (target.kind === "url") {
    channelId = target.ref.channel_id;
    messageTs = target.ref.message_ts;
    threadTsHint ??= target.ref.thread_ts_hint;
  } else if (target.kind === "user") {
    channelId = await openDmChannel(client, target.userId);
    messageTs = input.options.ts?.trim() || undefined;
  } else {
    await input.ctx.assertWorkspaceSpecifiedForChannelNames({
      workspaceUrl,
      channels: [target.channel],
    });
    channelId = await resolveChannelId(client, target.channel);
    messageTs = input.options.ts?.trim() || undefined;
  }

  // In DMs and group DMs people answer at the top level as often as in the thread.
  const info = await client.api("conversations.info", { channel: channelId }).catch(() => null);
  const ch = info && isRecord(info.channel) ? info.channel : undefined;
  const isDm = Boolean(ch && (ch.is_im === true || ch.is_mpim === true));

  let threadTs = threadTsHint;
  if (!threadTs && messageTs) {
    const msg = await fetchMessage(client, {
      ref: {
        workspace_url: workspace_url ?? workspaceUrl ?? "",
        channel_id: channelId,
        message_ts: messageTs,
        raw: input.targetInput,
      },
    });
    threadTs = msg.thread_ts ?? msg.ts;
  }

  const after = input.options.after?.trim() || messageTs || threadTs || nowTs();
  return {
    scope: { channelId, threadTs, topLevel: !threadTs || isDm },
    after,
    workspaceUrl: workspace_url ?? workspaceUrl,
  };
}

export async function handleMessageWatch(input: {
  ctx: CliContext;
  targetInput: string;
  options: MessageWatchOptions;
  log?: (message: string) => void;
}): Promise<{ payload: Record<string, unknown>; timedOut: boolean }> {
  const log = input.log ?? ((m: string) => console.error(m));
  const timeoutMs = parseWatchDuration(input.options.timeout ?? "30m", "--timeout");
  const settleMs = parseWatchDuration(input.options.settle ?? "3s", "--settle");
  const pollIntervalMs = Math.max(
    5000,
    parseWatchDuration(input.options.pollInterval ?? "15s", "--poll-interval"),
  );

  // The timeout covers startup too: when started offline, keep retrying until it runs out.
  const deadline = Date.now() + timeoutMs;
  const retry = <T>(what: string, fn: () => Promise<T>): Promise<T> =>
    retryWhileTransient({ what, fn, deadline, intervalMs: pollIntervalMs, log });

  const { scope, after, workspaceUrl } = await retry("Resolving the watch target", () =>
    resolveWatchScope(input),
  );
  const { client, auth } = await input.ctx.getClientForWorkspace(workspaceUrl);
  // Own messages count by default: the agent and its human often share one account,
  // and the agent's own message is already excluded by `after`.
  const self = input.options.excludeSelf
    ? getString((await retry("auth.test", () => client.api("auth.test"))).user_id)
    : undefined;

  // Blocking with no output looks like a hang, so say what we're waiting for.
  const where = [
    scope.threadTs ? `thread ${scope.threadTs}` : undefined,
    scope.topLevel ? "top level" : undefined,
  ]
    .filter(Boolean)
    .join(" + ");
  const since = new Date(Number.parseFloat(after) * 1000).toLocaleString();
  log(
    `Waiting up to ${input.options.timeout ?? "30m"} for messages in ${scope.channelId} (${where}) posted after ${since}; exits ${WATCH_TIMEOUT_EXIT_CODE} on timeout.`,
  );

  const fetchNew = async (since: string): Promise<SlackMessageSummary[]> => {
    const out: SlackMessageSummary[] = [];
    if (scope.threadTs) {
      out.push(
        ...(await fetchThread(client, { channelId: scope.channelId, threadTs: scope.threadTs })),
      );
    }
    if (scope.topLevel) {
      const history = await fetchChannelHistory(client, {
        channelId: scope.channelId,
        oldest: since,
        limit: 200,
      });
      // History also shows broadcast thread replies; keep only true top-level posts.
      out.push(...history.filter((m) => !m.thread_ts || m.thread_ts === m.ts));
    }
    const seen = new Set<string>();
    return out
      .filter((m) => (seen.has(m.ts) ? false : (seen.add(m.ts), true)))
      .sort((a, b) => Number.parseFloat(a.ts) - Number.parseFloat(b.ts));
  };

  const result = await waitForNewMessages({
    after,
    fetchNew,
    accept: (m) => !self || m.user !== self,
    subscribe: input.options.noRtm
      ? undefined
      : subscribeRtm({ ctx: input.ctx, workspaceUrl, scope, log }),
    timeoutMs: Math.max(0, deadline - Date.now()),
    settleMs,
    pollIntervalMs,
    log,
  });

  // Best-effort extras: if the network drops again here, still print the replies.
  const maxBodyChars = Number.parseInt(input.options.maxBodyChars, 10);
  const downloadedPaths = await downloadMessageFiles({ auth, messages: result.messages }).catch(
    (err: unknown) => {
      log(`Downloading attached files failed (${errorText(err)}).`);
      return {};
    },
  );
  const referencedUserIds = collectReferencedUserIds(result.messages, { includeReactions: false });
  const usersById = input.options.resolveUsers
    ? await resolveUsersById({
        client,
        workspaceUrl: workspaceUrl ?? "",
        userIds: referencedUserIds,
      }).catch((err: unknown) => {
        log(`Resolving users failed (${errorText(err)}).`);
        return new Map();
      })
    : new Map();
  const payload = pruneEmpty(
    {
      channel_id: scope.channelId,
      thread_ts: scope.threadTs,
      after,
      timed_out: result.timedOut || undefined,
      messages: result.messages.map((m) => {
        const compact = toCompactMessage(m, { maxBodyChars, downloadedPaths });
        return m.thread_ts === scope.threadTs ? toThreadListMessage(compact) : compact;
      }),
      referenced_users: toReferencedUsers(referencedUserIds, usersById),
    },
    { keepEmptyArrays: ["messages"] },
  ) as Record<string, unknown>;
  return { payload, timedOut: result.timedOut };
}
