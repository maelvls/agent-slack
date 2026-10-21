import { afterAll, describe, expect, spyOn, test } from "bun:test";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  hydrateXoxdCookie,
  parseCredentials,
  readCredentialsFile,
  xoxdCookieForFile,
} from "../src/auth/store.ts";

const SHARED = "__KEYCHAIN__";
const OWN = "__KEYCHAIN_WORKSPACE__";

describe("xoxd keychain storage", () => {
  test("the shared cookie gets the shared placeholder without a per-workspace write", () => {
    let ownWrites = 0;
    const storeOwn = () => {
      ownWrites++;
      return true;
    };
    expect(
      xoxdCookieForFile({ cookie: "xoxd-desktop", sharedXoxd: "xoxd-desktop", storeOwn }),
    ).toBe(SHARED);
    expect(ownWrites).toBe(0);
  });

  test("a differing cookie goes to its own keychain entry", () => {
    expect(
      xoxdCookieForFile({
        cookie: "xoxd-firefox",
        sharedXoxd: "xoxd-desktop",
        storeOwn: () => true,
      }),
    ).toBe(OWN);
  });

  test("falls back to the file when the keychain write fails", () => {
    expect(
      xoxdCookieForFile({
        cookie: "xoxd-firefox",
        sharedXoxd: "xoxd-desktop",
        storeOwn: () => false,
      }),
    ).toBe("xoxd-firefox");
    expect(
      xoxdCookieForFile({ cookie: "xoxd-desktop", sharedXoxd: undefined, storeOwn: () => false }),
    ).toBe("xoxd-desktop");
  });

  test("hydrates each placeholder from the matching keychain entry", () => {
    const keychain = { shared: () => "xoxd-desktop", own: () => "xoxd-firefox" };
    expect(hydrateXoxdCookie(SHARED, keychain)).toBe("xoxd-desktop");
    expect(hydrateXoxdCookie(OWN, keychain)).toBe("xoxd-firefox");
    expect(hydrateXoxdCookie("xoxd-file", keychain)).toBe("xoxd-file");
    expect(hydrateXoxdCookie(OWN, { shared: () => "xoxd-desktop", own: () => null })).toBe(OWN);
  });

  test("two workspaces with different cookies each load their own after a save", () => {
    const keychain = new Map<string, string>();
    const sharedXoxd = "xoxd-desktop";
    keychain.set("xoxd", sharedXoxd);
    const saved = [
      { url: "https://a.slack.com", cookie: "xoxd-desktop" },
      { url: "https://b.slack.com", cookie: "xoxd-firefox" },
    ].map(({ url, cookie }) => ({
      url,
      cookie: xoxdCookieForFile({
        cookie,
        sharedXoxd,
        storeOwn: () => {
          keychain.set(`xoxd:${url}`, cookie);
          return true;
        },
      }),
    }));
    const loaded = saved.map(({ url, cookie }) =>
      hydrateXoxdCookie(cookie, {
        shared: () => keychain.get("xoxd") ?? null,
        own: () => keychain.get(`xoxd:${url}`) ?? null,
      }),
    );
    expect(saved.map((s) => s.cookie)).toEqual([SHARED, OWN]);
    expect(loaded).toEqual(["xoxd-desktop", "xoxd-firefox"]);
  });
});

const good = {
  workspace_url: "https://a.slack.com",
  auth: { auth_type: "standard", token: "xoxp-1" },
};

describe("parseCredentials", () => {
  test("keeps valid workspaces when one entry is invalid", () => {
    const bad = { workspace_url: "not a url", auth: { auth_type: "standard", token: "x" } };
    const { credentials, invalid } = parseCredentials({ version: 1, workspaces: [good, bad] });
    expect(invalid).toBe(true);
    expect(credentials.workspaces.map((w) => w.workspace_url)).toEqual(["https://a.slack.com"]);
  });

  test("a missing file is empty and not invalid", () => {
    expect(parseCredentials(null)).toEqual({
      credentials: { version: 1, workspaces: [] },
      invalid: false,
    });
  });

  test("a fully valid file is not flagged", () => {
    const raw = { version: 1, default_workspace_url: "https://a.slack.com", workspaces: [good] };
    const { credentials, invalid } = parseCredentials(raw);
    expect(invalid).toBe(false);
    expect(credentials.default_workspace_url).toBe("https://a.slack.com");
  });
});

describe("readCredentialsFile", () => {
  const dirs: string[] = [];
  afterAll(async () => {
    await Promise.all(dirs.map((d) => rm(d, { recursive: true, force: true })));
  });

  async function fileWith(text: string): Promise<string> {
    const dir = await mkdtemp(join(tmpdir(), "agent-slack-creds-"));
    dirs.push(dir);
    const path = join(dir, "credentials.json");
    await writeFile(path, text);
    return path;
  }

  test("backs up unparseable JSON instead of silently treating it as empty", async () => {
    const text = '{"version":1,"workspaces":[{"workspace_url":"https://a.slack.com"';
    const path = await fileWith(text);
    const warn = spyOn(console, "error").mockImplementation(() => {});
    try {
      const creds = await readCredentialsFile(path);
      expect(creds.workspaces).toEqual([]);
      expect(await readFile(`${path}.invalid`, "utf8")).toBe(text);
      expect(warn).toHaveBeenCalledTimes(1);
    } finally {
      warn.mockRestore();
    }
  });

  test("keeps the valid workspaces of a partly invalid file and backs it up", async () => {
    const text = JSON.stringify({ version: 1, workspaces: [good, { workspace_url: 1 }] });
    const path = await fileWith(text);
    const warn = spyOn(console, "error").mockImplementation(() => {});
    try {
      const creds = await readCredentialsFile(path);
      expect(creds.workspaces).toHaveLength(1);
      expect(await readFile(`${path}.invalid`, "utf8")).toBe(text);
    } finally {
      warn.mockRestore();
    }
  });

  test("a valid file is read without a backup", async () => {
    const path = await fileWith(JSON.stringify({ version: 1, workspaces: [good] }));
    const creds = await readCredentialsFile(path);
    expect(creds.workspaces).toHaveLength(1);
    await expect(readFile(`${path}.invalid`, "utf8")).rejects.toThrow();
  });
});
