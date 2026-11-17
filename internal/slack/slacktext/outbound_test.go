package slacktext

import "testing"

func TestFormatOutboundText(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"bare user ID", "@U05BRPTKL6A heads up", "<@U05BRPTKL6A> heads up"},
		{"bare W and B IDs", "cc @W123456A and @BABCDEFG", "cc <@W123456A> and <@BABCDEFG>"},
		{"formatted mention", "hi <@U123456A>!", "hi <@U123456A>!"},
		{"formatted mention with label", "hi <@U123456A|nick>!", "hi <@U123456A|nick>!"},
		{"formatted usergroup with label", "ping <!subteam^S12345678|@team>", "ping <!subteam^S12345678|@team>"},
		{"formatted usergroup", "ping <!subteam^S12345678>", "ping <!subteam^S12345678>"},
		{"broadcast", "@here ping", "<!here> ping"},
		{"broadcasts", "cc @channel and @everyone", "cc <!channel> and <!everyone>"},
		{"escapes", "a < b && c > d", "a &lt; b &amp;&amp; c &gt; d"},
		{"labelled link", "see <https://example.com|link>", "see <https://example.com|link>"},
		{"mailto link", "mail <mailto:bob@example.com|Bob>", "mail <mailto:bob@example.com|Bob>"},
		{"link with ampersand", "see <https://a.test/?x=1&y=2>", "see <https://a.test/?x=1&y=2>"},
		{"email-like text", "mail me at user@Udomain.com", "mail me at user@Udomain.com"},
		{"empty", "", ""},
		{
			"CI dump with a mention and a URL",
			`@U05BRPTKL6A heads up: CI "Install dependencies" is failing: https://github.com/x/y/actions/runs/1 & it needs <fix>`,
			`<@U05BRPTKL6A> heads up: CI "Install dependencies" is failing: https://github.com/x/y/actions/runs/1 &amp; it needs &lt;fix&gt;`,
		},

		// Fixed in the port.
		{"formatted channel and broadcast", "in <#C12345678|general> <!here>", "in <#C12345678|general> <!here>"},
		{"date token", "at <!date^1392734382^{date}|Feb 18>", "at <!date^1392734382^{date}|Feb 18>"},
		{"mention after a token", "<@U123456A>@here", "<@U123456A><!here>"},
		{"short ID is not a mention", "@U123 and @hereafter", "@U123 and @hereafter"},
		{"no mention in a code span", "run `notify @here && @U05BRPTKL6A` now @here", "run `notify @here &amp;&amp; @U05BRPTKL6A` now <!here>"},
		{"no mention in a code block", "```\n@channel <- literal\n```\n@channel", "```\n@channel &lt;- literal\n```\n<!channel>"},
		{"unterminated backtick is text", "it`s @here", "it`s <!here>"},
		{"NUL bytes are harmless", "a\x000\x00 & b", "a\x000\x00 &amp; b"},
		{"multibyte text", "héllo @here — «ça» > là", "héllo <!here> — «ça» &gt; là"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertEqual(t, FormatOutboundText(tt.in), tt.want)
		})
	}
}
