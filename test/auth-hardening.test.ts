import { afterAll, describe, expect, test } from "bun:test";
import { createCipheriv, pbkdf2Sync } from "node:crypto";
import { existsSync } from "node:fs";
import { mkdir, mkdtemp, readdir, rm, utimes, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { snappyCompress } from "hysnappy";
import { buildKeychainAddCommand } from "../src/auth/keychain.ts";
import { readSlackCookieFromChromiumDb } from "../src/auth/chromium-cookie.ts";
import { chromeCookiesDbPath, extractFromChrome } from "../src/auth/chrome.ts";
import { decodeLocalStorageValue } from "../src/auth/firefox.ts";
import { pickNewestLocalConfig, sweepStaleLevelDbSnapshots } from "../src/auth/desktop.ts";
import {
  latestLiveEntries,
  readChromiumLevelDB,
  type LevelDBEntry,
} from "../src/lib/leveldb-reader.ts";

const dirs: string[] = [];
afterAll(async () => {
  await Promise.all(dirs.map((d) => rm(d, { recursive: true, force: true })));
});

async function tempDir(): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), "agent-slack-auth-"));
  dirs.push(dir);
  return dir;
}

describe("keychain add command", () => {
  test("puts the secret on stdin with -U, quoting quotes and backslashes", () => {
    expect(
      buildKeychainAddCommand({ service: "agent-slack", account: "xoxd", value: 'a\\b"c' }),
    ).toBe('add-generic-password -U -s "agent-slack" -a "xoxd" -w "a\\\\b\\"c"\n');
  });

  test("refuses values that would break the one-line command", () => {
    expect(buildKeychainAddCommand({ service: "s", account: "a", value: "x\ny" })).toBeNull();
  });
});

describe("Chromium cookie DB", () => {
  function encrypt(plain: string, password: string): Buffer {
    const key = pbkdf2Sync(password, "saltysalt", 1003, 16, "sha1");
    const cipher = createCipheriv("aes-128-cbc", key, Buffer.alloc(16, " "));
    return Buffer.concat([Buffer.from("v10"), cipher.update(plain), cipher.final()]);
  }

  async function cookiesDb(encrypted: Buffer): Promise<string> {
    const { Database } = await import("bun:sqlite");
    const path = join(await tempDir(), "Cookies");
    const db = new Database(path);
    db.run("create table cookies (host_key text, name text, value text, encrypted_value blob)");
    db.run("insert into cookies values (?, ?, ?, ?)", [".slack.com", "d", "", encrypted]);
    db.close();
    return path;
  }

  test("decrypts the HttpOnly d cookie with the safe-storage password", async () => {
    // Chrome 130+ prefixes the plaintext with a 32-byte SHA-256 of the host.
    const plain = `${"h".repeat(32)}xoxd-abc%2Fdef`;
    const dbPath = await cookiesDb(encrypt(plain, "pw"));
    const cookie = await readSlackCookieFromChromiumDb({
      dbPath,
      browser: "Chrome",
      getPasswords: () => ["wrong", "pw"],
    });
    expect(cookie).toBe("xoxd-abc/def");
  });

  test("prefers Network/Cookies, falling back to the profile root", async () => {
    const profile = await tempDir();
    expect(chromeCookiesDbPath(profile)).toBe(join(profile, "Cookies"));
    await mkdir(join(profile, "Network"));
    await writeFile(join(profile, "Network", "Cookies"), "");
    expect(chromeCookiesDbPath(profile)).toBe(join(profile, "Network", "Cookies"));
  });

  test("Chrome import skips the cookie DB (and its keychain prompt) without a Slack tab", async () => {
    let cookieReads = 0;
    const readCookie = async () => {
      cookieReads++;
      return "xoxd-abc";
    };
    expect(await extractFromChrome({ isMacos: true, readTeams: () => [], readCookie })).toBeNull();
    expect(cookieReads).toBe(0);

    const team = { url: "https://acme.slack.com/", token: "xoxc-1" };
    expect(await extractFromChrome({ isMacos: true, readTeams: () => [team], readCookie })).toEqual(
      { cookie_d: "xoxd-abc", teams: [team] },
    );
    expect(cookieReads).toBe(1);
  });
});

describe("Firefox localStorage values", () => {
  test("uncompresses Snappy values (compression_type = 1)", () => {
    const json = JSON.stringify({
      teams: { T1: { url: "https://a.slack.com/", token: "xoxc-1" } },
    });
    const compressed = snappyCompress(Buffer.from(json));
    const decoded = decodeLocalStorageValue({
      key: "localConfig_v2",
      value: compressed,
      compression_type: 1,
    });
    expect(Buffer.from(decoded as Uint8Array).toString("utf8")).toBe(json);
  });

  test("leaves uncompressed values alone", () => {
    const value = Buffer.from("{}");
    expect(decodeLocalStorageValue({ key: "k", value, compression_type: 0 })).toBe(value);
    expect(decodeLocalStorageValue({ key: "k", value })).toBe(value);
  });
});

function encodeVarint(value: number): Buffer {
  const bytes: number[] = [];
  while (value > 0x7f) {
    bytes.push((value & 0x7f) | 0x80);
    value >>>= 7;
  }
  bytes.push(value);
  return Buffer.from(bytes);
}

/** One FULL log record holding a write batch; `value: null` is a deletion. */
async function writeLogFile(input: {
  path: string;
  sequence: bigint;
  records: { key: string; value: string | null }[];
}): Promise<void> {
  const { path, sequence, records } = input;
  const body = records.map(({ key, value }) => {
    const k = Buffer.from(key);
    if (value === null) {
      return Buffer.concat([Buffer.from([0x00]), encodeVarint(k.length), k]);
    }
    const v = Buffer.from(value);
    return Buffer.concat([
      Buffer.from([0x01]),
      encodeVarint(k.length),
      k,
      encodeVarint(v.length),
      v,
    ]);
  });
  const header = Buffer.alloc(12);
  header.writeBigUInt64LE(sequence, 0);
  header.writeUInt32LE(records.length, 8);
  const batch = Buffer.concat([header, ...body]);
  const recordHeader = Buffer.alloc(7);
  recordHeader.writeUInt16LE(batch.length, 4);
  recordHeader[6] = 0x01;
  await writeFile(path, Buffer.concat([recordHeader, batch]));
}

function entry(input: { key: string; value?: string; seq: bigint }): LevelDBEntry {
  const k = Buffer.from(input.key);
  const deleted = input.value === undefined;
  return { key: k, userKey: k, value: Buffer.from(input.value ?? ""), seq: input.seq, deleted };
}

describe("LevelDB sequence numbers and deletions", () => {
  test("log records carry per-record sequence numbers and deletions", async () => {
    const dir = await tempDir();
    await writeLogFile({
      path: join(dir, "000003.log"),
      sequence: 100n,
      records: [
        { key: "a", value: "1" },
        { key: "b", value: null },
      ],
    });
    const entries = await readChromiumLevelDB(dir);
    expect(entries.map((e) => [e.key.toString(), e.seq, e.deleted ?? false])).toEqual([
      ["a", 100n, false],
      ["b", 101n, true],
    ]);
  });

  test("the newest localConfig wins by sequence number, not key name", () => {
    const picked = pickNewestLocalConfig([
      entry({ key: "_https://app.slack.com\0\x01localConfig_v3", value: "old-v3", seq: 5n }),
      entry({ key: "_https://app.slack.com\0\x01localConfig_v2", value: "new-v2", seq: 9n }),
    ]);
    expect(picked?.toString()).toBe("new-v2");
  });

  test("a newer deletion hides an older SSTable value", () => {
    const key = "_https://app.slack.com\0\x01localConfig_v3";
    expect(
      pickNewestLocalConfig([entry({ key, value: "stale", seq: 5n }), entry({ key, seq: 9n })]),
    ).toBeNull();
    expect(
      latestLiveEntries([entry({ key, seq: 5n }), entry({ key, value: "fresh", seq: 9n })]).map(
        (e) => e.value.toString(),
      ),
    ).toEqual(["fresh"]);
  });
});

describe("LevelDB snapshot sweep", () => {
  test("removes only stale snapshot dirs", async () => {
    const tmpRoot = await tempDir();
    const legacyDir = await tempDir();
    const stale = join(tmpRoot, "agent-slack-leveldb-old");
    const fresh = join(tmpRoot, "agent-slack-leveldb-new");
    const unrelated = join(tmpRoot, "something-else");
    const legacy = join(legacyDir, "1700000000000");
    for (const d of [stale, fresh, unrelated, legacy]) {
      await mkdir(d);
    }
    const old = new Date(Date.now() - 60 * 60_000);
    await utimes(stale, old, old);
    await utimes(unrelated, old, old);
    await utimes(legacy, old, old);

    await sweepStaleLevelDbSnapshots({ tmpRoot, legacyDir });

    expect(existsSync(stale)).toBe(false);
    expect(existsSync(legacy)).toBe(false);
    expect((await readdir(tmpRoot)).sort()).toEqual(["agent-slack-leveldb-new", "something-else"]);
  });
});
