import { asArray, getNumber, getString, isRecord } from "../lib/object-type-guards.ts";
import type { SlackApiClient } from "./client.ts";
import { isSlackRateLimitError } from "./rate-limit.ts";
import { renderSlackMessageContent } from "./render.ts";
import { resolveUsersById } from "./user-cache.ts";
import { isUserId } from "./user-id.ts";
import {
  buildPermalink,
  createLimiter,
  isFromOther,
  isHuman,
  type Limiter,
  type Msg,
  reactedBy,
  searchMatches,
  sortByTs,
  threadTsFromPermalink,
} from "./inbox-support.ts";

export type InboxKind = "dm" | "mpim" | "mention" | "thread";

export type InboxItem = {
  kind: InboxKind;
  channel_id: string;
  channel_name?: string;
  thread_ts?: string;
  ts: string;
  first_ts: string;
  count: number;
  author: { user_id: string; name?: string };
  text: string;
  permalink?: string;
};

export type InboxResult = {
  user_id: string;
  since: string;
  truncated: boolean;
  items: InboxItem[];
};

export type InboxOptions = {
  since: Date;
  /** Default 300; negative means unlimited. */
  maxBodyChars?: number;
  /** Cap on conversations/threads inspected with history/replies calls. Default 60. */
  maxConversations?: number;
  /** Include "threads you've posted in". Default true. */
  includeThreads?: boolean;
  /** Parallel API calls. Default 4. */
  concurrency?: number;
};

const HISTORY_LIMIT = 30;
const REPLIES_LIMIT = 200;
const REPLIES_PAGES = 5;
const CHANNEL_ANSWER_LIMIT = 50;
const DAY_MS = 24 * 60 * 60 * 1000;

type Candidate = {
  channelId: string;
  channelName?: string;
  imUser?: string;
  convKind: "dm" | "mpim" | "channel";
  /** Thread root; undefined for a top-level DM/mpim conversation. */
  root?: string;
  mentionTs: string[];
  /** Found by searching my own thread posts. */
  mine: boolean;
  newest: number;
};

type Draft = Omit<InboxItem, "author" | "channel_name"> & {
  authorId: string;
  imUser?: string;
  channelName?: string;
};

/** Parse `Nd`, `Nh`, or `YYYY-MM-DD` (UTC midnight). */
export function parseSince(input: string, now: Date = new Date()): Date {
  const value = input.trim();
  const rel = /^(\d+)([dh])$/.exec(value);
  if (rel) {
    const n = Number(rel[1]);
    return new Date(now.getTime() - n * (rel[2] === "d" ? DAY_MS : DAY_MS / 24));
  }
  if (/^\d{4}-\d{2}-\d{2}$/.test(value)) {
    const date = new Date(`${value}T00:00:00.000Z`);
    if (!Number.isNaN(date.getTime())) {
      return date;
    }
  }
  throw new Error(`Invalid --since "${input}": expected Nd, Nh, or YYYY-MM-DD`);
}

export async function fetchInbox(
  client: SlackApiClient,
  options: InboxOptions,
): Promise<InboxResult> {
  const maxBodyChars = options.maxBodyChars ?? 300;
  const maxConversations = options.maxConversations ?? 60;
  const includeThreads = options.includeThreads ?? true;
  const limit = createLimiter(options.concurrency ?? 4);
  const sinceTs = options.since.getTime() / 1000;

  const auth = await client.api("auth.test");
  const me = getString(auth.user_id);
  if (!me) {
    throw new Error("auth.test returned no user_id");
  }
  const baseUrl = (getString(auth.url) ?? client.getWorkspaceUrl?.() ?? "").replace(/\/+$/, "");

  // Slack's after: is an exclusive local date; two days back covers any timezone.
  const after = new Date(options.since.getTime() - 2 * DAY_MS).toISOString().slice(0, 10);
  const search = (query: string) =>
    searchMatches({ client, limit, query: `${query} after:${after}` });
  const [toMe, mentions, myThreads] = await Promise.all([
    search(`to:<@${me}>`),
    search(`<@${me}>`),
    includeThreads
      ? search(`is:thread from:<@${me}>`)
      : Promise.resolve({ matches: [], capped: false }),
  ]);

  const candidates = new Map<string, Candidate>();
  const add = (m: Msg, how: "to" | "mention" | "mine") => {
    const ch = isRecord(m.channel) ? m.channel : null;
    const channelId = ch ? getString(ch.id) : undefined;
    const ts = getString(m.ts);
    if (!ch || !channelId || !ts) {
      return;
    }
    if (how !== "mine" && (Number(ts) < sinceTs || getString(m.user) === me)) {
      return;
    }
    const isIm = ch.is_im === true;
    const name = getString(ch.name);
    const imUser = isIm
      ? (getString(ch.user) ?? (name && isUserId(name) ? name : undefined))
      : undefined;
    if (isIm && imUser === me) {
      return; // self-DM
    }
    const convKind = isIm ? "dm" : ch.is_mpim === true ? "mpim" : "channel";
    const threadTs = threadTsFromPermalink(getString(m.permalink));
    let root: string | undefined;
    if (how === "mine" || convKind === "channel") {
      root = threadTs ?? ts;
    } else {
      // DM/mpim: top-level messages are judged per conversation, replies per thread.
      root = threadTs && threadTs !== ts ? threadTs : undefined;
    }
    const key = root ? `${channelId}:${root}` : channelId;
    const c = candidates.get(key) ?? {
      channelId,
      channelName: isIm ? undefined : name,
      imUser,
      convKind,
      root,
      mentionTs: [],
      mine: false,
      newest: 0,
    };
    if (how === "mention" && convKind === "channel") {
      c.mentionTs.push(ts);
    }
    if (how === "mine") {
      c.mine = true;
    }
    c.newest = Math.max(c.newest, Number(ts));
    candidates.set(key, c);
  };
  toMe.matches.forEach((m) => add(m, "to"));
  mentions.matches.forEach((m) => add(m, "mention"));
  myThreads.matches.forEach((m) => add(m, "mine"));

  const ordered = [...candidates.values()].sort((a, b) => b.newest - a.newest);
  const inspected = ordered.slice(0, Math.max(0, maxConversations));
  const truncated =
    ordered.length > inspected.length || toMe.capped || mentions.capped || myThreads.capped;

  const ctx = { client, limit, me, sinceTs, baseUrl, maxBodyChars };
  const drafts = (await Promise.all(inspected.map((c) => evaluate(ctx, c)))).filter(
    (d): d is Draft => d !== null,
  );

  const names = await resolveNames(client, {
    workspaceUrl: baseUrl,
    ids: drafts.flatMap((d) => [d.authorId, d.imUser ?? ""]),
  });
  const items = drafts
    .sort((a, b) => Number(b.ts) - Number(a.ts))
    .map(({ authorId, imUser, channelName, ...rest }): InboxItem => {
      const item: InboxItem = {
        kind: rest.kind,
        channel_id: rest.channel_id,
        channel_name: imUser ? (names.get(imUser) ?? imUser) : channelName,
        thread_ts: rest.thread_ts,
        ts: rest.ts,
        first_ts: rest.first_ts,
        count: rest.count,
        author: { user_id: authorId, name: names.get(authorId) },
        text: rest.text,
        permalink: rest.permalink,
      };
      return item;
    });

  return { user_id: me, since: options.since.toISOString(), truncated, items };
}

type EvalContext = {
  client: SlackApiClient;
  limit: Limiter;
  me: string;
  sinceTs: number;
  baseUrl: string;
  maxBodyChars: number;
};

async function evaluate(ctx: EvalContext, c: Candidate): Promise<Draft | null> {
  try {
    return await evaluateUnsafe(ctx, c);
  } catch (err) {
    if (isSlackRateLimitError(err)) {
      throw err;
    }
    return null; // channel_not_found, not_in_channel...: nothing we can act on
  }
}

async function evaluateUnsafe(ctx: EvalContext, c: Candidate): Promise<Draft | null> {
  const { client, limit, me, sinceTs } = ctx;
  const msgs = c.root
    ? await fetchReplies(ctx, { channel: c.channelId, root: c.root })
    : await fetchHistory(ctx, c.channelId);

  // Anything I posted, reacted to, or (top-level) replied to in a thread
  // acknowledges everything up to it.
  let ack = 0;
  let posted = false;
  for (const m of msgs) {
    const ts = Number(getString(m.ts));
    if (isHuman(m) && getString(m.user) === me) {
      ack = Math.max(ack, ts);
      posted = true;
    } else if (reactedBy(m, me) || (!c.root && asArray(m.reply_users).includes(me))) {
      ack = Math.max(ack, ts);
    }
  }
  const waiting = msgs.filter((m) => {
    const ts = Number(getString(m.ts));
    return ts > ack && ts >= sinceTs && isFromOther(m, me);
  });
  if (waiting.length === 0) {
    return null;
  }

  let kind: InboxKind;
  if (c.convKind !== "channel") {
    kind = c.convKind;
  } else if (c.mentionTs.some((ts) => Number(ts) > ack)) {
    kind = "mention";
  } else if (c.mine || posted) {
    kind = "thread";
  } else {
    return null;
  }

  if (kind === "mention" && msgs.length <= 1 && !getNumber(msgs[0]?.reply_count)) {
    // Top-level mention nobody replied to: a later message of mine in the
    // channel counts as the answer.
    const mentionTs = c.mentionTs.reduce((a, b) => (Number(a) > Number(b) ? a : b));
    const later = await limit(() =>
      client.api("conversations.history", {
        channel: c.channelId,
        oldest: mentionTs,
        inclusive: false,
        limit: CHANNEL_ANSWER_LIMIT,
      }),
    );
    if (
      asArray(later.messages).some((m) => isRecord(m) && isHuman(m) && getString(m.user) === me)
    ) {
      return null;
    }
  }

  const newest = waiting.at(-1)!;
  const ts = getString(newest.ts)!;
  const rendered = renderSlackMessageContent(newest);
  const text =
    ctx.maxBodyChars >= 0 && rendered.length > ctx.maxBodyChars
      ? `${rendered.slice(0, ctx.maxBodyChars)}...`
      : rendered;
  return {
    kind,
    channel_id: c.channelId,
    channelName: c.channelName,
    imUser: c.imUser,
    thread_ts: c.root,
    ts,
    first_ts: getString(waiting[0]!.ts)!,
    count: waiting.length,
    authorId: getString(newest.user)!,
    text,
    permalink: ctx.baseUrl
      ? buildPermalink({ baseUrl: ctx.baseUrl, channel: c.channelId, ts, root: c.root })
      : undefined,
  };
}

/** Newest top-level messages since the window start, oldest first. */
async function fetchHistory(ctx: EvalContext, channel: string): Promise<Msg[]> {
  const resp = await ctx.limit(() =>
    ctx.client.api("conversations.history", {
      channel,
      oldest: String(ctx.sinceTs),
      inclusive: true,
      limit: HISTORY_LIMIT,
    }),
  );
  return sortByTs(asArray(resp.messages).filter(isRecord));
}

/** Thread messages from the window start on (root included when recent), oldest first. */
async function fetchReplies(
  ctx: EvalContext,
  thread: { channel: string; root: string },
): Promise<Msg[]> {
  const { channel, root } = thread;
  const out: Msg[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < REPLIES_PAGES; page++) {
    const resp = await ctx.limit(() =>
      ctx.client.api("conversations.replies", {
        channel,
        ts: root,
        oldest: String(ctx.sinceTs),
        inclusive: true,
        limit: REPLIES_LIMIT,
        cursor,
      }),
    );
    out.push(...asArray(resp.messages).filter(isRecord));
    const meta = isRecord(resp.response_metadata) ? resp.response_metadata : null;
    cursor = meta ? getString(meta.next_cursor) || undefined : undefined;
    if (!cursor || resp.has_more === false) {
      break;
    }
  }
  const seen = new Set<string>();
  return sortByTs(
    out.filter((m) => {
      const ts = getString(m.ts);
      if (!ts || seen.has(ts)) {
        return false;
      }
      seen.add(ts);
      return true;
    }),
  );
}

async function resolveNames(
  client: SlackApiClient,
  input: { workspaceUrl: string; ids: string[] },
): Promise<Map<string, string>> {
  const { workspaceUrl, ids } = input;
  const out = new Map<string, string>();
  try {
    const users = await resolveUsersById({ client, workspaceUrl, userIds: ids });
    for (const [id, u] of users) {
      const name = u.display_name || u.name || u.real_name;
      if (name) {
        out.set(id, name);
      }
    }
  } catch {
    // names are best effort
  }
  return out;
}
