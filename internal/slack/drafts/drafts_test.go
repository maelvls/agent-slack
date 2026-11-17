package drafts

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/slackapi"
)

type call struct {
	method string
	params map[string]any
}

// fake serves canned responses by method name and records the calls.
func fake(responses map[string]map[string]any) (*slackapi.Client, *[]call) {
	calls := &[]call{}
	client := slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		*calls = append(*calls, call{method, params})
		return responses[method], nil
	})
	return client, calls
}

func rawDraft() map[string]any {
	return map[string]any{
		"id":              "Dr123",
		"last_updated_ts": "1700000000.123",
		"date_created":    float64(1700000000),
		"destinations": []any{
			map[string]any{"channel_id": "C123", "thread_ts": "1699.000100", "broadcast": true},
		},
		"blocks": []any{map[string]any{
			"type": "rich_text",
			"elements": []any{map[string]any{
				"type":     "rich_text_section",
				"elements": []any{map[string]any{"type": "text", "text": "hello world"}},
			}},
		}},
		"file_ids":       []any{"F1"},
		"date_scheduled": float64(0),
	}
}

func plainBlocks(text string) []any {
	return []any{map[string]any{
		"type": "rich_text",
		"elements": []any{map[string]any{
			"type":     "rich_text_section",
			"elements": []any{map[string]any{"type": "text", "text": text}},
		}},
	}}
}

func TestPadTS(t *testing.T) {
	for in, want := range map[string]string{
		"1700000000.123":      "1700000000.1230000",
		"1700000000.1234567":  "1700000000.1234567",
		"1700000000.12345678": "1700000000.12345678",
		"1700000000":          "1700000000",
	} {
		if got := PadTS(in); got != want {
			t.Errorf("PadTS(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameTS(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"1700000000.123", "1700000000.1230000", true},
		{"1700000000", "1700000000.0000000", true},
		{"1700000000.123", "1700000000.124", false},
		{"1700000000", "170000000", false},
	} {
		if got := SameTS(tc.a, tc.b); got != tc.want {
			t.Errorf("SameTS(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestValidateID(t *testing.T) {
	for _, ok := range []string{"Dr123", "Dr06ABCDEF12", "DrNew"} {
		if err := ValidateID(ok); err != nil {
			t.Errorf("ValidateID(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "  ", "#general", "C0123456789", "U0123456789", "https://x.slack.com/archives/C1/p1", "Dr 123", "Dr123\n"} {
		if err := ValidateID(bad); err == nil {
			t.Errorf("ValidateID(%q) = nil, want an error", bad)
		}
	}
}

func TestValidateTS(t *testing.T) {
	for _, ok := range []string{"1700000000", "1700000000.123", "1700000000.1230000"} {
		if err := ValidateTS(ok); err != nil {
			t.Errorf("ValidateTS(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "abc", "1700000000.", ".5", "1700000000.12a", "1700000000,123", "-1"} {
		if err := ValidateTS(bad); err == nil {
			t.Errorf("ValidateTS(%q) = nil, want an error", bad)
		}
	}
}

func TestTextToBlocks(t *testing.T) {
	t.Run("wraps plain text in a rich_text section", func(t *testing.T) {
		if got := TextToBlocks("hello world"); !reflect.DeepEqual(got, plainBlocks("hello world")) {
			t.Errorf("got %#v", got)
		}
	})
	t.Run("converts bullet lists to rich_text lists", func(t *testing.T) {
		blocks := TextToBlocks("- one\n- two")
		if len(blocks) != 1 {
			t.Fatalf("got %d blocks, want 1", len(blocks))
		}
		found := false
		for _, el := range blocks[0].(map[string]any)["elements"].([]any) {
			if el.(map[string]any)["type"] == "rich_text_list" {
				found = true
			}
		}
		if !found {
			t.Errorf("no rich_text_list in %#v", blocks)
		}
	})
	t.Run("blank text gives no block", func(t *testing.T) {
		if got := TextToBlocks(" \n"); len(got) != 0 || got == nil {
			t.Errorf("got %#v, want an empty non-nil slice", got)
		}
	})
}

func TestParseRecord(t *testing.T) {
	got, ok := ParseRecord(rawDraft())
	want := Draft{
		ID:            "Dr123",
		Text:          "hello world",
		Destinations:  []Destination{{ChannelID: "C123", ThreadTS: "1699.000100", Broadcast: true}},
		LastUpdatedTS: "1700000000.123",
		DateCreated:   1700000000,
		FileIDs:       []string{"F1"},
	}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v (ok=%v), want %+v", got, ok, want)
	}

	for _, raw := range []any{map[string]any{"blocks": []any{}}, nil, "Dr123", []any{}} {
		if _, ok := ParseRecord(raw); ok {
			t.Errorf("ParseRecord(%#v) ok, want not ok", raw)
		}
	}
}

func TestParseRecordTolerates(t *testing.T) {
	got, ok := ParseRecord(map[string]any{
		"id":              "Dr1",
		"last_updated_ts": 1700000000.5, // a number rather than a string
		"date_scheduled":  float64(1800000000),
		"destinations":    []any{"junk", map[string]any{"thread_ts": "1.2"}, map[string]any{"channel_id": "D1"}},
		"blocks":          []any{"junk", map[string]any{"type": "rich_text"}},
		"file_ids":        []any{1, "F2", ""},
	})
	want := Draft{
		ID:            "Dr1",
		Destinations:  []Destination{{ChannelID: "D1"}},
		LastUpdatedTS: "1700000000.5",
		DateScheduled: 1800000000,
		FileIDs:       []string{"F2"},
	}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v (ok=%v), want %+v", got, ok, want)
	}
}

func TestList(t *testing.T) {
	t.Run("requests active drafts and parses the result", func(t *testing.T) {
		client, calls := fake(map[string]map[string]any{
			"drafts.list": {"ok": true, "drafts": []any{rawDraft(), map[string]any{"not": "a draft"}}},
		})
		got, err := List(context.Background(), client, ListOptions{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 1 || (*calls)[0].method != "drafts.list" {
			t.Fatalf("calls = %+v", *calls)
		}
		if want := map[string]any{"is_active": true, "limit": 10}; !reflect.DeepEqual((*calls)[0].params, want) {
			t.Errorf("params = %#v, want %#v", (*calls)[0].params, want)
		}
		if len(got) != 1 || got[0].ID != "Dr123" {
			t.Errorf("drafts = %+v", got)
		}
	})
	t.Run("omits is_active when inactive drafts are wanted", func(t *testing.T) {
		client, calls := fake(map[string]map[string]any{"drafts.list": {"ok": true, "drafts": []any{}}})
		got, err := List(context.Background(), client, ListOptions{IncludeInactive: true})
		if err != nil {
			t.Fatal(err)
		}
		if len((*calls)[0].params) != 0 {
			t.Errorf("params = %#v, want none", (*calls)[0].params)
		}
		if got == nil || len(got) != 0 {
			t.Errorf("drafts = %#v, want an empty non-nil slice", got)
		}
	})
	t.Run("enforces the limit even when Slack does not", func(t *testing.T) {
		a, b := rawDraft(), rawDraft()
		b["id"] = "Dr456"
		client, _ := fake(map[string]map[string]any{"drafts.list": {"ok": true, "drafts": []any{a, b}}})
		got, err := List(context.Background(), client, ListOptions{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != "Dr123" {
			t.Errorf("drafts = %+v", got)
		}
	})
}

func TestCreate(t *testing.T) {
	t.Run("sends blocks, destination, and composer flag", func(t *testing.T) {
		client, calls := fake(map[string]map[string]any{"drafts.create": {"ok": true, "draft": rawDraft()}})
		draft, err := Create(context.Background(), client, Content{
			ChannelID: "C123", Text: "hello", ThreadTS: "1699.000100", Broadcast: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 1 || (*calls)[0].method != "drafts.create" {
			t.Fatalf("calls = %+v", *calls)
		}
		p := (*calls)[0].params
		wantDest := []any{map[string]any{"channel_id": "C123", "thread_ts": "1699.000100", "broadcast": true}}
		if !reflect.DeepEqual(p["destinations"], wantDest) {
			t.Errorf("destinations = %#v", p["destinations"])
		}
		if !reflect.DeepEqual(p["blocks"], plainBlocks("hello")) {
			t.Errorf("blocks = %#v", p["blocks"])
		}
		if !reflect.DeepEqual(p["file_ids"], []string{}) {
			t.Errorf("file_ids = %#v, want an empty list", p["file_ids"])
		}
		if p["is_from_composer"] != true {
			t.Errorf("is_from_composer = %#v", p["is_from_composer"])
		}
		id, _ := p["client_msg_id"].(string)
		if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
			t.Errorf("client_msg_id = %q, want a v4 UUID", id)
		}
		if draft == nil || draft.ID != "Dr123" {
			t.Errorf("draft = %+v", draft)
		}
	})
	t.Run("the wire encoding keeps empty file_ids", func(t *testing.T) {
		// file_ids must reach Slack as "[]", not be dropped.
		fields, err := slackapi.EncodeParams(map[string]any{"file_ids": []string{}})
		if err != nil || fields["file_ids"] != "[]" {
			t.Errorf("fields = %v, err = %v", fields, err)
		}
	})
	t.Run("omits thread fields for channel drafts", func(t *testing.T) {
		client, calls := fake(map[string]map[string]any{"drafts.create": {"ok": true}})
		draft, err := Create(context.Background(), client, Content{ChannelID: "C123", Text: "hello", Broadcast: true})
		if err != nil {
			t.Fatal(err)
		}
		if draft != nil {
			t.Errorf("draft = %+v, want nil when Slack returns none", draft)
		}
		if want := []any{map[string]any{"channel_id": "C123"}}; !reflect.DeepEqual((*calls)[0].params["destinations"], want) {
			t.Errorf("destinations = %#v", (*calls)[0].params["destinations"])
		}
	})
	t.Run("includes file_ids on the wire", func(t *testing.T) {
		client, calls := fake(map[string]map[string]any{"drafts.create": {"ok": true, "draft": rawDraft()}})
		if _, err := Create(context.Background(), client, Content{ChannelID: "C123", FileIDs: []string{"F9"}}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual((*calls)[0].params["file_ids"], []string{"F9"}) {
			t.Errorf("file_ids = %#v", (*calls)[0].params["file_ids"])
		}
	})
	t.Run("rejects an empty draft before calling Slack", func(t *testing.T) {
		client, calls := fake(nil)
		for _, c := range []Content{{ChannelID: "C123", Text: "  "}, {Text: "hello"}} {
			if _, err := Create(context.Background(), client, c); err == nil {
				t.Errorf("Create(%+v) succeeded", c)
			}
		}
		if len(*calls) != 0 {
			t.Errorf("calls = %+v, want none", *calls)
		}
	})
	t.Run("returns Slack's error", func(t *testing.T) {
		client, _ := fake(map[string]map[string]any{"drafts.create": {"ok": false, "error": "denied"}})
		_, err := Create(context.Background(), client, Content{ChannelID: "C123", Text: "x"})
		if !slackapi.IsErrorCode(err, "denied") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestUpdate(t *testing.T) {
	client, calls := fake(map[string]map[string]any{"drafts.update": {"ok": true, "draft": rawDraft()}})
	_, err := Update(context.Background(), client, "Dr123", "1700000000.123", Content{
		ChannelID: "C123", Text: "updated", FileIDs: []string{"F1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].method != "drafts.update" {
		t.Fatalf("calls = %+v", *calls)
	}
	p := (*calls)[0].params
	if p["draft_id"] != "Dr123" || p["client_last_updated_ts"] != "1700000000.1230000" {
		t.Errorf("params = %#v", p)
	}
	if !reflect.DeepEqual(p["file_ids"], []string{"F1"}) {
		t.Errorf("file_ids = %#v", p["file_ids"])
	}
	if _, present := p["is_from_composer"]; present {
		t.Errorf("is_from_composer must only be sent on create")
	}

	for _, bad := range [][2]string{{"C0123456789", "1700000000.1"}, {"Dr123", "yesterday"}, {"Dr123", ""}} {
		if _, err := Update(context.Background(), client, bad[0], bad[1], Content{ChannelID: "C1", Text: "x"}); err == nil {
			t.Errorf("Update(%q, %q) succeeded", bad[0], bad[1])
		}
	}
	if len(*calls) != 1 {
		t.Errorf("invalid references reached Slack: %+v", (*calls)[1:])
	}
}

func TestDelete(t *testing.T) {
	t.Run("deletes with an explicit last-updated ts", func(t *testing.T) {
		client, calls := fake(nil)
		if err := Delete(context.Background(), client, "Dr123", "1700000000.123"); err != nil {
			t.Fatal(err)
		}
		want := []call{{"drafts.delete", map[string]any{"draft_id": "Dr123", "client_last_updated_ts": "1700000000.1230000"}}}
		if !reflect.DeepEqual(*calls, want) {
			t.Errorf("calls = %+v", *calls)
		}
	})
	t.Run("fetches the last-updated ts when omitted", func(t *testing.T) {
		client, calls := fake(map[string]map[string]any{"drafts.list": {"ok": true, "drafts": []any{rawDraft()}}})
		if err := Delete(context.Background(), client, "Dr123", ""); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 2 || (*calls)[0].method != "drafts.list" || (*calls)[1].method != "drafts.delete" {
			t.Fatalf("calls = %+v", *calls)
		}
		if got := (*calls)[1].params["client_last_updated_ts"]; got != "1700000000.1230000" {
			t.Errorf("client_last_updated_ts = %v", got)
		}
	})
	t.Run("fails when the draft does not exist", func(t *testing.T) {
		client, calls := fake(map[string]map[string]any{"drafts.list": {"ok": true, "drafts": []any{}}})
		err := Delete(context.Background(), client, "DrMissing", "")
		var nf *NotFoundError
		if !errors.As(err, &nf) || nf.ID != "DrMissing" || !strings.Contains(err.Error(), "draft not found: DrMissing") {
			t.Errorf("err = %v", err)
		}
		if len(*calls) != 1 {
			t.Errorf("calls = %+v, want only drafts.list", *calls)
		}
	})
	t.Run("fails when the draft has no last_updated_ts", func(t *testing.T) {
		client, calls := fake(map[string]map[string]any{
			"drafts.list": {"ok": true, "drafts": []any{map[string]any{"id": "Dr123"}}},
		})
		if err := Delete(context.Background(), client, "Dr123", ""); err == nil || !strings.Contains(err.Error(), "last_updated_ts") {
			t.Errorf("err = %v", err)
		}
		if len(*calls) != 1 {
			t.Errorf("calls = %+v, want only drafts.list", *calls)
		}
	})
}

func TestFind(t *testing.T) {
	t.Run("lists all drafts, inactive included, and finds by id", func(t *testing.T) {
		client, calls := fake(map[string]map[string]any{"drafts.list": {"ok": true, "drafts": []any{rawDraft()}}})
		draft, err := Find(context.Background(), client, "Dr123")
		if err != nil || draft.ID != "Dr123" {
			t.Fatalf("draft = %+v, err = %v", draft, err)
		}
		if _, present := (*calls)[0].params["is_active"]; present {
			t.Errorf("is_active must not be sent: %#v", (*calls)[0].params)
		}
	})
	t.Run("prefers a record that has last_updated_ts over a malformed duplicate", func(t *testing.T) {
		client, _ := fake(map[string]map[string]any{"drafts.list": {"ok": true, "drafts": []any{
			map[string]any{"id": "Dr123", "destinations": []any{}}, rawDraft(),
		}}})
		draft, err := Find(context.Background(), client, "Dr123")
		if err != nil || draft.LastUpdatedTS != "1700000000.123" {
			t.Errorf("draft = %+v, err = %v", draft, err)
		}
	})
	t.Run("falls back to the malformed record when it is the only one", func(t *testing.T) {
		client, _ := fake(map[string]map[string]any{"drafts.list": {"ok": true, "drafts": []any{
			map[string]any{"id": "Dr123"},
		}}})
		draft, err := Find(context.Background(), client, "Dr123")
		if err != nil || draft.ID != "Dr123" || draft.LastUpdatedTS != "" {
			t.Errorf("draft = %+v, err = %v", draft, err)
		}
	})
}

func TestStandardTokensAreRefusedUpFront(t *testing.T) {
	called := false
	client := slackapi.New(slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxb-1"}, "",
		slackapi.WithHandler(func(context.Context, string, map[string]any) (map[string]any, error) {
			called = true
			return nil, nil
		}))
	ctx := context.Background()
	content := Content{ChannelID: "C123", Text: "x"}
	errs := map[string]error{}
	_, errs["List"] = List(ctx, client, ListOptions{})
	_, errs["Find"] = Find(ctx, client, "Dr123")
	_, errs["Create"] = Create(ctx, client, content)
	_, errs["Update"] = Update(ctx, client, "Dr123", "1700000000.1", content)
	errs["Delete"] = Delete(ctx, client, "Dr123", "1700000000.1")
	errs["Delete (lookup)"] = Delete(ctx, client, "Dr123", "")
	for name, err := range errs {
		if !errors.Is(err, ErrBrowserAuthRequired) {
			t.Errorf("%s: err = %v, want ErrBrowserAuthRequired", name, err)
		}
	}
	if called {
		t.Error("Slack was called with a standard token")
	}
	if !strings.Contains(ErrBrowserAuthRequired.Error(), "xoxc/xoxd") {
		t.Errorf("error does not name the required auth: %v", ErrBrowserAuthRequired)
	}
}
