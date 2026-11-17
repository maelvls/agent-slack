package search

import (
	"fmt"
	"strings"

	"github.com/maelvls/slackcli/internal/slack/messages"
)

// ContentType filters search results by what they contain.
type ContentType string

const (
	// ContentAny keeps everything.
	ContentAny ContentType = "any"
	// ContentText keeps messages without files, and plain-text files.
	ContentText ContentType = "text"
	// ContentImage keeps images, and messages with an image attached.
	ContentImage ContentType = "image"
	// ContentSnippet keeps snippets, and messages with a snippet attached.
	ContentSnippet ContentType = "snippet"
	// ContentFile keeps every file, and messages with any file attached.
	ContentFile ContentType = "file"
)

// ParseContentType parses a --content-type value, case-insensitively. An
// empty value means ContentAny; an unknown one is an error rather than
// silently searching everything.
func ParseContentType(value string) (ContentType, error) {
	switch ct := ContentType(strings.ToLower(strings.TrimSpace(value))); ct {
	case "":
		return ContentAny, nil
	case ContentAny, ContentText, ContentImage, ContentSnippet, ContentFile:
		return ct, nil
	}
	return "", fmt.Errorf("invalid content type %q: expected any, text, image, snippet or file", value)
}

func isImage(mimetype string) bool {
	return strings.HasPrefix(strings.ToLower(mimetype), "image/")
}

// passesMessage reports whether a message matches the filter. It looks at
// the files attached to the message, not at what was downloaded: whether
// a message has an image does not depend on the download succeeding.
func passesMessage(msg messages.Message, ct ContentType) bool {
	hasFiles := len(msg.Files) > 0
	switch ct {
	case ContentText:
		return !hasFiles
	case ContentFile:
		return hasFiles
	case ContentSnippet:
		for _, f := range msg.Files {
			if f.Mode == "snippet" {
				return true
			}
		}
		return false
	case ContentImage:
		for _, f := range msg.Files {
			if isImage(f.Mimetype) {
				return true
			}
		}
		return false
	}
	return true
}

// passesFile reports whether a file matches the filter.
func passesFile(f fileHit, ct ContentType) bool {
	switch ct {
	case ContentSnippet:
		return f.mode == "snippet"
	case ContentImage:
		return isImage(f.mimetype)
	case ContentText:
		return strings.ToLower(f.mimetype) == "text/plain"
	}
	return true
}
