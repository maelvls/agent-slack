package slacktext

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TextToRichTextBlocks converts the text of an outgoing message to a
// rich_text block (the value of the `blocks` API parameter) when it contains
// a bullet or numbered list, which Slack only renders as a real list from
// blocks. It returns nil when the text has no list: send it as plain `text`.
func TextToRichTextBlocks(text string) []any {
	elements, hasLists, _ := richTextElements(text)
	if !hasLists {
		return nil
	}
	return richTextBlock(elements)
}

// TextToRichTextBlocksInline is [TextToRichTextBlocks] for when there is no
// `text` to fall back to (drafts): it also returns a block when the text
// has no list but has inline formatting, a quote or a code block. It returns
// nil for text with no formatting at all.
func TextToRichTextBlocksInline(text string) []any {
	elements, hasLists, hasFormatting := richTextElements(text)
	if !hasLists && !hasFormatting {
		return nil
	}
	return richTextBlock(elements)
}

func richTextBlock(elements []any) []any {
	return []any{map[string]any{"type": "rich_text", "elements": elements}}
}

const codeFence = "```"

// fenceInfoRE matches what may follow an opening fence without being code:
// nothing, or a Markdown language tag, which Slack has no use for.
var fenceInfoRE = regexp.MustCompile(`^[A-Za-z0-9_+#.-]*\s*$`)

func richTextElements(text string) (elements []any, hasLists, hasFormatting bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	idx := 0
	for idx < len(lines) {
		line := lines[idx]

		if strings.HasPrefix(line, codeFence) {
			var code string
			code, idx = collectCodeBlock(lines, idx)
			if strings.TrimSpace(code) != "" {
				elements = append(elements, map[string]any{
					"type":     "rich_text_preformatted",
					"elements": []any{map[string]any{"type": "text", "text": code}},
				})
				hasFormatting = true
			}
			continue
		}

		if _, ok := quoteLine(line); ok {
			var quoted []string
			for idx < len(lines) {
				content, ok := quoteLine(lines[idx])
				if !ok {
					break
				}
				quoted = append(quoted, content)
				idx++
			}
			if content := strings.Join(quoted, "\n"); strings.TrimSpace(content) != "" {
				elements = append(elements, map[string]any{
					"type":     "rich_text_quote",
					"elements": ParseInlineElements(content),
				})
				hasFormatting = true
			}
			continue
		}

		if _, ok := parseListLine(line); ok {
			var lists []any
			lists, idx = collectLists(lines, idx)
			elements = append(elements, lists...)
			hasLists = true
			continue
		}

		// Plain text: everything up to the next special line.
		start := idx
		for idx < len(lines) && !isSpecialLine(lines[idx]) {
			idx++
		}
		content := strings.Join(lines[start:idx], "\n")
		if strings.TrimSpace(content) == "" {
			continue
		}
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		inline := ParseInlineElements(content)
		for _, el := range inline {
			if el := el.(map[string]any); el["type"] != "text" || el["style"] != nil {
				hasFormatting = true
			}
		}
		elements = append(elements, map[string]any{"type": "rich_text_section", "elements": inline})
	}
	return elements, hasLists, hasFormatting
}

func isSpecialLine(line string) bool {
	if strings.HasPrefix(line, codeFence) {
		return true
	}
	if _, ok := quoteLine(line); ok {
		return true
	}
	_, ok := parseListLine(line)
	return ok
}

// quoteLine parses a "> quoted" line.
func quoteLine(line string) (content string, ok bool) {
	if line == ">" {
		return "", true
	}
	return strings.CutPrefix(line, "> ")
}

// collectCodeBlock reads the code block whose opening fence starts
// lines[idx]. It returns the code and the index of the next line to read.
// Text that follows the closing fence on its line is left in lines to be
// read as an ordinary line. An unclosed block runs to the end of the text.
func collectCodeBlock(lines []string, idx int) (string, int) {
	var code []string
	first := strings.TrimPrefix(lines[idx], codeFence)
	if before, after, closed := strings.Cut(first, codeFence); closed {
		// ```code``` on a single line.
		return before, leaveRemainder(lines, idx, after)
	}
	if !fenceInfoRE.MatchString(first) {
		// Slack-style "```code" with the code starting on the fence line.
		code = append(code, first)
	}
	idx++
	for idx < len(lines) {
		before, after, closed := strings.Cut(lines[idx], codeFence)
		if !closed {
			code = append(code, lines[idx])
			idx++
			continue
		}
		if before != "" {
			code = append(code, before)
		}
		idx = leaveRemainder(lines, idx, after)
		break
	}
	return strings.Join(code, "\n"), idx
}

// leaveRemainder replaces lines[idx] with what follows a closing fence, so
// that it is parsed next, or skips the line when nothing follows.
func leaveRemainder(lines []string, idx int, remainder string) int {
	if strings.TrimSpace(remainder) == "" {
		return idx + 1
	}
	lines[idx] = strings.TrimLeft(remainder, " \t")
	return idx
}

type listLine struct {
	ordered bool
	number  int // of an ordered item, as written
	indent  int // width of the leading whitespace
	content string
}

const bulletRunes = "•◦▪▫▸‣●○◆◇-*"

// parseListLine parses "- item", "• item", "1. item" and "1) item" lines,
// possibly indented. A marker with nothing after it is not a list item.
func parseListLine(line string) (listLine, bool) {
	var item listLine
	rest := line
	for rest != "" {
		r, size := utf8.DecodeRuneInString(rest)
		if !unicode.IsSpace(r) {
			break
		}
		if r == '\t' {
			item.indent += 4
		} else {
			item.indent++
		}
		rest = rest[size:]
	}

	if r, size := utf8.DecodeRuneInString(rest); size > 0 && strings.ContainsRune(bulletRunes, r) {
		rest = rest[size:]
	} else {
		digits := 0
		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
			item.number = item.number*10 + int(rest[digits]-'0')
			digits++
		}
		// More digits than a list can have items is a number, not a marker.
		if digits == 0 || digits > 6 || digits >= len(rest) || rest[digits] != '.' && rest[digits] != ')' {
			return listLine{}, false
		}
		item.ordered = true
		rest = rest[digits+1:]
	}

	item.content = strings.TrimLeftFunc(rest, unicode.IsSpace)
	if item.content == rest || item.content == "" {
		return listLine{}, false
	}
	return item, true
}

// maxListIndent is the deepest nesting Slack renders.
const maxListIndent = 8

// collectLists reads the run of list lines starting at lines[idx] and
// returns one rich_text_list per run of items of the same style and depth,
// which is how Slack represents nested lists, and the index after the run.
func collectLists(lines []string, idx int) ([]any, int) {
	var lists []any
	var items []any
	var current listLine // the first item of the list being collected
	currentLevel := -1
	flush := func() {
		if len(items) == 0 {
			return
		}
		list := map[string]any{"type": "rich_text_list", "style": "bullet", "elements": items}
		if current.ordered {
			list["style"] = "ordered"
			// Slack numbers from 1 unless told how many items came before:
			// keep the numbers as written ("3." or a list resumed after
			// nested items).
			if current.number > 1 {
				list["offset"] = current.number - 1
			}
		}
		if currentLevel > 0 {
			list["indent"] = currentLevel
		}
		lists = append(lists, list)
		items = nil
	}

	// widths holds the indentation of each open nesting level.
	var widths []int
	for ; idx < len(lines); idx++ {
		item, ok := parseListLine(lines[idx])
		if !ok {
			break
		}
		if widths == nil {
			widths = []int{item.indent}
		}
		for len(widths) > 1 && item.indent < widths[len(widths)-1] {
			widths = widths[:len(widths)-1]
		}
		// One stray space is not a new level: two or more are.
		if item.indent >= widths[len(widths)-1]+2 && len(widths) <= maxListIndent {
			widths = append(widths, item.indent)
		}
		level := len(widths) - 1
		if level != currentLevel || item.ordered != current.ordered {
			flush()
			current, currentLevel = item, level
		}
		items = append(items, map[string]any{
			"type":     "rich_text_section",
			"elements": ParseInlineElements(item.content),
		})
	}
	flush()
	return lists, idx
}
