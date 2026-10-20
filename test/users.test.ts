import { describe, expect, test } from "bun:test";
import { listUsers, resolveUserId } from "../src/slack/users.ts";

describe("resolveUserId", () => {
  test("returns user ids unchanged", async () => {
    const client = {
      api: async () => {
        throw new Error("should not call api for user id");
      },
    };
    expect(await resolveUserId(client as never, "U01ABCDEFG")).toBe("U01ABCDEFG");
  });

  test("returns W-prefixed user ids unchanged", async () => {
    const client = {
      api: async () => {
        throw new Error("should not call api for user id");
      },
    };
    expect(await resolveUserId(client as never, "W01ABCDEFG")).toBe("W01ABCDEFG");
  });

  test("resolves @handle via users.list", async () => {
    const client = {
      api: async (method: string) => {
        expect(method).toBe("users.list");
        return {
          ok: true,
          members: [{ id: "U02HANDLE", name: "alice" }],
        };
      },
    };
    expect(await resolveUserId(client as never, "@alice")).toBe("U02HANDLE");
  });

  test("resolves email via users.lookupByEmail", async () => {
    const client = {
      api: async (method: string, params?: Record<string, unknown>) => {
        expect(method).toBe("users.lookupByEmail");
        expect(params?.email).toBe("alice@example.com");
        return {
          ok: true,
          user: { id: "U03EMAIL" },
        };
      },
    };
    expect(await resolveUserId(client as never, "alice@example.com")).toBe("U03EMAIL");
  });

  test("resolves @handle case-insensitively", async () => {
    const client = {
      api: async (method: string) => {
        expect(method).toBe("users.list");
        return {
          ok: true,
          members: [{ id: "U05HANDLE", name: "jinjing" }],
        };
      },
    };
    expect(await resolveUserId(client as never, "@Jinjing")).toBe("U05HANDLE");
  });

  test("falls back to users.list when lookupByEmail is unavailable", async () => {
    const client = {
      api: async (method: string) => {
        if (method === "users.lookupByEmail") {
          throw new Error("missing_scope");
        }
        return {
          ok: true,
          members: [{ id: "U04FALLBACK", name: "alice", profile: { email: "alice@example.com" } }],
        };
      },
    };
    expect(await resolveUserId(client as never, "alice@example.com")).toBe("U04FALLBACK");
  });
});

describe("listUsers", () => {
  test("clears next_cursor after the last page", async () => {
    const pages: Record<string, Record<string, unknown>> = {
      start: {
        members: [{ id: "U1", name: "a" }],
        response_metadata: { next_cursor: "page-2" },
      },
      "page-2": {
        members: [{ id: "U2", name: "b" }],
        response_metadata: { next_cursor: "" },
      },
    };
    const client = {
      api: async (method: string, params: Record<string, unknown>) => {
        if (method === "conversations.list") {
          return { channels: [] };
        }
        return pages[String(params.cursor ?? "start")];
      },
    };

    const result = await listUsers(client as never, { limit: 10 });

    expect(result.users.map((u) => u.id)).toEqual(["U1", "U2"]);
    expect(result.next_cursor).toBeUndefined();
  });
});
