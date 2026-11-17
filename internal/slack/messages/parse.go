// Package messages fetches Slack messages (one message, a channel's
// history, a thread) and turns them into the compact shape the CLI prints.
package messages

import (
	"cmp"
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/slacktext"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// FileSummary describes a file attached to a message.
type FileSummary struct {
	ID                 string `json:"id"`
	Name               string `json:"name,omitempty"`
	Title              string `json:"title,omitempty"`
	Mimetype           string `json:"mimetype,omitempty"`
	Filetype           string `json:"filetype,omitempty"`
	Mode               string `json:"mode,omitempty"`
	Permalink          string `json:"permalink,omitempty"`
	URLPrivate         string `json:"url_private,omitempty"`
	URLPrivateDownload string `json:"url_private_download,omitempty"`
	Size               int64  `json:"size,omitempty"`
	// Snippet is the inline content of a snippet, when files.info has it.
	Snippet *FileSnippet `json:"snippet,omitempty"`
}

// FileSnippet is the text of a snippet file.
type FileSnippet struct {
	Content  string `json:"content,omitempty"`
	Language string `json:"language,omitempty"`
}

// Message is a fetched Slack message. Blocks, Attachments and Reactions
// are the raw Slack payloads.
type Message struct {
	ChannelID string `json:"channel_id"`
	TS        string `json:"ts"`
	// ThreadTS is the thread root ts, for thread roots and replies.
	ThreadTS   string `json:"thread_ts,omitempty"`
	ReplyCount int    `json:"reply_count,omitempty"`
	User       string `json:"user,omitempty"`
	BotID      string `json:"bot_id,omitempty"`
	// Text is the raw mrkdwn text; Markdown is its Markdown conversion.
	Text        string        `json:"text"`
	Markdown    string        `json:"markdown"`
	Blocks      []any         `json:"blocks,omitempty"`
	Attachments []any         `json:"attachments,omitempty"`
	Files       []FileSummary `json:"files,omitempty"`
	Reactions   []any         `json:"reactions,omitempty"`
}

// ParseMessage converts a raw Slack message object. Files are summarised
// as-is: no files.info lookup is made (the Fetch functions do that).
func ParseMessage(channelID string, raw map[string]any) Message {
	text := jsonx.Str(raw["text"])
	replyCount, _ := jsonx.Int(raw["reply_count"])
	msg := Message{
		ChannelID:   channelID,
		TS:          jsonx.Str(raw["ts"]),
		ThreadTS:    jsonx.Str(raw["thread_ts"]),
		ReplyCount:  replyCount,
		User:        jsonx.Str(raw["user"]),
		BotID:       jsonx.Str(raw["bot_id"]),
		Text:        text,
		Markdown:    slacktext.MrkdwnToMarkdown(text),
		Blocks:      jsonx.Slice(raw["blocks"]),
		Attachments: jsonx.Slice(raw["attachments"]),
		Reactions:   jsonx.Slice(raw["reactions"]),
	}
	for _, f := range jsonx.Slice(raw["files"]) {
		if summary, ok := ParseFile(f); ok {
			msg.Files = append(msg.Files, summary)
		}
	}
	return msg
}

// ParseFile summarises a raw Slack file object. ok is false when v is not
// a file object with an ID.
func ParseFile(v any) (summary FileSummary, ok bool) {
	f := jsonx.Map(v)
	id := jsonx.Str(f["id"])
	if id == "" {
		return FileSummary{}, false
	}
	size, _ := jsonx.Num(f["size"])
	return FileSummary{
		ID:                 id,
		Name:               jsonx.Str(f["name"]),
		Title:              jsonx.Str(f["title"]),
		Mimetype:           jsonx.Str(f["mimetype"]),
		Filetype:           jsonx.Str(f["filetype"]),
		Mode:               jsonx.Str(f["mode"]),
		Permalink:          jsonx.Str(f["permalink"]),
		URLPrivate:         jsonx.Str(f["url_private"]),
		URLPrivateDownload: jsonx.Str(f["url_private_download"]),
		Size:               int64(size),
	}, true
}

// filesInfoConcurrency bounds the files.info lookups of enrichFiles.
const filesInfoConcurrency = 4

// needsInfo reports whether the file object embedded in a message is
// incomplete: snippets carry their content only in files.info, and files
// shared from elsewhere often come without a download URL.
func needsInfo(f FileSummary) bool { return f.Mode == "snippet" || f.URLPrivateDownload == "" }

// enrichFiles completes the files of msgs with files.info. Each file is
// looked up once however many messages share it; a failed lookup leaves
// the summary as it was.
func enrichFiles(ctx context.Context, client *slackapi.Client, msgs []Message) error {
	var ids []string
	infos := map[string]map[string]any{}
	for _, m := range msgs {
		for _, f := range m.Files {
			if _, seen := infos[f.ID]; needsInfo(f) && !seen {
				infos[f.ID] = nil
				ids = append(ids, f.ID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		fatal error
		sem   = make(chan struct{}, filesInfoConcurrency)
	)
	for _, id := range ids {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			resp, err := client.API(ctx, "files.info", map[string]any{"file": id})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				infos[id] = jsonx.Map(resp["file"])
			case isFatal(ctx, err):
				fatal = cmp.Or(fatal, err)
			}
		})
	}
	wg.Wait()
	if fatal != nil {
		return fatal
	}

	for i := range msgs {
		for j, f := range msgs[i].Files {
			if info := infos[f.ID]; info != nil {
				msgs[i].Files[j] = mergeFileInfo(f, info)
			}
		}
	}
	return nil
}

func mergeFileInfo(f FileSummary, info map[string]any) FileSummary {
	fill := func(dst *string, key string) {
		if *dst == "" {
			*dst = jsonx.Str(info[key])
		}
	}
	fill(&f.Name, "name")
	fill(&f.Title, "title")
	fill(&f.Mimetype, "mimetype")
	fill(&f.Filetype, "filetype")
	fill(&f.Mode, "mode")
	fill(&f.Permalink, "permalink")
	fill(&f.URLPrivate, "url_private")
	fill(&f.URLPrivateDownload, "url_private_download")
	if content := jsonx.Str(info["content"]); strings.TrimSpace(content) != "" {
		f.Snippet = &FileSnippet{Content: content, Language: jsonx.Str(info["filetype"])}
	}
	return f
}

// isFatal reports whether err must abort the whole fetch rather than be
// treated as "this lookup found nothing": the command was cancelled, the
// credentials are dead (the caller refreshes them and retries), or Slack
// is rate limiting us.
func isFatal(ctx context.Context, err error) bool {
	var rl *slackapi.RateLimitError
	return err != nil && (ctx.Err() != nil || slackapi.IsAuthError(err) || errors.As(err, &rl))
}
