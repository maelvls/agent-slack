package messagecmd

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/clictx"
)

func findCommand(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	cmd, rest, err := root.Find(path)
	if err != nil || len(rest) != 0 || cmd.Name() != path[len(path)-1] {
		t.Fatalf("missing command: %s", strings.Join(path, " "))
	}
	return cmd
}

func flagUsage(t *testing.T, cmd *cobra.Command, name string) string {
	t.Helper()
	f := cmd.Flags().Lookup(name)
	if f == nil {
		t.Fatalf("missing flag --%s on %s", name, cmd.CommandPath())
	}
	return f.Usage
}

func wantContains(t *testing.T, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("%s = %q, want it to contain %q", what, got, want)
	}
}

// --help is the documentation agents read: the non-obvious behaviour must
// stay in it.
func TestHelpContracts(t *testing.T) {
	root := New(&clictx.Context{})

	send := findCommand(t, root, "send")
	for flag, want := range map[string]string{
		"attach":          "without automatic list conversion",
		"schedule":        "within 120 days",
		"schedule-in":     "local timezone",
		"thread-ts":       "channel targets",
		"reply-broadcast": "DM targets",
		"no-unfurl":       "link and media previews",
		"blocks":          "stdin",
	} {
		wantContains(t, "send --"+flag, flagUsage(t, send, flag), want)
	}
	wantContains(t, "send --schedule", flagUsage(t, send, "schedule"), "ISO 8601")
	wantContains(t, "send --schedule-in", flagUsage(t, send, "schedule-in"), "monday 9am")
	wantContains(t, "send help", send.Long, "safe mode")
	wantContains(t, "send help", send.Long, "user ID")

	cancel := findCommand(t, root, "scheduled", "cancel")
	wantContains(t, "scheduled cancel --channel", flagUsage(t, cancel, "channel"), "Required")
	wantContains(t, "scheduled cancel help", cancel.Long, "Not covered by safe mode")

	list := findCommand(t, root, "list")
	wantContains(t, "list --limit", flagUsage(t, list, "limit"), "1 to 1000")
	wantContains(t, "list --with-reaction", flagUsage(t, list, "with-reaction"), "requires --oldest")
	wantContains(t, "list --without-reaction", flagUsage(t, list, "without-reaction"), "requires --oldest")
	if f := list.Flags().Lookup("limit"); f.DefValue != "25" {
		t.Errorf("list --limit default = %s, want 25", f.DefValue)
	}

	get := findCommand(t, root, "get")
	wantContains(t, "get --max-body-chars", flagUsage(t, get, "max-body-chars"), "-1 for unlimited")
	if f := get.Flags().Lookup("max-body-chars"); f.DefValue != "8000" {
		t.Errorf("get --max-body-chars default = %s, want 8000", f.DefValue)
	}

	wantContains(t, "message help", root.Long, `short for "slackcli message get <target>"`)
	wantContains(t, "message help", root.Long, `NOT covered by safe mode`)
	wantContains(t, "react help", findCommand(t, root, "react").Long, "not covered by safe mode")
	wantContains(t, "edit help", findCommand(t, root, "edit").Long, "Refused in safe mode")
	wantContains(t, "delete help", findCommand(t, root, "delete").Long, "Refused in safe mode")
}

func TestCommandSurface(t *testing.T) {
	root := New(&clictx.Context{})
	allFlags := []string{
		"workspace", "ts", "thread-ts", "max-body-chars", "include-reactions", "resolve-users", "refresh-users",
		"limit", "oldest", "latest", "with-reaction", "without-reaction", "reply-broadcast", "attach", "blocks",
		"schedule", "schedule-in", "no-unfurl", "channel", "cursor",
	}
	for path, flags := range map[string][]string{
		"get":              {"workspace", "ts", "thread-ts", "max-body-chars", "include-reactions", "resolve-users", "refresh-users"},
		"list":             {"workspace", "thread-ts", "ts", "limit", "oldest", "latest", "with-reaction", "without-reaction", "max-body-chars", "include-reactions", "resolve-users", "refresh-users"},
		"send":             {"workspace", "thread-ts", "reply-broadcast", "attach", "blocks", "schedule", "schedule-in", "no-unfurl"},
		"edit":             {"workspace", "ts", "blocks"},
		"delete":           {"workspace", "ts"},
		"react add":        {"workspace", "ts"},
		"react remove":     {"workspace", "ts"},
		"scheduled":        {"workspace", "channel", "oldest", "latest", "cursor", "limit"},
		"scheduled list":   {"workspace", "channel", "oldest", "latest", "cursor", "limit"},
		"scheduled cancel": {"workspace", "channel"},
	} {
		cmd := findCommand(t, root, strings.Fields(path)...)
		for _, name := range allFlags {
			if has, want := cmd.Flags().Lookup(name) != nil, slices.Contains(flags, name); has != want {
				t.Errorf("%s: has flag --%s = %v, want %v", path, name, has, want)
			}
		}
	}
	// The implicit "get" takes the flags of "get".
	for _, name := range []string{"workspace", "ts", "thread-ts", "max-body-chars", "include-reactions", "resolve-users", "refresh-users"} {
		if root.Flags().Lookup(name) == nil {
			t.Errorf("message: missing flag --%s", name)
		}
	}
	// compose and draft are registered (by their own packages).
	for _, name := range []string{"compose", "draft"} {
		findCommand(t, root, name)
	}
}

// send and edit parse their own flags: --help must still print the usage,
// flags included, and do nothing else.
func TestHelpOfCommandsParsingTheirOwnFlags(t *testing.T) {
	for _, args := range [][]string{{"send", "--help"}, {"send", testChannel, "- text", "-h"}, {"edit", "--help"}} {
		h := newHarness(t)
		cmd := h.command()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		for _, want := range []string{"Usage:", "[flags]", "--blocks", "--workspace"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%v: help lacks %q:\n%s", args, want, out.String())
			}
		}
		h.wantCalls()
	}
}
