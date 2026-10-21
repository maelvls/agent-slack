import { pbkdf2Sync, createDecipheriv } from "node:crypto";
import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { copySqliteForRead, queryReadonlySqlite } from "./firefox-profile.ts";
import { getKeychainTimeoutMs } from "./keychain.ts";

/** Read each "<Browser> Safe Storage" password that exists in the macOS keychain. */
export function getSafeStoragePasswords(services: string[]): string[] {
  const passwords: string[] = [];
  for (const service of services) {
    try {
      const out = execFileSync("security", ["find-generic-password", "-w", "-s", service], {
        encoding: "utf8",
        stdio: ["ignore", "pipe", "ignore"],
        timeout: getKeychainTimeoutMs(),
      }).trim();
      if (out) {
        passwords.push(out);
      }
    } catch {
      // continue
    }
  }
  return passwords;
}

/**
 * Read Slack's `d` cookie from a Chromium-family (macOS) Cookies DB. The cookie is
 * HttpOnly, so page JavaScript can't see it; the DB is the only source.
 * `getPasswords` is only called when the value is encrypted.
 */
export async function readSlackCookieFromChromiumDb(input: {
  dbPath: string;
  browser: string;
  getPasswords: () => string[];
}): Promise<string> {
  const { dbPath, browser } = input;
  if (!existsSync(dbPath)) {
    throw new Error(`${browser} Cookies DB not found: ${dbPath}`);
  }

  // The browser keeps the DB open (and recent writes in WAL) while running; read a copy.
  const copied = await copySqliteForRead(dbPath);
  let rows: { value: string; encrypted_value: Uint8Array }[];
  try {
    rows = (await queryReadonlySqlite(
      copied.copyPath,
      "select host_key, name, value, encrypted_value from cookies where name = 'd' and host_key like '%slack.com' order by length(encrypted_value) desc",
    )) as typeof rows;
  } finally {
    await copied.cleanup();
  }

  if (!rows || rows.length === 0) {
    throw new Error(`No Slack 'd' cookie found in ${browser}`);
  }
  const row = rows[0]!;
  if (row.value && row.value.startsWith("xoxd-")) {
    return row.value;
  }

  const encrypted = Buffer.from(row.encrypted_value || []);
  if (encrypted.length === 0) {
    throw new Error(`${browser} Slack 'd' cookie had no encrypted_value`);
  }

  const prefix = encrypted.subarray(0, 3).toString("utf8");
  const data = prefix === "v10" || prefix === "v11" ? encrypted.subarray(3) : encrypted;

  for (const password of input.getPasswords()) {
    try {
      const decrypted = decryptChromiumCookieValue(data, { password, iterations: 1003 });
      const match = decrypted.match(/xoxd-[A-Za-z0-9%/+_=.-]+/);
      if (match) {
        return match[0]!;
      }
    } catch {
      // continue
    }
  }

  throw new Error(`Could not decrypt Slack 'd' cookie from ${browser}`);
}

export function decryptChromiumCookieValue(
  data: Buffer,
  options: { password: string; iterations: number },
): string {
  if (!data || data.length === 0) {
    return "";
  }

  const { password, iterations } = options;

  if (iterations < 1) {
    throw new RangeError(`iterations must be >= 1, got ${iterations}`);
  }

  const salt = Buffer.from("saltysalt", "utf8");
  const iv = Buffer.alloc(16, " ");
  const key = pbkdf2Sync(password, salt, iterations, 16, "sha1");

  const decipher = createDecipheriv("aes-128-cbc", key, iv);
  decipher.setAutoPadding(true);
  const plain = Buffer.concat([decipher.update(data), decipher.final()]);
  const marker = Buffer.from("xoxd-");
  const idx = plain.indexOf(marker);
  if (idx === -1) {
    return plain.toString("utf8");
  }

  let end = idx;
  while (end < plain.length) {
    const b = plain[end]!;
    if (b < 0x21 || b > 0x7e) {
      break;
    }
    end++;
  }
  const rawToken = plain.subarray(idx, end).toString("utf8");
  try {
    return decodeURIComponent(rawToken);
  } catch {
    return rawToken;
  }
}
