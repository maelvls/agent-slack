import type { CliContext } from "./context.ts";
import { parseMsgTarget } from "./targets.ts";
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
 * caller's last read and the socket coming up is missed.
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
  const fetchMatching = async (): Promise<SlackMessageSummary[]> =>
    (await input.fetchNew(input.after)).filter(
      (m) => tsAfter(m.ts, input.after) && input.accept(m),
    );

  try {
    for (;;) {
      if (pending) {
        pending = false;
        const found = await fetchMatching();
        if (found.length > 0) {
          // People often send a reply as several quick messages; wait briefly to gather them.
          const settle = Math.min(input.settleMs, Math.max(0, deadline - Date.now()));
          if (settle > 0) {
            await new Promise((r) => setTimeout(r, settle));
            const settled = await fetchMatching();
            return { messages: settled.length > 0 ? settled : found, timedOut: false };
          }
          return { messages: found, timedOut: false };
        }
      }
      const remaining = deadline - Date.now();
      if (remaining <= 0) {
        return { messages: [], timedOut: true };
      }
      const interval = live ? livePollIntervalMs : input.pollIntervalMs;
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
  const workspaceUrl = target.kind === "url" ? target.ref.workspace_url : flagWorkspace;
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

  const { scope, after, workspaceUrl } = await resolveWatchScope(input);
  const { client, auth } = await input.ctx.getClientForWorkspace(workspaceUrl);
  // Own messages count by default: the agent and its human often share one account,
  // and the agent's own message is already excluded by `after`.
  const self = input.options.excludeSelf
    ? getString((await client.api("auth.test")).user_id)
    : undefined;

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
    timeoutMs,
    settleMs,
    pollIntervalMs,
    log,
  });

  const maxBodyChars = Number.parseInt(input.options.maxBodyChars, 10);
  const downloadedPaths = await downloadMessageFiles({ auth, messages: result.messages });
  const referencedUserIds = collectReferencedUserIds(result.messages, { includeReactions: false });
  const usersById = input.options.resolveUsers
    ? await resolveUsersById({
        client,
        workspaceUrl: workspaceUrl ?? "",
        userIds: referencedUserIds,
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
