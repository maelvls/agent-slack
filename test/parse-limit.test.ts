import { describe, expect, test } from "bun:test";
import { parseLimit, parseMaxChars, requireMessageTs } from "../src/cli/message-actions.ts";

describe("parseLimit", () => {
  test("returns undefined when no value given", () => {
    expect(parseLimit(undefined)).toBeUndefined();
  });

  test("parses valid positive integers", () => {
    expect(parseLimit("10")).toBe(10);
    expect(parseLimit("1")).toBe(1);
    expect(parseLimit("200")).toBe(200);
  });

  test("rejects non-numeric strings", () => {
    expect(() => parseLimit("abc")).toThrow("must be a positive integer");
  });

  test("rejects zero", () => {
    expect(() => parseLimit("0")).toThrow("must be a positive integer");
  });

  test("rejects negative numbers", () => {
    expect(() => parseLimit("-5")).toThrow("must be a positive integer");
  });

  test("rejects floats (uses integer part)", () => {
    // parseInt("3.5") → 3, which is valid
    expect(parseLimit("3.5")).toBe(3);
  });
});

describe("parseLimit flag name", () => {
  test("names the flag that was invalid", () => {
    expect(() => parseLimit("abc", "--max-messages")).toThrow('Invalid --max-messages value "abc"');
  });
});

describe("parseMaxChars", () => {
  test("accepts zero, positive integers, and -1 for unlimited", () => {
    expect(parseMaxChars("0", "--max-body-chars")).toBe(0);
    expect(parseMaxChars("4000", "--max-body-chars")).toBe(4000);
    expect(parseMaxChars("-1", "--max-body-chars")).toBe(-1);
  });

  test.each(["abc", "", "-2"])("rejects %p instead of silently disabling truncation", (raw) => {
    expect(() => parseMaxChars(raw, "--max-body-chars")).toThrow(
      `Invalid --max-body-chars value "${raw}"`,
    );
  });
});

describe("requireMessageTs", () => {
  test("returns trimmed ts when present", () => {
    expect(requireMessageTs(" 1770165109.628379 ")).toBe("1770165109.628379");
  });

  test("throws when missing", () => {
    expect(() => requireMessageTs(undefined)).toThrow(
      'When targeting a channel, you must pass --ts "<seconds>.<micros>"',
    );
  });

  test("throws when blank", () => {
    expect(() => requireMessageTs("   ")).toThrow(
      'When targeting a channel, you must pass --ts "<seconds>.<micros>"',
    );
  });
});
