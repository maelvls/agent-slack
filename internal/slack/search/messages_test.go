package search

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/slack/users"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const general = "C12345678"

func TestSearchMessagesPagesUntilLimitSkippingDuplicates(t *testing.T) {
	f := newFake(t)
	a, b, c, d := match(general, jan1+3*day, "a"), match(general, jan1+2*day, "b"), match(general, jan1+day, "c"), match(general, jan1, "d")
	// A message posted between the two requests shifts b onto page 2.
	f.searchPages("search.messages", 6, []any{a, b, b}, []any{b, c, d})
	f.history(message(jan1+3*day, "a full"), message(jan1+2*day, "b full"), message(jan1+day, "c full"), message(jan1, "d full"))

	opts := options(t, KindMessages, "hello")
	opts.Limit = 3
	res := mustSearch(t, f, opts)

	if got, want := messageTS(res), []string{ts(jan1 + 3*day), ts(jan1 + 2*day), ts(jan1 + day)}; !reflect.DeepEqual(got, want) {
		t.Errorf("messages = %v, want %v", got, want)
	}
	if got := res.Messages[0]; got.Content != "a full" || got.Permalink != a["permalink"] || got.Author.UserID != "U11111111" || got.ChannelID != general {
		t.Errorf("first message = %+v", got)
	}
	calls := f.callsTo("search.messages")
	if len(calls) != 2 {
		t.Fatalf("search.messages calls = %d, want 2", len(calls))
	}
	want := map[string]any{"query": "hello", "count": 3, "page": 1, "highlight": false, "sort": "timestamp", "sort_dir": "desc"}
	if !reflect.DeepEqual(calls[0], want) {
		t.Errorf("first search.messages call = %v, want %v", calls[0], want)
	}
	if calls[1]["page"] != 2 {
		t.Errorf("second call page = %v", calls[1]["page"])
	}
	// Only the three messages returned are fetched in full.
	if n := len(f.callsTo("conversations.history")); n != 3 {
		t.Errorf("conversations.history calls = %d, want 3", n)
	}

	more := res.MoreMessages
	if more == nil {
		t.Fatal("more_messages is missing although a match was left out")
	}
	if more.Total != 6 || more.Before != "2026-01-03" {
		t.Errorf("more = %+v, want total 6 and before 2026-01-03", more)
	}
	if !strings.Contains(more.Hint, "--before 2026-01-03") || !strings.Contains(more.Hint, "--limit") {
		t.Errorf("hint = %q", more.Hint)
	}
}

func TestSearchMessagesCompleteResultHasNoMore(t *testing.T) {
	f := newFake(t)
	f.searchPages("search.messages", 1, []any{match(general, jan1, "a")})
	f.history(message(jan1, "a"))

	res := mustSearch(t, f, options(t, KindMessages, "hello"))
	if len(res.Messages) != 1 || res.MoreMessages != nil {
		t.Errorf("got %d messages, more = %+v", len(res.Messages), res.MoreMessages)
	}
	if got := f.callsTo("search.messages")[0]["count"]; got != DefaultLimit {
		t.Errorf("count = %v, want the default limit", got)
	}
}

func TestSearchMessagesContentTypeFilterKeepsPaging(t *testing.T) {
	f := newFake(t)
	f.files["https://files.slack.com/files/F1"] = "png1"
	f.files["https://files.slack.com/files/F2"] = "png2"
	f.searchPages("search.messages", 5,
		[]any{match(general, jan1+4, "text"), match(general, jan1+3, "image 1"), match(general, jan1+2, "text")},
		[]any{match(general, jan1+1, "image 2"), match(general, jan1, "text")},
	)
	f.history(
		message(jan1+4, "text"),
		withFile(message(jan1+3, "image 1"), slackFile("F1", "image/png")),
		message(jan1+2, "text"),
		withFile(message(jan1+1, "image 2"), slackFile("F2", "image/png")),
		message(jan1, "text"),
	)

	opts := options(t, KindMessages, "hello")
	opts.Limit, opts.ContentType = 2, ContentImage
	res := mustSearch(t, f, opts)

	if got, want := messageTS(res), []string{ts(jan1 + 3), ts(jan1 + 1)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("messages = %v, want %v (the filter must not eat into the limit)", got, want)
	}
	if got := f.callsTo("search.messages")[0]["count"]; got != searchPageSize {
		t.Errorf("count = %v, want full pages when filtering", got)
	}
	file := res.Messages[0].Files[0]
	if file.Name != "F1-name" || file.Mimetype != "image/png" || !strings.HasSuffix(file.Path, "/F1.png") || file.Error != "" {
		t.Errorf("file = %+v", file)
	}
	// Text messages are filtered out before anything is downloaded.
	if len(f.gets) != 2 {
		t.Errorf("downloads = %v, want 2", f.gets)
	}
}

func TestSearchMessagesStopsExaminingAtBudget(t *testing.T) {
	f := newFake(t)
	f.on("search.messages", func(p map[string]any) (map[string]any, error) {
		page := p["page"].(int)
		matches := make([]any, searchPageSize)
		for i := range matches {
			matches[i] = match(general, jan1-(page-1)*searchPageSize-i, "text")
		}
		return map[string]any{"messages": map[string]any{"matches": matches, "paging": map[string]any{"pages": 50, "total": 5000}}}, nil
	})
	f.on("conversations.history", func(p map[string]any) (map[string]any, error) {
		return map[string]any{"messages": []any{map[string]any{"ts": p["latest"], "text": "text"}}}, nil
	})

	opts := options(t, KindMessages, "hello")
	opts.ContentType = ContentImage
	res := mustSearch(t, f, opts)

	if len(res.Messages) != 0 {
		t.Errorf("messages = %d, want none", len(res.Messages))
	}
	if n := len(f.callsTo("conversations.history")); n != maxExamined {
		t.Errorf("messages fetched = %d, want %d", n, maxExamined)
	}
	if res.MoreMessages == nil || !strings.Contains(res.MoreMessages.Hint, "stopped after examining the 1000 most recent matches") {
		t.Errorf("more = %+v", res.MoreMessages)
	}
}

func TestSearchMessagesPaginationObjectAndRepeatedPages(t *testing.T) {
	t.Run("page_count", func(t *testing.T) {
		f := newFake(t)
		pages := [][]any{{match(general, jan1+1, "a")}, {match(general, jan1, "b")}}
		f.on("search.messages", func(p map[string]any) (map[string]any, error) {
			return map[string]any{"messages": map[string]any{
				"matches":    pages[p["page"].(int)-1],
				"pagination": map[string]any{"page_count": 2, "total_count": 2},
			}}, nil
		})
		f.history(message(jan1+1, "a"), message(jan1, "b"))
		res := mustSearch(t, f, options(t, KindMessages, "hello"))
		if len(res.Messages) != 2 || res.MoreMessages != nil {
			t.Errorf("got %d messages, more = %+v; want both pages read", len(res.Messages), res.MoreMessages)
		}
	})

	t.Run("the same page forever", func(t *testing.T) {
		f := newFake(t)
		f.on("search.messages", func(map[string]any) (map[string]any, error) {
			return map[string]any{"messages": map[string]any{
				"matches": []any{match(general, jan1, "a")},
				"paging":  map[string]any{"pages": 100000},
			}}, nil
		})
		f.history(message(jan1, "a"))
		res := mustSearch(t, f, options(t, KindMessages, "hello"))
		if len(res.Messages) != 1 {
			t.Errorf("messages = %d, want the one message once", len(res.Messages))
		}
		if n := len(f.callsTo("search.messages")); n > searchMaxPages {
			t.Errorf("search.messages calls = %d, want at most %d", n, searchMaxPages)
		}
	})
}

func TestSearchMessagesFallsBackToTheMatchWhenFetchFails(t *testing.T) {
	f := newFake(t)
	f.searchPages("search.messages", 1, []any{match(general, jan1, "from the *index*")})
	f.on("conversations.history", func(map[string]any) (map[string]any, error) { return apiError("channel_not_found") })

	res := mustSearch(t, f, options(t, KindMessages, "hello"))
	if len(res.Messages) != 1 || !strings.Contains(res.Messages[0].Content, "index") || res.Messages[0].Author.UserID != "U11111111" {
		t.Fatalf("messages = %+v, want the match rendered rather than dropped", res.Messages)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "1 message(s) could not be fetched in full") || !strings.Contains(res.Warnings[0], "channel_not_found") {
		t.Errorf("warnings = %q", res.Warnings)
	}
}

func TestSearchMessagesAuthErrorIsNotSwallowed(t *testing.T) {
	f := newFake(t)
	f.searchPages("search.messages", 1, []any{match(general, jan1, "a")})
	f.on("conversations.history", func(map[string]any) (map[string]any, error) { return apiError("invalid_auth") })

	_, err := Search(t.Context(), f.client(), options(t, KindMessages, "hello"))
	if !slackapi.IsAuthError(err) {
		t.Fatalf("err = %v, want the auth error so that credentials get refreshed", err)
	}
}

func TestSearchMessagesRateLimitedMidWayReturnsWhatItHas(t *testing.T) {
	f := newFake(t)
	f.on("search.messages", func(p map[string]any) (map[string]any, error) {
		if p["page"] == 1 {
			return map[string]any{"messages": map[string]any{
				"matches": []any{match(general, jan1, "a")},
				"paging":  map[string]any{"pages": 3, "total": 3},
			}}, nil
		}
		return nil, &slackapi.RateLimitError{Method: "search.messages"}
	})
	f.history(message(jan1, "a"))

	opts := options(t, KindMessages, "hello")
	opts.Limit = 2
	// Page size 2, one match per page: a second page is needed.
	res := mustSearch(t, f, opts)
	if len(res.Messages) != 1 {
		t.Fatalf("messages = %d, want the first page", len(res.Messages))
	}
	if res.MoreMessages == nil || !strings.Contains(res.MoreMessages.Hint, "rate limited") {
		t.Errorf("more = %+v, want the rate limit mentioned", res.MoreMessages)
	}

	// On the first page there is nothing to return: it is an error.
	f.on("search.messages", func(map[string]any) (map[string]any, error) {
		return nil, &slackapi.RateLimitError{Method: "search.messages"}
	})
	if _, err := Search(t.Context(), f.client(), opts); !isRateLimit(err) {
		t.Errorf("err = %v, want the rate limit error", err)
	}
}

func TestSearchRefusesBotTokensUpFront(t *testing.T) {
	f := newFake(t)
	bot := slackapi.New(slackapi.Auth{Type: slackapi.AuthStandard, Token: "xoxb-1"}, "", slackapi.WithHandler(f.handle))

	_, err := Search(t.Context(), bot, options(t, KindAll, "hello"))
	if !errors.Is(err, errSearchNotAllowed) || !strings.Contains(err.Error(), "--channel") {
		t.Fatalf("err = %v, want the actionable bot-token error", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("calls = %v, want none", f.calls)
	}

	// Scanning channels does work with a bot token.
	f.history(message(jan1, "hello there"))
	opts := options(t, KindMessages, "hello")
	opts.Channels = []string{general}
	res, err := Search(t.Context(), bot, opts)
	if err != nil || len(res.Messages) != 1 {
		t.Fatalf("scan with a bot token: %v, %+v", err, res)
	}
	// Without a workspace URL there is no permalink to build.
	if res.Messages[0].Permalink != "" {
		t.Errorf("permalink = %q", res.Messages[0].Permalink)
	}
}

func TestSearchExplainsNotAllowedTokenType(t *testing.T) {
	f := newFake(t)
	f.on("search.files", func(map[string]any) (map[string]any, error) { return apiError("not_allowed_token_type") })
	_, err := Search(t.Context(), f.client(), options(t, KindFiles, "hello"))
	if !errors.Is(err, errSearchNotAllowed) {
		t.Fatalf("err = %v", err)
	}
}

// Port of "searchSlack returns shared referenced_users while preserving
// canonical ids".
func TestScanMessagesReferencedUsers(t *testing.T) {
	f := newFake(t)
	f.history(map[string]any{
		"ts":        "1.000001",
		"text":      "hello <@U22222222>",
		"user":      "U11111111",
		"reactions": []any{map[string]any{"name": "eyes", "users": []any{"U11111111", "U44444444"}}},
	})
	names := map[string]string{"U11111111": "alice", "U22222222": "bob", "U44444444": "carol"}
	f.on("users.info", func(p map[string]any) (map[string]any, error) {
		id := p["user"].(string)
		return map[string]any{"user": map[string]any{"id": id, "name": names[id], "profile": map[string]any{"display_name": strings.ToUpper(names[id][:1]) + names[id][1:]}}}, nil
	})

	opts := options(t, KindMessages, "hello")
	opts.Channels, opts.ResolveUsers = []string{general}, true
	res := mustSearch(t, f, opts)

	if len(res.Messages) != 1 {
		t.Fatalf("messages = %+v", res.Messages)
	}
	if got := res.Messages[0]; got.Author.UserID != "U11111111" || !strings.Contains(got.Content, "@U22222222") {
		t.Errorf("message = %+v", got)
	}
	want := map[string]users.CompactUser{
		"U11111111": {ID: "U11111111", Name: "alice", DisplayName: "Alice"},
		"U22222222": {ID: "U22222222", Name: "bob", DisplayName: "Bob"},
	}
	if !reflect.DeepEqual(res.ReferencedUsers, want) {
		t.Errorf("referenced_users = %+v, want %+v (reactors are not referenced)", res.ReferencedUsers, want)
	}
	// No query is built, hence no channel lookup, when scanning.
	if n := len(f.callsTo("conversations.info")); n != 0 {
		t.Errorf("conversations.info calls = %d", n)
	}
}

func TestScanMessagesDoesNotResolveUsersUnlessAsked(t *testing.T) {
	f := newFake(t)
	f.history(message(jan1, "hello"))
	opts := options(t, KindMessages, "hello")
	opts.Channels, opts.Limit = []string{general}, 1 // the TS resolved users whenever the limit was reached
	res := mustSearch(t, f, opts)
	if res.ReferencedUsers != nil || len(f.callsTo("users.info")) != 0 {
		t.Errorf("referenced_users = %v, users.info calls = %d", res.ReferencedUsers, len(f.callsTo("users.info")))
	}
}

func TestScanMessagesReadsEveryPage(t *testing.T) {
	f := newFake(t)
	f.on("conversations.history", func(p map[string]any) (map[string]any, error) {
		switch {
		case p["latest"] != nil:
			return map[string]any{"messages": []any{}}, nil
		case p["cursor"] == nil:
			return map[string]any{
				"messages":          []any{message(jan1+2, "deploy one"), message(jan1+1, "noise")},
				"has_more":          true,
				"response_metadata": map[string]any{"next_cursor": "c1"},
			}, nil
		}
		// No cursor this time: the scan continues from the last ts.
		return map[string]any{"messages": []any{message(jan1, "deploy two")}, "has_more": true}, nil
	})

	opts := options(t, KindMessages, "deploy")
	opts.Channels = []string{general}
	res := mustSearch(t, f, opts)

	if got, want := messageTS(res), []string{ts(jan1 + 2), ts(jan1)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("messages = %v, want %v (the TS stopped after the first page)", got, want)
	}
	calls := f.callsTo("conversations.history")
	if len(calls) != 3 || calls[1]["cursor"] != "c1" || calls[2]["latest"] != ts(jan1) || calls[0]["limit"] != historyPageSize {
		t.Errorf("history calls = %v", calls)
	}
	if res.MoreMessages != nil {
		t.Errorf("more = %+v, want none: the whole channel was scanned", res.MoreMessages)
	}
	if want := "https://test.slack.com/archives/C12345678/p1767268802000100"; res.Messages[0].Permalink != want {
		t.Errorf("permalink = %q, want %q", res.Messages[0].Permalink, want)
	}
}

func TestScanMessagesWindowIsAppliedBySlack(t *testing.T) {
	f := newFake(t)
	f.history()
	opts := options(t, KindMessages, "x")
	opts.Channels, opts.After, opts.Before = []string{general}, "2026-01-01", "2026-01-03"
	mustSearch(t, f, opts)

	got := f.callsTo("conversations.history")[0]
	// Exclusive days: only 2026-01-02 (UTC) is searched.
	if got["oldest"] != "1767312000.000000" || got["latest"] != "1767398400.000000" {
		t.Errorf("oldest/latest = %v/%v", got["oldest"], got["latest"])
	}
}

func TestScanMessagesMergesChannelsNewestFirst(t *testing.T) {
	f := newFake(t)
	const other = "C87654321"
	f.on("conversations.history", func(p map[string]any) (map[string]any, error) {
		if p["channel"] == general {
			return map[string]any{"messages": []any{message(jan1+3*day, "x"), message(jan1+day, "x")}}, nil
		}
		return map[string]any{"messages": []any{message(jan1+4*day, "x"), message(jan1+2*day, "x")}}, nil
	})

	opts := options(t, KindMessages, "x")
	opts.Channels, opts.Limit = []string{general, other, general}, 3
	res := mustSearch(t, f, opts)

	if got, want := messageTS(res), []string{ts(jan1 + 4*day), ts(jan1 + 3*day), ts(jan1 + 2*day)}; !reflect.DeepEqual(got, want) {
		t.Errorf("messages = %v, want %v (not the first channel's messages first)", got, want)
	}
	if res.Messages[0].ChannelID != other {
		t.Errorf("first channel = %s", res.Messages[0].ChannelID)
	}
	if n := len(f.callsTo("conversations.history")); n != 2 {
		t.Errorf("history calls = %d, want one per distinct channel", n)
	}
	if res.MoreMessages == nil || res.MoreMessages.Before != "2026-01-04" {
		t.Errorf("more = %+v, want before 2026-01-04", res.MoreMessages)
	}
}

func TestScanMessagesFilters(t *testing.T) {
	f := newFake(t)
	f.files["https://files.slack.com/files/F1"] = "png"
	f.history(
		message(jan1+5, "Smoke tests FAILED again"),
		message(jan1+4, "tests of smoke detectors"),
		map[string]any{"ts": ts(jan1 + 3), "text": "smoke tests failed", "user": "W12345678"},
		withFile(message(jan1+2, "failed smoke tests screenshot"), slackFile("F1", "image/png")),
	)
	run := func(query string, mod func(*Options)) []string {
		opts := options(t, KindMessages, query)
		opts.Channels = []string{general}
		if mod != nil {
			mod(&opts)
		}
		return messageTS(mustSearch(t, f, opts))
	}

	// Every word must appear, in any order and any case, as with Slack.
	if got, want := run("smoke failed", nil), []string{ts(jan1 + 5), ts(jan1 + 3), ts(jan1 + 2)}; !reflect.DeepEqual(got, want) {
		t.Errorf("words: %v, want %v", got, want)
	}
	if got, want := run(`"smoke tests failed"`, nil), []string{ts(jan1 + 5), ts(jan1 + 3)}; !reflect.DeepEqual(got, want) {
		t.Errorf("phrase: %v, want %v", got, want)
	}
	if got := run("", nil); len(got) != 4 {
		t.Errorf("empty query: %v, want everything", got)
	}
	// A user ID is used as-is: no directory scan.
	if got, want := run("smoke", func(o *Options) { o.User = "W12345678" }), []string{ts(jan1 + 3)}; !reflect.DeepEqual(got, want) {
		t.Errorf("user: %v, want %v", got, want)
	}
	if n := len(f.callsTo("users.list")); n != 0 {
		t.Errorf("users.list calls = %d", n)
	}
	if got, want := run("smoke", func(o *Options) { o.ContentType = ContentImage }), []string{ts(jan1 + 2)}; !reflect.DeepEqual(got, want) {
		t.Errorf("image: %v, want %v", got, want)
	}
	if got := run("smoke", func(o *Options) { o.ContentType = ContentText }); len(got) != 3 {
		t.Errorf("text: %v, want the three messages without files", got)
	}
}

func TestScanRejectsWhatItCannotEvaluate(t *testing.T) {
	f := newFake(t)
	opts := options(t, KindAll, "deploy from:@bob")
	opts.Channels = []string{general}
	_, err := Search(t.Context(), f.client(), opts)
	if err == nil || !strings.Contains(err.Error(), `"from:@bob"`) || !strings.Contains(err.Error(), "--user") {
		t.Fatalf("err = %v, want the operator refused with the alternative", err)
	}

	// The user is in neither the search index nor the directory.
	f.on("search.messages", func(map[string]any) (map[string]any, error) {
		return map[string]any{"messages": map[string]any{"matches": []any{}}}, nil
	})
	f.on("users.list", func(map[string]any) (map[string]any, error) { return map[string]any{"members": []any{}}, nil })
	opts = options(t, KindMessages, "deploy")
	opts.Channels, opts.User = []string{general}, "nobody"
	_, err = Search(t.Context(), f.client(), opts)
	var notFound *users.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v, want an unknown user to fail rather than disable the filter", err)
	}
}

func TestMessageDownloads(t *testing.T) {
	f := newFake(t)
	f.files["https://files.slack.com/files/F1"] = "hello"
	shared := slackFile("F1", "text/plain")
	missing := slackFile("F2", "image/png")
	external := map[string]any{"id": "F3", "name": "doc", "url_private": "https://docs.google.com/document/d/1", "url_private_download": "https://docs.google.com/document/d/1"}
	f.history(
		withFile(message(jan1+3, "x one"), shared),
		withFile(message(jan1+2, "x two"), shared),
		withFile(message(jan1+1, "x three"), missing),
		withFile(message(jan1, "x four"), external),
	)
	opts := options(t, KindMessages, "x")
	opts.Channels = []string{general}
	res := mustSearch(t, f, opts)

	if len(f.gets) != 2 {
		t.Errorf("downloads = %v, want F1 once and F2 (credentials never go to docs.google.com)", f.gets)
	}
	one, two, three, four := res.Messages[0].Files[0], res.Messages[1].Files[0], res.Messages[2].Files[0], res.Messages[3].Files[0]
	if !strings.HasSuffix(one.Path, "/F1.txt") || one.Path != two.Path || one.Error != "" {
		t.Errorf("shared file = %+v / %+v", one, two)
	}
	if three.Error != "Failed to download file (404)" || !strings.HasSuffix(three.Path, "/F2.download-error.txt") || three.Mimetype != "image/png" {
		t.Errorf("failed file = %+v", three)
	}
	if !strings.Contains(four.Error, "Refusing to send Slack credentials") {
		t.Errorf("external file = %+v", four)
	}
	if len(res.Warnings) != 2 {
		t.Errorf("warnings = %q, want one per failed download", res.Warnings)
	}

	// --no-download: the files are listed, nothing is fetched.
	f.gets = nil
	opts.NoDownload = true
	res = mustSearch(t, f, opts)
	if len(f.gets) != 0 {
		t.Errorf("downloads with NoDownload = %v", f.gets)
	}
	if got := res.Messages[2].Files[0]; got.Name != "F2-name" || got.Path != "" || got.Error != "" {
		t.Errorf("file with NoDownload = %+v", got)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("warnings = %q", res.Warnings)
	}
}

func TestMaxContentChars(t *testing.T) {
	f := newFake(t)
	long := strings.Repeat("x", DefaultMaxContentChars+10)
	f.history(message(jan1, long))
	for _, tc := range []struct {
		max  int
		want string
	}{
		{0, long[:DefaultMaxContentChars] + "\n…"},
		{-1, long},
		{-5, long},
		{3, "xxx\n…"},
	} {
		opts := options(t, KindMessages, "x")
		opts.Channels, opts.MaxContentChars = []string{general}, tc.max
		if got := mustSearch(t, f, opts).Messages[0].Content; got != tc.want {
			t.Errorf("MaxContentChars %d: content has %d chars, want %d", tc.max, len(got), len(tc.want))
		}
	}
}

func TestSearchValidatesOptions(t *testing.T) {
	f := newFake(t)
	for name, mod := range map[string]func(*Options){
		"kind":         func(o *Options) { o.Kind = "everything" },
		"limit low":    func(o *Options) { o.Limit = -1 },
		"limit high":   func(o *Options) { o.Limit = MaxLimit + 1 },
		"content type": func(o *Options) { o.ContentType = "video" },
		"date":         func(o *Options) { o.After = "2026-02-30" },
	} {
		opts := options(t, KindMessages, "x")
		mod(&opts)
		if _, err := Search(t.Context(), f.client(), opts); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("calls = %v, want none before validation", f.calls)
	}
}
