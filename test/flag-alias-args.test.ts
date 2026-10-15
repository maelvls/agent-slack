import { describe, expect, test } from "bun:test";
import { resolveMessageReadArgs } from "../src/cli/message-command.ts";
import { pickPositionalOrFlag } from "../src/cli/options.ts";
import { resolveTargetArgs } from "../src/cli/targets.ts";

const CHANNEL = "C0123456789";
const ROOT_TS = "1700000000.000001";
const REPLY_TS = "1700000000.000002";

describe("resolveTargetArgs", () => {
  test("uses the positional target when --channel is absent", () => {
    expect(resolveTargetArgs({ positionals: ["#general", "hi"] })).toEqual({
      target: "#general",
      rest: ["hi"],
    });
  });

  test("uses --channel when the positional target is omitted", () => {
    expect(resolveTargetArgs({ positionals: [undefined, undefined], channel: CHANNEL })).toEqual({
      target: CHANNEL,
      rest: [undefined],
    });
  });

  test("accepts a positional target that matches --channel, ignoring a leading #", () => {
    expect(resolveTargetArgs({ positionals: ["general"], channel: "#general" })).toEqual({
      target: "general",
      rest: [],
    });
  });

  test("shifts a lone positional into the next slot when --channel is the target", () => {
    expect(
      resolveTargetArgs({ positionals: ["hello there", undefined], channel: CHANNEL }),
    ).toEqual({ target: CHANNEL, rest: ["hello there"] });
    expect(resolveTargetArgs({ positionals: [ROOT_TS, undefined], channel: CHANNEL })).toEqual({
      target: CHANNEL,
      rest: [ROOT_TS],
    });
  });

  test("rejects a positional that reads as a different target", () => {
    for (const other of [
      "C9999999999",
      "#random",
      "U11111111",
      "https://ws.slack.com/archives/C9999999999/p1700000000000001",
    ]) {
      expect(() =>
        resolveTargetArgs({ positionals: [other, undefined], channel: CHANNEL }),
      ).toThrow("Conflicting targets");
    }
  });

  test("rejects a differing positional when there is no slot to shift into", () => {
    expect(() => resolveTargetArgs({ positionals: ["general"], channel: CHANNEL })).toThrow(
      "Conflicting targets",
    );
    expect(() => resolveTargetArgs({ positionals: ["general", "hi"], channel: CHANNEL })).toThrow(
      "Conflicting targets",
    );
  });

  test("requires a target from one of the two spellings", () => {
    expect(() => resolveTargetArgs({ positionals: [undefined, undefined] })).toThrow(
      "Missing <target>",
    );
  });
});

describe("pickPositionalOrFlag", () => {
  const names = { positionalName: "[text]", flagName: "--text" };

  test("returns whichever spelling was given", () => {
    expect(pickPositionalOrFlag({ positional: "a", ...names })).toBe("a");
    expect(pickPositionalOrFlag({ flag: "b", ...names })).toBe("b");
    expect(pickPositionalOrFlag({ ...names })).toBeUndefined();
  });

  test("accepts both when they agree and rejects them when they differ", () => {
    expect(pickPositionalOrFlag({ positional: "a", flag: "a", ...names })).toBe("a");
    expect(() => pickPositionalOrFlag({ positional: "a", flag: "b", ...names })).toThrow(
      "Conflicting values for [text] and --text",
    );
  });
});

describe("resolveMessageReadArgs", () => {
  const base = { maxBodyChars: "8000" };

  test("maps a second positional to --ts", () => {
    const { targetInput, options } = resolveMessageReadArgs({
      positionals: [CHANNEL, ROOT_TS],
      options: base,
    });
    expect(targetInput).toBe(CHANNEL);
    expect(options.ts).toBe(ROOT_TS);
  });

  test("maps a lone positional ts to --ts when --channel is the target", () => {
    const { targetInput, options } = resolveMessageReadArgs({
      positionals: [ROOT_TS, undefined],
      options: { ...base, channel: CHANNEL },
    });
    expect(targetInput).toBe(CHANNEL);
    expect(options.ts).toBe(ROOT_TS);
  });

  test("rejects a second positional that is not a ts", () => {
    expect(() =>
      resolveMessageReadArgs({ positionals: [CHANNEL, "general"], options: base }),
    ).toThrow('Unexpected argument "general"');
  });

  test("rejects a positional ts that disagrees with --ts", () => {
    expect(() =>
      resolveMessageReadArgs({
        positionals: [CHANNEL, ROOT_TS],
        options: { ...base, ts: REPLY_TS },
      }),
    ).toThrow("Conflicting values for [ts] and --ts");
  });
});
