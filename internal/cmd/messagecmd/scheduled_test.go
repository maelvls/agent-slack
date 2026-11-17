package messagecmd

import (
	"reflect"
	"testing"
)

func scheduledListFixture(h *harness) {
	h.fixtures["chat.scheduledMessages.list"] = func(p map[string]any) (map[string]any, error) {
		channel, _ := p["channel"].(string)
		if channel == "" {
			channel = testChannel
		}
		return map[string]any{
			"ok": true,
			"scheduled_messages": []any{
				map[string]any{"id": "Q1234ABCD", "channel_id": channel, "post_at": 1770168709, "text": "scheduled"},
			},
			"response_metadata": map[string]any{"next_cursor": "next-1"},
		}, nil
	}
}

const wantScheduledList = `{
	"ok": true,
	"scheduled_messages": [{"id": "Q1234ABCD", "channel_id": "C12345678", "post_at": 1770168709, "text": "scheduled"}],
	"next_cursor": "next-1"
}`

func TestScheduledList(t *testing.T) {
	h := newHarness(t)
	scheduledListFixture(h)
	got := h.mustRun("scheduled", "list", "--channel", "general", "--cursor", "cursor-1",
		"--oldest", "1770160000", "--latest", "1770170000", "--limit", "25")
	wantJSON(t, got, wantScheduledList)
	calls := h.callsTo("chat.scheduledMessages.list")
	want := map[string]any{"channel": testChannel, "cursor": "cursor-1", "oldest": "1770160000", "latest": "1770170000", "limit": 25}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].params, want) {
		t.Errorf("chat.scheduledMessages.list calls = %v", calls)
	}
}

func TestScheduledDefaultsToList(t *testing.T) {
	h := newHarness(t)
	scheduledListFixture(h)
	wantJSON(t, h.mustRun("scheduled"), wantScheduledList)
	h.wantCalls(call{"chat.scheduledMessages.list", map[string]any{}})

	// The flags of "list" work without naming it.
	h.mustRun("scheduled", "--channel", "U05BRPTKL6A", "--limit", "5")
	h.wantCalls(
		call{"conversations.open", map[string]any{"users": "U05BRPTKL6A"}},
		call{"chat.scheduledMessages.list", map[string]any{"channel": "D12345678", "limit": 5}},
	)
}

func TestScheduledListValidation(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		// --limit is an integer flag, validated like on every other command.
		{[]string{"scheduled", "list", "--limit", "abc"}, `invalid argument "abc" for "--limit" flag`},
		{[]string{"scheduled", "list", "--limit", "0"}, "invalid --limit value 0: must be a positive integer"},
		{[]string{"scheduled", "--limit", "-2"}, "invalid --limit value -2: must be a positive integer"},
		{[]string{"scheduled", "list", "--limit", ""}, `invalid argument "" for "--limit" flag`},
		{[]string{"scheduled", "list", "--oldest", "yesterday"}, "invalid --oldest"},
		{[]string{"scheduled", "list", "--channel", ""}, "--channel needs a value"},
		{[]string{"scheduled", "list", "--channel", "https://example.com/x"}, "--channel: unsupported target URL"},
		{[]string{"scheduled", "list", "extra"}, `unknown command "extra"`},
	} {
		h := newHarness(t)
		h.mustFail(tt.want, tt.args...)
		if n := len(h.callsTo("chat.scheduledMessages.list")); n != 0 {
			t.Errorf("%v: Slack was called", tt.args)
		}
	}
}

// "scheduled_messages" is printed even when nothing is scheduled.
func TestScheduledListWithNothingScheduled(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"scheduled", "list"}, {"scheduled"}, {"scheduled", "list", "--channel", testChannel}} {
		h.mustRun(args...)
		if want := "{\n  \"ok\": true,\n  \"scheduled_messages\": []\n}\n"; h.stdout.String() != want {
			t.Errorf("%v printed %s, want %s", args, h.stdout, want)
		}
	}
	for _, name := range []string{"scheduled", "list"} {
		cmd := findCommand(t, h.command(), "scheduled")
		if name == "list" {
			cmd = findCommand(t, h.command(), "scheduled", "list")
		}
		if typ := cmd.Flags().Lookup("limit").Value.Type(); typ != "int" {
			t.Errorf("%s --limit is a %s flag, want int", name, typ)
		}
	}
}

// Cancelling deletes a message a human scheduled: refused in safe mode,
// before anything is looked up.
func TestSafeModeRefusesScheduledCancel(t *testing.T) {
	h := newHarness(t)
	h.rt.SafeModeFlag = true
	h.mustFail(`"message scheduled cancel" is blocked`, "scheduled", "cancel", "Q1234ABCD", "--channel", "#general")
	h.wantCalls()
	// Listing them is reading.
	h.mustRun("scheduled", "list")
}

func TestScheduledCancel(t *testing.T) {
	h := newHarness(t)
	got := h.mustRun("scheduled", "cancel", "Q1234ABCD", "--channel", testChannel)
	h.wantCalls(call{"chat.deleteScheduledMessage", map[string]any{"channel": testChannel, "scheduled_message_id": "Q1234ABCD"}})
	wantJSON(t, got, `{"ok": true, "channel_id": "C12345678", "scheduled_message_id": "Q1234ABCD"}`)

	// The conversation may be given as for "send": name, user or URL.
	for target, wantChannel := range map[string]string{
		"#general":    testChannel,
		"U05BRPTKL6A": "D12345678",
		testURL:       testChannel,
	} {
		got := h.mustRun("scheduled", "cancel", "Q1234ABCD", "--channel", target)
		if got["channel_id"] != wantChannel {
			t.Errorf("--channel %s: channel_id = %v, want %s", target, got["channel_id"], wantChannel)
		}
	}
}

func TestScheduledCancelFailures(t *testing.T) {
	h := newHarness(t)
	h.mustFail(`required flag(s) "channel" not set`, "scheduled", "cancel", "Q1234ABCD")
	h.mustFail("accepts 1 arg", "scheduled", "cancel", "--channel", testChannel)
	h.mustFail("--channel: missing target", "scheduled", "cancel", "Q1234ABCD", "--channel", "")
	h.mustFail("<id> is empty", "scheduled", "cancel", " ", "--channel", testChannel)
	h.wantCalls()

	h.fixtures["chat.deleteScheduledMessage"] = func(map[string]any) (map[string]any, error) {
		return nil, apiError("chat.deleteScheduledMessage", "invalid_scheduled_message_id")
	}
	h.mustFail("no pending scheduled message Q1234ABCD", "scheduled", "cancel", "Q1234ABCD", "--channel", testChannel)
}
