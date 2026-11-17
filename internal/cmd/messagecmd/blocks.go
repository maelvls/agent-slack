package messagecmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/maelvls/slackcli/internal/jsonx"
)

const (
	// maxBlocksBytes caps what --blocks reads: far above any payload Slack
	// accepts, and it keeps "--blocks /dev/zero" from exhausting memory.
	maxBlocksBytes = 1 << 20
	// maxBlocks is Slack's limit of blocks per message.
	maxBlocks = 50
)

const blocksUsage = `Path to a JSON file holding a Block Kit blocks array (at most 50 blocks), or "-" to read it from stdin. The blocks replace the automatic rich-text conversion of <text>, which is still sent as the notification/fallback text`

// loadBlocks reads the Block Kit array given to --blocks: a file path, or
// "-" for stdin.
func loadBlocks(stdin io.Reader, path string) ([]any, error) {
	source := path
	var r io.Reader
	if path == "-" {
		source, r = "stdin", stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("--blocks: %w", err)
		}
		defer f.Close()
		r = f
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxBlocksBytes+1))
	if err != nil {
		return nil, fmt.Errorf("--blocks: reading %s: %w", source, err)
	}
	if len(raw) > maxBlocksBytes {
		return nil, fmt.Errorf("--blocks: %s is larger than %d bytes, which no Slack message can hold", source, maxBlocksBytes)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("--blocks: %s is empty: expected a JSON array of Block Kit blocks", source)
	}

	// UseNumber keeps numbers exactly as written when they are re-encoded
	// for Slack.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("--blocks: failed to parse JSON from %s: %w", source, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("--blocks: failed to parse JSON from %s: unexpected data after the JSON value", source)
	}

	blocks, ok := parsed.([]any)
	if !ok {
		return nil, fmt.Errorf("--blocks: expected a JSON array of Block Kit blocks, got %s", jsonKind(parsed))
	}
	if len(blocks) == 0 {
		return nil, errors.New("--blocks: the blocks array is empty")
	}
	if len(blocks) > maxBlocks {
		return nil, fmt.Errorf("--blocks: %d blocks, but Slack accepts at most %d per message", len(blocks), maxBlocks)
	}
	for i, b := range blocks {
		block := jsonx.Map(b)
		if block == nil {
			return nil, fmt.Errorf("--blocks: element at index %d is not a Block Kit block object (got %s)", i, jsonKind(b))
		}
		if strings.TrimSpace(jsonx.Str(block["type"])) == "" {
			return nil, fmt.Errorf(`--blocks: element at index %d has no "type" (e.g. "section", "header", "divider")`, i)
		}
	}
	return blocks, nil
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	}
	return "number"
}
