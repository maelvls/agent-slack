package later

import (
	"strings"
	"testing"
	"time"
)

func TestParseReminderTime(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no timezone database: %v", err)
	}
	at := func(year int, month time.Month, day, hour, minute int) int64 {
		return time.Date(year, month, day, hour, minute, 0, 0, ny).Unix()
	}
	// Friday afternoon; clocks go forward on Sunday 2026-03-08 at 02:00.
	friday := time.Date(2026, 3, 6, 14, 0, 30, 0, ny)
	// Monday, before 9:00.
	monday := time.Date(2026, 3, 9, 8, 0, 0, 0, ny)

	tests := []struct {
		name  string
		now   time.Time
		input string
		want  int64
	}{
		{"minutes", friday, "30m", friday.Unix() + 30*60},
		{"hours", friday, "2h", friday.Unix() + 2*3600},
		{"fractional hours", friday, "1.5h", friday.Unix() + 5400},
		{"verbose units", friday, "5 minutes", friday.Unix() + 300},
		{"hour", friday, "1 hour", friday.Unix() + 3600},
		{"upper case and blanks", friday, "  2H ", friday.Unix() + 2*3600},
		// Days are calendar days: the TypeScript added 48 hours, which is
		// 15:00 after the clocks went forward.
		{"days across a DST change", friday, "2 days", time.Date(2026, 3, 8, 14, 0, 30, 0, ny).Unix()},
		{"tomorrow is 9:00 local", friday, "tomorrow", at(2026, 3, 7, 9, 0)},
		{"weekday", friday, "monday", at(2026, 3, 9, 9, 0)},
		{"weekday after a DST change", friday, "sunday", at(2026, 3, 8, 9, 0)},
		{"today's weekday, 9:00 already past", friday, "friday", at(2026, 3, 13, 9, 0)},
		{"today's weekday, 9:00 still ahead", monday, "monday", at(2026, 3, 9, 9, 0)},
		{"day and time", friday, "tomorrow at 9:30am", at(2026, 3, 7, 9, 30)},
		{"today with a time", friday, "today 5pm", at(2026, 3, 6, 17, 0)},
		{"unix timestamp", friday, "1800000000", 1800000000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReminderTime(tt.input, tt.now, ny)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %s, want %s", time.Unix(got, 0).In(ny), time.Unix(tt.want, 0).In(ny))
			}
		})
	}

	invalid := []struct {
		name, input, wantErr string
	}{
		{"word", "banana", "invalid --in value"},
		{"empty", "", "invalid --in value"},
		{"letters then digits", "abc123", "invalid --in value"},
		{"digits then letters", "12abc", "invalid --in value"},
		{"number without unit", "30", "invalid --in value"},
		// Number("1e10") and Number("0x6b49d200") passed the TypeScript's
		// timestamp check.
		{"exponent", "1e10", "invalid --in value"},
		{"hex", "0x6b49d200", "invalid --in value"},
		{"past timestamp", "1700000000", "future time"},
		{"zero duration", "0m", "future time"},
		{"milliseconds", "1800000000000", "milliseconds"},
		{"today, 9:00 already past", "today", "already passed"},
		{"time skipped by DST", "sunday 2:30am", "does not exist"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReminderTime(tt.input, friday, ny)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got %d, err = %v; want an error containing %q", got, err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "--schedule") {
				t.Errorf("error names another command's flag: %v", err)
			}
		})
	}
}

func TestParseReminderTimeDefaultsToLocalTime(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	got, err := ParseReminderTime("tomorrow", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	local := time.Unix(got, 0).In(time.Local)
	if local.Hour() != 9 || local.Minute() != 0 {
		t.Errorf("tomorrow = %s, want 9:00 local time", local)
	}
}
