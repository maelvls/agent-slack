package slacktext

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// inlineStyle is the style of a rich text inline element.
type inlineStyle struct {
	bold, italic, strike, code bool
}

func (s inlineStyle) apply(el map[string]any) map[string]any {
	style := map[string]any{}
	for name, on := range map[string]bool{"bold": s.bold, "italic": s.italic, "strike": s.strike, "code": s.code} {
		if on {
			style[name] = true
		}
	}
	if len(style) > 0 {
		el["style"] = style
	}
	return el
}

// ParseInlineElements parses the inline formatting a user types into Slack
// rich text inline elements: `*bold*`, `_italic_`, `~strike~`, backtick-quoted code,
// `:emoji:` (one element for `:wave::skin-tone-3:`), `<url|label>`, `<url>`,
// bare http(s) URLs, `<@U123>`, `<#C123>`, `<!subteam^S123>`, `<!here>`,
// `<!date^...>`, and the bare `@U123ABCDE`, `@here`, `@channel` and
// `@everyone` (see [bareMentionAt]). Everything else, CommonMark syntax
// included, is kept as literal text.
//
// As in Slack, a `*`, `_` or `~` only opens or closes a span at a word
// boundary, so that `snake_case_name`, `2 * 3 * 4` and URLs stay intact.
func ParseInlineElements(text string) []any {
	p := inlineParser{}
	p.parse(text, inlineStyle{})
	if len(p.out) == 0 {
		return []any{map[string]any{"type": "text", "text": text}}
	}
	return p.out
}

type inlineParser struct {
	out []any
}

func (p *inlineParser) text(s string, style inlineStyle) {
	if s != "" {
		p.out = append(p.out, style.apply(map[string]any{"type": "text", "text": s}))
	}
}

var (
	userTokenRE      = regexp.MustCompile(`^@([UWB][A-Z0-9]+)(?:\|.*)?$`)
	channelTokenRE   = regexp.MustCompile(`^#([CG][A-Z0-9]+)(?:\|.*)?$`)
	usergroupTokenRE = regexp.MustCompile(`^!subteam\^([A-Z0-9]+)(?:\|.*)?$`)
	broadcastTokenRE = regexp.MustCompile(`^!(here|channel|everyone)(?:\|.*)?$`)
	manualLinkRE     = regexp.MustCompile(`^(?i:https?://|mailto:)`)
)

func (p *inlineParser) parse(text string, style inlineStyle) {
	pending := 0 // start of the literal text not yet emitted
	flush := func(end int) {
		p.text(text[pending:end], style)
	}
	i := 0
	for i < len(text) {
		end := p.elementAt(text, i, style, flush)
		if end > i {
			pending, i = end, end
			continue
		}
		i++
	}
	flush(len(text))
}

// elementAt tries to parse an element starting at text[i]. On success it
// calls flush(i), appends the element and returns the index after it. It
// returns i when there is none.
func (p *inlineParser) elementAt(text string, i int, style inlineStyle, flush func(int)) int {
	rest := text[i:]
	afterWord := i > 0 && isWordByte(text[i-1])
	switch text[i] {
	case '`':
		end := strings.IndexAny(rest[1:], "`\n")
		if end <= 0 || rest[1+end] != '`' {
			return i
		}
		flush(i)
		code := style
		code.code = true
		p.text(rest[1:1+end], code)
		return i + end + 2

	case ':':
		name, end, ok := shortcodeAt(text, i)
		if !ok || afterWord || end < len(text) && isShortcodeByte(text[end]) {
			return i
		}
		flush(i)
		emoji := map[string]any{"type": "emoji", "name": name}
		if tone, toneEnd, ok := skinToneAt(text, end); ok {
			// Slack has one element for an emoji and its skin tone.
			emoji["skin_tone"] = int(tone - '0')
			end = toneEnd
		}
		p.out = append(p.out, emoji)
		return end

	case '*', '_', '~':
		content, end, ok := spanAt(text, i)
		if !ok {
			return i
		}
		flush(i)
		inner := style
		switch text[i] {
		case '*':
			inner.bold = true
		case '_':
			inner.italic = true
		default:
			inner.strike = true
		}
		p.parse(content, inner)
		return end

	case '<':
		end := strings.IndexAny(rest[1:], ">\n")
		if end <= 0 || rest[1+end] != '>' {
			return i
		}
		flush(i)
		p.out = append(p.out, angleToken(rest[1:1+end], style))
		return i + end + 2

	case '@':
		m, end, ok := bareMentionAt(text, i)
		if !ok {
			return i
		}
		flush(i)
		if m.userID != "" {
			p.out = append(p.out, map[string]any{"type": "user", "user_id": m.userID})
		} else {
			p.out = append(p.out, map[string]any{"type": "broadcast", "range": m.broadcast})
		}
		return end

	case 'h':
		// A bare URL is a link, as it is in a message sent as plain text,
		// and its `_`, `~` and `@` are neither formatting nor mentions.
		if afterWord {
			return i
		}
		loc := bareURLRE.FindStringIndex(rest)
		if loc == nil || loc[0] != 0 {
			return i
		}
		url := trimBareURL(rest[:loc[1]])
		flush(i)
		p.out = append(p.out, style.apply(map[string]any{"type": "link", "url": url}))
		return i + len(url)
	}
	return i
}

// angleToken converts the inside of a `<...>` token to an element. What is
// neither a mention nor a link stays literal text, brackets included.
func angleToken(token string, style inlineStyle) map[string]any {
	if m := userTokenRE.FindStringSubmatch(token); m != nil {
		return map[string]any{"type": "user", "user_id": m[1]}
	}
	if m := channelTokenRE.FindStringSubmatch(token); m != nil {
		return map[string]any{"type": "channel", "channel_id": m[1]}
	}
	if m := usergroupTokenRE.FindStringSubmatch(token); m != nil {
		return map[string]any{"type": "usergroup", "usergroup_id": m[1]}
	}
	if m := broadcastTokenRE.FindStringSubmatch(token); m != nil {
		return map[string]any{"type": "broadcast", "range": m[1]}
	}
	if date := dateToken(token); date != nil {
		return date
	}
	if url, label, _ := strings.Cut(token, "|"); manualLinkRE.MatchString(url) {
		link := map[string]any{"type": "link", "url": url}
		if label != "" {
			link["text"] = label
		}
		return style.apply(link)
	}
	return style.apply(map[string]any{"type": "text", "text": "<" + token + ">"})
}

// dateToken converts the inside of a `<!date^timestamp^format^link|fallback>`
// token to a rich text `date` element. It returns nil for anything else.
func dateToken(token string) map[string]any {
	command, fallback, _ := strings.Cut(token, "|")
	rest, ok := strings.CutPrefix(command, "!date^")
	if !ok {
		return nil
	}
	parts := strings.SplitN(rest, "^", 3)
	timestamp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || len(parts) < 2 || parts[1] == "" {
		return nil
	}
	date := map[string]any{"type": "date", "timestamp": timestamp, "format": parts[1]}
	if len(parts) == 3 && parts[2] != "" {
		date["url"] = parts[2]
	}
	if fallback != "" {
		date["fallback"] = fallback
	}
	return date
}

// spanAt parses a `*bold*`, `_italic_` or `~strike~` span whose opening
// marker is text[i]. It returns the content and the index after the closing
// marker.
func spanAt(text string, i int) (content string, end int, ok bool) {
	marker := text[i]
	if prev, size := utf8.DecodeLastRuneInString(text[:i]); size > 0 && (isAlnum(prev) || prev == rune(marker)) {
		return "", 0, false
	}
	next, size := utf8.DecodeRuneInString(text[i+1:])
	if size == 0 || unicode.IsSpace(next) || next == rune(marker) {
		return "", 0, false
	}
	for k := i + 1 + size; k < len(text) && text[k] != '\n'; k++ {
		if text[k] != marker {
			continue
		}
		if before, _ := utf8.DecodeLastRuneInString(text[:k]); unicode.IsSpace(before) {
			continue
		}
		if after, size := utf8.DecodeRuneInString(text[k+1:]); size > 0 && (isAlnum(after) || after == rune(marker)) {
			continue
		}
		return text[i+1 : k], k + 1, true
	}
	return "", 0, false
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}
