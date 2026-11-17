package scheduled

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxAhead is how far in the future Slack lets a message be scheduled.
const MaxAhead = 120 * 24 * time.Hour

// AtHelp describes what ParseAbsolute (--schedule) accepts, for flag help.
const AtHelp = "an ISO 8601 timestamp with an explicit timezone (2026-06-15T18:00:00-07:00, 2026-06-15T16:00Z) or a Unix timestamp in seconds"

// InHelp describes what ParseRelative (--schedule-in) accepts, for flag
// help.
const InHelp = `a duration from now (30m, 1.5h, 2d), ` +
	`a day with an optional time ("today 5pm", "tomorrow", "tomorrow at 9:30am", "monday 9am", "next friday 17:00", "sat noon"; the time defaults to 9am), ` +
	`or a Unix timestamp in seconds. Days and times are in the local timezone`

const relativeUsage = "Use: 30m, 1h, 3h, 2d, tomorrow 9am, monday 9am, or a Unix timestamp."

const absoluteUsage = "Use an ISO 8601 timestamp with an explicit timezone (YYYY-MM-DDTHH:mm:ss-07:00 or ...Z), or a Unix timestamp in seconds."

// ResolvePostAt turns the --schedule and --schedule-in flag values into a
// validated post_at (Unix seconds) for chat.scheduleMessage. scheduled is
// false, with a nil error, when both values are empty: the message is to
// be sent immediately.
//
// A value that is not empty but blank is an error, so that
// `--schedule " "` cannot silently send right away. An explicitly passed
// empty string cannot be told apart from an unset flag here: commands
// should reject it themselves (cobra: cmd.Flags().Changed).
//
// now and loc are the clock and the local timezone (nil means time.Local);
// see ParseAbsolute and ParseRelative for the accepted syntax and Validate
// for the limits.
func ResolvePostAt(schedule, scheduleIn string, now time.Time, loc *time.Location) (postAt int64, scheduled bool, err error) {
	switch {
	case schedule != "" && scheduleIn != "":
		return 0, false, errors.New("--schedule and --schedule-in are mutually exclusive.")
	case schedule == "" && scheduleIn == "":
		return 0, false, nil
	case schedule != "":
		postAt, err = ParseAbsolute(schedule)
	default:
		postAt, err = ParseRelative(scheduleIn, now, loc)
	}
	if err != nil {
		return 0, false, err
	}
	if err := Validate(postAt, now, loc); err != nil {
		return 0, false, err
	}
	return postAt, true, nil
}

// Validate checks that postAt (Unix seconds) is something Slack will
// accept: strictly in the future and at most MaxAhead from now. loc (nil
// means time.Local) is only used to render times in error messages.
func Validate(postAt int64, now time.Time, loc *time.Location) error {
	if loc == nil {
		loc = time.Local
	}
	show := func(t time.Time) string { return t.In(loc).Format(time.RFC3339) }
	if postAt <= now.Unix() {
		return fmt.Errorf("--schedule/--schedule-in must resolve to a future time (got %s; it is now %s).",
			show(time.Unix(postAt, 0)), show(now))
	}
	if limit := now.Add(MaxAhead); postAt > limit.Unix() {
		if postAt >= 1e11 {
			return fmt.Errorf("--schedule/--schedule-in cannot be more than 120 days in the future: %d looks like a Unix timestamp in milliseconds; pass seconds.", postAt)
		}
		return fmt.Errorf("--schedule/--schedule-in cannot be more than 120 days in the future (got %s; the latest allowed is %s).",
			show(time.Unix(postAt, 0)), show(limit))
	}
	return nil
}

var (
	unixRe     = regexp.MustCompile(`^\d+(?:\.\d+)?$`)
	isoStartRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}`)
	isoZoneRe  = regexp.MustCompile(`(?:Z|[+-]\d{2}:?\d{2})$`)
	durationRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(m|min|mins|minutes?|h|hr|hrs|hours?|d|days?)$`)
	timeRe     = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?(am|pm)?$`)
)

// isoLayouts are tried in order; Go accepts fractional seconds after a
// seconds field even though the layouts do not spell them out.
var isoLayouts = []string{
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04Z07:00",
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04Z0700",
}

// ParseAbsolute parses a --schedule value into Unix seconds. It accepts:
//
//   - an ISO 8601 timestamp with an explicit timezone: seconds and
//     fractional seconds are optional, the zone is Z, ±HH:MM or ±HHMM
//     (2026-06-15T18:00:00-07:00, 2026-06-15T16:00Z);
//   - a Unix timestamp in seconds (at least 1000000000).
//
// A timestamp without a timezone is rejected rather than guessed. The
// result is not range-checked: see Validate.
func ParseAbsolute(input string) (int64, error) {
	s := strings.ToUpper(strings.TrimSpace(input))
	if unix, ok := parseUnix(s); ok {
		return unix, nil
	}
	invalid := fmt.Errorf("invalid --schedule value %q. %s", input, absoluteUsage)
	if !isoStartRe.MatchString(s) || !isoZoneRe.MatchString(s) {
		return 0, invalid
	}
	for _, layout := range isoLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, invalid
}

// ParseRelative parses a --schedule-in value into Unix seconds, relative
// to now. Input is case-insensitive. The grammar is:
//
//	<n><unit>                     a duration from now; n may be fractional
//	                              (30m, 1.5h, 2 days). Units: m, min, mins,
//	                              minute(s); h, hr, hrs, hour(s); d, day(s).
//	                              Days keep the local wall-clock time across
//	                              daylight-saving changes (1d at 14:00 is
//	                              tomorrow 14:00, not "24 hours later").
//	today [[at] <time>]           today; an error if that time has passed
//	tomorrow|tmrw [[at] <time>]   tomorrow
//	<weekday> [[at] <time>]       the next occurrence of that day and time
//	                              (today if the time is still ahead)
//	next <weekday> [[at] <time>]  that weekday in the following week (weeks
//	                              start on Sunday): on a Tuesday, "next
//	                              monday" is in 6 days and "next friday" in 10
//	<unix timestamp>              seconds, at least 1000000000
//
// <weekday> is sun, mon, tue, tues, wed, thu, thur, thurs, fri, sat or the
// full name. <time> is 9am, 9:30pm, "9 am", 12am (midnight), 12pm (noon),
// noon, midnight, or 24-hour 17:30 / 9; it defaults to 9am.
//
// Days and times are interpreted in loc (nil means time.Local). A time
// skipped by a daylight-saving change (02:30 on the spring-forward day) is
// an error. The result is not range-checked: see Validate.
func ParseRelative(input string, now time.Time, loc *time.Location) (int64, error) {
	if loc == nil {
		loc = time.Local
	}
	s := strings.ToLower(strings.TrimSpace(input))
	invalid := fmt.Errorf("invalid --schedule-in value %q. %s", input, relativeUsage)
	if s == "" {
		return 0, invalid
	}
	if unix, ok := parseUnix(s); ok {
		return unix, nil
	}
	if m := durationRe.FindStringSubmatch(s); m != nil {
		return addDuration(now.In(loc), m[1], m[2][0]).Unix(), nil
	}
	t, ok, err := parseNamed(s, now.In(loc))
	if err != nil {
		return 0, fmt.Errorf("invalid --schedule-in value %q: %w", input, err)
	}
	if !ok {
		return 0, invalid
	}
	return t.Unix(), nil
}

// parseUnix accepts a Unix timestamp in seconds (fractions are dropped).
// Numbers below 1e9 are not timestamps: "30" is far more likely a duration
// missing its unit.
func parseUnix(s string) (int64, bool) {
	if !unixRe.MatchString(s) {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 1e9 {
		return 0, false
	}
	// Clamp so the conversion cannot overflow; Validate rejects it anyway.
	return int64(math.Min(f, 1e17)), true
}

func addDuration(now time.Time, amountText string, unit byte) time.Time {
	amount, _ := strconv.ParseFloat(amountText, 64)
	// Far beyond MaxAhead already, and small enough not to overflow a
	// time.Duration below; Validate reports it as too far.
	amount = math.Min(amount, 1e6)
	switch unit {
	case 'm':
		return now.Add(time.Duration(amount * float64(time.Minute)))
	case 'h':
		return now.Add(time.Duration(amount * float64(time.Hour)))
	}
	// Whole days are calendar days so the wall-clock time survives a DST
	// change; only the fractional part is counted in hours.
	whole, frac := math.Modf(amount)
	return now.AddDate(0, 0, int(whole)).Add(time.Duration(frac * 24 * float64(time.Hour)))
}

type clockTime struct{ hour, minute int }

var defaultTime = clockTime{9, 0}

// parseNamed handles the day-based phrases. now must already be in the
// local timezone. ok is false when s is not such a phrase at all.
func parseNamed(s string, now time.Time) (t time.Time, ok bool, err error) {
	words := strings.Fields(s)
	next := len(words) > 0 && words[0] == "next"
	if next {
		words = words[1:]
	}
	if len(words) == 0 {
		return time.Time{}, false, nil
	}
	day, rest := words[0], words[1:]

	tod := defaultTime
	if len(rest) > 0 {
		if rest[0] == "at" {
			rest = rest[1:]
		}
		// One word ("9am") or two ("9 am"); anything else is not a time.
		if len(rest) == 0 || len(rest) > 2 || (len(rest) == 2 && rest[1] != "am" && rest[1] != "pm") {
			return time.Time{}, false, nil
		}
		if tod, ok = parseTimeOfDay(strings.Join(rest, "")); !ok {
			return time.Time{}, false, nil
		}
	}

	on := func(days int) (time.Time, error) {
		t := time.Date(now.Year(), now.Month(), now.Day()+days, tod.hour, tod.minute, 0, 0, now.Location())
		// time.Date silently moves a wall-clock time that a DST change
		// skipped; posting an hour off is worse than asking again.
		if t.Hour() != tod.hour || t.Minute() != tod.minute {
			return time.Time{}, fmt.Errorf("%02d:%02d does not exist on %s in %s (daylight-saving change); pick another time",
				tod.hour, tod.minute, t.Format("2006-01-02"), now.Location())
		}
		return t, nil
	}

	switch day {
	case "today", "tomorrow", "tmrw":
		if next {
			return time.Time{}, false, nil
		}
		if day != "today" {
			t, err = on(1)
			return t, err == nil, err
		}
		if t, err = on(0); err != nil {
			return time.Time{}, false, err
		}
		if !t.After(now) {
			return time.Time{}, false, fmt.Errorf("today %02d:%02d has already passed (it is now %s); use \"tomorrow\" or a later time",
				tod.hour, tod.minute, now.Format("15:04 MST"))
		}
		return t, true, nil
	}

	target, isWeekday := weekdays[day]
	if !isWeekday {
		return time.Time{}, false, nil
	}
	daysUntil := int(target) - int(now.Weekday())
	if daysUntil < 0 || next {
		daysUntil += 7
	}
	if t, err = on(daysUntil); err != nil {
		return time.Time{}, false, err
	}
	if !t.After(now) {
		// Same weekday, time already past: the following week.
		if t, err = on(daysUntil + 7); err != nil {
			return time.Time{}, false, err
		}
	}
	return t, true, nil
}

func parseTimeOfDay(s string) (clockTime, bool) {
	switch s {
	case "noon":
		return clockTime{12, 0}, true
	case "midnight":
		return clockTime{0, 0}, true
	}
	m := timeRe.FindStringSubmatch(s)
	if m == nil {
		return clockTime{}, false
	}
	hour, _ := strconv.Atoi(m[1])
	minute := 0
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	if minute > 59 {
		return clockTime{}, false
	}
	switch m[3] {
	case "":
		if hour > 23 {
			return clockTime{}, false
		}
	default:
		if hour < 1 || hour > 12 {
			return clockTime{}, false
		}
		// 12am is midnight, 12pm is noon.
		hour %= 12
		if m[3] == "pm" {
			hour += 12
		}
	}
	return clockTime{hour, minute}, true
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tues": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
}
