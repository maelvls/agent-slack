package slacktext

import (
	"reflect"
	"strings"

	"github.com/maelvls/slackcli/internal/jsonx"
)

// maxAttachmentDepth bounds how deep forwarded messages nested in forwarded
// messages are followed.
const maxAttachmentDepth = 8

// RenderMessageContent renders a raw Slack message object as Markdown. The
// blocks and the attachments (bot cards, link unfurls, forwarded messages)
// are preferred, since `text` is often only a short fallback for them; the
// text is used when they render to nothing.
func RenderMessageContent(msg map[string]any) string {
	r := &renderer{seen: map[uintptr]bool{}}
	var parts []string
	for _, part := range []string{r.blocks(msg["blocks"]), r.attachments(msg["attachments"], 0)} {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n\n")
	}
	return strings.TrimSpace(MrkdwnToMarkdown(jsonx.Str(msg["text"])))
}

// renderer renders message parts to Markdown. It remembers the attachments
// already rendered, so that an attachment reachable twice (or through a
// cycle, in a hand-built message) is rendered once.
type renderer struct {
	seen map[uintptr]bool
}

func (r *renderer) blocks(blocks any) string {
	var out []string
	add := func(markdown string) {
		if strings.TrimSpace(markdown) != "" {
			out = append(out, markdown)
		}
	}
	for _, b := range jsonx.Maps(blocks) {
		switch jsonx.Str(b["type"]) {
		case "section":
			add(textObject(b["text"]))
			for _, f := range jsonx.Maps(b["fields"]) {
				add(textObject(f))
			}
			// Buttons often carry the only URL (e.g. "View Progress").
			add(buttonLink(jsonx.Map(b["accessory"])))
		case "header":
			add(textObject(b["text"]))
		case "markdown":
			// Already Markdown, not mrkdwn.
			add(jsonx.Str(b["text"]))
		case "actions":
			for _, el := range jsonx.Maps(b["elements"]) {
				add(buttonLink(el))
			}
		case "context":
			for _, el := range jsonx.Maps(b["elements"]) {
				add(textObject(el))
			}
		case "image":
			url := jsonx.Str(b["image_url"])
			if url == "" {
				url = jsonx.GetStr(b, "slack_file", "url")
			}
			add(labelled(jsonx.Str(b["alt_text"]), url))
		case "video":
			url := jsonx.Str(b["title_url"])
			if url == "" {
				url = jsonx.Str(b["video_url"])
			}
			add(labelled(textObject(b["title"]), url))
		case "rich_text":
			add(MrkdwnToMarkdown(richTextMrkdwn(b, true)))
		case "table":
			add(tableBlock(b))
		}
	}
	return strings.Join(out, "\n\n")
}

// textObject renders a `{type: mrkdwn|plain_text, text}` composition object.
func textObject(v any) string {
	obj := jsonx.Map(v)
	switch jsonx.Str(obj["type"]) {
	case "mrkdwn", "plain_text":
		return MrkdwnToMarkdown(jsonx.Str(obj["text"]))
	}
	return ""
}

func buttonLink(el map[string]any) string {
	if jsonx.Str(el["type"]) != "button" {
		return ""
	}
	return labelled(MrkdwnToMarkdown(jsonx.GetStr(el, "text", "text")), jsonx.Str(el["url"]))
}

// labelled renders "label: url", or nothing when there is no URL.
func labelled(label, url string) string {
	switch {
	case url == "":
		return ""
	case label == "":
		return url
	}
	return label + ": " + url
}

var tableCellEscaper = strings.NewReplacer("|", `\|`, "\n", " ")

// tableBlock renders a table block as a Markdown table whose first row is
// the header.
func tableBlock(b map[string]any) string {
	var lines []string
	for i, row := range jsonx.Slice(b["rows"]) {
		cells := jsonx.Maps(row)
		if len(cells) == 0 {
			continue
		}
		line := "|"
		for _, cell := range cells {
			text := jsonx.Str(cell["text"])
			if jsonx.Str(cell["type"]) == "rich_text" {
				text = MrkdwnToMarkdown(richTextMrkdwn(cell, true))
			}
			line += " " + tableCellEscaper.Replace(strings.TrimSpace(text)) + " |"
		}
		lines = append(lines, line)
		if i == 0 {
			lines = append(lines, "|"+strings.Repeat(" --- |", len(cells)))
		}
	}
	return strings.Join(lines, "\n")
}

func (r *renderer) attachments(attachments any, depth int) string {
	if depth >= maxAttachmentDepth {
		return ""
	}
	var parts []string
	for _, a := range jsonx.Maps(attachments) {
		id := reflect.ValueOf(a).Pointer()
		if r.seen[id] {
			continue
		}
		r.seen[id] = true

		if jsonx.Bool(a["is_share"]) || jsonx.Bool(a["is_msg_unfurl"]) && jsonx.Slice(a["message_blocks"]) != nil {
			parts = append(parts, r.forwarded(a, depth))
			continue
		}

		var chunk []string
		add := func(markdown string) {
			if markdown != "" {
				chunk = append(chunk, markdown)
			}
		}
		add(r.blocks(a["blocks"]))
		add(MrkdwnToMarkdown(jsonx.Str(a["pretext"])))
		title, titleLink := MrkdwnToMarkdown(jsonx.Str(a["title"])), jsonx.Str(a["title_link"])
		switch {
		case title != "" && titleLink != "":
			add(markdownLink(title, titleLink))
		case title != "":
			add(title)
		default:
			add(titleLink)
		}
		add(MrkdwnToMarkdown(jsonx.Str(a["text"])))
		for _, f := range jsonx.Maps(a["fields"]) {
			fieldTitle, value := MrkdwnToMarkdown(jsonx.Str(f["title"])), MrkdwnToMarkdown(jsonx.Str(f["value"]))
			switch {
			case fieldTitle != "" && value != "":
				add(fieldTitle + "\n" + value)
			default:
				add(fieldTitle + value)
			}
		}
		if len(uniqueTexts(chunk)) == 0 {
			add(MrkdwnToMarkdown(jsonx.Str(a["fallback"])))
		}
		add(r.attachments(a["attachments"], depth+1))
		if chunk = uniqueTexts(chunk); len(chunk) > 0 {
			parts = append(parts, strings.Join(chunk, "\n"))
		}
	}
	return strings.Join(uniqueTexts(parts), "\n\n")
}

// forwarded renders a message shared into another one: a header naming the
// author and linking to the original, then the message as a quote.
func (r *renderer) forwarded(a map[string]any, depth int) string {
	body := strings.TrimSpace(r.forwardedBody(a, depth))
	if body == "" {
		body = strings.TrimSpace(r.attachments(a["attachments"], depth+1))
	}
	if body == "" {
		body = strings.TrimSpace(MrkdwnToMarkdown(jsonx.Str(a["text"])))
	}
	header := forwardHeader(a)
	if body == "" {
		return header
	}
	return header + "\n" + quoteMarkdown(body)
}

func forwardHeader(a map[string]any) string {
	author := MrkdwnToMarkdown(jsonx.Str(a["author_name"]))
	if link := jsonx.Str(a["author_link"]); author != "" && link != "" {
		author = markdownLink(author, link)
	}
	source := ""
	if fromURL := jsonx.Str(a["from_url"]); fromURL != "" {
		source = markdownLink("original", fromURL)
	}
	switch {
	case author != "" && source != "":
		return "*Forwarded from " + author + " | " + source + "*"
	case author != "":
		return "*Forwarded from " + author + "*"
	case source != "":
		return "*Forwarded message | " + source + "*"
	}
	return "*Forwarded message*"
}

func (r *renderer) forwardedBody(a map[string]any, depth int) string {
	out := []string{fileMentions(a["files"])}
	for _, mb := range jsonx.Maps(a["message_blocks"]) {
		message := jsonx.Map(mb["message"])
		if message == nil {
			continue
		}
		content := uniqueTexts([]string{
			r.blocks(message["blocks"]),
			r.attachments(message["attachments"], depth+1),
			MrkdwnToMarkdown(jsonx.Str(message["text"])),
			fileMentions(message["files"]),
		})
		out = append(out, strings.Join(content, "\n\n"))
	}
	return strings.Join(uniqueTexts(out), "\n")
}

func fileMentions(files any) string {
	var lines []string
	for _, f := range jsonx.Maps(files) {
		name := firstNonEmpty(jsonx.Str(f["title"]), jsonx.Str(f["name"]), "file")
		url := firstNonEmpty(jsonx.Str(f["permalink"]), jsonx.Str(f["url_private_download"]), jsonx.Str(f["url_private"]))
		if url == "" {
			lines = append(lines, name)
			continue
		}
		lines = append(lines, markdownLink(name, url))
	}
	return strings.Join(uniqueTexts(lines), "\n")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func quoteMarkdown(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight("> "+line, " ")
	}
	return strings.Join(lines, "\n")
}

// uniqueTexts trims the texts and drops the empty and repeated ones: Slack
// commonly repeats the same content in `text`, `blocks` and `fallback`.
func uniqueTexts(values []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
	}
	return out
}
