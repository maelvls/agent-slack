package canvascmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/slack/canvas"
)

// maxMarkdownBytes bounds what is read from --file: the largest document
// Slack accepts in one change, if every character took four bytes.
const maxMarkdownBytes = 4 * canvas.MaxEditChars

const (
	fileUsage     = `Read the Markdown from a local file, or from standard input with "-"; mutually exclusive with --markdown`
	markdownUsage = "Use this Markdown string; mutually exclusive with --file (prefer --file for anything long or with shell-special characters)"
)

// readMarkdown returns the Markdown given with --file or --markdown,
// exactly one of which must have been passed. The content is returned
// unchanged.
func readMarkdown(cmd *cobra.Command, file, markdown string) (string, error) {
	hasFile, hasMarkdown := cmd.Flags().Changed("file"), cmd.Flags().Changed("markdown")
	if hasFile == hasMarkdown {
		return "", errors.New(`pass exactly one Markdown source: --file <path> ("-" for standard input) or --markdown <text>`)
	}
	if hasFile {
		data, err := readSource(cmd, file)
		if err != nil {
			return "", err
		}
		markdown = string(data)
	}
	if !utf8.ValidString(markdown) {
		return "", errors.New("the Markdown source is not valid UTF-8 text (wrong file?)")
	}
	if strings.TrimSpace(markdown) == "" {
		return "", canvas.ErrEmptyMarkdown
	}
	return markdown, nil
}

func readSource(cmd *cobra.Command, file string) ([]byte, error) {
	name, src := "standard input", cmd.InOrStdin()
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return nil, fmt.Errorf("could not read Markdown file %q: %w", file, err)
		}
		defer f.Close()
		name, src = fmt.Sprintf("file %q", file), f
	}
	data, err := io.ReadAll(io.LimitReader(src, maxMarkdownBytes+1))
	if err != nil {
		return nil, fmt.Errorf("could not read Markdown from %s: %w", name, err)
	}
	if len(data) > maxMarkdownBytes {
		return nil, fmt.Errorf("Markdown from %s is larger than %d MiB, more than a canvas can hold", name, maxMarkdownBytes>>20)
	}
	return data, nil
}
