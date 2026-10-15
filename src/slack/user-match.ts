import type { CompactSlackUser } from "./users.ts";

const AMBIGUOUS_CANDIDATES_SHOWN = 10;

export function matchesUserQuery(user: CompactSlackUser, terms: string[]): boolean {
  const fields = [user.name, user.real_name, user.display_name, user.email, user.title]
    .filter((f): f is string => Boolean(f))
    .map(normalizeForMatch);
  return terms.every((term) => fields.some((f) => f.includes(term)));
}

/**
 * Keep users matching every query term, best matches first: exact handle or name,
 * then exact email, then matches at word starts in names, then any substring match.
 * Deactivated users sort after active ones with the same score.
 */
export function rankUserMatches(users: CompactSlackUser[], query: string): CompactSlackUser[] {
  const terms = queryTerms(query);
  const normalized = normalizeForMatch(stripAt(query.trim()));
  const seen = new Set<string>();
  const scored: { user: CompactSlackUser; score: number; index: number }[] = [];
  for (const [index, user] of users.entries()) {
    if (seen.has(user.id) || !matchesUserQuery(user, terms)) {
      continue;
    }
    seen.add(user.id);
    scored.push({ user, score: matchScore(user, { normalized, terms }), index });
  }
  scored.sort(
    (a, b) =>
      a.score - b.score ||
      Number(Boolean(a.user.deleted)) - Number(Boolean(b.user.deleted)) ||
      a.index - b.index,
  );
  return scored.map((s) => s.user);
}

/**
 * Pick the one user an input refers to: exact handle (or email), then exact display
 * or real name. With allowPartial, fall back to users matching every term. Active
 * users win over deactivated ones; throws when several remain.
 */
export function pickUserMatch(
  users: CompactSlackUser[],
  input: { name: string; allowPartial?: boolean },
): CompactSlackUser | null {
  const needle = stripAt(input.name.trim());
  const lower = needle.toLowerCase();
  const byHandle = users.find((u) => u.name?.toLowerCase() === lower);
  if (byHandle) {
    return byHandle;
  }
  if (looksLikeEmail(needle)) {
    const byEmail = users.find((u) => u.email?.toLowerCase() === lower);
    if (byEmail) {
      return byEmail;
    }
  }

  const normalized = normalizeForMatch(needle);
  const exact = users.filter((u) =>
    [u.display_name, u.real_name].some((f) => f && normalizeForMatch(f) === normalized),
  );
  if (exact.length > 0 || !input.allowPartial) {
    return pickUnique(exact, input.name);
  }
  const terms = queryTerms(needle);
  return pickUnique(
    users.filter((u) => matchesUserQuery(u, terms)),
    input.name,
  );
}

export function queryTerms(query: string): string[] {
  return normalizeForMatch(query).split(/\s+/).map(stripAt).filter(Boolean);
}

export function normalizeForMatch(value: string): string {
  return value
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .trim();
}

export function stripAt(value: string): string {
  return value.startsWith("@") ? value.slice(1) : value;
}

export function looksLikeEmail(value: string): boolean {
  return /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(value);
}

function matchScore(
  user: CompactSlackUser,
  query: { normalized: string; terms: string[] },
): number {
  const names = [user.name, user.real_name, user.display_name]
    .filter((f): f is string => Boolean(f))
    .map(normalizeForMatch);
  if (names.includes(query.normalized)) {
    return 0;
  }
  const email = user.email ? normalizeForMatch(user.email) : "";
  if (email && (email === query.normalized || email.split("@")[0] === query.normalized)) {
    return 1;
  }
  if (query.terms.every((term) => names.some((name) => startsWord(name, term)))) {
    return 2;
  }
  return 3;
}

function startsWord(haystack: string, term: string): boolean {
  let from = 0;
  for (;;) {
    const at = haystack.indexOf(term, from);
    if (at < 0) {
      return false;
    }
    if (at === 0 || !/[\p{L}\p{N}]/u.test(haystack[at - 1] ?? "")) {
      return true;
    }
    from = at + 1;
  }
}

function pickUnique(matches: CompactSlackUser[], input: string): CompactSlackUser | null {
  if (matches.length === 0) {
    return null;
  }
  const active = matches.filter((u) => !u.deleted);
  const pool = active.length > 0 ? active : matches;
  if (pool.length === 1) {
    return pool[0] ?? null;
  }
  throw ambiguousUserError(input, pool);
}

function ambiguousUserError(input: string, users: CompactSlackUser[]): Error {
  const lines = users.slice(0, AMBIGUOUS_CANDIDATES_SHOWN).map((u) => {
    const parts = [u.id, u.name ? `@${u.name}` : "", u.real_name ?? u.display_name ?? ""];
    if (u.title) {
      parts.push(`(${u.title})`);
    }
    return `  ${parts.filter(Boolean).join("  ")}`;
  });
  const hidden = users.length - AMBIGUOUS_CANDIDATES_SHOWN;
  if (hidden > 0) {
    lines.push(`  ...and ${hidden} more`);
  }
  return new Error(
    `Multiple users match "${input}":\n${lines.join("\n")}\n` +
      `Pass a user ID or @handle instead, or narrow it down with: agent-slack user search "${input}"`,
  );
}
