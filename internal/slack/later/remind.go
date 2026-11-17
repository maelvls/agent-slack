package later

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maelvls/slackcli/internal/slack/scheduled"
)

// ReminderHelp describes what ParseReminderTime accepts, for flag help.
const ReminderHelp = `a duration from now (30m, 1.5h, 2d), ` +
	`a day with an optional time (tomorrow, monday, "today 5pm", "tomorrow at 9:30am", "next friday 17:00"), ` +
	`or a Unix timestamp in seconds. Named days are in this process's local timezone at 9:00 unless a time is given; ` +
	`a weekday is its next occurrence (today if that time is still ahead)`

// ParseReminderTime turns a `later remind --in` value into the Unix time,
// in seconds, of the reminder. The grammar is that of
// scheduled.ParseRelative (durations, "tomorrow", weekdays with an
// optional time of day, Unix timestamps), evaluated at now in loc (nil
// means time.Local). The result is always in the future.
func ParseReminderTime(input string, now time.Time, loc *time.Location) (int64, error) {
	if loc == nil {
		loc = time.Local
	}
	at, err := scheduled.ParseRelative(input, now, loc)
	if err != nil {
		// The parser is shared with `message send --schedule-in` and names
		// that flag in its errors.
		return 0, errors.New(strings.ReplaceAll(err.Error(), "--schedule-in", "--in"))
	}
	if at >= 1e11 {
		return 0, fmt.Errorf("invalid --in value %q: it looks like a Unix timestamp in milliseconds; pass seconds", input)
	}
	if at <= now.Unix() {
		return 0, fmt.Errorf("--in must resolve to a future time (got %s; it is now %s)",
			time.Unix(at, 0).In(loc).Format(time.RFC3339), now.In(loc).Format(time.RFC3339))
	}
	return at, nil
}
