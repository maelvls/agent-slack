package slacktext

import (
	"strings"
	"testing"
)

func assertContains(t *testing.T, rendered string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in:\n%s", want, rendered)
		}
	}
}

func TestRenderPrefersBlocks(t *testing.T) {
	msg := object(t, `{
		"text": "Title only",
		"blocks": [{"type": "section", "text": {"type": "mrkdwn", "text": "*Hi*\n<https://example.com|View>"}}]
	}`)
	assertEqual(t, RenderMessageContent(msg), "*Hi*\n[View](https://example.com)")
}

func TestRenderFallsBackToText(t *testing.T) {
	assertEqual(t, RenderMessageContent(object(t, `{"text": " Hello <@U1> :wave: "}`)), "Hello @U1 👋")
	assertEqual(t, RenderMessageContent(object(t, `{"text": "Hello", "blocks": [{"type": "divider"}]}`)), "Hello")
	assertEqual(t, RenderMessageContent(nil), "")
	assertEqual(t, RenderMessageContent(object(t, `{"text": 12, "blocks": "nope", "attachments": {}}`)), "")
}

func TestRenderRichTextUsergroupAndBroadcast(t *testing.T) {
	msg := object(t, `{"blocks": [{"type": "rich_text", "elements": [{"type": "rich_text_section", "elements": [
		{"type": "text", "text": "Notify "},
		{"type": "usergroup", "usergroup_id": "S12345678"},
		{"type": "text", "text": " and "},
		{"type": "usergroup", "usergroup_id": "G123ABC456"},
		{"type": "text", "text": " with "},
		{"type": "broadcast", "range": "here"},
		{"type": "text", "text": ", "},
		{"type": "broadcast", "range": "channel"},
		{"type": "text", "text": ", and "},
		{"type": "broadcast", "range": "everyone"}
	]}]}]}`)
	assertEqual(t, RenderMessageContent(msg),
		"Notify <!subteam^S12345678> and <!subteam^G123ABC456> with @here, @channel, and @everyone")
}

func TestRenderOmitsMalformedRichTextElements(t *testing.T) {
	msg := object(t, `{"blocks": [{"type": "rich_text", "elements": [{"type": "rich_text_section", "elements": [
		{"type": "text", "text": "before"},
		{"type": "user", "user_id": "U123> [forged](https://evil.example) <"},
		{"type": "channel", "channel_id": "C123> [forged](https://evil.example) <"},
		{"type": "usergroup"},
		{"type": "usergroup", "usergroup_id": "S123> [forged](https://evil.example) <"},
		{"type": "usergroup", "usergroup_id": 12345},
		{"type": "broadcast", "range": "unknown"},
		{"type": "emoji", "name": "x: <@U1> :y"},
		{"type": "text", "text": "after"}
	]}]}]}`)
	assertEqual(t, RenderMessageContent(msg), "beforeafter")
}

func TestRenderRichTextLiteralTextIsNotMrkdwn(t *testing.T) {
	// The text of a rich text element is literal: what looks like a token
	// or an entity was typed as such.
	msg := object(t, `{"blocks": [{"type": "rich_text", "elements": [{"type": "rich_text_section", "elements": [
		{"type": "text", "text": "use <https://evil.example|safe> &amp; a < b, <@U123> "},
		{"type": "link", "url": "https://example.com/a>b|c", "text": "x > y"}
	]}]}]}`)
	assertEqual(t, RenderMessageContent(msg),
		"use <https://evil.example|safe> &amp; a < b, <@U123> [x > y](https://example.com/a%3Eb%7Cc)")
}

func TestRenderRichTextInline(t *testing.T) {
	msg := object(t, `{"blocks": [{"type": "rich_text", "elements": [{"type": "rich_text_section", "elements": [
		{"type": "text", "text": "Hi "},
		{"type": "user", "user_id": "U123"},
		{"type": "text", "text": " in "},
		{"type": "channel", "channel_id": "C456"},
		{"type": "text", "text": " "},
		{"type": "emoji", "name": "wave", "skin_tone": 3, "unicode": "1f44b-1f3fc"},
		{"type": "emoji", "name": "partyparrot"},
		{"type": "emoji", "name": "brand_new_emoji", "unicode": "1fae8"},
		{"type": "text", "text": " bold ", "style": {"bold": true}},
		{"type": "text", "text": "both", "style": {"bold": true, "italic": true}},
		{"type": "text", "text": " "},
		{"type": "text", "text": ":rocket: a<b", "style": {"code": true}},
		{"type": "text", "text": " "},
		{"type": "link", "url": "https://example.com"},
		{"type": "text", "text": " "},
		{"type": "link", "url": "https://example.com/x", "text": "docs", "style": {"bold": true}},
		{"type": "text", "text": " "},
		{"type": "date", "timestamp": 1392734382, "format": "{date}", "fallback": "Feb 18, 2014"},
		{"type": "text", "text": " "},
		{"type": "color", "value": "#ff0000"}
	]}]}]}`)
	assertEqual(t, RenderMessageContent(msg),
		"Hi @U123 in #C456 👋🏼:partyparrot:\U0001fae8 *bold* _*both*_ `:rocket: a<b` https://example.com *[docs](https://example.com/x)* Feb 18, 2014 #ff0000")
}

func TestRenderRichTextBlocks(t *testing.T) {
	msg := object(t, `{"blocks": [{"type": "rich_text", "elements": [
		{"type": "rich_text_section", "elements": [{"type": "text", "text": "Plan:\n"}]},
		{"type": "rich_text_list", "style": "ordered", "elements": [
			{"type": "rich_text_section", "elements": [{"type": "text", "text": "one"}]},
			{"type": "rich_text_section", "elements": [{"type": "text", "text": "two\nmore"}]}
		]},
		{"type": "rich_text_list", "style": "bullet", "indent": 1, "elements": [
			{"type": "rich_text_section", "elements": [{"type": "text", "text": "nested"}]}
		]},
		{"type": "rich_text_list", "style": "bullet", "indent": 2, "elements": [
			{"type": "rich_text_section", "elements": [{"type": "text", "text": "deeper"}]}
		]},
		{"type": "rich_text_list", "style": "ordered", "offset": 2, "elements": [
			{"type": "rich_text_section", "elements": [{"type": "text", "text": "three"}]},
			{"type": "rich_text_section", "elements": []}
		]},
		{"type": "rich_text_quote", "elements": [{"type": "text", "text": "quoted\n\nagain"}]},
		{"type": "rich_text_preformatted", "elements": [{"type": "text", "text": "if a < b {\n  :rocket: &amp;\n}"}]},
		{"type": "rich_text_section", "elements": [{"type": "text", "text": "\nDone"}]}
	]}]}`)
	want := strings.Join([]string{
		"Plan:",
		"",
		"1. one",
		"2. two",
		"   more",
		"    - nested",
		"        - deeper",
		"3. three",
		"",
		"> quoted",
		">",
		"> again",
		"",
		"```",
		"if a < b {",
		"  :rocket: &amp;",
		"}",
		"```",
		"",
		"Done",
	}, "\n")
	assertEqual(t, RenderMessageContent(msg), want)
}

func TestRenderFallsBackToAttachments(t *testing.T) {
	msg := object(t, `{"text": "", "attachments": [
		{"pretext": "New release published", "title": "<https://example.com|Release>", "text": "Hello"}
	]}`)
	assertEqual(t, RenderMessageContent(msg), "New release published\n[Release](https://example.com)\nHello")
}

func TestRenderSectionFieldsAndButtons(t *testing.T) {
	msg := object(t, `{"blocks": [
		{"type": "section", "text": {"type": "mrkdwn", "text": "*Started*"},
		 "accessory": {"type": "button", "text": {"type": "plain_text", "text": "View"}, "url": "https://example.com/run/1"}},
		{"type": "section", "fields": [
			{"type": "mrkdwn", "text": "*Total Tests:*\n1"},
			{"type": "mrkdwn", "text": "*Triggered By:*\nSCHEDULED"}
		]},
		{"type": "actions", "elements": [
			{"type": "button", "text": {"type": "plain_text", "text": "Logs"}, "url": "https://example.com/logs"},
			{"type": "button", "text": {"type": "plain_text", "text": "No URL"}},
			{"type": "button", "url": "https://example.com/bare"}
		]},
		{"type": "context", "elements": [
			{"type": "mrkdwn", "text": "by <@U1>"},
			{"type": "plain_text", "text": "today"},
			{"type": "image", "image_url": "https://example.com/i.png", "alt_text": "avatar"}
		]},
		{"type": "image", "image_url": "https://example.com/chart.png", "alt_text": "Chart"},
		{"type": "image", "image_url": "https://example.com/noalt.png"}
	]}`)
	want := strings.Join([]string{
		"*Started*",
		"View: https://example.com/run/1",
		"*Total Tests:*\n1",
		"*Triggered By:*\nSCHEDULED",
		"Logs: https://example.com/logs",
		"https://example.com/bare",
		"by @U1",
		"today",
		"Chart: https://example.com/chart.png",
		"https://example.com/noalt.png",
	}, "\n\n")
	assertEqual(t, RenderMessageContent(msg), want)
}

func TestRenderOtherBlockTypes(t *testing.T) {
	// The TypeScript version silently dropped these.
	msg := object(t, `{"blocks": [
		{"type": "header", "text": {"type": "plain_text", "text": "Deploy :rocket:"}},
		{"type": "markdown", "text": "**already** [markdown](https://example.com)"},
		{"type": "video", "title": {"type": "plain_text", "text": "Demo"}, "video_url": "https://example.com/embed", "title_url": "https://example.com/watch"},
		{"type": "table", "rows": [
			[{"type": "raw_text", "text": "Name"}, {"type": "raw_text", "text": "Status"}],
			[{"type": "rich_text", "elements": [{"type": "rich_text_section", "elements": [{"type": "text", "text": "api", "style": {"bold": true}}]}]},
			 {"type": "raw_text", "text": "up | ok\nfine"}]
		]}
	]}`)
	want := strings.Join([]string{
		"Deploy 🚀",
		"**already** [markdown](https://example.com)",
		"Demo: https://example.com/watch",
		"| Name | Status |\n| --- | --- |\n| *api* | up \\| ok fine |",
	}, "\n\n")
	assertEqual(t, RenderMessageContent(msg), want)
}

func TestRenderAttachmentFields(t *testing.T) {
	msg := object(t, `{"text": "", "attachments": [{"fields": [
		{"title": "Total Tests:", "value": "1"},
		{"title": "Triggered By:", "value": "SCHEDULED"},
		{"title": "Only title"},
		{"value": "Only value"}
	]}]}`)
	assertEqual(t, RenderMessageContent(msg), "Total Tests:\n1\nTriggered By:\nSCHEDULED\nOnly title\nOnly value")
}

func TestRenderAttachmentFallback(t *testing.T) {
	msg := object(t, `{"attachments": [{"fallback": "Only a fallback"}, {"fallback": "ignored", "text": "Real text"}]}`)
	assertEqual(t, RenderMessageContent(msg), "Only a fallback\n\nReal text")
}

func TestRenderAttachmentTitleLink(t *testing.T) {
	msg := object(t, `{"attachments": [
		{"title": "[PR] a > b", "title_link": "https://example.com/pr/1"},
		{"title_link": "https://example.com/only-link"}
	]}`)
	assertEqual(t, RenderMessageContent(msg), `[\[PR\] a > b](https://example.com/pr/1)`+"\n\nhttps://example.com/only-link")
}

func TestRenderForwardedWithAuthorAndSource(t *testing.T) {
	msg := object(t, `{
		"blocks": [{"type": "rich_text", "elements": [{"type": "rich_text_section", "elements": [{"type": "emoji", "name": "eyes"}]}]}],
		"attachments": [{
			"is_msg_unfurl": true,
			"is_share": true,
			"author_name": "Alice",
			"author_link": "https://example.slack.com/team/U111",
			"from_url": "https://example.slack.com/archives/C222/p333",
			"message_blocks": [{"message": {"blocks": [{"type": "rich_text", "elements": [
				{"type": "rich_text_section", "elements": [{"type": "text", "text": "Hello from Alice"}]}
			]}]}}],
			"text": "Hello from Alice"
		}]
	}`)
	assertEqual(t, RenderMessageContent(msg), strings.Join([]string{
		"👀",
		"",
		"*Forwarded from [Alice](https://example.slack.com/team/U111) | [original](https://example.slack.com/archives/C222/p333)*",
		"> Hello from Alice",
	}, "\n"))
}

func TestRenderForwardedWithAuthorNameOnly(t *testing.T) {
	msg := object(t, `{"text": "", "attachments": [{"is_share": true, "author_name": "Bob", "text": "Some forwarded text"}]}`)
	assertEqual(t, RenderMessageContent(msg), "*Forwarded from Bob*\n> Some forwarded text")
}

func TestRenderForwardedWithNoAuthor(t *testing.T) {
	msg := object(t, `{"text": "", "attachments": [
		{"is_share": true, "from_url": "https://example.slack.com/archives/C222/p333", "text": "Anonymous forward"}
	]}`)
	assertEqual(t, RenderMessageContent(msg),
		"*Forwarded message | [original](https://example.slack.com/archives/C222/p333)*\n> Anonymous forward")
}

func TestRenderForwardedWithNothing(t *testing.T) {
	assertEqual(t, RenderMessageContent(object(t, `{"attachments": [{"is_share": true}]}`)), "*Forwarded message*")
}

func TestRenderMessageUnfurlIsForwarded(t *testing.T) {
	msg := object(t, `{"attachments": [{"is_msg_unfurl": true, "author_name": "Dan", "message_blocks": [{"message": {"text": "Unfurled"}}]}]}`)
	assertEqual(t, RenderMessageContent(msg), "*Forwarded from Dan*\n> Unfurled")
}

func TestRenderForwardedFileLinks(t *testing.T) {
	msg := object(t, `{"text": "", "attachments": [{
		"is_share": true,
		"from_url": "https://example.slack.com/archives/C222/p333",
		"message_blocks": [{"message": {"text": "Forwarded with image"}}],
		"files": [
			{"name": "image.png", "permalink": "https://example.slack.com/files/U1/F1/image.png"},
			{"title": "No link"},
			{}
		]
	}]}`)
	assertContains(t, RenderMessageContent(msg),
		"> Forwarded with image",
		"> [image.png](https://example.slack.com/files/U1/F1/image.png)",
		"> No link",
		"> file",
	)
}

func TestRenderNestedForwardedAttachments(t *testing.T) {
	msg := object(t, `{"text": "", "attachments": [{
		"is_share": true,
		"author_name": "Carol",
		"message_blocks": [{"message": {"attachments": [{
			"title": "Nested update",
			"title_link": "https://example.com/update",
			"text": "Deployment passed",
			"fields": [{"title": "Env", "value": "prod"}]
		}]}}]
	}]}`)
	assertEqual(t, RenderMessageContent(msg), strings.Join([]string{
		"*Forwarded from Carol*",
		"> [Nested update](https://example.com/update)",
		"> Deployment passed",
		"> Env",
		"> prod",
	}, "\n"))
}

func TestRenderDeduplicatesForwardedBody(t *testing.T) {
	msg := object(t, `{"text": "", "attachments": [{
		"is_share": true,
		"text": "Same content",
		"message_blocks": [{"message": {"text": "Same content"}}]
	}]}`)
	if n := strings.Count(RenderMessageContent(msg), "> Same content"); n != 1 {
		t.Errorf("forwarded body rendered %d times, want 1", n)
	}
}

func TestRenderCyclicForwardedAttachments(t *testing.T) {
	shared := map[string]any{"is_share": true, "author_name": "Loop User", "text": "Cycle-safe text"}
	shared["message_blocks"] = []any{map[string]any{"message": map[string]any{"attachments": []any{shared}}}}
	msg := map[string]any{"text": "", "attachments": []any{shared}}
	assertEqual(t, RenderMessageContent(msg), "*Forwarded from Loop User*\n> Cycle-safe text")
}

func TestRenderDeeplyNestedForwardsAreBounded(t *testing.T) {
	inner := map[string]any{"is_share": true, "text": "bottom"}
	for range 20 {
		inner = map[string]any{"is_share": true, "attachments": []any{inner}}
	}
	rendered := RenderMessageContent(map[string]any{"attachments": []any{inner}})
	if strings.Contains(rendered, "bottom") {
		t.Errorf("rendered past the depth limit:\n%s", rendered)
	}
	assertContains(t, rendered, "*Forwarded message*")
}

func TestRenderLinkUnfurlIsNotForwarded(t *testing.T) {
	msg := object(t, `{"text": "", "attachments": [{
		"from_url": "https://github.com/org/repo/pull/42",
		"title": "Fix login bug",
		"title_link": "https://github.com/org/repo/pull/42",
		"text": "This PR fixes the login flow"
	}]}`)
	assertEqual(t, RenderMessageContent(msg),
		"[Fix login bug](https://github.com/org/repo/pull/42)\nThis PR fixes the login flow")
}

func TestRenderCombinesBlocksAndAttachments(t *testing.T) {
	msg := object(t, `{
		"blocks": [{"type": "section", "text": {"type": "mrkdwn", "text": "Main content"}}],
		"attachments": [{"pretext": "Bot notification", "text": "Details here"}]
	}`)
	assertEqual(t, RenderMessageContent(msg), "Main content\n\nBot notification\nDetails here")
}

func TestMrkdwnFromRichTextBlock(t *testing.T) {
	// The draft view: literal text is kept as typed, with no entities.
	block := object(t, `{"type": "rich_text", "elements": [
		{"type": "rich_text_section", "elements": [
			{"type": "text", "text": "a < b & c "},
			{"type": "text", "text": "bold", "style": {"bold": true}},
			{"type": "text", "text": " "},
			{"type": "emoji", "name": "rocket"},
			{"type": "text", "text": " "},
			{"type": "link", "url": "https://example.com", "text": "site"},
			{"type": "text", "text": " "},
			{"type": "link", "url": "https://example.com/bare"},
			{"type": "text", "text": " "},
			{"type": "user", "user_id": "U12345678"},
			{"type": "text", "text": "\n"}
		]},
		{"type": "rich_text_list", "style": "bullet", "elements": [
			{"type": "rich_text_section", "elements": [{"type": "text", "text": "item"}]}
		]},
		{"type": "rich_text_preformatted", "elements": [{"type": "text", "text": "x < y"}]}
	]}`)
	want := "a < b & c *bold* :rocket: <https://example.com|site> https://example.com/bare <@U12345678>\n\n- item\n\n```\nx < y\n```"
	assertEqual(t, MrkdwnFromRichTextBlock(block), want)
	assertEqual(t, MrkdwnFromRichTextBlock(nil), "")
}

func TestRichTextRoundTrip(t *testing.T) {
	// What a draft is shown as must parse back to the same blocks.
	for _, text := range []string{
		"Hello *world* :tada:",
		"Intro:\n\n- one\n- two\n    - nested\n- three",
		"1. first\n2. second\n    - sub\n3. third",
		"> quoted _text_\n> more",
		"```\ncode < here\n  indented\n```",
		"see <https://example.com|the docs> and <@U12345678> in <#C12345678>",
		"at <!date^1392734382^{date_short} at {time}^https://example.com/x|Feb 18> :wave::skin-tone-3: see https://example.com/a_b",
		"```\nmake\nmake install\n```",
	} {
		blocks := TextToRichTextBlocksInline(text)
		if blocks == nil {
			t.Errorf("no blocks for %q", text)
			continue
		}
		back := MrkdwnFromRichTextBlock(blocks[0].(map[string]any))
		assertEqual(t, back, text)
	}
}

// A forged block must not make the renderer allocate without bound.
func TestRichTextListClampsSizesFromJSON(t *testing.T) {
	msg := object(t, `{"blocks": [{"type": "rich_text", "elements": [
		{"type": "rich_text_list", "style": "bullet", "elements": [
			{"type": "rich_text_section", "elements": [{"type": "text", "text": "top"}]}
		]},
		{"type": "rich_text_list", "style": "ordered", "indent": 9000000000000000000, "offset": 9223372036854775807, "elements": [
			{"type": "rich_text_section", "elements": [{"type": "text", "text": "deep\nitem"}]}
		]},
		{"type": "rich_text_list", "style": "bullet", "indent": -5, "offset": -7, "elements": [
			{"type": "rich_text_section", "elements": [{"type": "text", "text": "shallow"}]}
		]}
	]}]}`)
	pad := strings.Repeat("    ", 8)
	want := "- top\n" + pad + "1000001. deep\n" + pad + "         item\n- shallow"
	assertEqual(t, RenderMessageContent(msg), want)
}
