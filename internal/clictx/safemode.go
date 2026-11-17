package clictx

import (
	"fmt"
	"os"
	"strings"

	"github.com/maelvls/slackcli/internal/appinfo"
)

// SafeModeReviewed is the one command safe mode lets through, with a human
// in the loop: instead of posting, it opens the compose editor.
const SafeModeReviewed = "message send"

// safeModeBlocked is everything safe mode refuses outright, because there
// is no way for a human to review it first: the command, and what an
// agent could otherwise do with it. Together with safeModeNotCovered it is
// the single source of the help texts (SafeModeSummary), of the refusals
// (CheckSafeMode) and of what the docs must list (see the tests).
var safeModeBlocked = []struct{ command, action string }{
	{"message edit", "edit messages"},
	{"message delete", "delete messages"},
	{"message scheduled cancel", "delete pending scheduled messages"},
	{"canvas create", "create canvases"},
	{"canvas edit", "edit canvases"},
	{"workflow run", "start workflows (which may post messages or act on other systems)"},
	{"channel new", "create channels"},
	{"channel invite", "add people to channels or send Slack Connect invitations to external e-mail addresses"},
}

// safeModeNotCovered are the commands that change something in Slack and
// that safe mode deliberately lets through: they only touch the user's
// own, low-impact state (a reaction, a draft that is not posted, a read
// marker, the saved-for-later list, an open DM). "*" stands for every
// subcommand.
var safeModeNotCovered = []string{"message react", "message draft *", "channel mark", "later *", "user dm-open"}

// SafeModeBlocked lists the commands safe mode refuses (CheckSafeMode).
func SafeModeBlocked() []string {
	out := make([]string, len(safeModeBlocked))
	for i, b := range safeModeBlocked {
		out[i] = b.command
	}
	return out
}

// SafeModeNotCovered lists the writing commands safe mode lets through
// unreviewed; "*" stands for every subcommand.
func SafeModeNotCovered() []string { return append([]string(nil), safeModeNotCovered...) }

// SafeModeSummary says, for help texts, what safe mode does and what it
// does not cover.
func SafeModeSummary() string {
	return fmt.Sprintf(`"%s" opens the compose editor, where a human reviews and sends the message, instead of posting; %s are refused; %s are NOT covered by safe mode`,
		SafeModeReviewed, quotedList(SafeModeBlocked()), quotedList(safeModeNotCovered))
}

// quotedList renders items as `"a", "b" and "c"`.
func quotedList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = `"` + item + `"`
	}
	last := len(quoted) - 1
	return strings.Join(quoted[:last], ", ") + " and " + quoted[last]
}

// CheckSafeMode refuses command (e.g. "canvas edit") when safe mode is on;
// it returns nil when safe mode is off. command must be one of those safe
// mode blocks: an unknown one is refused all the same.
func (c *Context) CheckSafeMode(command string) error {
	if !c.SafeMode() {
		return nil
	}
	action := "do this"
	for _, b := range safeModeBlocked {
		if b.command == command {
			action = b.action
		}
	}
	return fmt.Errorf(`safe mode is active (%s or --safe-mode): %q is blocked so that an agent cannot %s without human review. Ask the user to do it, or to disable safe mode`,
		appinfo.EnvName("SAFE_MODE"), command, action)
}

// IsCI reports whether the CI environment variable says this is an
// automated run, where no browser can be opened for a human: it is set to
// anything but "", "0" or "false".
//
// Whatever it answers, safe mode holds: in CI, commands that need the
// compose editor fail under safe mode; outside CI the editor opens and
// nothing is sent until a human presses Send.
func IsCI() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CI"))) {
	case "", "0", "false":
		return false
	}
	return true
}
