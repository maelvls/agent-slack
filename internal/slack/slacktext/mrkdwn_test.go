package slacktext

import "testing"

func TestMrkdwnToMarkdown(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"labelled link", "See <https://example.com|this>.", "See [this](https://example.com)."},
		{"bare link", "See <https://example.com>.", "See https://example.com."},
		{"user and channel mentions", "Hi <@U12345> in <#C1|general>", "Hi @U12345 in #general"},
		{"labelled user mention", "Hi <@U12345|nick>", "Hi @nick"},
		{"emoji shortcode", "Ship it :rocket:", "Ship it 🚀"},
		{"broadcasts", "<!here> <!channel> <!everyone>", "@here @channel @everyone"},
		{"entities", "a &lt; b &amp;&amp; c &gt; d", "a < b && c > d"},
		{"slack formatting markers are kept", "*bold* _it_ ~no~", "*bold* _it_ ~no~"},

		// Fixed in the port.
		{"channel with an empty label", "in <#C123ABC|>", "in #C123ABC"},
		{"channel without label", "in <#C123ABC>", "in #C123ABC"},
		{"labelled broadcast", "<!here|here> look", "@here look"},
		{"labelled usergroup", "ping <!subteam^S123|@backend-team>", "ping @backend-team"},
		{"unlabelled usergroup keeps its token", "ping <!subteam^S123>", "ping <!subteam^S123>"},
		{"date uses its fallback", "due <!date^1392734382^{date} at {time}|Feb 18, 2014 at 6:39 AM PST>", "due Feb 18, 2014 at 6:39 AM PST"},
		{"date with a link", "<!date^1392734382^{date}^https://example.com/a|Feb 18>", "[Feb 18](https://example.com/a)"},
		{"date without fallback", "<!date^1392734382^{date}>", "2014-02-18 14:39 UTC"},
		{"mailto link", "<mailto:bob@example.com|Bob>", "[Bob](mailto:bob@example.com)"},
		{"mailto link labelled with the address", "<mailto:bob@example.com|bob@example.com>", "bob@example.com"},
		{"link labelled with its own URL", "<https://example.com|https://example.com>", "https://example.com"},
		{"escaped entity is decoded once", "&amp;lt;b&amp;gt;", "&lt;b&gt;"},
		{"entities in a URL and a label", "<https://a.test/?x=1&amp;y=2|A &amp; B>", "[A & B](https://a.test/?x=1&y=2)"},
		{"brackets in a label", "<https://example.com|[JIRA-1] fix>", `[\[JIRA-1\] fix](https://example.com)`},
		{"parentheses in a URL", "<https://en.wikipedia.org/wiki/Go_(game)|Go>", "[Go](https://en.wikipedia.org/wiki/Go_%28game%29)"},
		{"pipe in a label", "<https://example.com|a | b>", "[a | b](https://example.com)"},
		{"emoji in a URL is not converted", "<https://example.com/:rocket:/x>", "https://example.com/:rocket:/x"},
		{"emoji in a label", "<https://example.com|ship :rocket:>", "[ship 🚀](https://example.com)"},
		{"escaped angle brackets are not a token", "&lt;https://example.com|x&gt;", "<https://example.com|x>"},
		{"unknown token is kept", "a <fix> b", "a <fix> b"},
		{"unterminated token", "a < b <@U1", "a < b <@U1"},
		{"code span is not converted", "run `:rocket: &amp;&amp; *x*` now :rocket:", "run `:rocket: && *x*` now 🚀"},
		{"link in a code span becomes its text", "`curl <https://example.com>` and `<http://a.com|a.com>`", "`curl https://example.com` and `a.com`"},
		{"mention in a code span", "`<@U123>`", "`@U123`"},
		{"inline code block gets its own lines", "see ```x := 1``` ok", "see \n```\nx := 1\n```\n ok"},
		{"code block is not converted", "```\nif a &lt; b { :rocket: }\n```", "```\nif a < b { :rocket: }\n```"},
		{"code block with code on the fence line", "before\n```line1\nline2```\nafter :tada:", "before\n```\nline1\nline2\n```\nafter 🎉"},
		{"unterminated code block is text", "```oops :rocket:", "```oops 🚀"},
		{"unterminated backtick is text", "it`s :rocket:", "it`s 🚀"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertEqual(t, MrkdwnToMarkdown(tt.in), tt.want)
		})
	}
}
