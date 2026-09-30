import type { SlackApiClient } from "./client.ts";
import { asArray, getNumber, getString, isRecord } from "../lib/object-type-guards.ts";

// huddles.history is an undocumented browser-client endpoint (the Huddles tab).
// It returns huddles newest-first by end date and caps pages at 100.
const MAX_PAGE_SIZE = 100;

export type CompactHuddle = {
  id: string;
  channel_id?: string;
  channel_type?: string;
  name?: string;
  created_by?: string;
  participants: string[];
  date_start?: number;
  date_end?: number;
  duration_seconds?: number;
  active?: boolean;
  thread_ts?: string;
  huddle_link?: string;
};

export async function fetchHuddles(
  client: SlackApiClient,
  options?: {
    limit?: number;
    after?: number;
    before?: number;
    cursor?: string;
    missedOnly?: boolean;
  },
): Promise<{ huddles: CompactHuddle[]; next_cursor?: string }> {
  const limit = options?.limit ?? 20;
  const huddles: CompactHuddle[] = [];
  let cursor = options?.cursor;

  while (huddles.length < limit) {
    // Ask only for what is still needed so next_cursor never skips unreturned items.
    const resp = await client.api("huddles.history", {
      limit: Math.min(limit - huddles.length, MAX_PAGE_SIZE),
      cursor,
      missed_huddles_only: options?.missedOnly ? true : undefined,
    });
    const page = asArray(resp.huddles).filter(isRecord).map(toCompactHuddle);
    const meta = isRecord(resp.response_metadata) ? resp.response_metadata : null;
    cursor = meta ? getString(meta.next_cursor) || undefined : undefined;

    let reachedAfter = false;
    for (const huddle of page) {
      const start = huddle.date_start ?? 0;
      if (options?.after !== undefined && start < options.after) {
        // Ongoing huddles sort first; ended ones are ordered by end date, so once an
        // ended huddle started before --after, every later one did too.
        if (!huddle.active) {
          reachedAfter = true;
        }
        continue;
      }
      if (options?.before !== undefined && start >= options.before) {
        continue;
      }
      huddles.push(huddle);
    }

    if (reachedAfter) {
      return { huddles };
    }
    if (!cursor || page.length === 0) {
      break;
    }
  }

  return { huddles, next_cursor: cursor };
}

function toCompactHuddle(raw: Record<string, unknown>): CompactHuddle {
  const participants = new Set<string>();
  for (const id of [...asArray(raw.participant_history), ...asArray(raw.participants)]) {
    const userId = getString(id);
    if (userId) {
      participants.add(userId);
    }
  }

  const dateStart = getNumber(raw.date_start) || undefined;
  const dateEnd = getNumber(raw.date_end) || undefined;
  const active = raw.has_ended === false;

  return {
    id: getString(raw.id) ?? "",
    channel_id: getString(asArray(raw.channels)[0]),
    channel_type: getString(raw.channel_type),
    name: getString(raw.name) || undefined,
    created_by: getString(raw.created_by),
    participants: Array.from(participants),
    date_start: dateStart,
    date_end: active ? undefined : dateEnd,
    duration_seconds:
      !active && dateStart !== undefined && dateEnd !== undefined ? dateEnd - dateStart : undefined,
    active: active || undefined,
    thread_ts: getString(raw.thread_root_ts) || undefined,
    huddle_link: getString(raw.huddle_link),
  };
}

// Parses YYYY-MM-DD as local midnight, in epoch seconds.
export function parseHuddleDate(raw: string, flag: string): number {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(raw.trim());
  if (!match) {
    throw new Error(`Invalid ${flag} value "${raw}": expected YYYY-MM-DD`);
  }
  const [, y, m, d] = match;
  const date = new Date(Number(y), Number(m) - 1, Number(d));
  if (date.getMonth() !== Number(m) - 1) {
    throw new Error(`Invalid ${flag} value "${raw}": not a real date`);
  }
  return Math.floor(date.getTime() / 1000);
}
