package messages

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/maelvls/slackcli/internal/output"
	"github.com/maelvls/slackcli/internal/slack/files"
)

func message(fs ...FileSummary) Message {
	return Message{ChannelID: "C123", TS: "1234567890.000001", Text: "hello", Markdown: "hello", Files: fs}
}

func TestToCompactFiles(t *testing.T) {
	png := FileSummary{ID: "F1", Name: "diagram.png", Mimetype: "image/png", URLPrivate: "https://example.com/f1"}
	snippet := FileSummary{ID: "F2", Mimetype: "text/plain", Mode: "snippet", URLPrivate: "https://example.com/f2"}
	ok := files.DownloadResult{OK: true, Path: "/tmp/F1.png"}
	failed404 := files.DownloadResult{Error: "Failed to download file (404)", HTTPStatus: 404, Path: "/tmp/F1.download-error.txt"}
	failed401 := files.DownloadResult{Error: "Failed to download file (401)", HTTPStatus: 401, Path: "/tmp/F2.download-error.txt"}

	tests := []struct {
		name      string
		files     []FileSummary
		downloads map[string]files.DownloadResult
		want      []CompactFile
	}{
		{"path on successful download", []FileSummary{png}, map[string]files.DownloadResult{"F1": ok},
			[]CompactFile{{Name: "diagram.png", Mimetype: "image/png", Path: "/tmp/F1.png"}}},
		{"error on failed download", []FileSummary{png}, map[string]files.DownloadResult{"F1": failed404},
			[]CompactFile{{Name: "diagram.png", Mimetype: "image/png", Path: "/tmp/F1.download-error.txt", Error: "Failed to download file (404)"}}},
		{"no entry, no file", []FileSummary{png}, map[string]files.DownloadResult{}, nil},
		{"nil downloads", []FileSummary{png}, nil, nil},
		{"mixed", []FileSummary{png, snippet}, map[string]files.DownloadResult{"F1": ok, "F2": failed401},
			[]CompactFile{
				{Name: "diagram.png", Mimetype: "image/png", Path: "/tmp/F1.png"},
				{Mimetype: "text/plain", Mode: "snippet", Path: "/tmp/F2.download-error.txt", Error: "Failed to download file (401)"},
			}},
		// Content-type filtering relies on the metadata of failed downloads.
		{"failed download keeps metadata", []FileSummary{{ID: "F1", Mimetype: "image/png"}}, map[string]files.DownloadResult{"F1": failed404},
			[]CompactFile{{Mimetype: "image/png", Path: "/tmp/F1.download-error.txt", Error: "Failed to download file (404)"}}},
		{"name does not fall back to title", []FileSummary{{ID: "F2", Title: "My Document", Mimetype: "text/plain"}},
			map[string]files.DownloadResult{"F2": {OK: true, Path: "/tmp/F2/doc.txt"}},
			[]CompactFile{{Mimetype: "text/plain", Path: "/tmp/F2/doc.txt"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToCompact(message(tt.files...), CompactOptions{Downloads: tt.downloads})
			if !reflect.DeepEqual(got.Files, tt.want) {
				t.Errorf("files = %+v, want %+v", got.Files, tt.want)
			}
		})
	}
}

func TestToCompactReactions(t *testing.T) {
	msg := message()
	msg.Reactions = []any{map[string]any{"name": "rocket", "users": []any{"U12345678", "U87654321"}, "count": float64(2)}}

	if got := ToCompact(msg, CompactOptions{}); got.Reactions != nil {
		t.Errorf("reactions included by default: %+v", got.Reactions)
	}
	got := ToCompact(msg, CompactOptions{IncludeReactions: true})
	want := []CompactReaction{{Name: "rocket", Users: []string{"U12345678", "U87654321"}}}
	if !reflect.DeepEqual(got.Reactions, want) {
		t.Errorf("reactions = %+v", got.Reactions)
	}

	// W-prefixed user IDs are kept, non-users dropped; count then says how
	// many reacted in total.
	msg.Reactions = []any{map[string]any{"name": "eyes", "users": []any{"W12345678", "B12345678"}, "count": float64(2)}}
	got = ToCompact(msg, CompactOptions{IncludeReactions: true})
	want = []CompactReaction{{Name: "eyes", Users: []string{"W12345678"}, Count: 2}}
	if !reflect.DeepEqual(got.Reactions, want) {
		t.Errorf("reactions = %+v", got.Reactions)
	}
}

func TestCompactReactions(t *testing.T) {
	if got := CompactReactions(nil); got != nil {
		t.Errorf("nil → %+v", got)
	}
	got := CompactReactions([]any{
		"junk",
		map[string]any{"name": "  ", "users": []any{"U12345678"}},
		map[string]any{"name": " tada ", "users": []any{"U12345678", 42}},
		map[string]any{"name": "no_users", "count": float64(3)},
	})
	want := []CompactReaction{
		{Name: "tada", Users: []string{"U12345678"}},
		{Name: "no_users", Users: []string{}, Count: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestToCompactForwardedThreads(t *testing.T) {
	msg := message()
	msg.Text = "outer"
	msg.Attachments = []any{map[string]any{
		"is_share":    true,
		"reply_count": float64(4),
		"from_url":    "https://example.slack.com/archives/C222/p333?thread_ts=1771564510.386389&cid=C222",
	}}
	got := ToCompact(msg, CompactOptions{}).ForwardedThreads
	want := []ForwardedThread{{
		URL:      "https://example.slack.com/archives/C222/p333?thread_ts=1771564510.386389&cid=C222",
		ThreadTS: "1771564510.386389", ChannelID: "C222", ReplyCount: 4,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("forwarded threads = %+v", got)
	}
}

func TestExtractForwardedThreads(t *testing.T) {
	threadURL := "https://example.slack.com/archives/C222/p333?thread_ts=1771564510.386389"
	got := ExtractForwardedThreads([]any{
		"junk",
		map[string]any{"text": "no from_url"},
		map[string]any{"from_url": "https://example.slack.com/archives/C222/p333"},             // not in a thread
		map[string]any{"from_url": "https://example.slack.com/archives/C222/p333?thread_ts=x"}, // bad ts
		map[string]any{"from_url": "://bad url?thread_ts=1771564510.386389"},
		map[string]any{"from_url": " " + threadURL + " "},
		map[string]any{"from_url": threadURL}, // duplicate
	})
	want := []ForwardedThread{{URL: threadURL, ThreadTS: "1771564510.386389"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := ExtractForwardedThreads(nil); got != nil {
		t.Errorf("nil → %+v", got)
	}
}

func TestToCompactThreadAndAuthor(t *testing.T) {
	tests := []struct {
		name       string
		msg        Message
		wantThread string
		wantAuthor *Author
	}{
		{"plain message", Message{TS: "5.000001"}, "", nil},
		{"reply", Message{TS: "5.000001", ThreadTS: "1.000001", User: "U11111111"}, "1.000001", &Author{UserID: "U11111111"}},
		{"root with replies but no thread_ts", Message{TS: "5.000001", ReplyCount: 3, BotID: "B11111111"}, "5.000001", &Author{BotID: "B11111111"}},
		{"bot user", Message{TS: "5.000001", User: "U11111111", BotID: "B11111111"}, "", &Author{UserID: "U11111111", BotID: "B11111111"}},
	}
	for _, tt := range tests {
		got := ToCompact(tt.msg, CompactOptions{})
		if got.ThreadTS != tt.wantThread || !reflect.DeepEqual(got.Author, tt.wantAuthor) {
			t.Errorf("%s: thread_ts = %q, author = %+v", tt.name, got.ThreadTS, got.Author)
		}
	}
}

func TestToCompactTruncatesContent(t *testing.T) {
	msg := message()
	msg.Text = strings.Repeat("a", 9000)
	if got := ToCompact(msg, CompactOptions{}).Content; got != strings.Repeat("a", DefaultMaxBodyChars)+"\n…" {
		t.Errorf("default: %d bytes", len(got))
	}
	if got := ToCompact(msg, CompactOptions{MaxBodyChars: 10}).Content; got != "aaaaaaaaaa\n…" {
		t.Errorf("limit 10: %q", got)
	}
	if got := ToCompact(msg, CompactOptions{MaxBodyChars: -1}).Content; len(got) != 9000 {
		t.Errorf("unlimited: %d bytes", len(got))
	}
	if got := ToCompact(msg, CompactOptions{MaxBodyChars: 9000}).Content; len(got) != 9000 {
		t.Errorf("exact fit: %d bytes", len(got))
	}

	// Characters, not bytes: nothing is cut in half.
	msg.Text = "héllo wörld ✅ done"
	got := ToCompact(msg, CompactOptions{MaxBodyChars: 13}).Content
	if got != "héllo wörld ✅\n…" || !utf8.ValidString(got) {
		t.Errorf("multi-byte: %q", got)
	}
}

func TestCompactMessageJSON(t *testing.T) {
	msg := Message{
		ChannelID: "C123", TS: "5.000001", User: "U11111111", Text: "hello", ReplyCount: 2,
		Files:     []FileSummary{{ID: "F1", Name: "a.png", Mimetype: "image/png"}},
		Reactions: []any{map[string]any{"name": "eyes", "users": []any{"U22222222"}, "count": float64(1)}},
	}
	compact := ToCompact(msg, CompactOptions{
		IncludeReactions: true,
		Downloads:        map[string]files.DownloadResult{"F1": {OK: true, Path: "/tmp/F1.png"}},
	})
	got, err := output.Marshal(compact)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "channel_id": "C123",
  "ts": "5.000001",
  "thread_ts": "5.000001",
  "author": {
    "user_id": "U11111111"
  },
  "content": "hello",
  "files": [
    {
      "name": "a.png",
      "mimetype": "image/png",
      "path": "/tmp/F1.png"
    }
  ],
  "reactions": [
    {
      "name": "eyes",
      "users": [
        "U22222222"
      ]
    }
  ]
}`
	if string(got) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}
