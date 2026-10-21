import { describe, expect, test } from "bun:test";
import {
  buildSlackMessageUrl,
  parseSlackChannelUrl,
  parseSlackMessageUrl,
} from "../src/slack/url.ts";

describe("parseSlackChannelUrl", () => {
  test("parses a channel or DM link without a message part", () => {
    expect(
      parseSlackChannelUrl("https://paloaltonetworks.enterprise.slack.com/archives/D0A8RUY2JG0"),
    ).toEqual({
      workspace_url: "https://paloaltonetworks.enterprise.slack.com",
      channel_id: "D0A8RUY2JG0",
    });
    expect(parseSlackChannelUrl("https://acme.slack.com/archives/C060RS20UMV/").channel_id).toBe(
      "C060RS20UMV",
    );
  });

  test("rejects message URLs and non-channel paths", () => {
    expect(() =>
      parseSlackChannelUrl("https://acme.slack.com/archives/C060RS20UMV/p1770165109628379"),
    ).toThrow();
    expect(() => parseSlackChannelUrl("https://acme.slack.com/archives/general")).toThrow();
    expect(() => parseSlackChannelUrl("https://example.com/archives/C060RS20UMV")).toThrow();
  });
});

describe("parseSlackMessageUrl", () => {
  test("parses archives URL with p<digits>", () => {
    const ref = parseSlackMessageUrl(
      "https://stablygroup.slack.com/archives/C060RS20UMV/p1770165109628379",
    );
    expect(ref.workspace_url).toBe("https://stablygroup.slack.com");
    expect(ref.channel_id).toBe("C060RS20UMV");
    expect(ref.message_ts).toBe("1770165109.628379");
  });

  test("captures thread_ts from query when present", () => {
    const ref = parseSlackMessageUrl(
      "https://stablygroup.slack.com/archives/C060RS20UMV/p1770165109628379?thread_ts=1770160000.000001&cid=C060RS20UMV",
    );
    expect(ref.message_ts).toBe("1770165109.628379");
    expect(ref.thread_ts_hint).toBe("1770160000.000001");
  });
});

describe("buildSlackMessageUrl", () => {
  test("builds a permalink for a root message", () => {
    expect(
      buildSlackMessageUrl({
        workspace_url: "https://stablygroup.slack.com/",
        channel_id: "C060RS20UMV",
        message_ts: "1770165109.628379",
      }),
    ).toBe("https://stablygroup.slack.com/archives/C060RS20UMV/p1770165109628379");
  });

  test("includes thread metadata for replies", () => {
    expect(
      buildSlackMessageUrl({
        workspace_url: "https://stablygroup.slack.com",
        channel_id: "C060RS20UMV",
        message_ts: "1770165110.000001",
        thread_ts: "1770165109.628379",
      }),
    ).toBe(
      "https://stablygroup.slack.com/archives/C060RS20UMV/p1770165110000001?thread_ts=1770165109.628379&cid=C060RS20UMV",
    );
  });
});
