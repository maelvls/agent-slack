package canvas

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/maelvls/slackcli/internal/slackapi"
)

// Operations are the canvases.edit operations.
var Operations = []string{
	"insert_after",
	"insert_before",
	"insert_at_start",
	"insert_at_end",
	"replace",
	"delete",
	"rename",
}

// MaxEditChars is Slack's limit on the Markdown of one change.
const MaxEditChars = 1_048_576

// Edit is one change to a canvas. Slack accepts exactly one change per
// canvases.edit call.
type Edit struct {
	CanvasID string
	// Operation is one of Operations.
	Operation string
	// Markdown is the content of an insert_* or replace operation.
	Markdown string
	// SectionID is the section an insert_after, insert_before or delete
	// is relative to; optional for replace (default: the whole canvas).
	SectionID string
	// Title is the new title of a rename operation.
	Title string
}

// EditedCanvas identifies an edited canvas and what was done to it.
type EditedCanvas struct {
	ID        string `json:"id"`
	Operation string `json:"operation"`
}

// EditResult is the result of Apply, in the shape the CLI prints.
type EditResult struct {
	OK     bool         `json:"ok"`
	Canvas EditedCanvas `json:"canvas"`
}

// NeedsMarkdown reports whether an operation takes Markdown content.
func NeedsMarkdown(operation string) bool {
	return slices.Contains(Operations, operation) && operation != "delete" && operation != "rename"
}

// Validate checks that the fields fit the operation, without calling
// Slack. CanvasID is not checked: it may not be known yet. Error messages
// name the CLI flags, the vocabulary of the only caller.
func (e Edit) Validate() error {
	_, err := e.change()
	return err
}

// change validates e and returns the canvases.edit change for it.
func (e Edit) change() (map[string]any, error) {
	op := strings.TrimSpace(e.Operation)
	if !slices.Contains(Operations, op) {
		return nil, fmt.Errorf("unsupported canvas edit operation %q: expected one of %s", e.Operation, strings.Join(Operations, ", "))
	}
	section := strings.TrimSpace(e.SectionID)
	title := strings.TrimSpace(e.Title)
	if title != "" && op != "rename" {
		return nil, fmt.Errorf("--title is only valid with the rename operation (the operation is %q)", op)
	}
	if e.Markdown != "" && !NeedsMarkdown(op) {
		return nil, fmt.Errorf("canvas edit operation %q does not accept Markdown content: drop --file/--markdown", op)
	}

	change := map[string]any{"operation": op}
	switch op {
	case "delete":
		if section == "" {
			return nil, errors.New(`canvas edit operation "delete" requires --section-id`)
		}
	case "rename":
		if title == "" {
			return nil, errors.New(`canvas edit operation "rename" requires a non-empty --title`)
		}
		if utf8.RuneCountInString(title) > MaxEditChars {
			return nil, fmt.Errorf("canvas title exceeds Slack's %d character limit", MaxEditChars)
		}
		if section != "" {
			return nil, errors.New(`canvas edit operation "rename" cannot use --section-id`)
		}
		change["title_content"] = map[string]any{"type": "markdown", "markdown": title}
	default:
		if strings.TrimSpace(e.Markdown) == "" {
			return nil, fmt.Errorf("canvas edit operation %q requires non-empty Markdown content (--file or --markdown)", op)
		}
		if n := utf8.RuneCountInString(e.Markdown); n > MaxEditChars {
			return nil, fmt.Errorf("canvas edit Markdown is %d characters, over Slack's %d character limit per change: split it into several edits", n, MaxEditChars)
		}
		switch op {
		case "insert_after", "insert_before":
			if section == "" {
				return nil, fmt.Errorf("canvas edit operation %q requires --section-id", op)
			}
		case "insert_at_start", "insert_at_end":
			if section != "" {
				return nil, fmt.Errorf("canvas edit operation %q cannot use --section-id", op)
			}
		}
		change["document_content"] = map[string]any{"type": "markdown", "markdown": e.Markdown}
	}
	if section != "" {
		change["section_id"] = section
	}
	return change, nil
}

// Apply validates and applies one edit (canvases.edit). It needs a
// standard token: Slack does not accept browser credentials here, which
// is reported as ErrBrowserEdit before any call.
func Apply(ctx context.Context, client *slackapi.Client, e Edit) (EditResult, error) {
	id := strings.TrimSpace(e.CanvasID)
	if id == "" {
		return EditResult{}, errors.New("canvas ID is required")
	}
	change, err := e.change()
	if err != nil {
		return EditResult{}, err
	}
	if client.Auth().IsBrowser() {
		return EditResult{}, ErrBrowserEdit
	}
	_, err = client.API(ctx, "canvases.edit", map[string]any{
		"canvas_id": id,
		"changes":   []any{change},
	})
	if err != nil {
		return EditResult{}, fmt.Errorf("editing canvas %s: %w", id, err)
	}
	return EditResult{OK: true, Canvas: EditedCanvas{ID: id, Operation: strings.TrimSpace(e.Operation)}}, nil
}
