package slacktext

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// bareUserRE matches the user ID of a bare mention. Slack user IDs are
	// "U" or "W" followed by at least 8 upper-case letters and digits;
	// [bareMentionAt] also requires a digit, which real IDs have and words
	// in capitals (@USERNAME, @WEBHOOKS) do not.
	bareUserRE      = regexp.MustCompile(`^@([UW][A-Z0-9]{8,})\b`)
	bareBroadcastRE = regexp.MustCompile(`^@(here|channel|everyone)\b`)

	// bareURLRE matches a URL written as is, not as a `<url>` token.
	bareURLRE = regexp.MustCompile(`https?://[^\s<>]+`)
)

// bareMention is a mention written without its Slack token syntax.
type bareMention struct {
	userID    string // of "@U05BRPTKL6A", or
	broadcast string // "here", "channel" or "everyone"
}

// bareMentionAt parses the bare mention (`@U05BRPTKL6A`, `@here`,
// `@channel`, `@everyone`) whose "@" is text[i], and returns the index
// after it.
//
// A mention notifies people, so only what was clearly meant as one is
// accepted: the "@" must start the text or follow whitespace, an opening
// bracket or quote, or one of `,;:>*_~`. That leaves alone e-mail addresses
// (bob@here.com), paths and URLs (medium.com/@channel/post) and handles on
// other sites (@channel.io, @here-and-now).
func bareMentionAt(text string, i int) (m bareMention, end int, ok bool) {
	if prev, size := utf8.DecodeLastRuneInString(text[:i]); size > 0 && !opensMention(prev) {
		return bareMention{}, 0, false
	}
	rest := text[i:]
	if match := bareBroadcastRE.FindStringSubmatch(rest); match != nil {
		m.broadcast = match[1]
		end = i + len(match[0])
	} else if match := bareUserRE.FindStringSubmatch(rest); match != nil && strings.ContainsAny(match[1], "0123456789") {
		m.userID = match[1]
		end = i + len(match[0])
	} else {
		return bareMention{}, 0, false
	}
	// "@here." ends a sentence, "@here.com" is a domain.
	if after := text[end:]; len(after) >= 2 && strings.IndexByte(".-/@", after[0]) >= 0 {
		if next, _ := utf8.DecodeRuneInString(after[1:]); isAlnum(next) {
			return bareMention{}, 0, false
		}
	}
	return m, end, true
}

// opensMention reports whether a bare mention may follow r.
func opensMention(r rune) bool {
	return unicode.IsSpace(r) ||
		unicode.Is(unicode.Ps, r) || unicode.Is(unicode.Pi, r) ||
		strings.ContainsRune(`"',;:>*_~`, r)
}

// trimBareURL removes from a bare URL the punctuation that ends the
// sentence, or closes the parenthesis around the URL, rather than being
// part of it.
func trimBareURL(url string) string {
	for url != "" {
		last := url[len(url)-1]
		switch {
		case strings.IndexByte(".,;:!?'\"*~", last) >= 0:
		case last == ')' && strings.Count(url, ")") > strings.Count(url, "("):
		default:
			return url
		}
		url = url[:len(url)-1]
	}
	return url
}
