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
// agent could otherwise do with it. It is the single source of the help
// texts (SafeModeSummary) and of the refusals (CheckSafeMode).
var safeModeBlocked = []struct{ command, action string }{
	{"message edit", "edit messages"},
	{"message delete", "delete messages"},
	{"canvas create", "create canvases"},
	{"canvas edit", "edit canvases"},
	{"workflow run", "start workflows (which may post messages or act on other systems)"},
}

// SafeModeSummary says, for help texts, what safe mode does.
func SafeModeSummary() string {
	quoted := make([]string, len(safeModeBlocked))
	for i, b := range safeModeBlocked {
		quoted[i] = `"` + b.command + `"`
	}
	last := len(quoted) - 1
	return fmt.Sprintf(`"%s" opens the compose editor, where a human reviews and sends the message, instead of posting; %s and %s are refused`,
		SafeModeReviewed, strings.Join(quoted[:last], ", "), quoted[last])
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
