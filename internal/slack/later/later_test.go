package later

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maelvls/slackcli/internal/slackapi"
)

type call struct {
	method string
	params map[string]any
}

// recorder is a fake Slack API that records the calls it serves.
type recorder struct {
	mu    sync.Mutex
	calls []call
	serve func(method string, params map[string]any) map[string]any
}

func (r *recorder) client() *slackapi.Client {
	return slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		r.mu.Lock()
		r.calls = append(r.calls, call{method, params})
		r.mu.Unlock()
		return r.serve(method, params), nil
	})
}

func (r *recorder) only(method string) []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []call
	for _, c := range r.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

// responses serves a fixed response per method ({"ok": true} otherwise).
func responses(byMethod map[string]map[string]any) *recorder {
	return &recorder{serve: func(method string, _ map[string]any) map[string]any { return byMethod[method] }}
}

func apiError(code string) map[string]any { return map[string]any{"ok": false, "error": code} }

func saved(channel, ts, state string, created float64) map[string]any {
	return map[string]any{"item_id": channel, "item_type": "message", "ts": ts, "state": state, "date_created": created}
}

func savedList(counts [4]float64, items ...any) map[string]any {
	return map[string]any{
		"saved_items": items,
		"counts": map[string]any{
			"uncompleted_count": counts[0], "archived_count": counts[1], "completed_count": counts[2], "total_count": counts[3],
		},
	}
}

func channelIDs(res ListResult) []string {
	var out []string
	for _, it := range res.Items {
		out = append(out, it.ChannelID)
	}
	return out
}

func TestListCountsOnly(t *testing.T) {
	rec := responses(map[string]map[string]any{
		"saved.list": savedList([4]float64{5, 1, 10, 16}, saved("C123", "1.1", "in_progress", 100)),
	})
	res, err := List(context.Background(), rec.client(), ListOptions{CountsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 1 || rec.calls[0].method != "saved.list" {
		t.Errorf("calls = %+v", rec.calls)
	}
	if want := (Counts{InProgress: 5, Archived: 1, Completed: 10, Total: 16}); res.Counts == nil || *res.Counts != want {
		t.Errorf("counts = %+v, want %+v", res.Counts, want)
	}
	if len(res.Items) != 0 || res.NextCursor != "" {
		t.Errorf("result = %+v", res)
	}
}

func TestListFiltersToMessageItems(t *testing.T) {
	rec := responses(map[string]map[string]any{
		"saved.list": savedList([4]float64{2, 0, 0, 2},
			map[string]any{"item_id": "Sa123", "item_type": "reminder", "ts": "", "state": "in_progress", "date_created": 100.0},
			saved("C123", "1.1", "in_progress", 200)),
		"conversations.info":    {"channel": map[string]any{"name": "general"}},
		"conversations.history": {"messages": []any{map[string]any{"ts": "1.1", "text": "hello", "user": "U1"}}},
	})
	res, err := List(context.Background(), rec.client(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(channelIDs(res), []string{"C123"}) {
		t.Errorf("items = %+v", res.Items)
	}
}

func TestListFiltersByState(t *testing.T) {
	completed := saved("C2", "2.2", "completed", 200)
	completed["date_completed"] = 300.0
	completed["date_due"] = 250.0
	list := savedList([4]float64{1, 1, 1, 3},
		saved("C1", "1.1", "in_progress", 100), completed, saved("C3", "3.3", "archived", 150),
		// No state at all counts as in progress.
		map[string]any{"item_id": "C4", "item_type": "message", "ts": "4.4", "date_created": 50.0})
	serve := map[string]map[string]any{
		"saved.list":         list,
		"conversations.info": {"channel": map[string]any{"name": "test"}},
	}

	res, err := List(context.Background(), responses(serve).client(), ListOptions{State: StateCompleted})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %+v", res.Items)
	}
	if it := res.Items[0]; it.State != "completed" || it.DateSaved != 200 || it.DateCompleted != 300 || it.DateDue != 250 {
		t.Errorf("item = %+v", it)
	}

	res, err = List(context.Background(), responses(serve).client(), ListOptions{State: StateAll})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(channelIDs(res), []string{"C1", "C2", "C3", "C4"}) {
		t.Errorf("all: items = %+v", res.Items)
	}

	res, err = List(context.Background(), responses(serve).client(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(channelIDs(res), []string{"C1", "C4"}) || res.Items[1].State != "in_progress" {
		t.Errorf("default state: items = %+v", res.Items)
	}
}

func TestListHydratesMessageContent(t *testing.T) {
	rec := responses(map[string]map[string]any{
		"saved.list":         savedList([4]float64{1, 0, 0, 1}, saved("C123", "1.1", "in_progress", 100)),
		"conversations.info": {"channel": map[string]any{"name": "general", "is_channel": true}},
		"conversations.history": {"messages": []any{
			map[string]any{"ts": "1.1", "text": "Hello world", "user": "U456", "thread_ts": "1.0", "reply_count": 3.0},
		}},
	})
	res, err := List(context.Background(), rec.client(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{{
		ChannelID: "C123", ChannelName: "general", TS: "1.1", State: "in_progress", DateSaved: 100,
		Message: &Message{Author: &Author{UserID: "U456"}, Content: "Hello world", ThreadTS: "1.0", ReplyCount: 3},
	}}
	if !reflect.DeepEqual(res.Items, want) {
		t.Errorf("items = %+v", res.Items)
	}
	history := rec.only("conversations.history")
	wantParams := map[string]any{"channel": "C123", "latest": "1.1", "inclusive": true, "limit": 1}
	if len(history) != 1 || !reflect.DeepEqual(history[0].params, wantParams) {
		t.Errorf("history calls = %+v", history)
	}
}

func TestListResolvesDMUserNames(t *testing.T) {
	rec := responses(map[string]map[string]any{
		"saved.list":            savedList([4]float64{1, 0, 0, 1}, saved("D123", "1.1", "in_progress", 100)),
		"conversations.info":    {"channel": map[string]any{"is_im": true, "user": "U789"}},
		"conversations.history": {"messages": []any{map[string]any{"ts": "1.1", "text": "hi", "bot_id": "B1"}}},
		"users.info":            {"user": map[string]any{"id": "U789", "real_name": "Alice Smith", "profile": map[string]any{"display_name": "alice"}}},
	})
	res, err := List(context.Background(), rec.client(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].ChannelName != "alice" {
		t.Errorf("channel_name = %q", res.Items[0].ChannelName)
	}
	if a := res.Items[0].Message.Author; a == nil || a.BotID != "B1" || a.UserID != "" {
		t.Errorf("author = %+v", a)
	}
}

func TestListTruncatesContentOnCharacterBoundaries(t *testing.T) {
	serve := func(text string) *recorder {
		return responses(map[string]map[string]any{
			"saved.list":            savedList([4]float64{1, 0, 0, 1}, saved("C1", "1.1", "in_progress", 100)),
			"conversations.history": {"messages": []any{map[string]any{"ts": "1.1", "text": text, "user": "U1"}}},
		})
	}
	tests := []struct {
		name  string
		text  string
		chars int
		want  string
	}{
		{"long", strings.Repeat("x", 5000), 100, strings.Repeat("x", 100) + "\n…"},
		// The TypeScript cut UTF-16 units, splitting characters in two.
		{"multi-byte", strings.Repeat("é", 10), 4, "éééé\n…"},
		{"default is 4000", strings.Repeat("x", 4001), 0, strings.Repeat("x", 4000) + "\n…"},
		{"short enough", "hello", 5, "hello"},
		{"unlimited", strings.Repeat("x", 5000), -1, strings.Repeat("x", 5000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := List(context.Background(), serve(tt.text).client(), ListOptions{MaxBodyChars: tt.chars})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Items[0].Message.Content; got != tt.want {
				t.Errorf("content = %d bytes %.20q…, want %d bytes", len(got), got, len(tt.want))
			}
		})
	}
}

// pagedSaved serves saved.list pages keyed by cursor ("" is the first).
func pagedSaved(pages map[string]map[string]any) *recorder {
	return &recorder{serve: func(method string, params map[string]any) map[string]any {
		if method != "saved.list" {
			return nil
		}
		cursor, _ := params["cursor"].(string)
		return pages[cursor]
	}}
}

func withNext(resp map[string]any, cursor string) map[string]any {
	resp["response_metadata"] = map[string]any{"next_cursor": cursor}
	return resp
}

func TestListLimitAndCursorResumeMidPage(t *testing.T) {
	pages := func() *recorder {
		return pagedSaved(map[string]map[string]any{
			"": withNext(savedList([4]float64{5, 0, 1, 6},
				saved("C1", "1.1", "in_progress", 100), saved("CX", "9.9", "completed", 100),
				saved("C2", "2.2", "in_progress", 200), saved("C3", "3.3", "in_progress", 300)), "p2"),
			// Only the first page carries the counts.
			"p2": {"saved_items": []any{saved("C4", "4.4", "in_progress", 400), saved("C5", "5.5", "in_progress", 500)}},
		})
	}

	rec := pages()
	res, err := List(context.Background(), rec.client(), ListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(channelIDs(res), []string{"C1", "C2"}) {
		t.Fatalf("page 1 = %v", channelIDs(res))
	}
	// The TypeScript returned Slack's cursor for the next page here, and
	// C3 was never listed.
	if res.NextCursor != "offset-3:" {
		t.Errorf("next_cursor = %q", res.NextCursor)
	}
	if got := rec.only("saved.list"); len(got) != 1 || !reflect.DeepEqual(got[0].params, map[string]any{"limit": 50}) {
		t.Errorf("saved.list calls = %+v", got)
	}

	res, err = List(context.Background(), pages().client(), ListOptions{Limit: 2, Cursor: res.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(channelIDs(res), []string{"C3", "C4"}) || res.NextCursor != "offset-1:p2" {
		t.Fatalf("page 2 = %v, next %q", channelIDs(res), res.NextCursor)
	}
	// The counts of the TypeScript were all zero when a cursor was given.
	if res.Counts == nil || res.Counts.Total != 6 {
		t.Errorf("counts on a resumed listing = %+v", res.Counts)
	}

	rec = pages()
	res, err = List(context.Background(), rec.client(), ListOptions{Limit: 2, Cursor: res.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(channelIDs(res), []string{"C5"}) || res.NextCursor != "" {
		t.Fatalf("page 3 = %v, next %q", channelIDs(res), res.NextCursor)
	}
	if got := rec.only("saved.list"); len(got) != 1 || got[0].params["cursor"] != "p2" {
		t.Errorf("saved.list calls = %+v", got)
	}
	// A page without counts must not be reported as "0 saved items".
	if res.Counts != nil {
		t.Errorf("counts = %+v, but page p2 has none", res.Counts)
	}
}

func TestListNextCursorWhenLimitEndsThePage(t *testing.T) {
	rec := pagedSaved(map[string]map[string]any{
		"": withNext(savedList([4]float64{2, 0, 1, 3},
			saved("C1", "1.1", "in_progress", 100), saved("C2", "2.2", "in_progress", 200), saved("CX", "9.9", "completed", 100)), "p2"),
	})
	res, err := List(context.Background(), rec.client(), ListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	// Nothing left to list on this page: Slack's own cursor is enough.
	if !reflect.DeepEqual(channelIDs(res), []string{"C1", "C2"}) || res.NextCursor != "p2" {
		t.Errorf("items = %v, next %q", channelIDs(res), res.NextCursor)
	}
}

func TestListFollowsPagesUntilLimit(t *testing.T) {
	rec := pagedSaved(map[string]map[string]any{
		"":   withNext(savedList([4]float64{1, 0, 2, 3}, saved("C1", "1.1", "completed", 100)), "p2"),
		"p2": withNext(map[string]any{"saved_items": []any{saved("C2", "2.2", "completed", 100)}}, "p3"),
		"p3": {"saved_items": []any{saved("C3", "3.3", "in_progress", 100)}},
	})
	res, err := List(context.Background(), rec.client(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(channelIDs(res), []string{"C3"}) || res.NextCursor != "" {
		t.Errorf("items = %v, next %q", channelIDs(res), res.NextCursor)
	}
	if n := len(rec.only("saved.list")); n != 3 {
		t.Errorf("saved.list calls = %d, want 3", n)
	}
}

func TestListStopsWhenSlackRepeatsACursor(t *testing.T) {
	rec := &recorder{serve: func(method string, _ map[string]any) map[string]any {
		if method != "saved.list" {
			return nil
		}
		return withNext(savedList([4]float64{0, 0, 1, 1}, saved("C1", "1.1", "completed", 100)), "again")
	}}
	res, err := List(context.Background(), rec.client(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rec.only("saved.list")); n != 2 {
		t.Errorf("saved.list calls = %d, want 2", n)
	}
	if len(res.Items) != 0 || res.NextCursor != "" {
		t.Errorf("result = %+v", res)
	}
}

func TestListRejectsMalformedCursor(t *testing.T) {
	rec := responses(nil)
	for _, cursor := range []string{"offset-x:abc", "offset-3", "offset--1:abc"} {
		if _, err := List(context.Background(), rec.client(), ListOptions{Cursor: cursor}); err == nil || !strings.Contains(err.Error(), "invalid cursor") {
			t.Errorf("cursor %q: err = %v", cursor, err)
		}
	}
	if len(rec.calls) != 0 {
		t.Errorf("calls = %+v", rec.calls)
	}
}

func TestListFindsSavedThreadReplies(t *testing.T) {
	rec := &recorder{serve: func(method string, params map[string]any) map[string]any {
		switch method {
		case "saved.list":
			return savedList([4]float64{1, 0, 0, 1}, saved("C1", "2.2", "in_progress", 100))
		case "conversations.history":
			// A reply is not in the channel history: Slack answers with
			// the newest top-level message before it.
			return map[string]any{"messages": []any{map[string]any{"ts": "1.1", "text": "root"}}}
		case "conversations.replies":
			if params["ts"] != "2.2" || params["channel"] != "C1" {
				return apiError("thread_not_found")
			}
			return map[string]any{"messages": []any{map[string]any{"ts": "2.2", "text": "a reply", "user": "U1", "thread_ts": "1.1"}}}
		}
		return nil
	}}
	res, err := List(context.Background(), rec.client(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if m := res.Items[0].Message; m == nil || m.Content != "a reply" || m.ThreadTS != "1.1" {
		t.Errorf("item = %+v", res.Items[0])
	}
}

func TestListReportsUnreadableMessages(t *testing.T) {
	rec := &recorder{serve: func(method string, params map[string]any) map[string]any {
		switch method {
		case "saved.list":
			return savedList([4]float64{2, 0, 0, 2}, saved("C1", "1.1", "in_progress", 100), saved("C2", "2.2", "in_progress", 100))
		case "conversations.history":
			if params["channel"] == "C2" {
				return apiError("channel_not_found")
			}
			return map[string]any{"messages": []any{}}
		case "conversations.replies":
			return apiError("thread_not_found")
		}
		return apiError("channel_not_found")
	}}
	res, err := List(context.Background(), rec.client(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if it := res.Items[0]; it.Message != nil || it.MessageError != "message_not_found" || it.ChannelName != "" {
		t.Errorf("deleted message: %+v", it)
	}
	if it := res.Items[1]; it.Message != nil || it.MessageError != "channel_not_found" {
		t.Errorf("invisible channel: %+v", it)
	}
}

func TestListPropagatesAuthErrors(t *testing.T) {
	// The TypeScript swallowed every hydration error, so expired
	// credentials produced a list of empty items and no refresh.
	rec := &recorder{serve: func(method string, _ map[string]any) map[string]any {
		if method == "saved.list" {
			return savedList([4]float64{1, 0, 0, 1}, saved("C1", "1.1", "in_progress", 100))
		}
		return apiError("invalid_auth")
	}}
	if _, err := List(context.Background(), rec.client(), ListOptions{}); !slackapi.IsAuthError(err) {
		t.Errorf("err = %v, want an auth error", err)
	}

	rec = responses(map[string]map[string]any{"saved.list": apiError("token_expired")})
	if _, err := List(context.Background(), rec.client(), ListOptions{}); !slackapi.IsAuthError(err) {
		t.Errorf("err = %v, want an auth error", err)
	}
}

func TestListResolvesEachChannelOnceWithBoundedConcurrency(t *testing.T) {
	var items []any
	for i := range 40 {
		items = append(items, saved(fmt.Sprintf("C%d", i%4), fmt.Sprintf("%d.1", i), "in_progress", 100))
	}
	var mu sync.Mutex
	inFlight, peak := 0, 0
	rec := &recorder{serve: func(method string, params map[string]any) map[string]any {
		if method == "saved.list" {
			return savedList([4]float64{40, 0, 0, 40}, items...)
		}
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		if method == "conversations.info" {
			return map[string]any{"channel": map[string]any{"name": "name-" + params["channel"].(string)}}
		}
		return map[string]any{"messages": []any{map[string]any{"ts": params["latest"], "text": "m"}}}
	}}
	res, err := List(context.Background(), rec.client(), ListOptions{Limit: 40})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rec.only("conversations.info")); n != 4 {
		t.Errorf("conversations.info calls = %d, want 4", n)
	}
	if peak > hydrateConcurrency {
		t.Errorf("peak concurrency = %d, want <= %d", peak, hydrateConcurrency)
	}
	for i, it := range res.Items {
		if it.TS != fmt.Sprintf("%d.1", i) || it.ChannelName != "name-"+it.ChannelID || it.Message == nil {
			t.Fatalf("item %d = %+v", i, it)
		}
	}
}

func standardClient(t *testing.T) *slackapi.Client {
	return slackapi.New(slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxb-test"}, "",
		slackapi.WithHandler(func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
			t.Errorf("unexpected call to %s", method)
			return nil, nil
		}))
}

func TestStandardTokensAreRefusedUpFront(t *testing.T) {
	ctx := context.Background()
	c := standardClient(t)
	_, listErr := List(ctx, c, ListOptions{})
	for name, err := range map[string]error{
		"List":        listErr,
		"Mark":        Mark(ctx, c, "C1", "1.1", MarkCompleted),
		"Reopen":      Reopen(ctx, c, "C1", "1.1"),
		"Save":        Save(ctx, c, "C1", "1.1"),
		"Remove":      Remove(ctx, c, "C1", "1.1"),
		"SetReminder": SetReminder(ctx, c, "C1", "1.1", 1800000000),
	} {
		if err == nil || !strings.Contains(err.Error(), "browser session credentials") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestMark(t *testing.T) {
	for _, mark := range []MarkAction{MarkCompleted, MarkArchived, MarkUncompleted, MarkUnarchived} {
		rec := responses(nil)
		if err := Mark(context.Background(), rec.client(), "C123", "1.1", mark); err != nil {
			t.Fatal(err)
		}
		want := []call{{"saved.update", map[string]any{"item_id": "C123", "item_type": "message", "ts": "1.1", "mark": string(mark)}}}
		if !reflect.DeepEqual(rec.calls, want) {
			t.Errorf("%s: calls = %+v", mark, rec.calls)
		}
	}
	if err := Mark(context.Background(), responses(map[string]map[string]any{"saved.update": apiError("not_found")}).client(), "C1", "1.1", MarkCompleted); !slackapi.IsErrorCode(err, "not_found") {
		t.Errorf("err = %v", err)
	}
}

// TestSavedUpdateIsMultipart checks the wire format: saved.update ignores
// form-encoded parameters.
func TestSavedUpdateIsMultipart(t *testing.T) {
	type request struct {
		path, contentType string
		form              map[string]string
	}
	var got []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := request{path: r.URL.Path, contentType: r.Header.Get("Content-Type"), form: map[string]string{}}
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			for k, v := range r.MultipartForm.Value {
				req.form[k] = v[0]
			}
		}
		got = append(got, req)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()
	client := slackapi.New(slackapi.Auth{Type: slackapi.AuthBrowser, XoxcToken: "xoxc-test", XoxdCookie: "xoxd-test"}, srv.URL,
		slackapi.WithLoopbackWorkspaceForTesting())

	if err := Mark(context.Background(), client, "C123", "1.1", MarkCompleted); err != nil {
		t.Fatal(err)
	}
	if err := SetReminder(context.Background(), client, "C123", "1.1", 1800000000); err != nil {
		t.Fatal(err)
	}
	want := []map[string]string{
		{"token": "xoxc-test", "item_id": "C123", "item_type": "message", "ts": "1.1", "mark": "completed"},
		{"token": "xoxc-test", "item_id": "C123", "item_type": "message", "ts": "1.1", "date_due": "1800000000"},
	}
	if len(got) != 2 {
		t.Fatalf("requests = %+v", got)
	}
	for i, req := range got {
		if req.path != "/api/saved.update" || !strings.HasPrefix(req.contentType, "multipart/form-data") {
			t.Errorf("request %d = %s %s", i, req.path, req.contentType)
		}
		if !reflect.DeepEqual(req.form, want[i]) {
			t.Errorf("request %d form = %v, want %v", i, req.form, want[i])
		}
	}
}

func TestSaveRemoveSetReminder(t *testing.T) {
	ctx := context.Background()
	item := func(extra map[string]any) map[string]any {
		params := map[string]any{"item_id": "C123", "item_type": "message", "ts": "1.1"}
		for k, v := range extra {
			params[k] = v
		}
		return params
	}
	tests := []struct {
		name string
		do   func(*slackapi.Client) error
		want call
	}{
		{"Save", func(c *slackapi.Client) error { return Save(ctx, c, "C123", "1.1") }, call{"saved.add", item(nil)}},
		{"Remove", func(c *slackapi.Client) error { return Remove(ctx, c, "C123", "1.1") }, call{"saved.delete", item(nil)}},
		{"SetReminder", func(c *slackapi.Client) error { return SetReminder(ctx, c, "C123", "1.1", 1700000000) },
			call{"saved.update", item(map[string]any{"date_due": int64(1700000000)})}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := responses(nil)
			if err := tt.do(rec.client()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rec.calls, []call{tt.want}) {
				t.Errorf("calls = %+v", rec.calls)
			}
			failing := responses(map[string]map[string]any{tt.want.method: apiError("not_found")})
			if err := tt.do(failing.client()); !slackapi.IsErrorCode(err, "not_found") {
				t.Errorf("err = %v, want not_found", err)
			}
		})
	}
}

func TestReopen(t *testing.T) {
	tests := []struct {
		name      string
		fail      map[string]string // mark → error code
		wantErr   string
		wantMarks []string
	}{
		{"both accepted", nil, "", []string{"uncompleted", "unarchived"}},
		{"was completed", map[string]string{"unarchived": "invalid_state"}, "", []string{"uncompleted", "unarchived"}},
		{"was archived", map[string]string{"uncompleted": "invalid_state"}, "", []string{"uncompleted", "unarchived"}},
		// The TypeScript ignored both outcomes and printed {"ok": true}.
		{"neither", map[string]string{"uncompleted": "item_not_found", "unarchived": "item_not_found"}, "item_not_found", []string{"uncompleted", "unarchived"}},
		{"expired credentials", map[string]string{"uncompleted": "invalid_auth"}, "invalid_auth", []string{"uncompleted"}},
		{"credentials expire midway", map[string]string{"unarchived": "invalid_auth"}, "invalid_auth", []string{"uncompleted", "unarchived"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{serve: func(_ string, params map[string]any) map[string]any {
				if code := tt.fail[params["mark"].(string)]; code != "" {
					return apiError(code)
				}
				return nil
			}}
			err := Reopen(context.Background(), rec.client(), "C1", "1.1")
			if got := slackapi.ErrorCode(err); got != tt.wantErr {
				t.Errorf("err = %v, want code %q", err, tt.wantErr)
			}
			var marks []string
			for _, c := range rec.calls {
				marks = append(marks, c.params["mark"].(string))
			}
			if !reflect.DeepEqual(marks, tt.wantMarks) {
				t.Errorf("marks = %v, want %v", marks, tt.wantMarks)
			}
		})
	}
}

func TestParseState(t *testing.T) {
	for in, want := range map[string]State{
		"in_progress": StateInProgress, "in-progress": StateInProgress, "Active": StateInProgress, " open ": StateInProgress,
		"archived": StateArchived, "archive": StateArchived,
		"completed": StateCompleted, "complete": StateCompleted, "DONE": StateCompleted,
		"all": StateAll,
	} {
		if got, err := ParseState(in); err != nil || got != want {
			t.Errorf("ParseState(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// The TypeScript silently listed in_progress items for these.
	for _, in := range []string{"", "finished", "archivd"} {
		if _, err := ParseState(in); err == nil || !strings.Contains(err.Error(), "--state") {
			t.Errorf("ParseState(%q): err = %v", in, err)
		}
	}
}

// Channel names and message contents are enrichments: a rate limit must
// not lose the list of saved items.
func TestListDegradesWhenRateLimited(t *testing.T) {
	var items []any
	for i := range 30 {
		items = append(items, saved(fmt.Sprintf("C%d", i), "1.1", "in_progress", 100))
	}
	for _, limited := range []string{"conversations.history", "conversations.replies", "conversations.info"} {
		t.Run(limited, func(t *testing.T) {
			var mu sync.Mutex
			calls := map[string]int{}
			client := slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
				mu.Lock()
				calls[method]++
				mu.Unlock()
				switch {
				case method == "saved.list":
					return savedList([4]float64{30, 0, 0, 30}, items...), nil
				case method == limited:
					return nil, &slackapi.RateLimitError{Method: method, RetryAfter: time.Minute}
				case method == "conversations.info":
					return map[string]any{"channel": map[string]any{"name": "name-" + params["channel"].(string)}}, nil
				case method == "conversations.history" && limited == "conversations.replies":
					return map[string]any{"messages": []any{}}, nil // not a top-level message
				}
				return map[string]any{"messages": []any{map[string]any{"ts": "1.1", "text": "hello"}}}, nil
			})
			res, err := List(context.Background(), client, ListOptions{Limit: 30})
			if err != nil {
				t.Fatalf("err = %v, want the items without what was rate limited", err)
			}
			if len(res.Items) != 30 || res.Counts == nil || res.Counts.Total != 30 {
				t.Fatalf("result = %+v", res)
			}
			if n := calls[limited]; n > hydrateConcurrency {
				t.Errorf("%d %s calls after being rate limited, want at most %d", n, limited, hydrateConcurrency)
			}
			if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "30 of 30") || !strings.Contains(res.Warnings[0], "rate limited") {
				t.Errorf("warnings = %q", res.Warnings)
			}
			for _, it := range res.Items {
				if limited == "conversations.info" {
					if it.ChannelName != "" || it.MessageError != "" || it.Message == nil {
						t.Fatalf("item = %+v", it)
					}
				} else if it.MessageError != "ratelimited" || it.Message != nil || it.ChannelName != "name-"+it.ChannelID {
					t.Fatalf("item = %+v", it)
				}
			}
		})
	}
}
