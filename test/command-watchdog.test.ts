import { describe, expect, test } from "bun:test";
import { shouldStartCommandWatchdog } from "../src/lib/command-watchdog.ts";

describe("shouldStartCommandWatchdog", () => {
  test("skips help output and update", () => {
    expect(shouldStartCommandWatchdog([])).toBe(false);
    expect(shouldStartCommandWatchdog(["--help"])).toBe(false);
    expect(shouldStartCommandWatchdog(["update"])).toBe(false);
  });

  test("finds the command after leading global options", () => {
    expect(shouldStartCommandWatchdog(["--safe-mode", "update"])).toBe(false);
    expect(shouldStartCommandWatchdog(["--safe-mode", "message", "send", "#general"])).toBe(true);
  });

  test("covers Slack-native draft commands, which no longer open the editor", () => {
    expect(shouldStartCommandWatchdog(["message", "draft", "list"])).toBe(true);
  });
});
