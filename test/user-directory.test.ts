import { afterEach, beforeEach, describe, expect, setSystemTime, spyOn, test } from "bun:test";
import { mkdtemp, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { resolveUserId as resolveSearchUserId } from "../src/slack/search-query.ts";
import { loadUserDirectory, searchUsers } from "../src/slack/user-directory.ts";
import { matchesUserQuery, rankUserMatches } from "../src/slack/user-match.ts";
import { getUser, resolveUserId, type CompactSlackUser } from "../src/slack/users.ts";

type ApiCall = { method: string; params: Record<string, unknown> };

const alice = { id: "U1", name: "alice", real_name: "Alice Doe", title: "Staff Engineer" };
const aliceRaw = {
  id: "U1",
  name: "alice",
  real_name: "Alice Doe",
  profile: { display_name: "Alice", email: "alice@example.com", title: "Staff Engineer" },
};
const malice = {
  id: "U2",
  name: "malice",
  real_name: "Mal Ice",
  profile: { display_name: "malice", title: "Product Manager" },
};
const janeA = { id: "U3", name: "jdoe", real_name: "Jane Doe", profile: { title: "Engineer" } };
const janeB = { id: "U4", name: "jdoe2", real_name: "Jane Doe", profile: { title: "Designer" } };
const janeDeleted = { id: "U5", name: "jane.doe", real_name: "Jane Doe", deleted: true };
const bot = { id: "U6", name: "alice-bot", real_name: "Alice Bot", is_bot: true };

function edgeClient(results: Record<string, unknown>[] | ((query: string) => unknown[])) {
  const apiCalls: ApiCall[] = [];
  const edgeCalls: Record<string, unknown>[] = [];
  const client = {
    getWorkspaceUrl: () => "https://edge-test.slack.com",
    api: async (method: string, params: Record<string, unknown> = {}) => {
      apiCalls.push({ method, params });
      if (method === "users.info") {
        return { ok: true, user: { id: params.user, name: `name-of-${String(params.user)}` } };
      }
      throw new Error(`unexpected api call ${method}`);
    },
    edgeApi: async (path: string, body: Record<string, unknown>) => {
      expect(path).toBe("users/search");
      edgeCalls.push(body);
      const query = String(body.query);
      return { ok: true, results: typeof results === "function" ? results(query) : results };
    },
  };
  return { client: client as never, apiCalls, edgeCalls };
}

function directoryClient(pages: Record<string, unknown>[][], workspaceUrl: string) {
  const calls: ApiCall[] = [];
  let failOnPage: number | undefined;
  const client = {
    getWorkspaceUrl: () => workspaceUrl,
    api: async (method: string, params: Record<string, unknown> = {}) => {
      calls.push({ method, params });
      if (method !== "users.list") {
        throw new Error(`unexpected api call ${method}`);
      }
      const page = params.cursor ? Number(params.cursor) : 0;
      if (page === failOnPage) {
        throw new Error("ratelimited");
      }
      const next = page + 1 < pages.length ? String(page + 1) : "";
      return { ok: true, members: pages[page] ?? [], response_metadata: { next_cursor: next } };
    },
    edgeApi: async () => {
      throw new Error("Slack edge API requires browser auth");
    },
  };
  return {
    client: client as never,
    calls,
    failOn: (page: number | undefined) => {
      failOnPage = page;
    },
  };
}

let appDir = "";
let originalXdg: string | undefined;
let errorSpy: ReturnType<typeof spyOn>;

beforeEach(async () => {
  originalXdg = process.env.XDG_RUNTIME_DIR;
  appDir = await mkdtemp(join(tmpdir(), "agent-slack-user-directory-"));
  process.env.XDG_RUNTIME_DIR = appDir;
  errorSpy = spyOn(console, "error").mockImplementation(() => {});
});

afterEach(async () => {
  setSystemTime();
  errorSpy.mockRestore();
  if (originalXdg === undefined) {
    delete process.env.XDG_RUNTIME_DIR;
  } else {
    process.env.XDG_RUNTIME_DIR = originalXdg;
  }
  await rm(appDir, { recursive: true, force: true });
});

describe("matchesUserQuery", () => {
  test("requires every term to match some field, including email and title", () => {
    const user: CompactSlackUser = { ...alice, email: "alice@example.com" };
    expect(matchesUserQuery(user, ["alice", "staff"])).toBe(true);
    expect(matchesUserQuery(user, ["example.com"])).toBe(true);
    expect(matchesUserQuery(user, ["alice", "manager"])).toBe(false);
  });
});

describe("rankUserMatches", () => {
  const users: CompactSlackUser[] = [
    { id: "U10", name: "xmalice", real_name: "Someone Else" },
    { id: "U11", name: "alicia", real_name: "Alicia Keys" },
    { id: "U12", name: "alice", real_name: "Alice Doe", deleted: true },
    { id: "U13", name: "adoe", real_name: "Alice", display_name: "Alice" },
    { id: "U14", name: "bob", real_name: "Bob" },
  ];

  test("drops non-matching users and ranks exact, then word-start, then substring", () => {
    expect(rankUserMatches(users, "alice").map((u) => u.id)).toEqual(["U13", "U12", "U10"]);
    expect(rankUserMatches(users, "ali").map((u) => u.id)).toEqual(["U11", "U13", "U12", "U10"]);
  });

  test("is case- and accent-insensitive and ignores a leading @", () => {
    const accented: CompactSlackUser[] = [{ id: "U20", name: "mvalais", real_name: "Maël Valais" }];
    expect(rankUserMatches(accented, "MAEL valais").map((u) => u.id)).toEqual(["U20"]);
    expect(rankUserMatches(accented, "@mvalais").map((u) => u.id)).toEqual(["U20"]);
  });
});

describe("searchUsers via the edge API", () => {
  test("uses server-side search without paging users.list", async () => {
    const { client, apiCalls, edgeCalls } = edgeClient([aliceRaw, malice]);

    const result = await searchUsers(client, { query: "Alice" });

    expect(apiCalls).toEqual([]);
    expect(edgeCalls[0]).toMatchObject({ query: "Alice", filter: "NOT bots" });
    expect(result.users.map((u) => u.id)).toEqual(["U1", "U2"]);
    expect(result.count).toBe(2);
    expect(result.has_more).toBeUndefined();
  });

  test("narrows by title by searching each term when the full query finds nothing", async () => {
    const { client, edgeCalls } = edgeClient((query) =>
      query === "doe" ? [janeA, janeB, janeDeleted] : [],
    );

    const result = await searchUsers(client, { query: "doe designer" });

    expect(edgeCalls.map((c) => c.query)).toEqual(["doe designer", "doe", "designer"]);
    expect(result.users.map((u) => u.id)).toEqual(["U4"]);
  });

  test("applies --limit, reports has_more, and keeps users when empty", async () => {
    const { client } = edgeClient([janeA, janeB, janeDeleted]);
    const limited = await searchUsers(client, { query: "jane", limit: 2 });
    expect(limited.users.map((u) => u.id)).toEqual(["U3", "U4"]);
    expect(limited.has_more).toBe(true);

    const none = await searchUsers(client, { query: "nobody" });
    expect(none).toEqual({ users: [], count: 0 });
  });

  test("excludes bots unless includeBots is set", async () => {
    const { client, edgeCalls } = edgeClient([aliceRaw, bot]);
    expect((await searchUsers(client, { query: "alice" })).users.map((u) => u.id)).toEqual(["U1"]);
    const withBots = await searchUsers(client, { query: "alice", includeBots: true });
    expect(withBots.users.map((u) => u.id)).toEqual(["U1", "U6"]);
    expect(edgeCalls[1]?.filter).toBeUndefined();
  });

  test("leaves status fields out of search results", async () => {
    const withStatus = {
      ...aliceRaw,
      profile: { ...aliceRaw.profile, status_text: "lunch", status_emoji: ":taco:" },
    };
    const { client } = edgeClient([withStatus]);
    const [user] = (await searchUsers(client, { query: "alice" })).users;
    expect(user?.status_text).toBeUndefined();
    expect(user?.email).toBe("alice@example.com");
  });
});

describe("user directory cache fallback", () => {
  test("fills from users.list at 1000 per page, then serves later lookups from disk", async () => {
    const first = directoryClient([[aliceRaw], [malice]], "https://dir-test.slack.com");
    const result = await searchUsers(first.client, { query: "alice" });
    expect(result.users.map((u) => u.id)).toEqual(["U1", "U2"]);
    expect(first.calls.map((c) => c.params.limit)).toEqual([1000, 1000]);
    expect(
      (await readdir(join(appDir, "agent-slack"))).some((f) => f.startsWith("users-directory-")),
    ).toBe(true);

    const second = directoryClient([[aliceRaw], [malice]], "https://dir-test.slack.com");
    expect((await searchUsers(second.client, { query: "malice" })).users.map((u) => u.id)).toEqual([
      "U2",
    ]);
    expect(second.calls).toEqual([]);
  });

  test("refetches after the 24h TTL and when refresh is requested", async () => {
    const url = "https://ttl-test.slack.com";
    setSystemTime(new Date("2026-01-01T00:00:00Z"));
    await loadUserDirectory(directoryClient([[aliceRaw]], url).client);

    setSystemTime(new Date("2026-01-01T23:00:00Z"));
    const fresh = directoryClient([[aliceRaw]], url);
    await loadUserDirectory(fresh.client);
    expect(fresh.calls).toEqual([]);

    const refreshed = directoryClient([[aliceRaw]], url);
    await loadUserDirectory(refreshed.client, { refresh: true });
    expect(refreshed.calls.length).toBe(1);

    setSystemTime(new Date("2026-01-03T00:00:00Z"));
    const expired = directoryClient([[aliceRaw]], url);
    await loadUserDirectory(expired.client);
    expect(expired.calls.length).toBe(1);
  });

  test("keeps a partial fill when users.list fails midway and resumes from its cursor", async () => {
    const url = "https://partial-test.slack.com";
    const first = directoryClient([[aliceRaw], [malice]], url);
    first.failOn(1);
    const partial = await loadUserDirectory(first.client);
    expect(partial.complete).toBe(false);
    expect(partial.users.map((u) => u.id)).toEqual(["U1"]);

    const second = directoryClient([[aliceRaw], [malice]], url);
    const resumed = await loadUserDirectory(second.client);
    expect(second.calls.map((c) => c.params.cursor)).toEqual(["1"]);
    expect(resumed.complete).toBe(true);
    expect(resumed.users.map((u) => u.id)).toEqual(["U1", "U2"]);
  });

  test("an unresolved name on a partial directory says the cache is still filling", async () => {
    const { client, failOn } = directoryClient([[aliceRaw], [malice]], "https://p2.slack.com");
    failOn(1);
    await expect(resolveUserId(client, "@malice")).rejects.toThrow(/still being cached/);
  });
});

describe("name resolution fast path", () => {
  test("resolveUserId finds a handle via the edge API", async () => {
    const { client, apiCalls } = edgeClient([malice, aliceRaw]);
    expect(await resolveUserId(client, "@Alice")).toBe("U1");
    expect(apiCalls).toEqual([]);
  });

  test("resolveUserId matches a display name exactly but not partially", async () => {
    const { client } = edgeClient([
      { id: "U7", name: "jsmith", real_name: "John Smith", profile: { display_name: "Johnny" } },
    ]);
    expect(await resolveUserId(client, "johnny")).toBe("U7");
    expect(await resolveUserId(client, "John")).toBeNull();
  });

  test("the search-query resolver shares the same fast path", async () => {
    const { client, apiCalls } = edgeClient([aliceRaw]);
    expect(await resolveSearchUserId(client, "alice")).toBe("U1");
    expect(await resolveSearchUserId(client, "nobody")).toBeUndefined();
    expect(apiCalls).toEqual([]);
  });
});

describe("getUser by full name", () => {
  test("returns the single matching user, preferring active over deactivated", async () => {
    const { client, apiCalls } = edgeClient([janeA, janeDeleted]);
    const user = await getUser(client, "Jane Doe");
    expect(user.id).toBe("U3");
    expect(apiCalls.map((c) => c.method)).toEqual(["users.info"]);
  });

  test("accepts a partial full name when it identifies one user", async () => {
    const { client } = edgeClient([aliceRaw]);
    expect((await getUser(client, "alice doe")).id).toBe("U1");
  });

  test("fails listing candidates when several users match", async () => {
    const { client } = edgeClient([janeA, janeB, janeDeleted]);
    let message = "";
    try {
      await getUser(client, "Jane Doe");
    } catch (err) {
      message = err instanceof Error ? err.message : String(err);
    }
    expect(message).toContain('Multiple users match "Jane Doe"');
    expect(message).toContain("U3  @jdoe  Jane Doe  (Engineer)");
    expect(message).toContain("U4  @jdoe2  Jane Doe  (Designer)");
    expect(message).not.toContain("U5");
    expect(message).toContain('agent-slack user search "Jane Doe"');
  });

  test("fails with a not-found error suggesting user search", async () => {
    const { client } = edgeClient([]);
    await expect(getUser(client, "No Body")).rejects.toThrow(
      'No user matches "No Body". Try: agent-slack user search "No Body"',
    );
  });
});
