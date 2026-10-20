import { join } from "node:path";
import { getAppDir } from "../lib/app-dir.ts";
import { readJsonFile, writeCompactJsonFile } from "../lib/fs.ts";
import { asArray, getString, isRecord } from "../lib/object-type-guards.ts";
import type { SlackApiClient } from "./client.ts";
import { hashWorkspaceUrl } from "./user-cache.ts";
import {
  looksLikeEmail,
  matchesUserQuery,
  pickUserMatch,
  queryTerms,
  rankUserMatches,
  stripAt,
} from "./user-match.ts";
import { toCompactUser, type CompactSlackUser } from "./users.ts";

const DIRECTORY_CACHE_VERSION = 1;
const DIRECTORY_TTL_MS = 24 * 60 * 60 * 1000;
const DIRECTORY_PAGE_SIZE = 1000;
// Enterprise Grid workspaces can have 40k+ members, and users.list gets rate limited
// after ~40 pages. Stop filling after this budget (well under the CLI watchdog) and
// resume from the saved cursor on the next run.
const DIRECTORY_FILL_BUDGET_MS = 15_000;
// edgeapi users/search returns at most this many results, whatever `count` says.
const EDGE_SEARCH_MAX_RESULTS = 30;

export type UserSearchResult = {
  users: CompactSlackUser[];
  count: number;
  has_more?: boolean;
};

type DirectoryFile = {
  version: number;
  fetched_at: number;
  complete: boolean;
  next_cursor?: string;
  users: CompactSlackUser[];
};

const loadedDirectories = new WeakMap<SlackApiClient, DirectoryFile>();

/**
 * Search workspace users by name, handle, email, or title. Every whitespace-separated
 * term must match one of those fields (case- and accent-insensitive).
 */
export async function searchUsers(
  client: SlackApiClient,
  input: { query: string; limit?: number; includeBots?: boolean; refresh?: boolean },
): Promise<UserSearchResult> {
  const query = input.query.trim();
  if (queryTerms(query).length === 0) {
    throw new Error("Search query is empty");
  }
  const requestedLimit = input.limit ?? Number.NaN;
  const limit = Number.isFinite(requestedLimit) ? Math.min(Math.max(requestedLimit, 1), 1000) : 20;
  const includeBots = input.includeBots ?? false;

  let candidates: CompactSlackUser[] = [];
  if (looksLikeEmail(query)) {
    const byEmail = await lookupUserByEmail(client, query);
    if (byEmail) {
      candidates = [byEmail];
    }
  }
  if (candidates.length === 0) {
    candidates =
      (await edgeCandidates(client, { query, includeBots })) ??
      (await loadUserDirectory(client, { refresh: input.refresh })).users;
  }

  const matched = rankUserMatches(
    candidates.filter((u) => includeBots || !u.is_bot),
    query,
  );
  const users = matched.slice(0, limit);
  return {
    users,
    count: users.length,
    ...(matched.length > limit ? { has_more: true } : {}),
  };
}

/**
 * Find one user by handle, display name, or real name, using Slack's server-side
 * search when available and the cached user directory otherwise. With allowPartial,
 * a query matching exactly one active user (e.g. "Jane Doe") is also accepted.
 * Throws when several users match equally well.
 */
export async function findUserByName(
  client: SlackApiClient,
  input: { name: string; allowPartial?: boolean },
): Promise<CompactSlackUser | null> {
  const name = stripAt(input.name.trim());
  if (!name) {
    return null;
  }
  const match = { name, allowPartial: input.allowPartial };

  // When the edge API answers, trust it: falling back to the directory would mean
  // paging users.list, which takes minutes on large Enterprise Grid workspaces.
  const edgeUsers = await edgeCandidates(client, { query: name, includeBots: true });
  if (edgeUsers) {
    return pickUserMatch(edgeUsers, match);
  }

  const directory = await loadUserDirectory(client);
  const found = pickUserMatch(directory.users, match);
  if (!found && !directory.complete) {
    throw new Error(
      `Could not find user "${input.name}" among the ${directory.users.length} users cached so far; ` +
        "the workspace user directory is still being cached. Re-run the command to continue.",
    );
  }
  return found;
}

/**
 * Load the workspace's users.list into a local cache (24h TTL, under the app dir).
 * Large directories are filled across several runs; `complete` is false until the
 * last page has been fetched.
 */
export async function loadUserDirectory(
  client: SlackApiClient,
  options?: { refresh?: boolean },
): Promise<{ users: CompactSlackUser[]; complete: boolean }> {
  const now = Date.now();
  const path = await directoryCachePath(client);
  let file: DirectoryFile | null = null;
  if (!options?.refresh) {
    file = loadedDirectories.get(client) ?? (path ? await readDirectoryFile(path) : null);
  }
  if (file && now - file.fetched_at >= DIRECTORY_TTL_MS) {
    file = null;
  }
  if (file?.complete) {
    loadedDirectories.set(client, file);
    return file;
  }

  const state: DirectoryFile = file ?? {
    version: DIRECTORY_CACHE_VERSION,
    fetched_at: now,
    complete: false,
    users: [],
  };
  console.error(
    state.users.length === 0
      ? "Caching the workspace user directory (refreshed every 24h)..."
      : `Resuming the workspace user directory cache (${state.users.length} users so far)...`,
  );
  await fillDirectory(client, { state, deadline: now + DIRECTORY_FILL_BUDGET_MS });
  if (!state.complete) {
    console.error(
      `Cached ${state.users.length} users so far; the directory is large, re-run to continue caching.`,
    );
  }

  loadedDirectories.set(client, state);
  if (path) {
    try {
      await writeCompactJsonFile(path, state);
    } catch {
      // Cache writes are best effort.
    }
  }
  return state;
}

async function fillDirectory(
  client: SlackApiClient,
  input: { state: DirectoryFile; deadline: number },
): Promise<void> {
  const { state } = input;
  for (;;) {
    let resp: Record<string, unknown>;
    try {
      resp = await client.api("users.list", {
        limit: DIRECTORY_PAGE_SIZE,
        cursor: state.next_cursor,
      });
    } catch (err) {
      if (state.users.length === 0) {
        throw err;
      }
      // Usually a rate limit; keep what we have and resume next time.
      return;
    }
    for (const m of asArray(resp.members).filter(isRecord)) {
      if (getString(m.id)) {
        state.users.push(toDirectoryUser(m));
      }
    }
    const meta = isRecord(resp.response_metadata) ? resp.response_metadata : null;
    const next = meta ? getString(meta.next_cursor) : undefined;
    if (!next) {
      state.complete = true;
      state.next_cursor = undefined;
      return;
    }
    state.next_cursor = next;
    if (Date.now() >= input.deadline) {
      return;
    }
  }
}

// Returns null when the edge API is unavailable (standard token, network or API
// error) so callers can fall back to the cached directory.
async function edgeCandidates(
  client: SlackApiClient,
  input: { query: string; includeBots: boolean },
): Promise<CompactSlackUser[] | null> {
  const rawTerms = Array.from(
    new Set(stripAt(input.query).split(/\s+/).map(stripAt).filter(Boolean)),
  );
  if (rawTerms.length === 0) {
    return [];
  }
  try {
    const users = await edgeSearchUsers(client, { ...input, query: rawTerms.join(" ") });
    const terms = queryTerms(input.query);
    if (rawTerms.length > 1 && !users.some((u) => matchesUserQuery(u, terms))) {
      // The edge index only covers names and handles. Search each term on its own so
      // that email or title terms can still narrow the results locally.
      for (const term of rawTerms) {
        users.push(...(await edgeSearchUsers(client, { ...input, query: term })));
      }
    }
    // A user found by several terms must count once, or matching reports them as ambiguous.
    return Array.from(new Map(users.map((u) => [u.id, u])).values());
  } catch {
    return null;
  }
}

async function edgeSearchUsers(
  client: SlackApiClient,
  input: { query: string; includeBots: boolean },
): Promise<CompactSlackUser[]> {
  const resp = await client.edgeApi("users/search", {
    query: input.query,
    count: EDGE_SEARCH_MAX_RESULTS,
    fuzz: 1,
    uax29_tokenizer: false,
    ...(input.includeBots ? {} : { filter: "NOT bots" }),
  });
  return asArray(resp.results)
    .filter(isRecord)
    .filter((m) => Boolean(getString(m.id)))
    .map(toDirectoryUser);
}

async function lookupUserByEmail(
  client: SlackApiClient,
  email: string,
): Promise<CompactSlackUser | null> {
  try {
    const resp = await client.api("users.lookupByEmail", { email });
    const user = isRecord(resp.user) ? resp.user : null;
    return user && getString(user.id) ? toDirectoryUser(user) : null;
  } catch {
    return null;
  }
}

async function directoryCachePath(client: SlackApiClient): Promise<string> {
  try {
    let workspaceUrl = client.getWorkspaceUrl();
    if (!workspaceUrl) {
      workspaceUrl = getString((await client.api("auth.test")).url);
    }
    const key = hashWorkspaceUrl(workspaceUrl ?? "");
    return key === "unknown" ? "" : join(getAppDir(), `users-directory-${key}.json`);
  } catch {
    // Without a workspace key the directory is only kept in memory.
    return "";
  }
}

async function readDirectoryFile(path: string): Promise<DirectoryFile | null> {
  let file: unknown;
  try {
    file = await readJsonFile<unknown>(path);
  } catch {
    return null;
  }
  if (
    !isRecord(file) ||
    file.version !== DIRECTORY_CACHE_VERSION ||
    typeof file.fetched_at !== "number" ||
    !Array.isArray(file.users)
  ) {
    return null;
  }
  return {
    version: DIRECTORY_CACHE_VERSION,
    fetched_at: file.fetched_at,
    complete: file.complete === true,
    next_cursor: getString(file.next_cursor),
    users: file.users.filter(
      (u): u is CompactSlackUser => isRecord(u) && typeof u.id === "string" && u.id !== "",
    ),
  };
}

// Status fields change often, so they are left out of cached and search results;
// `user get` returns them fresh.
function toDirectoryUser(raw: Record<string, unknown>): CompactSlackUser {
  const {
    status_text: _statusText,
    status_emoji: _statusEmoji,
    status_expiration: _statusExpiration,
    ...user
  } = toCompactUser(raw);
  return user;
}
