package slacktext

import "testing"

func TestParseInlineElements(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain text", "Hello world", `[{"type":"text","text":"Hello world"}]`},
		{"empty", "", `[{"type":"text","text":""}]`},
		{"bold", "Hello *world*!", `[
			{"type":"text","text":"Hello "},
			{"type":"text","text":"world","style":{"bold":true}},
			{"type":"text","text":"!"}]`},
		{"italic", "This is _important_", `[
			{"type":"text","text":"This is "},
			{"type":"text","text":"important","style":{"italic":true}}]`},
		{"strike", "~done~", `[{"type":"text","text":"done","style":{"strike":true}}]`},
		{"code", "Run `npm install`", `[
			{"type":"text","text":"Run "},
			{"type":"text","text":"npm install","style":{"code":true}}]`},
		{"emoji", "Launch :rocket: now", `[
			{"type":"text","text":"Launch "},
			{"type":"emoji","name":"rocket"},
			{"type":"text","text":" now"}]`},
		{"emoji name with underscores is not italic", ":white_check_mark: all clear", `[
			{"type":"emoji","name":"white_check_mark"},
			{"type":"text","text":" all clear"}]`},
		{"time is not an emoji", "Time 12:30:00", `[{"type":"text","text":"Time 12:30:00"}]`},
		{"labelled link", "Visit <https://example.com|Example>", `[
			{"type":"text","text":"Visit "},
			{"type":"link","url":"https://example.com","text":"Example"}]`},
		{"bare link", "See <https://example.com>", `[
			{"type":"text","text":"See "},
			{"type":"link","url":"https://example.com"}]`},
		{"mailto link", "Email <mailto:bob@example.com|Bob>", `[
			{"type":"text","text":"Email "},
			{"type":"link","url":"mailto:bob@example.com","text":"Bob"}]`},
		{"non-URL angle brackets stay text", "Use <fix>", `[
			{"type":"text","text":"Use "},
			{"type":"text","text":"<fix>"}]`},
		{"non-URL labelled angle brackets stay text", "Use <fix|label>", `[
			{"type":"text","text":"Use "},
			{"type":"text","text":"<fix|label>"}]`},
		{"labelled channel", "See <#C12345678|general>", `[
			{"type":"text","text":"See "},
			{"type":"channel","channel_id":"C12345678"}]`},
		{"channel", "See <#C12345678>", `[
			{"type":"text","text":"See "},
			{"type":"channel","channel_id":"C12345678"}]`},
		{"usergroup", "Ping <!subteam^S12345678|@team>", `[
			{"type":"text","text":"Ping "},
			{"type":"usergroup","usergroup_id":"S12345678"}]`},
		{"user tokens and bare IDs", "<@U12345678> <@W12345678|nick> @U05BRPTKL6A, x@U05BRPTKL6A @U123", `[
			{"type":"user","user_id":"U12345678"},
			{"type":"text","text":" "},
			{"type":"user","user_id":"W12345678"},
			{"type":"text","text":" "},
			{"type":"user","user_id":"U05BRPTKL6A"},
			{"type":"text","text":", x@U05BRPTKL6A @U123"}]`},
		{"broadcasts", "<!here> <!channel|channel> @everyone @hereafter", `[
			{"type":"broadcast","range":"here"},
			{"type":"text","text":" "},
			{"type":"broadcast","range":"channel"},
			{"type":"text","text":" "},
			{"type":"broadcast","range":"everyone"},
			{"type":"text","text":" @hereafter"}]`},

		// Fixed in the port: the TypeScript regex formatted all of these.
		{"underscores inside words", "rename snake_case_name to other_name", `[{"type":"text","text":"rename snake_case_name to other_name"}]`},
		{"arithmetic", "2 * 3 * 4 and a*b*c", `[{"type":"text","text":"2 * 3 * 4 and a*b*c"}]`},
		{"tilde as approximately", "~5 min or ~10 min", `[{"type":"text","text":"~5 min or ~10 min"}]`},
		{"bare URL with underscores", "see https://example.com/a_b_c/_d_ now", `[
			{"type":"text","text":"see "},
			{"type":"link","url":"https://example.com/a_b_c/_d_"},
			{"type":"text","text":" now"}]`},
		{"bare URL ending a sentence", "(see https://example.com/a_(b)). Or http://x.test/?q=1&r=2, ok", `[
			{"type":"text","text":"(see "},
			{"type":"link","url":"https://example.com/a_(b)"},
			{"type":"text","text":"). Or "},
			{"type":"link","url":"http://x.test/?q=1&r=2"},
			{"type":"text","text":", ok"}]`},
		{"styled bare URL", "*https://example.com*", `[{"type":"link","url":"https://example.com","style":{"bold":true}}]`},
		{"URL with a mention-like path", "https://medium.com/@channel/post-1 https://www.youtube.com/@UNBOXTHERAPY", `[
			{"type":"link","url":"https://medium.com/@channel/post-1"},
			{"type":"text","text":" "},
			{"type":"link","url":"https://www.youtube.com/@UNBOXTHERAPY"}]`},
		{"words and addresses are not mentions", "@WEBHOOKS @USERNAME bob@here.com @channel.io a/@here", `[{"type":"text","text":"@WEBHOOKS @USERNAME bob@here.com @channel.io a/@here"}]`},
		{"mention after punctuation", "(@here) cc:@U05BRPTKL6A.", `[
			{"type":"text","text":"("},
			{"type":"broadcast","range":"here"},
			{"type":"text","text":") cc:"},
			{"type":"user","user_id":"U05BRPTKL6A"},
			{"type":"text","text":"."}]`},
		{"date token", "due <!date^1392734382^{date_short} at {time}^https://example.com|Feb 18> ok", `[
			{"type":"text","text":"due "},
			{"type":"date","timestamp":1392734382,"format":"{date_short} at {time}","url":"https://example.com","fallback":"Feb 18"},
			{"type":"text","text":" ok"}]`},
		{"date token without link or fallback", "<!date^1392734382^{date}>", `[{"type":"date","timestamp":1392734382,"format":"{date}"}]`},
		{"malformed date token stays text", "<!date^soon^{date}|x> <!date^1392734382>", `[
			{"type":"text","text":"<!date^soon^{date}|x>"},
			{"type":"text","text":" "},
			{"type":"text","text":"<!date^1392734382>"}]`},
		{"skin-toned emoji is one element", "hi :wave::skin-tone-3: :+1::skin-tone-6:!", `[
			{"type":"text","text":"hi "},
			{"type":"emoji","name":"wave","skin_tone":3},
			{"type":"text","text":" "},
			{"type":"emoji","name":"+1","skin_tone":6},
			{"type":"text","text":"!"}]`},
		{"CommonMark bold stays literal", "**bold**", `[{"type":"text","text":"**bold**"}]`},
		{"span does not cross lines", "*not\nbold*", `[{"type":"text","text":"*not\nbold*"}]`},
		{"marker inside a span", "_snake_case_", `[{"type":"text","text":"snake_case","style":{"italic":true}}]`},
		{"nested styles", "*bold _both_ `code`* <https://example.com|x>", `[
			{"type":"text","text":"bold ","style":{"bold":true}},
			{"type":"text","text":"both","style":{"bold":true,"italic":true}},
			{"type":"text","text":" ","style":{"bold":true}},
			{"type":"text","text":"code","style":{"bold":true,"code":true}},
			{"type":"text","text":" "},
			{"type":"link","url":"https://example.com","text":"x"}]`},
		{"styled link", "*<https://example.com|x>*", `[{"type":"link","url":"https://example.com","text":"x","style":{"bold":true}}]`},
		{"code is literal", "`*a* :b: <c> _d_`", `[{"type":"text","text":"*a* :b: <c> _d_","style":{"code":true}}]`},
		{"non-ASCII neighbours", "été *très* «_bien_»", `[
			{"type":"text","text":"été "},
			{"type":"text","text":"très","style":{"bold":true}},
			{"type":"text","text":" «"},
			{"type":"text","text":"bien","style":{"italic":true}},
			{"type":"text","text":"»"}]`},
		{"link with an empty label", "<https://example.com|>", `[{"type":"link","url":"https://example.com"}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertJSON(t, ParseInlineElements(tt.in), tt.want)
		})
	}
}

func TestTextToRichTextBlocksNil(t *testing.T) {
	for _, text := range []string{"Hello world", "Visit <https://example.com|Example>", "*bold* only", "", "-5 degrees\n2024.5 is not a list\n-\n1.", "2024. Great year", "In short:\n2024) was fine\nthe end", "use ``` to open a block"} {
		if got := TextToRichTextBlocks(text); got != nil {
			t.Errorf("TextToRichTextBlocks(%q) = %v, want nil", text, got)
		}
	}
	for _, text := range []string{"Hello world", "Use <fix>", "Use <fix|label>", "", "```\n```"} {
		if got := TextToRichTextBlocksInline(text); got != nil {
			t.Errorf("TextToRichTextBlocksInline(%q) = %v, want nil", text, got)
		}
	}
}

func TestTextToRichTextBlocks(t *testing.T) {
	item := func(text string) string {
		return `{"type":"rich_text_section","elements":[{"type":"text","text":"` + text + `"}]}`
	}
	tests := []struct {
		name   string
		in     string
		inline bool
		want   string // the elements of the single rich_text block
	}{
		{"non-URL angle brackets with formatting", "Use <fix|label> and *bold*", true, `[
			{"type":"rich_text_section","elements":[
				{"type":"text","text":"Use "},
				{"type":"text","text":"<fix|label>"},
				{"type":"text","text":" and "},
				{"type":"text","text":"bold","style":{"bold":true}},
				{"type":"text","text":"\n"}]}]`},
		{"inline-only link", "Visit <https://example.com|Example>", true, `[
			{"type":"rich_text_section","elements":[
				{"type":"text","text":"Visit "},
				{"type":"link","url":"https://example.com","text":"Example"},
				{"type":"text","text":"\n"}]}]`},
		{"inline-only mailto", "Email <mailto:bob@example.com|Bob>", true, `[
			{"type":"rich_text_section","elements":[
				{"type":"text","text":"Email "},
				{"type":"link","url":"mailto:bob@example.com","text":"Bob"},
				{"type":"text","text":"\n"}]}]`},
		{"inline-only channel", "See <#C12345678|general>", true, `[
			{"type":"rich_text_section","elements":[
				{"type":"text","text":"See "},
				{"type":"channel","channel_id":"C12345678"},
				{"type":"text","text":"\n"}]}]`},
		{"bullets with -", "- Item 1\n- Item 2\n- Item 3", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("Item 1") + `,` + item("Item 2") + `,` + item("Item 3") + `]}]`},
		{"bullets with a bullet character", "• Item 1\n• Item 2", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("Item 1") + `,` + item("Item 2") + `]}]`},
		{"sub-bullets", "- Main 1\n- Main 2\n  - Sub 2a\n  - Sub 2b\n- Main 3", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("Main 1") + `,` + item("Main 2") + `]},
			{"type":"rich_text_list","style":"bullet","indent":1,"elements":[` + item("Sub 2a") + `,` + item("Sub 2b") + `]},
			{"type":"rich_text_list","style":"bullet","elements":[` + item("Main 3") + `]}]`},
		{"white bullet sub-bullets", "• Top level\n  ◦ Sub-bullet\n  ◦ Another sub", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("Top level") + `]},
			{"type":"rich_text_list","style":"bullet","indent":1,"elements":[` + item("Sub-bullet") + `,` + item("Another sub") + `]}]`},
		{"text then bullets", "Here is a list:\n- Item 1\n- Item 2", false, `[
			` + item(`Here is a list:\n`) + `,
			{"type":"rich_text_list","style":"bullet","elements":[` + item("Item 1") + `,` + item("Item 2") + `]}]`},
		{"numbered list", "1. First\n2. Second\n3) Third", false, `[
			{"type":"rich_text_list","style":"ordered","elements":[` + item("First") + `,` + item("Second") + `,` + item("Third") + `]}]`},
		{"bold in list items", "- *Bold item*\n- Normal item", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[
				{"type":"rich_text_section","elements":[{"type":"text","text":"Bold item","style":{"bold":true}}]},
				` + item("Normal item") + `]}]`},
		{"emoji and channels in list items", "Header:\n- :rocket: launch sequence\n- discuss in <#C0AHR9XAT8B>\n- :white_check_mark: all clear", false, `[
			` + item(`Header:\n`) + `,
			{"type":"rich_text_list","style":"bullet","elements":[
				{"type":"rich_text_section","elements":[{"type":"emoji","name":"rocket"},{"type":"text","text":" launch sequence"}]},
				{"type":"rich_text_section","elements":[{"type":"text","text":"discuss in "},{"type":"channel","channel_id":"C0AHR9XAT8B"}]},
				{"type":"rich_text_section","elements":[{"type":"emoji","name":"white_check_mark"},{"type":"text","text":" all clear"}]}]}]`},
		{"Slack links and CommonMark links stay distinct", "- Review <https://example.com/pull/42|PR #42>\n- Review [PR #43](https://example.com/pull/43)", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[
				{"type":"rich_text_section","elements":[{"type":"text","text":"Review "},{"type":"link","url":"https://example.com/pull/42","text":"PR #42"}]},
				{"type":"rich_text_section","elements":[{"type":"text","text":"Review [PR #43]("},{"type":"link","url":"https://example.com/pull/43"},{"type":"text","text":")"}]}]}]`},
		{"code block after a list", "- Item\n```\ncode here\n```", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("Item") + `]},
			{"type":"rich_text_preformatted","elements":[{"type":"text","text":"code here"}]}]`},
		{"blockquote after a list", "- Item\n> quoted text\n>\n> more", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("Item") + `]},
			{"type":"rich_text_quote","elements":[{"type":"text","text":"quoted text\n\nmore"}]}]`},

		// Fixed in the port.
		{"three levels of nesting", "- a\n  - b\n    - c\n  - d\n- e", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			{"type":"rich_text_list","style":"bullet","indent":1,"elements":[` + item("b") + `]},
			{"type":"rich_text_list","style":"bullet","indent":2,"elements":[` + item("c") + `]},
			{"type":"rich_text_list","style":"bullet","indent":1,"elements":[` + item("d") + `]},
			{"type":"rich_text_list","style":"bullet","elements":[` + item("e") + `]}]`},
		{"bullets nested in a numbered list keep its numbering", "1. a\n   - sub\n2. b\n3. c", false, `[
			{"type":"rich_text_list","style":"ordered","elements":[` + item("a") + `]},
			{"type":"rich_text_list","style":"bullet","indent":1,"elements":[` + item("sub") + `]},
			{"type":"rich_text_list","style":"ordered","offset":1,"elements":[` + item("b") + `,` + item("c") + `]}]`},
		{"numbered list not starting at 1", "3. c\n4. d", false, `[
			{"type":"rich_text_list","style":"ordered","offset":2,"elements":[` + item("c") + `,` + item("d") + `]}]`},
		{"bullets then numbers are two lists", "- a\n1. b", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			{"type":"rich_text_list","style":"ordered","elements":[` + item("b") + `]}]`},
		{"tab-indented sub-bullets", "- a\n\t- b", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			{"type":"rich_text_list","style":"bullet","indent":1,"elements":[` + item("b") + `]}]`},
		{"empty marker is not an item", "- a\n- \nb", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			` + item(`- \nb\n`) + `]`},
		{"list markers inside a code block are code", "- a\n```\n- not a list\n1. nor this\n> nor a quote\n```\ndone", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			{"type":"rich_text_preformatted","elements":[{"type":"text","text":"- not a list\n1. nor this\n> nor a quote"}]},
			` + item(`done\n`) + `]`},
		{"language tag is dropped", "- a\n```go\nx := 1\n```", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			{"type":"rich_text_preformatted","elements":[{"type":"text","text":"x := 1"}]}]`},
		{"single-line code block keeps its code and what follows", "- a\n```x := 1``` then\n- b", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			{"type":"rich_text_preformatted","elements":[{"type":"text","text":"x := 1"}]},
			` + item(`then\n`) + `,
			{"type":"rich_text_list","style":"bullet","elements":[` + item("b") + `]}]`},
		{"code on the fence lines", "- a\n```first line\nsecond line```", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			{"type":"rich_text_preformatted","elements":[{"type":"text","text":"first line\nsecond line"}]}]`},
		{"unclosed code block runs to the end", "- a\n```\ncode\n- still code", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			{"type":"rich_text_preformatted","elements":[{"type":"text","text":"code\n- still code"}]}]`},
		{"empty code block is dropped", "- a\n```\n```", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]}]`},
		{"CRLF line endings", "- a\r\n- b\r\n", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `,` + item("b") + `]}]`},
		{"first line of a code block that is not a language tag is code", "- a\n```make\nmake install```", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			{"type":"rich_text_preformatted","elements":[{"type":"text","text":"make\nmake install"}]}]`},
		{"one-word first line is kept, inline mode", "```make\nmake install```", true, `[
			{"type":"rich_text_preformatted","elements":[{"type":"text","text":"make\nmake install"}]}]`},
		{"unknown info string is kept", "- a\n```ls\nls -l\n```", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			{"type":"rich_text_preformatted","elements":[{"type":"text","text":"ls\nls -l"}]}]`},
		{"language name as Slack-style code is kept", "- a\n```go\ngo build```", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			{"type":"rich_text_preformatted","elements":[{"type":"text","text":"go\ngo build"}]}]`},
		{"fence in the middle of a line", "- a\nRun: ```\n- x\n1. y\n```\n- b\n- c", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			` + item(`Run: \n`) + `,
			{"type":"rich_text_preformatted","elements":[{"type":"text","text":"- x\n1. y"}]},
			{"type":"rich_text_list","style":"bullet","elements":[` + item("b") + `,` + item("c") + `]}]`},
		{"indented fences", "- a\n  ```python\n  - x\n  - y\n  ```\n- b", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			{"type":"rich_text_preformatted","elements":[{"type":"text","text":"  - x\n  - y"}]},
			{"type":"rich_text_list","style":"bullet","elements":[` + item("b") + `]}]`},
		{"year at the start of a paragraph is not a list", "- a\n2024. Great year\nfor us", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[` + item("a") + `]},
			` + item(`2024. Great year\nfor us\n`) + `]`},
		{"single numbered item", "1. only item", false, `[
			{"type":"rich_text_list","style":"ordered","elements":[` + item("only item") + `]}]`},
		{"large numbers in a run are a list", "100. a\n101. b", false, `[
			{"type":"rich_text_list","style":"ordered","offset":99,"elements":[` + item("a") + `,` + item("b") + `]}]`},
		{"links, dates and skin tones in list items", "- see https://example.com/a_b.\n- at <!date^1392734382^{time}|noon> :wave::skin-tone-2:", false, `[
			{"type":"rich_text_list","style":"bullet","elements":[
				{"type":"rich_text_section","elements":[{"type":"text","text":"see "},{"type":"link","url":"https://example.com/a_b"},{"type":"text","text":"."}]},
				{"type":"rich_text_section","elements":[{"type":"text","text":"at "},{"type":"date","timestamp":1392734382,"format":"{time}","fallback":"noon"},{"type":"text","text":" "},{"type":"emoji","name":"wave","skin_tone":2}]}]}]`},
		{"code block alone, inline mode", "```\ncode\n```", true, `[
			{"type":"rich_text_preformatted","elements":[{"type":"text","text":"code"}]}]`},
		{"quote alone, inline mode", "> *quoted*", true, `[
			{"type":"rich_text_quote","elements":[{"type":"text","text":"quoted","style":{"bold":true}}]}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			convert := TextToRichTextBlocks
			if tt.inline {
				convert = TextToRichTextBlocksInline
			}
			assertJSON(t, convert(tt.in), `[{"type":"rich_text","elements":`+tt.want+`}]`)
		})
	}
}
