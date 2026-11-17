package searchcmd

import (
	"strings"
	"testing"
)

// The lists a search is about are printed even when nothing matches, so
// that jq '.messages[]' sees an empty list rather than a missing field.
func TestNoMatchPrintsEmptyLists(t *testing.T) {
	h := newHarness(t)
	h.on("search.messages", func(map[string]any) (map[string]any, error) {
		return map[string]any{"messages": map[string]any{"matches": []any{}}}, nil
	})
	h.on("search.files", func(map[string]any) (map[string]any, error) {
		return map[string]any{"files": map[string]any{"matches": []any{}}}, nil
	})
	for kind, want := range map[string]string{
		"messages": "{\n  \"messages\": []\n}\n",
		"files":    "{\n  \"files\": []\n}\n",
		"all":      "{\n  \"messages\": [],\n  \"files\": []\n}\n",
	} {
		got, err := h.run(kind, "nothing like this")
		if err != nil {
			t.Fatalf("search %s: %v", kind, err)
		}
		if got != want {
			t.Errorf("search %s printed %s, want %s", kind, got, want)
		}
	}
}

// "--channel a b" was how the TypeScript took several channels; here b is
// a second positional argument, and the error says what to write instead.
func TestSpaceSeparatedChannelsGetAnActionableError(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"messages", "deploy", "--channel", "general", "random"},
		{"all", "--channel", "general", "random", "deploy"},
		{"files", "--channel=general", "random", "deploy", "--limit", "5"},
	} {
		_, err := h.run(args...)
		if err == nil {
			t.Fatalf("search %v succeeded", args)
		}
		for _, want := range []string{"takes one <query>", "to search several channels repeat --channel or use --channel a,b", `"random"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("search %v: err = %v, want it to contain %q", args, err, want)
			}
		}
		if len(h.calls) != 0 || h.stdout.Len() != 0 {
			t.Errorf("search %v: Slack was called (%v) or something was printed (%q)", args, h.calls, h.stdout.String())
		}
	}
	// Without --channel the usual message stands.
	if _, err := h.run("all", "x", "y"); err == nil || !strings.Contains(err.Error(), "accepts 1 arg(s), received 2") {
		t.Errorf("err = %v", err)
	}
}
