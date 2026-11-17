package slacktext

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/maelvls/slackcli/internal/jsonx"
)

// MrkdwnFromRichTextBlock renders a rich_text block as the mrkdwn-like text
// a user would type to get it: `*bold*`, `:emoji:`, `<url|label>`, `<@U123>`,
// "- " and "1. " lists, "> " quotes and ``` fences. It is the inverse of
// [TextToRichTextBlocksInline], and what a draft is shown and edited as.
//
// The text of the block is kept literally: `&`, `<` and `>` are not escaped
// as entities. Use [RenderMessageContent] to render a message for reading.
func MrkdwnFromRichTextBlock(block map[string]any) string {
	return richTextMrkdwn(block, false)
}

// richTextMrkdwn renders the elements of a rich_text block (or of a
// rich_text table cell). With escape set, the result is strict mrkdwn, safe
// to give to [MrkdwnToMarkdown]: literal text is entity-escaped so that it
// cannot be read as a token.
func richTextMrkdwn(block map[string]any, escape bool) string {
	rt := richText{escape: escape}
	var b strings.Builder
	previousWasList := false
	for _, el := range jsonx.Maps(block["elements"]) {
		text := strings.Trim(rt.element(el), "\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		isList := jsonx.Str(el["type"]) == "rich_text_list"
		switch {
		case b.Len() == 0:
		case isList && previousWasList:
			// Slack splits a nested list into one element per run of items
			// at the same depth: together they are a single list.
			b.WriteString("\n")
		default:
			b.WriteString("\n\n")
		}
		b.WriteString(text)
		previousWasList = isList
	}
	return b.String()
}

type richText struct {
	escape bool
}

var entityEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func (rt richText) literal(text string) string {
	if rt.escape {
		return entityEscaper.Replace(text)
	}
	return text
}

func (rt richText) inline(el map[string]any) string {
	var b strings.Builder
	for _, child := range jsonx.Maps(el["elements"]) {
		b.WriteString(rt.element(child))
	}
	return b.String()
}

func (rt richText) element(el map[string]any) string {
	switch jsonx.Str(el["type"]) {
	case "rich_text_section":
		return rt.inline(el)
	case "rich_text_preformatted":
		text := strings.Trim(rt.inline(el), "\n")
		if text == "" {
			return ""
		}
		return "```\n" + text + "\n```"
	case "rich_text_quote":
		text := strings.TrimSpace(rt.inline(el))
		if text == "" {
			return ""
		}
		return quoteMarkdown(text)
	case "rich_text_list":
		return rt.list(el)
	case "text":
		return styled(rt.literal(jsonx.Str(el["text"])), jsonx.Map(el["style"]))
	case "link":
		return styled(rt.link(jsonx.Str(el["url"]), jsonx.Str(el["text"])), jsonx.Map(el["style"]))
	case "emoji":
		return rt.emoji(el)
	case "user":
		if id := jsonx.Str(el["user_id"]); isSafeEncodedID(id) {
			return "<@" + id + ">"
		}
	case "channel":
		if id := jsonx.Str(el["channel_id"]); isSafeEncodedID(id) {
			return "<#" + id + ">"
		}
	case "usergroup":
		if id := jsonx.Str(el["usergroup_id"]); isSafeEncodedID(id) {
			return "<!subteam^" + id + ">"
		}
	case "broadcast":
		switch r := jsonx.Str(el["range"]); r {
		case "here", "channel", "everyone":
			return "<!" + r + ">"
		}
	case "date":
		return rt.date(el)
	case "color":
		return rt.literal(jsonx.Str(el["value"]))
	}
	return ""
}

// list renders one rich_text_list element. Nested items are indented by
// four spaces per level, which Markdown reads as nesting under both "- " and
// "1. " items.
func (rt richText) list(el map[string]any) string {
	indent, _ := jsonx.Int(el["indent"])
	pad := strings.Repeat("    ", max(indent, 0))
	// A list that resumes after nested items carries the number of items
	// before it.
	num, _ := jsonx.Int(el["offset"])
	num = max(num, 0)
	ordered := jsonx.Str(el["style"]) == "ordered"

	var items []string
	for _, item := range jsonx.Maps(el["elements"]) {
		text := strings.TrimSpace(rt.element(item))
		if text == "" {
			continue
		}
		num++
		prefix := "- "
		if ordered {
			prefix = strconv.Itoa(num) + ". "
		}
		continuation := "\n" + pad + strings.Repeat(" ", len(prefix))
		items = append(items, pad+prefix+strings.ReplaceAll(text, "\n", continuation))
	}
	return strings.Join(items, "\n")
}

var tokenURLEscaper = strings.NewReplacer("<", "%3C", ">", "%3E", "|", "%7C", "\n", "%0A")

func (rt richText) link(url, text string) string {
	if url == "" {
		return rt.literal(text)
	}
	url = tokenURLEscaper.Replace(url)
	switch {
	case text != "" && text != url:
		return "<" + url + "|" + rt.literal(text) + ">"
	case rt.escape:
		return "<" + url + ">"
	}
	return url
}

var shortcodeNameRE = regexp.MustCompile(`^[A-Za-z0-9_+'-]+$`)

func (rt richText) emoji(el map[string]any) string {
	name := jsonx.Str(el["name"])
	if !shortcodeNameRE.MatchString(name) {
		return ""
	}
	if rt.escape {
		// Slack gives the code points of standard emoji, which covers the
		// ones missing from our table.
		if _, known := emojiByName(name); !known {
			if char := emojiFromCodePoints(jsonx.Str(el["unicode"])); char != "" {
				return char
			}
		}
	}
	shortcode := ":" + name + ":"
	if tone, ok := jsonx.Int(el["skin_tone"]); ok && tone >= 2 && tone <= 6 {
		shortcode += ":skin-tone-" + strconv.Itoa(tone) + ":"
	}
	return shortcode
}

// emojiFromCodePoints decodes the `unicode` field of an emoji element, such
// as "1f44b-1f3fd".
func emojiFromCodePoints(hex string) string {
	if hex == "" {
		return ""
	}
	var b strings.Builder
	for part := range strings.SplitSeq(hex, "-") {
		cp, err := strconv.ParseUint(part, 16, 32)
		if err != nil || cp > 0x10FFFF {
			return ""
		}
		b.WriteRune(rune(cp))
	}
	return b.String()
}

func (rt richText) date(el map[string]any) string {
	fallback := jsonx.Str(el["fallback"])
	ts, ok := jsonx.Int(el["timestamp"])
	if !ok {
		return rt.literal(fallback)
	}
	token := "<!date^" + strconv.Itoa(ts) + "^{date_short_pretty} {time}"
	if fallback != "" {
		token += "|" + rt.literal(fallback)
	}
	return token + ">"
}

// styled wraps text in the mrkdwn markers of a rich text style. Surrounding
// whitespace stays outside the markers, where it does not break them.
func styled(text string, style map[string]any) string {
	if len(style) == 0 {
		return text
	}
	core := strings.TrimSpace(text)
	if core == "" {
		return text
	}
	start := strings.Index(text, core)
	lead, trail := text[:start], text[start+len(core):]
	if jsonx.Bool(style["code"]) {
		core = "`" + core + "`"
	}
	if jsonx.Bool(style["bold"]) {
		core = "*" + core + "*"
	}
	if jsonx.Bool(style["italic"]) {
		core = "_" + core + "_"
	}
	if jsonx.Bool(style["strike"]) {
		core = "~" + core + "~"
	}
	return lead + core + trail
}

var encodedIDRE = regexp.MustCompile(`^[A-Z0-9]+$`)

// isSafeEncodedID reports whether an ID can be put in a `<@...>` token
// without letting a forged element inject mrkdwn of its own.
func isSafeEncodedID(id string) bool {
	return encodedIDRE.MatchString(id)
}
