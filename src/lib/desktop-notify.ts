import { spawn } from "node:child_process";

/** AppleScript string literal; it understands the same \" and \\ escapes as JSON. */
function appleScriptString(value: string): string {
  return JSON.stringify(value.replace(/[\r\n]+/g, " "));
}

/**
 * Show a desktop notification (macOS: osascript, Linux: notify-send). Best effort:
 * never throws and never waits, since it only exists to reach a human who may not
 * be looking at the terminal.
 */
export function notifyDesktop(title: string, message: string): void {
  try {
    const child =
      process.platform === "darwin"
        ? spawn(
            "osascript",
            [
              "-e",
              `display notification ${appleScriptString(message)} with title ${appleScriptString(title)}`,
            ],
            { stdio: "ignore", detached: true },
          )
        : process.platform === "linux"
          ? spawn("notify-send", [title, message], { stdio: "ignore", detached: true })
          : undefined;
    child?.on("error", () => {});
    child?.unref();
  } catch {
    // No notifier available; the stderr log line still says what happened.
  }
}
