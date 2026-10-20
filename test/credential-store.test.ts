import { describe, expect, test } from "bun:test";
import { hydrateXoxdCookie, xoxdCookieForFile } from "../src/auth/store.ts";

const PLACEHOLDER = "__KEYCHAIN__";

describe("shared xoxd keychain entry", () => {
  test("only the cookie stored in the keychain is replaced by the placeholder", () => {
    expect(xoxdCookieForFile("xoxd-desktop", "xoxd-desktop")).toBe(PLACEHOLDER);
    expect(xoxdCookieForFile("xoxd-firefox", "xoxd-desktop")).toBe("xoxd-firefox");
  });

  test("keeps every cookie in the file when the keychain write failed", () => {
    expect(xoxdCookieForFile("xoxd-desktop", undefined)).toBe("xoxd-desktop");
  });

  test("hydrates from the keychain only for placeholder cookies", () => {
    const readKeychain = () => "xoxd-desktop";
    expect(hydrateXoxdCookie(PLACEHOLDER, readKeychain)).toBe("xoxd-desktop");
    expect(hydrateXoxdCookie("xoxd-firefox", readKeychain)).toBe("xoxd-firefox");
    expect(hydrateXoxdCookie(PLACEHOLDER, () => null)).toBe(PLACEHOLDER);
  });

  test("two workspaces with different cookies each load their own after a save", () => {
    const keychainXoxd = "xoxd-desktop";
    const saved = ["xoxd-desktop", "xoxd-firefox"].map((c) => xoxdCookieForFile(c, keychainXoxd));
    const loaded = saved.map((c) => hydrateXoxdCookie(c, () => keychainXoxd));
    expect(saved).toEqual([PLACEHOLDER, "xoxd-firefox"]);
    expect(loaded).toEqual(["xoxd-desktop", "xoxd-firefox"]);
  });
});
