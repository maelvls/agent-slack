import { join } from "node:path";
import { getAppDir } from "../lib/app-dir.ts";
import { readJsonFile, writeJsonFile } from "../lib/fs.ts";
import { isRecord } from "../lib/object-type-guards.ts";
import { hashWorkspaceUrl } from "./user-cache.ts";

// Channel names → IDs. Resolving a name costs a search.messages call, or worse,
// paging conversations.list across the whole workspace, which large Enterprise
// Grid orgs rate limit. Renames are rare, so a week-long TTL is safe enough;
// a stale entry at worst points at the renamed channel.
const CACHE_VERSION = 1;
const CHANNEL_TTL_MS = 7 * 24 * 60 * 60 * 1000;
const CHANNEL_ID_PATTERN = /^[CDG][A-Z0-9]{8,}$/;

type ChannelCacheEntry = {
  id: string;
  fetched_at: number;
};

type ChannelCacheFile = {
  version: number;
  entries: Record<string, ChannelCacheEntry>;
};

function cachePathFor(workspaceUrl: string | undefined): string | undefined {
  const key = hashWorkspaceUrl(workspaceUrl ?? "");
  return key === "unknown" ? undefined : join(getAppDir(), `channels-cache-${key}.json`);
}

export async function getCachedChannelId(
  workspaceUrl: string | undefined,
  name: string,
): Promise<string | undefined> {
  const path = cachePathFor(workspaceUrl);
  if (!path) {
    return undefined;
  }
  const entry = (await loadCache(path)).entries[name];
  return entry && Date.now() - entry.fetched_at < CHANNEL_TTL_MS ? entry.id : undefined;
}

export async function cacheChannelIds(
  workspaceUrl: string | undefined,
  idsByName: Map<string, string>,
): Promise<void> {
  const path = cachePathFor(workspaceUrl);
  if (!path || idsByName.size === 0) {
    return;
  }
  const now = Date.now();
  const file = await loadCache(path);
  const entries: Record<string, ChannelCacheEntry> = {};
  for (const [name, entry] of Object.entries(file.entries)) {
    if (now - entry.fetched_at < CHANNEL_TTL_MS) {
      entries[name] = entry;
    }
  }
  for (const [name, id] of idsByName) {
    if (name && CHANNEL_ID_PATTERN.test(id)) {
      entries[name] = { id, fetched_at: now };
    }
  }
  try {
    await writeJsonFile(path, { version: CACHE_VERSION, entries });
  } catch {
    // Cache writes are best effort.
  }
}

async function loadCache(path: string): Promise<ChannelCacheFile> {
  const file = await readJsonFile<ChannelCacheFile>(path).catch(() => null);
  if (!file || file.version !== CACHE_VERSION || !isRecord(file.entries)) {
    return { version: CACHE_VERSION, entries: {} };
  }

  const entries: Record<string, ChannelCacheEntry> = {};
  for (const [name, rawEntry] of Object.entries(file.entries)) {
    if (!isRecord(rawEntry)) {
      continue;
    }
    const id = typeof rawEntry.id === "string" ? rawEntry.id : undefined;
    const fetchedAt = typeof rawEntry.fetched_at === "number" ? rawEntry.fetched_at : undefined;
    if (!id || !CHANNEL_ID_PATTERN.test(id) || !fetchedAt) {
      continue;
    }
    entries[name] = { id, fetched_at: fetchedAt };
  }
  return { version: CACHE_VERSION, entries };
}
