import { execFileSync } from "node:child_process";
import { homedir, platform } from "node:os";
import { join } from "node:path";
import { getSafeStoragePasswords, readSlackCookieFromChromiumDb } from "./chromium-cookie.ts";
import { isRecord } from "../lib/object-type-guards.ts";

type BraveExtractedTeam = { url: string; name?: string; token: string };

export type BraveExtracted = {
  cookie_d: string;
  teams: BraveExtractedTeam[];
};

const IS_MACOS = platform() === "darwin";

// Brave (like Chrome) ships with `Allow JavaScript from Apple Events` OFF.
// Without it, `execute javascript` from osascript fails — and that's how this
// module reads `localStorage.localConfig_v2` from a Slack tab. We detect that
// specific failure and surface an actionable message instead of swallowing it.
export class BraveAppleScriptDisabledError extends Error {
  constructor() {
    super(
      "Brave is blocking JavaScript from Apple Events.\n" +
        "Enable it: Brave menu → View → Developer → Allow JavaScript from Apple Events\n" +
        "(macOS will prompt for your password the first time.)\n" +
        "Then re-run: agent-slack auth import-brave",
    );
    this.name = "BraveAppleScriptDisabledError";
  }
}

// --- AppleScript helpers (for extracting teams from Brave tabs) ---

const APPLESCRIPT_JS_DISABLED_MARKER = "Executing JavaScript through AppleScript is turned off";

function osascript(script: string): string {
  try {
    return execFileSync("osascript", ["-e", script], {
      encoding: "utf8",
      timeout: 7000,
      stdio: ["ignore", "pipe", "pipe"],
    }).trim();
  } catch (err: unknown) {
    const stderr =
      err && typeof err === "object" && "stderr" in err
        ? String((err as { stderr: unknown }).stderr ?? "")
        : "";
    const message = err instanceof Error ? err.message : String(err);
    if (
      stderr.includes(APPLESCRIPT_JS_DISABLED_MARKER) ||
      message.includes(APPLESCRIPT_JS_DISABLED_MARKER)
    ) {
      throw new BraveAppleScriptDisabledError();
    }
    throw err;
  }
}

const TEAM_JSON_PATHS = [
  "JSON.stringify(JSON.parse(localStorage.localConfig_v2).teams)",
  "JSON.stringify(JSON.parse(localStorage.localConfig_v3).teams)",
  "JSON.stringify(JSON.parse(localStorage.getItem('reduxPersist:localConfig'))?.teams || {})",
  "JSON.stringify(window.boot_data?.teams || {})",
];

function teamsScript(): string {
  const tryPaths = TEAM_JSON_PATHS.map(
    (expr) => `try { var v = ${expr}; if (v && v !== '{}' && v !== 'null') return v; } catch(e) {}`,
  );
  return `
    tell application "Brave Browser"
      repeat with w in windows
        repeat with t in tabs of w
          if URL of t contains "slack.com" then
            return execute t javascript "(function(){ ${tryPaths.join(" ")} return '{}'; })()"
          end if
        end repeat
      end repeat
      return "{}"
    end tell
  `;
}

function toBraveTeam(value: unknown): BraveExtractedTeam | null {
  if (!isRecord(value)) {
    return null;
  }
  const token = typeof value.token === "string" ? value.token : null;
  const url = typeof value.url === "string" ? value.url : null;
  if (!token || !url || !token.startsWith("xoxc-")) {
    return null;
  }
  const name = typeof value.name === "string" ? value.name : undefined;
  return { url, name, token };
}

function extractTeamsFromBraveTab(): BraveExtractedTeam[] {
  const teamsRaw = osascript(teamsScript());
  let teamsObj: unknown = {};
  try {
    teamsObj = JSON.parse(teamsRaw || "{}");
  } catch {
    teamsObj = {};
  }

  const teamsRecord = isRecord(teamsObj) ? teamsObj : {};
  return Object.values(teamsRecord)
    .map((t) => toBraveTeam(t))
    .filter((t): t is BraveExtractedTeam => t !== null);
}

// --- Cookie extraction from Brave's SQLite database ---

const BRAVE_COOKIES_DB = join(
  homedir(),
  "Library",
  "Application Support",
  "BraveSoftware",
  "Brave-Browser",
  "Default",
  "Cookies",
);

const BRAVE_SAFE_STORAGE_SERVICES = [
  "Brave Safe Storage",
  "Brave Browser Safe Storage",
  "Chrome Safe Storage",
  "Chromium Safe Storage",
];

function extractCookieDFromBrave(): Promise<string> {
  return readSlackCookieFromChromiumDb({
    dbPath: BRAVE_COOKIES_DB,
    browser: "Brave",
    getPasswords: () => getSafeStoragePasswords(BRAVE_SAFE_STORAGE_SERVICES),
  });
}

// --- Main export ---

export async function extractFromBrave(): Promise<BraveExtracted | null> {
  if (!IS_MACOS) {
    return null;
  }
  try {
    const teams = extractTeamsFromBraveTab();
    if (teams.length === 0) {
      return null;
    }

    const cookie_d = await extractCookieDFromBrave();
    if (!cookie_d || !cookie_d.startsWith("xoxd-")) {
      return null;
    }

    return { cookie_d, teams };
  } catch (err) {
    if (err instanceof BraveAppleScriptDisabledError) {
      throw err;
    }
    return null;
  }
}
