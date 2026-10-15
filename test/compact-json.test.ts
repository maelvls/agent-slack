import { describe, expect, test } from "bun:test";
import { pruneEmpty } from "../src/lib/compact-json.ts";

describe("pruneEmpty", () => {
  test("drops null/undefined/empty containers", () => {
    const input: Record<string, unknown> = {
      a: null,
      b: undefined,
      c: "",
      d: "ok",
      e: [],
      f: {},
      g: { x: "", y: 1 },
      h: [null, "", 2, { z: "" }, { z: "a" }],
    };
    expect(pruneEmpty(input)).toEqual({
      d: "ok",
      g: { y: 1 },
      h: [2, { z: "a" }],
    });
  });

  test("keepEmptyArrays keeps listed top-level arrays as [] and still prunes the rest", () => {
    const input: Record<string, unknown> = {
      channel_id: "C123",
      messages: [],
      files: [null, "", {}],
      users: undefined,
      referenced_users: {},
      other: [],
      nested: { messages: [], keep: 1 },
    };
    expect(
      pruneEmpty(input, { keepEmptyArrays: ["messages", "files", "users", "referenced_users"] }),
    ).toEqual({
      channel_id: "C123",
      messages: [],
      files: [],
      nested: { keep: 1 },
    });
  });

  test("keepEmptyArrays still prunes empty fields inside kept arrays", () => {
    expect(
      pruneEmpty({ messages: [{ ts: "1", files: [], content: "" }] } as Record<string, unknown>, {
        keepEmptyArrays: ["messages"],
      }),
    ).toEqual({ messages: [{ ts: "1" }] });
  });
});
