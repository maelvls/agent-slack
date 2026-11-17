package slacktext

import (
	"regexp"
	"strings"
)

// outboundTokenRE matches the Slack tokens a user may already have
// written, whose `<`, `>` and `&` must not be escaped.
var outboundTokenRE = regexp.MustCompile(`<(?:@[UWB][A-Z0-9]+(?:\|[^>]*)?|#[CG][A-Z0-9]+(?:\|[^>]*)?|!subteam\^[A-Z0-9]+(?:\|[^>]*)?|!date\^[^>]+|![a-zA-Z]+(?:\|[^>]*)?|(?:https?://|mailto:)[^>]+)>`)

// FormatOutboundText prepares user-authored text for the `text` parameter
// of chat.postMessage and chat.update, which is mrkdwn: literal `&`, `<` and
// `>` are escaped as entities, and the mentions people (and models) write
// as `@U05BRPTKL6A`, `@here`, `@channel` and `@everyone` are turned into the
// `<@U05BRPTKL6A>` and `<!here>` tokens that actually notify.
//
// Well-formed Slack tokens already in the text (`<@U123>`, `<#C123|name>`,
// `<!subteam^S123>`, `<!date^...>`, `<https://example.com|label>`) are left
// alone. Nothing inside a code span, a code block or a URL becomes a
// mention, nor does an "@" in the middle of a word, an e-mail address or a
// path: see [bareMentionAt] for what counts as one.
func FormatOutboundText(text string) string {
	var b strings.Builder
	splitCode(text, func(seg string, kind codeKind) {
		literal := func(s string) {
			if kind == notCode {
				writeWithMentions(&b, s)
			} else {
				b.WriteString(entityEscaper.Replace(s))
			}
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

// writeWithMentions writes s entity-escaped, with its bare mentions turned
// into tokens. s is a run of literal text between tokens and code: its
// start is a place where a mention may begin.
func writeWithMentions(b *strings.Builder, s string) {
	urls := bareURLRE.FindAllStringIndex(s, -1)
	pending := 0
	for i := 0; i < len(s); i++ {
		if len(urls) > 0 && i == urls[0][0] {
			// "https://medium.com/@channel/post" must stay a working link.
			i = urls[0][1] - 1
			urls = urls[1:]
			continue
		}
		if s[i] != '@' {
			continue
		}
		m, end, ok := bareMentionAt(s, i)
		if !ok {
			continue
		}
		b.WriteString(entityEscaper.Replace(s[pending:i]))
		if m.userID != "" {
			b.WriteString("<@" + m.userID + ">")
		} else {
			b.WriteString("<!" + m.broadcast + ">")
		}
		pending = end
		i = end - 1
	}
	b.WriteString(entityEscaper.Replace(s[pending:]))
}
