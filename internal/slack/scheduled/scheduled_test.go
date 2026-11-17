package scheduled

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/maelvls/slackcli/internal/slackapi"
)

func newYork(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestListForwardsFilters(t *testing.T) {
	var got map[string]any
	client := slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		if method != "chat.scheduledMessages.list" {
			t.Errorf("unexpected method %s", method)
		}
		got = params
		return map[string]any{
			"ok":                 true,
			"scheduled_messages": []any{map[string]any{"id": "Q1234ABCD", "channel_id": "C12345678", "post_at": 1780000000.0, "text": "scheduled"}, "junk"},
			"response_metadata":  map[string]any{"next_cursor": "next"},
		}, nil
	})
	res, err := List(context.Background(), client, ListOptions{ChannelID: "C12345678", Cursor: "abc", Oldest: "1700000000", Latest: " 1800000000.5 ", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	wantParams := map[string]any{"channel": "C12345678", "cursor": "abc", "oldest": "1700000000", "latest": "1800000000.5", "limit": 5}
	if !reflect.DeepEqual(got, wantParams) {
		t.Errorf("params = %v, want %v", got, wantParams)
	}
	if !res.OK || res.NextCursor != "next" || len(res.ScheduledMessages) != 1 || res.ScheduledMessages[0]["id"] != "Q1234ABCD" {
		t.Errorf("result = %+v", res)
	}
}

func TestListSendsNoEmptyParams(t *testing.T) {
	client := slackapi.NewFake(func(_ context.Context, _ string, params map[string]any) (map[string]any, error) {
		if len(params) != 0 {
			t.Errorf("params = %v, want none", params)
		}
		return nil, nil
	})
	res, err := List(context.Background(), client, ListOptions{})
	if err != nil || !res.OK || res.NextCursor != "" || len(res.ScheduledMessages) != 0 {
		t.Errorf("List = %+v, %v", res, err)
	}
}

func TestListRejectsNonTimestampBounds(t *testing.T) {
	client := slackapi.NewFake(func(context.Context, string, map[string]any) (map[string]any, error) {
		t.Error("API must not be called")
		return nil, nil
	})
	_, err := List(context.Background(), client, ListOptions{Latest: "tomorrow"})
	if err == nil || !strings.Contains(err.Error(), "--latest") {
		t.Errorf("err = %v", err)
	}
}

func TestCancel(t *testing.T) {
	var got map[string]any
	client := slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		if method != "chat.deleteScheduledMessage" {
			t.Errorf("unexpected method %s", method)
		}
		got = params
		if params["scheduled_message_id"] == "Qgone" {
			return map[string]any{"ok": false, "error": "invalid_scheduled_message_id"}, nil
		}
		return nil, nil
	})
	if err := Cancel(context.Background(), client, "C12345678", "Q1234ABCD"); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"channel": "C12345678", "scheduled_message_id": "Q1234ABCD"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("params = %v, want %v", got, want)
	}

	err := Cancel(context.Background(), client, "C12345678", "Qgone")
	if !slackapi.IsErrorCode(err, "invalid_scheduled_message_id") || !strings.Contains(err.Error(), "already sent or cancelled") {
		t.Errorf("err = %v", err)
	}
}

func TestParseAbsolute(t *testing.T) {
	want := time.Date(2026, 6, 16, 1, 0, 0, 0, time.UTC).Unix()
	for _, in := range []string{
		"2026-06-15T18:00:00-07:00",
		"2026-06-15T18:00-07:00",
		"2026-06-15T18:00:00-0700",
		"2026-06-15T18:00-0700",
		"2026-06-16T01:00:00Z",
		"2026-06-16t01:00z",
		" 2026-06-16T01:00:00.750Z ",
		"2026-06-16T03:00:00+02:00",
	} {
		if got, err := ParseAbsolute(in); err != nil || got != want {
			t.Errorf("ParseAbsolute(%q) = %d, %v, want %d", in, got, err, want)
		}
	}
	if got, err := ParseAbsolute("1781571600.9"); err != nil || got != 1781571600 {
		t.Errorf("unix timestamp = %d, %v", got, err)
	}
	for _, in := range []string{
		"2026-06-15T18:00:00", // no timezone: never guessed
		"2026-06-15 18:00:00Z",
		"2026-06-15",
		"2026-13-45T00:00:00Z",
		"tomorrow",
		"12345",
		"",
	} {
		if _, err := ParseAbsolute(in); err == nil || !strings.Contains(err.Error(), "explicit timezone") {
			t.Errorf("ParseAbsolute(%q) err = %v", in, err)
		}
	}
}

func TestParseRelative(t *testing.T) {
	loc := newYork(t)
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, loc) }
	saturdayNoon := at(2026, 5, 30, 12, 0) // a Saturday
	tests := []struct {
		name  string
		now   time.Time
		input string
		want  time.Time
	}{
		{"minutes", saturdayNoon, "30m", at(2026, 5, 30, 12, 30)},
		{"minutes spelled out", saturdayNoon, "90 minutes", at(2026, 5, 30, 13, 30)},
		{"fractional hours", saturdayNoon, "1.5h", at(2026, 5, 30, 13, 30)},
		{"hours, upper case", saturdayNoon, " 3 HRS ", at(2026, 5, 30, 15, 0)},
		{"days", saturdayNoon, "2 days", at(2026, 6, 1, 12, 0)},
		{"fractional days", saturdayNoon, "1.5d", at(2026, 6, 1, 0, 0)},
		{"unix timestamp", saturdayNoon, "1781571600", time.Unix(1781571600, 0)},

		// Ported from the TypeScript suite.
		{"weekday", saturdayNoon, "monday 9am", at(2026, 6, 1, 9, 0)},

		{"weekday default time", saturdayNoon, "mon", at(2026, 6, 1, 9, 0)},
		{"weekday today, time ahead", saturdayNoon, "sat 1pm", at(2026, 5, 30, 13, 0)},
		{"weekday today, time past", saturdayNoon, "saturday 9am", at(2026, 6, 6, 9, 0)},
		{"weekday today, exactly now", saturdayNoon, "sat 12pm", at(2026, 6, 6, 12, 0)},
		{"next weekday, later this week is skipped", at(2026, 5, 26, 12, 0), "next friday", at(2026, 6, 5, 9, 0)},
		{"next weekday, earlier in week", at(2026, 5, 26, 12, 0), "next monday", at(2026, 6, 1, 9, 0)},
		{"next same weekday", saturdayNoon, "next sat 8am", at(2026, 6, 6, 8, 0)},
		// "next monday 9 am" is four words and used to be rejected.
		{"next weekday with spaced meridiem", saturdayNoon, "next monday 9 am", at(2026, 6, 1, 9, 0)},
		{"at", saturdayNoon, "tomorrow at 9:30am", at(2026, 5, 31, 9, 30)},
		{"today", saturdayNoon, "today 5pm", at(2026, 5, 30, 17, 0)},
		{"tomorrow default time", saturdayNoon, "tomorrow", at(2026, 5, 31, 9, 0)},
		{"tmrw 24h", saturdayNoon, "tmrw 17:45", at(2026, 5, 31, 17, 45)},
		{"bare hour is 24h", saturdayNoon, "tomorrow 9", at(2026, 5, 31, 9, 0)},
		{"12am is midnight", saturdayNoon, "tomorrow 12am", at(2026, 5, 31, 0, 0)},
		{"12pm is noon", saturdayNoon, "tomorrow 12pm", at(2026, 5, 31, 12, 0)},
		{"12:30am", saturdayNoon, "tomorrow 12:30am", at(2026, 5, 31, 0, 30)},
		{"noon", saturdayNoon, "sun noon", at(2026, 5, 31, 12, 0)},
		{"midnight", saturdayNoon, "sun midnight", at(2026, 5, 31, 0, 0)},

		{"end of month", at(2026, 1, 31, 10, 0), "tomorrow", at(2026, 2, 1, 9, 0)},
		{"end of year", at(2026, 12, 31, 10, 0), "2d", at(2027, 1, 2, 10, 0)},
		{"weekday across month end", at(2026, 2, 27, 10, 0), "monday", at(2026, 3, 2, 9, 0)},

		// Clocks spring forward on 2026-03-08 and fall back on 2026-11-01
		// in New York: a day is then 23 or 25 hours long.
		{"1d keeps wall clock over spring forward", at(2026, 3, 7, 14, 0), "1d", at(2026, 3, 8, 14, 0)},
		{"1d keeps wall clock over fall back", at(2026, 10, 31, 14, 0), "1d", at(2026, 11, 1, 14, 0)},
		{"24h is 24 elapsed hours", at(2026, 3, 7, 14, 0), "24h", at(2026, 3, 8, 15, 0)},
		{"tomorrow over spring forward", at(2026, 3, 7, 14, 0), "tomorrow 9am", at(2026, 3, 8, 9, 0)},
		{"weekday over fall back", at(2026, 10, 30, 14, 0), "monday 9am", at(2026, 11, 2, 9, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRelative("--schedule-in", tt.input, tt.now, loc)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want.Unix() {
				t.Errorf("ParseRelative(%q) = %s, want %s", tt.input, time.Unix(got, 0).In(loc), tt.want)
			}
		})
	}
}

func TestParseRelativeUsesTheGivenLocation(t *testing.T) {
	// 2026-05-30 23:30 in New York is already Sunday 03:30 in UTC.
	now := time.Date(2026, 5, 30, 23, 30, 0, 0, newYork(t))
	inNY, _ := ParseRelative("--schedule-in", "tomorrow 9am", now, newYork(t))
	inUTC, _ := ParseRelative("--schedule-in", "tomorrow 9am", now, time.UTC)
	if want := time.Date(2026, 5, 31, 9, 0, 0, 0, newYork(t)).Unix(); inNY != want {
		t.Errorf("New York: got %s", time.Unix(inNY, 0).UTC())
	}
	if want := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC).Unix(); inUTC != want {
		t.Errorf("UTC: got %s", time.Unix(inUTC, 0).UTC())
	}
}

func TestParseRelativeErrors(t *testing.T) {
	loc := newYork(t)
	saturdayNoon := time.Date(2026, 5, 30, 12, 0, 0, 0, loc)
	tests := []struct {
		input, want string
		now         time.Time
	}{
		{"", "Use: 30m", saturdayNoon},
		{"30", "Use: 30m", saturdayNoon}, // a duration without a unit is not a timestamp
		{"soon", "Use: 30m", saturdayNoon},
		{"2w", "Use: 30m", saturdayNoon},
		{"next", "Use: 30m", saturdayNoon},
		{"next today", "Use: 30m", saturdayNoon},
		{"next tomorrow 9am", "Use: 30m", saturdayNoon},
		{"monday at", "Use: 30m", saturdayNoon},
		{"monday 9 30", "Use: 30m", saturdayNoon},
		{"monday 1 2am", "Use: 30m", saturdayNoon},
		{"monday 13pm", "Use: 30m", saturdayNoon},
		{"monday 0am", "Use: 30m", saturdayNoon},
		{"monday 24:00", "Use: 30m", saturdayNoon},
		{"monday 9:60", "Use: 30m", saturdayNoon},
		{"monday 9am extra", "Use: 30m", saturdayNoon},
		// Used to fail with the generic "invalid value" message.
		{"today 9am", "today 09:00 has already passed", saturdayNoon},
		{"today", "today 09:00 has already passed", saturdayNoon},
		// 02:30 does not exist on the spring-forward day.
		{"tomorrow 2:30am", "does not exist on 2026-03-08", time.Date(2026, 3, 7, 14, 0, 0, 0, loc)},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseRelative("--schedule-in", tt.input, tt.now, loc)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ParseRelative(%q) = %d, %v, want error containing %q", tt.input, got, err, tt.want)
			}
		})
	}
}

func TestResolvePostAt(t *testing.T) {
	loc := newYork(t)
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, loc)

	if _, scheduled, err := ResolvePostAt("", "", now, loc); scheduled || err != nil {
		t.Errorf("no flags: scheduled=%v err=%v", scheduled, err)
	}
	if got, scheduled, err := ResolvePostAt("", "3h", now, loc); err != nil || !scheduled || got != now.Add(3*time.Hour).Unix() {
		t.Errorf("--schedule-in 3h = %d, %v, %v", got, scheduled, err)
	}
	when := now.Add(48 * time.Hour)
	if got, scheduled, err := ResolvePostAt(when.Format(time.RFC3339), "", now, loc); err != nil || !scheduled || got != when.Unix() {
		t.Errorf("--schedule = %d, %v, %v", got, scheduled, err)
	}
	// Exactly 120 days ahead is still allowed.
	if _, _, err := ResolvePostAt(now.Add(MaxAhead).UTC().Format(time.RFC3339), "", now, loc); err != nil {
		t.Errorf("120 days ahead: %v", err)
	}

	errs := []struct {
		name, schedule, scheduleIn, want string
	}{
		{"both", "2026-06-15T18:00:00Z", "3h", "mutually exclusive"},
		// Ported from the TypeScript suite.
		{"beyond 120 days", now.AddDate(0, 0, 121).UTC().Format(time.RFC3339), "", "120 days"},
		{"beyond 120 days, relative", "", "121d", "120 days"},
		{"absurd duration", "", "99999999999999999999d", "120 days"},
		{"milliseconds", "1780000000000", "", "milliseconds"},
		{"past", "2026-05-30T11:59:59-04:00", "", "future time"},
		{"now", "", "0m", "future time"},
		{"past unix", "", "1000000000", "future time"},
		{"blank is not unset", " ", "", "invalid --schedule value"},
		{"blank relative is not unset", "", " ", "invalid --schedule-in value"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			got, scheduled, err := ResolvePostAt(tt.schedule, tt.scheduleIn, now, loc)
			if err == nil || scheduled || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("= %d, %v, %v, want error containing %q", got, scheduled, err, tt.want)
			}
		})
	}
}
