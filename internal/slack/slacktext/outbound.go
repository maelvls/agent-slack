package slacktext

import (
	"regexp"
	"strings"
)

var (
	// outboundTokenRE matches the Slack tokens a user may already have
	// written, whose `<`, `>` and `&` must not be escaped.
	outboundTokenRE = regexp.MustCompile(`<(?:@[UWB][A-Z0-9]+(?:\|[^>]*)?|#[CG][A-Z0-9]+(?:\|[^>]*)?|!subteam\^[A-Z0-9]+(?:\|[^>]*)?|!date\^[^>]+|![a-zA-Z]+(?:\|[^>]*)?|(?:https?://|mailto:)[^>]+)>`)

	outboundUserRE      = regexp.MustCompile(`(^|[^A-Za-z0-9_])@([UWB][A-Z0-9]{6,})\b`)
	outboundBroadcastRE = regexp.MustCompile(`(^|[^A-Za-z0-9_])@(here|channel|everyone)\b`)
)

// FormatOutboundText prepares user-authored text for the `text` parameter
// of chat.postMessage and chat.update, which is mrkdwn: literal `&`, `<` and
// `>` are escaped as entities, and the mentions people (and models) write
// as `@U05BRPTKL6A`, `@here`, `@channel` and `@everyone` are turned into the
// `<@U05BRPTKL6A>` and `<!here>` tokens that actually notify.
//
// Well-formed Slack tokens already in the text (`<@U123>`, `<#C123|name>`,
// `<!subteam^S123>`, `<!date^...>`, `<https://example.com|label>`) are left
// alone, and nothing inside a code span or a code block becomes a mention.
func FormatOutboundText(text string) string {
	var b strings.Builder
	splitCode(text, func(seg string, kind codeKind) {
		literal := func(s string) {
			s = entityEscaper.Replace(s)
			if kind == notCode {
				s = outboundUserRE.ReplaceAllString(s, "$1<@$2>")
				s = outboundBroadcastRE.ReplaceAllString(s, "$1<!$2>")
			}
			b.WriteString(s)
		}
		last := 0
		for _, loc := range outboundTokenRE.FindAllStringIndex(seg, -1) {
			literal(seg[last:loc[0]])
			b.WriteString(seg[loc[0]:loc[1]])
			last = loc[1]
		}
		literal(seg[last:])
	})
	return b.String()
}
