import { describe, expect, test } from "bun:test";
import { slackMrkdwnToMarkdown } from "../src/slack/mrkdwn.ts";

describe("slackMrkdwnToMarkdown", () => {
  test("converts Slack-style links", () => {
    expect(slackMrkdwnToMarkdown("See <https://example.com|this>.")).toBe(
      "See [this](https://example.com).",
    );
    expect(slackMrkdwnToMarkdown("See <https://example.com>.")).toBe("See https://example.com.");
  });

  test("converts user and channel mentions", () => {
    expect(slackMrkdwnToMarkdown("Hi <@U12345> in <#C1|general>")).toBe("Hi @U12345 in #general");
    expect(slackMrkdwnToMarkdown("Hi <@U12345|nick>")).toBe("Hi @nick");
  });

  test("converts channel mentions without a name to the channel id", () => {
    expect(slackMrkdwnToMarkdown("in <#C0123ABCD> and <#C123|>")).toBe("in #C0123ABCD and #C123");
  });

  test("converts usergroup mentions", () => {
    expect(slackMrkdwnToMarkdown("cc <!subteam^S0123|@oncall> <!subteam^S0456>")).toBe(
      "cc @oncall @S0456",
    );
  });

  test("converts date tokens to their fallback, else a UTC timestamp", () => {
    expect(slackMrkdwnToMarkdown("<!date^1700000000^{date_short}^https://x.test|Nov 14>")).toBe(
      "Nov 14",
    );
    expect(slackMrkdwnToMarkdown("at <!date^1700000000^{time}>")).toBe("at 2023-11-14 22:13 UTC");
  });

  test("converts :emoji: shortcodes to unicode", () => {
    expect(slackMrkdwnToMarkdown("Ship it :rocket:")).toBe("Ship it 🚀");
  });
});
