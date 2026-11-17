package slacktext

import (
	"regexp"
	"strings"
	"sync"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/strikethrough"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"golang.org/x/net/html"
)

var htmlConverter = sync.OnceValue(func() *converter.Converter {
	conv := converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(
			commonmark.WithEmDelimiter("_"),
			commonmark.WithBulletListMarker("-"),
			commonmark.WithHeadingStyle(commonmark.HeadingStyleATX),
			commonmark.WithCodeBlockFence("```"),
			// The "<!--THE END-->" markers between adjacent lists are noise
			// for a reader.
			commonmark.WithListEndComment(false),
		),
		strikethrough.NewStrikethroughPlugin(),
		table.NewTablePlugin(
			table.WithHeaderPromotion(true),
			table.WithCellPaddingBehavior(table.CellPaddingBehaviorMinimal),
			table.WithNewlineBehavior(table.NewlineBehaviorPreserve),
		),
	))
	// A plain newline rather than a Markdown hard break ("  \n"): the
	// output is read by a model, not rendered.
	conv.Register.RendererFor("br", converter.TagTypeInline, func(_ converter.Context, w converter.Writer, _ *html.Node) converter.RenderStatus {
		w.WriteString("\n")
		return converter.RenderSuccess
	}, converter.PriorityEarly)
	// GFM task list items.
	conv.Register.RendererFor("input", converter.TagTypeInline, func(_ converter.Context, w converter.Writer, n *html.Node) converter.RenderStatus {
		checked, checkbox := false, false
		for _, attr := range n.Attr {
			switch {
			case attr.Key == "type" && strings.EqualFold(attr.Val, "checkbox"):
				checkbox = true
			case attr.Key == "checked":
				checked = true
			}
		}
		if !checkbox {
			return converter.RenderSuccess
		}
		box := "[ ]"
		if checked {
			box = "[x]"
		}
		// The label usually brings its own leading space.
		if next := n.NextSibling; next == nil || next.Type != html.TextNode || !strings.HasPrefix(next.Data, " ") {
			box += " "
		}
		w.WriteString(box)
		return converter.RenderSuccess
	}, converter.PriorityEarly)
	return conv
})

// HTMLToMarkdown converts HTML, such as a canvas or an email-like file, to
// GitHub-flavoured Markdown. Only the primary content of a full document is
// converted: its <main>, else its <article>, else its <body>.
func HTMLToMarkdown(input string) string {
	for _, tag := range []string{"main", "article", "body"} {
		if inner, ok := extractTag(input, tag); ok {
			input = inner
			break
		}
	}
	markdown, err := htmlConverter().ConvertString(input)
	if err != nil {
		// The parser accepts any input, so this is not expected: the text
		// without its tags is still better than nothing.
		return strings.TrimSpace(tagRE.ReplaceAllString(input, ""))
	}
	return trimTrailingSpaces(markdown)
}

// trimTrailingSpaces removes the whitespace the converter leaves at the end
// of lines (on the blank line before a nested list, for instance), except in
// code blocks.
func trimTrailingSpaces(markdown string) string {
	lines := strings.Split(markdown, "\n")
	inCode := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "```") {
			inCode = !inCode
		}
		if !inCode {
			lines[i] = strings.TrimRight(line, " \t")
		}
	}
	return strings.Join(lines, "\n")
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

var primaryTagREs = map[string]*regexp.Regexp{
	"main":    regexp.MustCompile(`(?is)<main\b[^>]*>(.*?)</main>`),
	"article": regexp.MustCompile(`(?is)<article\b[^>]*>(.*?)</article>`),
	"body":    regexp.MustCompile(`(?is)<body\b[^>]*>(.*?)</body>`),
}

func extractTag(input, tag string) (string, bool) {
	m := primaryTagREs[tag].FindStringSubmatch(input)
	if m == nil {
		return "", false
	}
	return m[1], true
}
