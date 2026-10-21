import { describe, expect, test } from "bun:test";
import { renderSlackMessageContent } from "../src/slack/render.ts";

function richText(elements: unknown[]) {
  return { blocks: [{ type: "rich_text", elements }] };
}

function section(elements: unknown[]) {
  return { type: "rich_text_section", elements };
}

function text(value: string) {
  return { type: "text", text: value };
}

describe("rich_text rendering", () => {
  test("renders date elements from fallback, else the timestamp", () => {
    const msg = richText([
      section([
        text("Due "),
        { type: "date", timestamp: 1700000000, format: "{date_short}", fallback: "Nov 14, 2023" },
        text(", review "),
        { type: "date", timestamp: 1700000000, format: "{date_short}" },
      ]),
    ]);

    expect(renderSlackMessageContent(msg)).toBe("Due Nov 14, 2023, review 2023-11-14 22:13 UTC");
  });

  test("leaves out-of-range dates as tokens instead of throwing", () => {
    const element = richText([
      section([text("at "), { type: "date", timestamp: 99999999999999, format: "{date}" }]),
    ]);
    expect(renderSlackMessageContent(element)).toBe("at <!date^99999999999999^{date}>");

    // A token typed literally into a text element goes through the same mrkdwn pass.
    const typed = richText([section([text("see <!date^99999999999999^x> ok")])]);
    expect(renderSlackMessageContent(typed)).toBe("see <!date^99999999999999^x> ok");

    expect(renderSlackMessageContent({ text: "<!date^99999999999999^{date}>" })).toBe(
      "<!date^99999999999999^{date}>",
    );
  });

  test("renders channel mentions as #id", () => {
    const msg = richText([section([text("See "), { type: "channel", channel_id: "C0123ABCD" }])]);

    expect(renderSlackMessageContent(msg)).toBe("See #C0123ABCD");
  });

  test("numbers ordered lists from offset and indents nested lists", () => {
    const msg = richText([
      { type: "rich_text_list", style: "ordered", elements: [section([text("one")])] },
      {
        type: "rich_text_list",
        style: "bullet",
        indent: 1,
        elements: [section([text("nested a")]), section([text("nested b")])],
      },
      { type: "rich_text_list", style: "ordered", offset: 1, elements: [section([text("two")])] },
      section([text("after")]),
    ]);

    expect(renderSlackMessageContent(msg)).toBe(
      "1. one\n  - nested a\n  - nested b\n2. two\n\nafter",
    );
  });
});
