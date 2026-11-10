import { asArray, getNumber, getString, isRecord } from "../lib/object-type-guards.ts";
import type { SlackApiClient } from "./client.ts";

/**
 * True for Slack's `*_is_restricted` errors (`team_is_restricted`,
 * `enterprise_is_restricted`, ...). On Enterprise Grid, browser/session
 * tokens get these from `client.counts` and friends.
 */
export function isRestrictedError(err: unknown): boolean {
  const codes: string[] = [];
  if (err instanceof Error) {
    codes.push(err.message);
  }
  if (isRecord(err) && isRecord(err.data)) {
    const code = getString(err.data.error);
    if (code) {
      codes.push(code);
    }
  }
  return codes.some((code) => /\b[a-z_]+_is_restricted\b/.test(code));
}

export type UsersCountsEntry = {
  id: string;
  type: "channel" | "mpim" | "dm";
  unread_count: number;
  mention_count: number;
  is_member: boolean;
  is_muted: boolean;
  is_archived: boolean;
};

/**
 * Read `users.counts`, the sidebar-counts method that still works on
 * Enterprise Grid when `client.counts` is restricted. It has no `last_read`
 * (use `conversations.info` for that). Public and private channels come back
 * in `channels`/`groups` (group DMs are `groups` with `is_mpim`), DMs in `ims`
 * with `dm_count` as their unread count.
 */
export async function fetchUsersCountsEntries(client: SlackApiClient): Promise<UsersCountsEntry[]> {
  const resp = await client.api("users.counts", {});
  const out: UsersCountsEntry[] = [];
  for (const raw of [...asArray(resp.channels), ...asArray(resp.groups)]) {
    if (!isRecord(raw)) {
      continue;
    }
    const id = getString(raw.id);
    if (!id) {
      continue;
    }
    out.push({
      id,
      type: raw.is_mpim === true ? "mpim" : "channel",
      unread_count: getNumber(raw.unread_count_display) ?? getNumber(raw.unread_count) ?? 0,
      mention_count: getNumber(raw.mention_count_display) ?? getNumber(raw.mention_count) ?? 0,
      // users.counts only lists joined channels; an mpim may carry is_open instead.
      is_member: raw.is_member !== false || raw.is_open === true,
      is_muted: raw.is_muted === true,
      is_archived: raw.is_archived === true,
    });
  }
  for (const raw of asArray(resp.ims)) {
    if (!isRecord(raw)) {
      continue;
    }
    const id = getString(raw.id);
    if (!id) {
      continue;
    }
    const dmCount = getNumber(raw.dm_count) ?? 0;
    out.push({
      id,
      type: "dm",
      unread_count: dmCount,
      mention_count: dmCount,
      is_member: raw.is_open === true,
      is_muted: false,
      is_archived: false,
    });
  }
  return out;
}

/** Entries the sidebar would flag: unread or mentioned, not archived, muted only with mentions. */
export function hasUsersCountsUnreads(entry: UsersCountsEntry): boolean {
  if (entry.is_archived) {
    return false;
  }
  if (entry.mention_count > 0) {
    return true;
  }
  return entry.unread_count > 0 && !entry.is_muted;
}
