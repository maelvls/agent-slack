import { platform } from "node:os";
import { execFileSync } from "node:child_process";

const IS_MACOS = platform() === "darwin";
const DEFAULT_KEYCHAIN_TIMEOUT_MS = 3_000;

export function getKeychainTimeoutMs(): number {
  const raw = process.env.AGENT_SLACK_KEYCHAIN_TIMEOUT_MS?.trim();
  if (!raw) {
    return DEFAULT_KEYCHAIN_TIMEOUT_MS;
  }
  const parsed = Number(raw);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return DEFAULT_KEYCHAIN_TIMEOUT_MS;
  }
  return Math.floor(parsed);
}

export function keychainGet(account: string, service: string): string | null {
  if (!IS_MACOS) {
    return null;
  }
  try {
    const result = execFileSync(
      "security",
      ["find-generic-password", "-s", service, "-a", account, "-w"],
      {
        encoding: "utf8",
        stdio: ["pipe", "pipe", "ignore"],
        timeout: getKeychainTimeoutMs(),
      },
    );
    return result.trim() || null;
  } catch {
    return null;
  }
}

export function keychainSet(input: { account: string; value: string; service: string }): boolean {
  if (!IS_MACOS) {
    return false;
  }
  const { account, value, service } = input;
  const command = buildKeychainAddCommand(input);
  if (!command) {
    return false;
  }
  try {
    // Pass the secret on stdin (`security -i`), never in argv where `ps` can read it.
    // -U updates in place, so a failed write never loses the previous secret.
    execFileSync("security", ["-i"], {
      input: command,
      stdio: ["pipe", "pipe", "pipe"],
      timeout: getKeychainTimeoutMs(),
    });
  } catch {
    return false;
  }
  // `security -i` exits 0 even when the command fails, so confirm by reading back.
  return keychainGet(account, service) === value;
}

/** Quote an argument for the `security -i` command parser. */
function quoteSecurityArg(value: string): string {
  return `"${value.replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"`;
}

/** The stdin line for `security -i`, or null if a value can't be expressed on one line. */
export function buildKeychainAddCommand(input: {
  account: string;
  value: string;
  service: string;
}): string | null {
  const { account, value, service } = input;
  if ([account, value, service].some((s) => /[\r\n]/.test(s))) {
    return null;
  }
  return `add-generic-password -U -s ${quoteSecurityArg(service)} -a ${quoteSecurityArg(account)} -w ${quoteSecurityArg(value)}\n`;
}
