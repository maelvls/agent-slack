import { execSync } from "node:child_process";
import { existsSync } from "node:fs";
import { homedir, platform } from "node:os";
import { join } from "node:path";
import { getSafeStoragePasswords, readSlackCookieFromChromiumDb } from "./chromium-cookie.ts";

type ChromeExtractedTeam = { url: string; name?: string; token: string };

export type ChromeExtracted = {
  cookie_d: string;
  teams: ChromeExtractedTeam[];
};

const IS_MACOS = platform() === "darwin";

function escapeOsaScript(script: string): string {
  // osascript -e '...'
  return script.replace(/'/g, `'"'"'`);
}

function osascript(script: string): string {
  return execSync(`osascript -e '${escapeOsaScript(script)}'`, {
    encoding: "utf8",
    timeout: 7000,
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
}

const TEAM_JSON_PATHS = [
  // Current known storage
  "JSON.stringify(JSON.parse(localStorage.localConfig_v2).teams)",
  "JSON.stringify(JSON.parse(localStorage.localConfig_v3).teams)",
  "JSON.stringify(JSON.parse(localStorage.getItem('reduxPersist:localConfig'))?.teams || {})",
  "JSON.stringify(window.boot_data?.teams || {})",
];

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function toChromeTeam(value: unknown): ChromeExtractedTeam | null {
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

function teamsScript(): string {
  const tryPaths = TEAM_JSON_PATHS.map(
    (expr) => `try { var v = ${expr}; if (v && v !== '{}' && v !== 'null') return v; } catch(e) {}`,
  );
  return `
    tell application "Google Chrome"
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

const CHROME_PROFILE_DIR = join(
  homedir(),
  "Library",
  "Application Support",
  "Google",
  "Chrome",
  "Default",
);
const CHROME_SAFE_STORAGE_SERVICES = ["Chrome Safe Storage", "Chromium Safe Storage"];

/** Chrome moved the Cookies DB under Network/ (M96+); older profiles keep it at the root. */
export function chromeCookiesDbPath(profileDir: string = CHROME_PROFILE_DIR): string {
  const networkPath = join(profileDir, "Network", "Cookies");
  return existsSync(networkPath) ? networkPath : join(profileDir, "Cookies");
}

function extractTeamsFromChromeTab(): ChromeExtractedTeam[] {
  let teamsObj: unknown = {};
  try {
    teamsObj = JSON.parse(osascript(teamsScript()) || "{}");
  } catch {
    teamsObj = {};
  }
  const teamsRecord = isRecord(teamsObj) ? teamsObj : {};
  return Object.values(teamsRecord)
    .map((t) => toChromeTeam(t))
    .filter((t): t is ChromeExtractedTeam => t !== null);
}

function extractCookieDFromChrome(): Promise<string> {
  // Slack's `d` cookie is HttpOnly, so `document.cookie` in a tab never sees it.
  return readSlackCookieFromChromiumDb({
    dbPath: chromeCookiesDbPath(),
    browser: "Chrome",
    getPasswords: () => getSafeStoragePasswords(CHROME_SAFE_STORAGE_SERVICES),
  });
}

/**
 * Only the Default profile's cookie is read: AppleScript can't tell which profile
 * a tab belongs to, so Slack must be open in that profile.
 */
export async function extractFromChrome(
  deps: {
    isMacos?: boolean;
    readTeams?: () => ChromeExtractedTeam[];
    readCookie?: () => Promise<string>;
  } = {},
): Promise<ChromeExtracted | null> {
  if (!(deps.isMacos ?? IS_MACOS)) {
    return null;
  }
  try {
    // Look for a Slack tab first so a missing tab doesn't cost a keychain prompt.
    const teams = (deps.readTeams ?? extractTeamsFromChromeTab)();
    if (teams.length === 0) {
      return null;
    }
    const cookie = await (deps.readCookie ?? extractCookieDFromChrome)();
    if (!cookie || !cookie.startsWith("xoxd-")) {
      return null;
    }
    return { cookie_d: cookie, teams };
  } catch {
    return null;
  }
}
