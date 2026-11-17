package messagecmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/cmd/composecmd"
)

func TestSafeModeEnabling(t *testing.T) {
	blocked := func(h *harness) bool {
		_, err := h.run("delete", testURL)
		return err != nil && strings.Contains(err.Error(), `"message delete" is blocked`)
	}

	h := newHarness(t)
	if blocked(h) {
		t.Error("safe mode is on without the flag or the environment variable")
	}
	h.rt.SafeModeFlag = true
	if !blocked(h) {
		t.Error("the --safe-mode flag does not enable safe mode")
	}

	for _, name := range []string{"SLACKCLI_SAFE_MODE", "AGENT_SLACK_SAFE_MODE"} {
		for _, value := range []string{"1", "true", "TRUE", "yes", "on", " 1 "} {
			h := newHarness(t)
			t.Setenv(name, value)
			if !blocked(h) {
				t.Errorf("%s=%q does not enable safe mode", name, value)
			}
		}
		for _, value := range []string{"0", "false", "off", "no", ""} {
			h := newHarness(t)
			t.Setenv(name, value)
			if blocked(h) {
				t.Errorf("%s=%q enables safe mode", name, value)
			}
		}
	}
}

func TestSafeModeBlocksEditAndDelete(t *testing.T) {
	h := newHarness(t)
	h.rt.SafeModeFlag = true
	h.mustFail(`"message edit" is blocked`, "edit", testURL, "new text")
	h.mustFail(`"message delete" is blocked`, "delete", testURL)
	// Blocked before anything else is looked at, bad arguments included.
	h.mustFail(`"message delete" is blocked`, "delete", testChannel)
	h.wantCalls()

	if err := h.rt.CheckSafeMode("message edit"); !strings.Contains(err.Error(), "SLACKCLI_SAFE_MODE") {
		t.Errorf("error does not say how safe mode was enabled: %v", err)
	}
}

// The --safe-mode flag belongs to the root command; "send" and "edit"
// parse their own flags and must still see it wherever it is written.
func TestSafeModeFlagOnRoot(t *testing.T) {
	for _, args := range [][]string{
		{"--safe-mode", "message", "edit", testURL, "- new text"},
		{"message", "--safe-mode", "edit", testURL, "- new text"},
		{"message", "edit", testURL, "- new text", "--safe-mode"},
		{"message", "edit", "--safe-mode", testURL, "- new text"},
		{"message", "send", testChannel, "- hi", "--safe-mode", "--attach", "x"},
	} {
		h := newHarness(t)
		root := &cobra.Command{Use: "root", SilenceUsage: true, SilenceErrors: true}
		root.PersistentFlags().BoolVar(&h.rt.SafeModeFlag, "safe-mode", false, "")
		root.AddCommand(h.command())
		root.SetArgs(args)
		err := root.Execute()
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "safe mode is active") {
			t.Errorf("%v: err = %v, want a safe mode refusal", args, err)
		}
		h.wantCalls()
	}
}

func TestSafeModeSendRejectsUnsupportedFlags(t *testing.T) {
	blocks, _ := writeBlocks(t, []any{map[string]any{"type": "divider"}})
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--attach", "./report.md"}, "does not support --attach."},
		{[]string{"--blocks", blocks}, "does not support --blocks."},
		{[]string{"--schedule", "2030-01-01T00:00:00Z"}, "does not support --schedule."},
		{[]string{"--schedule-in", "3h"}, "does not support --schedule-in."},
		{[]string{"--thread-ts", "1770160000.000001", "--reply-broadcast"}, "does not support --reply-broadcast."},
		// All of them at once.
		{[]string{"--attach", "./a.md", "--schedule-in", "3h"}, "--attach, --schedule-in"},
		{[]string{"--reply-broadcast"}, "does not support --reply-broadcast."},
	}
	for _, tt := range tests {
		h := newHarness(t)
		h.rt.SafeModeFlag = true
		h.mustFail(tt.want, append([]string{"send", "#general", "hi"}, tt.args...)...)
		h.wantCalls()
		if len(h.composed) != 0 {
			t.Errorf("%v: the editor was opened", tt.args)
		}
	}
}

func TestSafeModeSendRefusesInCI(t *testing.T) {
	h := newHarness(t)
	h.rt.SafeModeFlag = true
	t.Setenv("CI", "1")
	h.mustFail("unavailable in CI", "send", "#general", "hi")
	h.wantCalls()
	if len(h.composed) != 0 {
		t.Error("the editor was opened in CI")
	}
}

// CI=0 and CI=false are not CI (clictx.IsCI): the editor opens, and a
// human still has to press Send there.
func TestSafeModeSendOpensTheEditorWhenCIIsOff(t *testing.T) {
	for _, value := range []string{"0", "false", "FALSE"} {
		h := newHarness(t)
		h.rt.SafeModeFlag = true
		h.composeResult = map[string]any{"ok": true, "cancelled": true}
		t.Setenv("CI", value)
		h.mustRun("send", "#general", "hi")
		h.wantCalls()
		if len(h.composed) != 1 {
			t.Errorf("CI=%q: the editor was opened %d times", value, len(h.composed))
		}
	}
}

func TestSafeModeSendOpensComposeEditor(t *testing.T) {
	h := newHarness(t)
	h.rt.SafeModeFlag = true
	h.composeResult = map[string]any{"ok": true, "sent": true}
	watchdogStopped := false
	h.rt.SetWatchdogCanceller(func() { watchdogStopped = true })

	got := h.mustRun("send", "#general", "here's the report", "--workspace", "myteam", "--thread-ts", "1770165109.628379", "--no-unfurl")
	want := []composeCall{{
		target: "#general",
		text:   "here's the report",
		opts:   composecmd.Options{Workspace: "myteam", ThreadTS: "1770165109.628379", NoUnfurl: true},
	}}
	if !reflect.DeepEqual(h.composed, want) {
		t.Errorf("compose calls = %+v\nwant %+v", h.composed, want)
	}
	wantJSON(t, got, `{"safe_mode": true, "redirected_from": "send", "ok": true, "sent": true}`)
	// Nothing is sent by "send" itself, and the editor may take forever.
	h.wantCalls()
	if !watchdogStopped {
		t.Error("the command timeout was not disabled before waiting on the editor")
	}
	if !strings.Contains(h.stderr.String(), "Safe mode active") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

// The editor picks the workspace itself (cmdutil.Workspace): it is handed
// the --workspace flag as given, not SLACK_WORKSPACE_URL, which a URL
// target may override.
func TestSafeModeSendPassesWorkspaceFlag(t *testing.T) {
	h := newHarness(t)
	h.rt.SafeModeFlag = true
	h.composeResult = map[string]any{"ok": true, "cancelled": true}
	t.Setenv("SLACK_WORKSPACE_URL", "https://other.slack.com")
	got := h.mustRun("send", testURL, "hi")
	if len(h.composed) != 1 || h.composed[0].opts.Workspace != "" || h.composed[0].target != testURL {
		t.Errorf("compose calls = %+v", h.composed)
	}
	h.mustRun("send", "#general", "hi", "--workspace", "acme")
	if last := h.composed[len(h.composed)-1]; last.target != "#general" || last.opts.Workspace != "acme" {
		t.Errorf("compose calls = %+v", h.composed)
	}
	wantJSON(t, got, `{"safe_mode": true, "redirected_from": "send", "ok": true, "cancelled": true}`)
}

// Flag mistakes are reported in safe mode as they are without it.
func TestSafeModeSendStillValidates(t *testing.T) {
	h := newHarness(t)
	h.rt.SafeModeFlag = true
	h.mustFail("<text> is required", "send", "#general")
	h.mustFail("invalid --thread-ts", "send", "#general", "hi", "--thread-ts", "x")
	if len(h.composed) != 0 {
		t.Error("the editor was opened")
	}
}
