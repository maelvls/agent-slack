import { asArray, getNumber, getString, isRecord } from "../lib/object-type-guards.ts";
import type { SlackApiClient } from "./client.ts";

export type Msg = Record<string, unknown>;

const SEARCH_PAGES = 2;
const SEARCH_PAGE_SIZE = 100;
// Subtypes that are a person talking; everything else (joins, topic, bots...) is noise.
const HUMAN_SUBTYPES = new Set(["thread_broadcast", "file_share", "me_message"]);

/** search.messages newest first, up to SEARCH_PAGES pages; `capped` when more pages exist. */
export async function searchMatches(input: {
  client: SlackApiClient;
  limit: Limiter;
  query: string;
}): Promise<{ matches: Msg[]; capped: boolean }> {
  const { client, limit, query } = input;
  const matches: Msg[] = [];
  for (let page = 1; page <= SEARCH_PAGES; page++) {
    const resp = await limit(() =>
      client.api("search.messages", {
        query,
        count: SEARCH_PAGE_SIZE,
        page,
        sort: "timestamp",
        sort_dir: "desc",
      }),
    );
    const messages = isRecord(resp.messages) ? resp.messages : {};
    matches.push(...asArray(messages.matches).filter(isRecord));
    const paging = isRecord(messages.paging) ? messages.paging : {};
    const pages = getNumber(paging.pages) ?? 1;
    if (page >= pages) {
      return { matches, capped: false };
    }
  }
  return { matches, capped: true };
}

export function isHuman(m: Msg): boolean {
  const subtype = getString(m.subtype);
  return !subtype || HUMAN_SUBTYPES.has(subtype);
}

export function isFromOther(m: Msg, me: string): boolean {
  const user = getString(m.user);
  return Boolean(user) && user !== me && user !== "USLACKBOT" && !m.bot_id && isHuman(m);
}

export function reactedBy(m: Msg, me: string): boolean {
  return asArray(m.reactions).some((r) => isRecord(r) && asArray(r.users).includes(me));
}

export function sortByTs(msgs: Msg[]): Msg[] {
  return msgs.sort((a, b) => Number(getString(a.ts)) - Number(getString(b.ts)));
}

export function threadTsFromPermalink(permalink: string | undefined): string | undefined {
  if (!permalink) {
    return undefined;
  }
  try {
    return new URL(permalink).searchParams.get("thread_ts") || undefined;
  } catch {
    return undefined;
  }
}

export function buildPermalink(input: {
  baseUrl: string;
  channel: string;
  ts: string;
  root?: string;
}): string {
  const { baseUrl, channel, ts, root } = input;
  const link = `${baseUrl}/archives/${channel}/p${ts.replace(".", "")}`;
  return root && root !== ts ? `${link}?thread_ts=${root}&cid=${channel}` : link;
}

export type Limiter = <T>(fn: () => Promise<T>) => Promise<T>;

export function createLimiter(max: number): Limiter {
  let active = 0;
  const queue: (() => void)[] = [];
  return async (fn) => {
    if (active >= max) {
      // The finishing call hands its slot over, so `active` stays put.
      await new Promise<void>((resolve) => queue.push(resolve));
    } else {
      active++;
    }
    try {
      return await fn();
    } finally {
      const next = queue.shift();
      if (next) {
        next();
      } else {
        active--;
      }
    }
  };
}
