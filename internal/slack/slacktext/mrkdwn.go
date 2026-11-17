package slacktext

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MrkdwnToMarkdown converts Slack mrkdwn, as found in the text of messages,
// sections and attachments, to Markdown: `<url|label>` links, user, channel,
// group, broadcast and date tokens, HTML entities and emoji shortcodes.
// Slack's `*bold*`, `_italic_` and `~strike~` markers are kept as they are.
//
// The contents of code spans and code blocks are not converted, apart from
// tokens and entities being turned back into the text they stand for.
func MrkdwnToMarkdown(text string) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	afterBlock := false
	splitCode(text, func(seg string, kind codeKind) {
		if kind == codeBlock {
			writeFencedBlock(&b, convertTokens(seg[3:len(seg)-3], true))
			afterBlock = true
			return
		}
		if afterBlock && !strings.HasPrefix(seg, "\n") {
			b.WriteByte('\n')
		}
		afterBlock = false
		b.WriteString(convertTokens(seg, kind == codeSpan))
	})
	return b.String()
}

type codeKind int

const (
	notCode   codeKind = iota
	codeSpan           // `code`, backticks included
	codeBlock          // ```code```, fences included
)

// splitCode cuts text into code and non-code segments, in order. An
// unterminated fence or backtick is ordinary text.
func splitCode(text string, emit func(seg string, kind codeKind)) {
	start, i := 0, 0
	flush := func(end int) {
		if end > start {
			emit(text[start:end], notCode)
		}
	}
	for i < len(text) {
		j := strings.IndexByte(text[i:], '`')
		if j < 0 {
			break
		}
		j += i
		if strings.HasPrefix(text[j:], "```") {
			end := strings.Index(text[j+3:], "```")
			if end < 0 {
				i = j + 3
				continue
			}
			flush(j)
			start = j + 3 + end + 3
			emit(text[j:start], codeBlock)
			i = start
			continue
		}
		end := strings.IndexAny(text[j+1:], "`\n")
		if end <= 0 || text[j+1+end] != '`' {
			i = j + 1
			continue
		}
		flush(j)
		start = j + 1 + end + 1
		emit(text[j:start], codeSpan)
		i = start
	}
	flush(len(text))
}

// writeFencedBlock writes code as a Markdown fenced block. Slack accepts
// "```code```" on one line, but in Markdown the fences need their own lines:
// otherwise the first line of the code is read as the info string.
func writeFencedBlock(b *strings.Builder, code string) {
	code = strings.TrimPrefix(code, "\n")
	code = strings.TrimSuffix(code, "\n")
	if s := b.String(); s != "" && !strings.HasSuffix(s, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("```\n")
	b.WriteString(code)
	b.WriteString("\n```")
}

var entityDecoder = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")

// convertTokens converts the `<...>` tokens of a mrkdwn fragment and decodes
// the entities of the text around them. In code, tokens become the plain
// text they stand for and emoji shortcodes are kept.
func convertTokens(text string, code bool) string {
	literal := func(s string) string {
		s = entityDecoder.Replace(s)
		if code {
			return s
		}
		return EmojiShortcodesToUnicode(s)
	}
	if !strings.Contains(text, "<") {
		return literal(text)
	}
	var b strings.Builder
	for text != "" {
		open := strings.IndexByte(text, '<')
		if open < 0 {
			break
		}
		b.WriteString(literal(text[:open]))
		text = text[open:]
		end := strings.IndexAny(text[1:], "<>")
		if end < 0 || text[1+end] != '>' {
			b.WriteByte('<')
			text = text[1:]
			continue
		}
		token := text[1 : 1+end]
		if converted, ok := convertToken(token, code, literal); ok {
			b.WriteString(converted)
		} else {
			b.WriteString(text[:end+2])
		}
		text = text[end+2:]
	}
	b.WriteString(literal(text))
	return b.String()
}

var (
	linkSchemeRE  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	specialWordRE = regexp.MustCompile(`^[A-Za-z]+$`)
)

// convertToken converts the inside of one `<...>` token. It reports false
// for what is not a token Slack produces, which is then kept verbatim.
func convertToken(token string, plain bool, literal func(string) string) (string, bool) {
	target, label, _ := strings.Cut(token, "|")
	if target == "" {
		return "", false
	}
	label = literal(label)
	switch target[0] {
	case '@':
		if len(target) == 1 {
			return "", false
		}
		if label != "" {
			return "@" + strings.TrimPrefix(label, "@"), true
		}
		return "@" + target[1:], true
	case '#':
		if len(target) == 1 {
			return "", false
		}
		if label != "" {
			return "#" + strings.TrimPrefix(label, "#"), true
		}
		return "#" + target[1:], true
	case '!':
		return convertSpecialToken(target[1:], label, plain)
	}
	if !linkSchemeRE.MatchString(target) {
		return "", false
	}
	url := entityDecoder.Replace(target)
	bare := url
	for _, scheme := range []string{"mailto:", "tel:"} {
		bare = strings.TrimPrefix(bare, scheme)
	}
	switch {
	case label == "" || label == url || label == bare:
		return bare, true
	case plain:
		return label, true
	}
	return markdownLink(label, url), true
}

// convertSpecialToken handles `<!here>`, `<!subteam^S123|@team>` and
// `<!date^1700000000^{date}|fallback>`.
func convertSpecialToken(command, label string, plain bool) (string, bool) {
	switch {
	case command == "here" || command == "channel" || command == "everyone":
		return "@" + command, true
	case strings.HasPrefix(command, "subteam^"):
		if label == "" {
			// The handle is unknown: keep the token, which still identifies
			// the group and can be sent back to Slack as is.
			return "", false
		}
		return "@" + strings.TrimPrefix(label, "@"), true
	case strings.HasPrefix(command, "date^"):
		parts := strings.Split(command, "^")
		text := label
		if text == "" {
			sec, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return "", false
			}
			text = time.Unix(sec, 0).UTC().Format("2006-01-02 15:04 UTC")
		}
		if len(parts) > 3 && parts[3] != "" && !plain {
			return markdownLink(text, entityDecoder.Replace(strings.Join(parts[3:], "^"))), true
		}
		return text, true
	case label != "":
		return label, true
	case specialWordRE.MatchString(command):
		return "@" + command, true
	}
	return "", false
}

var (
	linkLabelEscaper = strings.NewReplacer(`\`, `\\`, "[", `\[`, "]", `\]`, "\n", " ")
	linkURLEscaper   = strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29", "<", "%3C", ">", "%3E", "\n", "%0A")
)

// markdownLink formats a Markdown link whose label and URL cannot break out
// of the link syntax.
func markdownLink(label, url string) string {
	return "[" + linkLabelEscaper.Replace(label) + "](" + linkURLEscaper.Replace(url) + ")"
}
