package slacktext

import (
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

// fenceLanguages are the Markdown info strings dropped from an opening
// fence: Slack has no use for them. The list is deliberately short of words
// that are also commands ("make", "git", "go run"): a first line that is
// not surely a language tag is code, and is kept.
var fenceLanguages = map[string]bool{
	"bash": true, "console": true, "cpp": true, "csharp": true, "css": true,
	"diff": true, "dockerfile": true, "go": true, "golang": true,
	"graphql": true, "hcl": true, "html": true, "ini": true, "java": true,
	"javascript": true, "js": true, "json": true, "jsx": true, "kotlin": true,
	"lua": true, "makefile": true, "markdown": true, "md": true, "php": true,
	"plaintext": true, "powershell": true, "proto": true, "py": true,
	"python": true, "rb": true, "rs": true, "ruby": true, "rust": true,
	"scala": true, "sh": true, "shell": true, "sql": true, "swift": true,
	"terraform": true, "text": true, "toml": true, "ts": true, "tsx": true,
	"txt": true, "typescript": true, "xml": true, "yaml": true, "yml": true,
	"zsh": true,
}

// richChunk is a piece of the text of a message: a code block, or the text
// between two code blocks.
type richChunk struct {
	text string
	code bool
}

// splitFences carves the ``` code blocks out of text, wherever their fences
// are: at the start of a line, indented, or in the middle of one. It is done
// before any line is looked at, so that nothing in a code block is read as
// a list item or a quote, and no fence is mistaken for text. An unclosed
// block whose fence starts a line runs to the end of the text, as it does in
// Slack's composer; a lone ``` in the middle of a line is text.
func splitFences(text string) []richChunk {
	var chunks []richChunk
	for text != "" {
		before, rest, found := strings.Cut(text, codeFence)
		ownLine := lastLineIsBlank(before)
		if found && !ownLine && !strings.Contains(rest, codeFence) {
			before, found = text, false
		}
		if ownLine {
			// The indentation of the fence, and the line break before it.
			before = strings.TrimRight(before, " \t")
			before = strings.TrimSuffix(before, "\n")
		}
		if before != "" {
			chunks = append(chunks, richChunk{text: before})
		}
		if !found {
			break
		}
		code, after, closed := strings.Cut(rest, codeFence)
		if code = fencedCode(code, ownLine, closed); strings.TrimSpace(code) != "" {
			chunks = append(chunks, richChunk{text: code, code: true})
		}
		// What follows the closing fence on its line is ordinary text.
		text = strings.TrimPrefix(strings.TrimLeft(after, " \t"), "\n")
	}
	return chunks
}

// lastLineIsBlank reports whether nothing but spaces follows the last line
// break of s (or its start).
func lastLineIsBlank(s string) bool {
	return strings.TrimRight(s[strings.LastIndexByte(s, '\n')+1:], " \t") == ""
}

// fencedCode returns the code of a block given what is between its fences.
// The line breaks next to the fences are not part of the code.
//
// The first line is dropped as a Markdown language tag only when all of
// this holds: the opening fence starts its line, what follows it on that
// line is exactly one of [fenceLanguages], and the closing fence, if any,
// is on a line of its own. So "```go\nx := 1\n```" loses "go", while
// "```make\nmake install```" and "```ls -l\n```" keep every word.
func fencedCode(raw string, fenceStartsLine, closed bool) string {
	closingOwnLine := !closed || strings.Contains(raw, "\n") && lastLineIsBlank(raw)
	if first, rest, multiline := strings.Cut(raw, "\n"); multiline && fenceStartsLine && closingOwnLine &&
		fenceLanguages[strings.ToLower(strings.TrimRight(first, " \t"))] {
		raw = "\n" + rest
	}
	if strings.TrimSpace(strings.SplitN(raw, "\n", 2)[0]) == "" {
		// Nothing on the line of the opening fence.
		_, raw, _ = strings.Cut(raw, "\n")
	}
	if lastLineIsBlank(raw) {
		// The indentation of the closing fence and the line break before it.
		raw = strings.TrimSuffix(strings.TrimRight(raw, " \t"), "\n")
	}
	return raw
}

func richTextElements(text string) (elements []any, hasLists, hasFormatting bool) {
	for _, chunk := range splitFences(strings.ReplaceAll(text, "\r\n", "\n")) {
		if chunk.code {
			elements = append(elements, map[string]any{
				"type":     "rich_text_preformatted",
				"elements": []any{map[string]any{"type": "text", "text": chunk.text}},
			})
			hasFormatting = true
			continue
		}
		proseElements, lists, formatting := proseElements(strings.Split(chunk.text, "\n"))
		elements = append(elements, proseElements...)
		hasLists = hasLists || lists
		hasFormatting = hasFormatting || formatting
	}
	return elements, hasLists, hasFormatting
}

// proseElements converts lines that hold no code block: quotes, lists and
// paragraphs.
func proseElements(lines []string) (elements []any, hasLists, hasFormatting bool) {
	idx := 0
	for idx < len(lines) {
		if _, ok := quoteLine(lines[idx]); ok {
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

		if _, ok := listLineAt(lines, idx); ok {
			var lists []any
			lists, idx = collectLists(lines, idx)
			elements = append(elements, lists...)
			hasLists = true
			continue
		}

		// Plain text: everything up to the next special line.
		start := idx
		for idx < len(lines) && !isSpecialLine(lines, idx) {
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

func isSpecialLine(lines []string, idx int) bool {
	if _, ok := quoteLine(lines[idx]); ok {
		return true
	}
	_, ok := listLineAt(lines, idx)
	return ok
}

// quoteLine parses a "> quoted" line.
func quoteLine(line string) (content string, ok bool) {
	if line == ">" {
		return "", true
	}
	return strings.CutPrefix(line, "> ")
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

// maxLoneNumber is the highest number a numbered line may carry and be a
// list item without a neighbour to confirm it.
const maxLoneNumber = 99

// listLineAt parses lines[idx] as a list item, as [parseListLine] does, but
// refuses a numbered line that is more likely a sentence starting with a
// number, such as "2024. Great year": a marker above 99 only makes an item
// when the line before or after it is a numbered item too ("100. x" next to
// "101. y", or to any other numbered line). "1. only item", "3. c" and
// every "N." up to 99 are items on their own, as before.
func listLineAt(lines []string, idx int) (listLine, bool) {
	item, ok := parseListLine(lines[idx])
	if !ok || !item.ordered || item.number <= maxLoneNumber {
		return item, ok
	}
	for _, neighbour := range []int{idx - 1, idx + 1} {
		if neighbour < 0 || neighbour >= len(lines) {
			continue
		}
		if other, ok := parseListLine(lines[neighbour]); ok && other.ordered {
			return item, true
		}
	}
	return listLine{}, false
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
		item, ok := listLineAt(lines, idx)
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
