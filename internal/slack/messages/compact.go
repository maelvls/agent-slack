package messages

import (
	"net/url"
	"strings"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/files"
	"github.com/maelvls/slackcli/internal/slack/slacktext"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
	"github.com/maelvls/slackcli/internal/slackid"
)

// DefaultMaxBodyChars is the content length ToCompact truncates to when
// CompactOptions.MaxBodyChars is zero.
const DefaultMaxBodyChars = 8000

// CompactMessage is the token-efficient message shape the CLI prints.
type CompactMessage struct {
	ChannelID string `json:"channel_id"`
	TS        string `json:"ts"`
	// ThreadTS is set for replies and for roots that have replies.
	ThreadTS string  `json:"thread_ts,omitempty"`
	Author   *Author `json:"author,omitempty"`
	// Content is the message rendered as Markdown.
	Content          string            `json:"content,omitempty"`
	Files            []CompactFile     `json:"files,omitempty"`
	Reactions        []CompactReaction `json:"reactions,omitempty"`
	ForwardedThreads []ForwardedThread `json:"forwarded_threads,omitempty"`
}

// Author says who posted a message.
type Author struct {
	UserID string `json:"user_id,omitempty"`
	BotID  string `json:"bot_id,omitempty"`
}

// CompactFile is a downloaded (or failed to download) attachment.
type CompactFile struct {
	Name     string `json:"name,omitempty"`
	Mimetype string `json:"mimetype,omitempty"`
	Mode     string `json:"mode,omitempty"`
	// Path is the local file, or the error file when Error is set.
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
}

// CompactReaction is one emoji and who reacted with it.
type CompactReaction struct {
	Name  string   `json:"name"`
	Users []string `json:"users"`
	// Count is only set when it differs from len(Users), i.e. when Slack
	// truncated the user list.
	Count int `json:"count,omitempty"`
}

// ForwardedThread points at the thread of a message that was shared
// (forwarded) into this one.
type ForwardedThread struct {
	URL        string `json:"url"`
	ThreadTS   string `json:"thread_ts"`
	ChannelID  string `json:"channel_id,omitempty"`
	ReplyCount int    `json:"reply_count,omitempty"`
}

// CompactOptions tunes ToCompact.
type CompactOptions struct {
	// MaxBodyChars truncates Content to that many characters: 0 means
	// DefaultMaxBodyChars, a negative value means no limit.
	MaxBodyChars int
	// IncludeReactions adds the reactions.
	IncludeReactions bool
	// Downloads maps file IDs to download outcomes. Only files with an
	// entry are listed.
	Downloads map[string]files.DownloadResult
}

// ToCompact converts a fetched message to the shape the CLI prints.
func ToCompact(msg Message, opts CompactOptions) CompactMessage {
	out := CompactMessage{
		ChannelID: msg.ChannelID,
		TS:        msg.TS,
		ThreadTS:  msg.ThreadTS,
		Content: truncate(slacktext.RenderMessageContent(map[string]any{
			"text":        msg.Text,
			"blocks":      msg.Blocks,
			"attachments": msg.Attachments,
		}), opts.MaxBodyChars),
		ForwardedThreads: ExtractForwardedThreads(msg.Attachments),
	}
	if out.ThreadTS == "" && msg.ReplyCount > 0 {
		out.ThreadTS = msg.TS
	}
	if msg.User != "" || msg.BotID != "" {
		out.Author = &Author{UserID: msg.User, BotID: msg.BotID}
	}
	for _, f := range msg.Files {
		dl, ok := opts.Downloads[f.ID]
		if !ok {
			continue
		}
		file := CompactFile{Name: f.Name, Mimetype: f.Mimetype, Mode: f.Mode, Path: dl.Path}
		if !dl.OK {
			file.Error = dl.Error
		}
		out.Files = append(out.Files, file)
	}
	if opts.IncludeReactions {
		out.Reactions = CompactReactions(msg.Reactions)
	}
	return out
}

// truncate cuts s to limit characters (not bytes, so that no character is
// split) and marks the cut.
func truncate(s string, limit int) string {
	if limit == 0 {
		limit = DefaultMaxBodyChars
	}
	if limit < 0 || len(s) <= limit {
		return s
	}
	n := 0
	for i := range s {
		if n == limit {
			return s[:i] + "\n…"
		}
		n++
	}
	return s
}

// CompactReactions converts raw Slack reactions. Entries without a name
// are dropped, as are users that are not user IDs.
func CompactReactions(reactions []any) []CompactReaction {
	var out []CompactReaction
	for _, r := range jsonx.Maps(reactions) {
		name := strings.TrimSpace(jsonx.Str(r["name"]))
		if name == "" {
			continue
		}
		reaction := CompactReaction{Name: name, Users: []string{}}
		for _, u := range jsonx.Slice(r["users"]) {
			if id := jsonx.Str(u); slackid.IsUserID(id) {
				reaction.Users = append(reaction.Users, id)
			}
		}
		if count, ok := jsonx.Int(r["count"]); ok && count != len(reaction.Users) {
			reaction.Count = count
		}
		out = append(out, reaction)
	}
	return out
}

// ExtractForwardedThreads finds, among raw Slack attachments, the shared
// messages that belong to a thread (their from_url carries thread_ts), so
// that the reader knows there is a thread to fetch.
func ExtractForwardedThreads(attachments []any) []ForwardedThread {
	var out []ForwardedThread
	seen := map[string]bool{}
	for _, a := range jsonx.Maps(attachments) {
		fromURL := strings.TrimSpace(jsonx.Str(a["from_url"]))
		if fromURL == "" {
			continue
		}
		u, err := url.Parse(fromURL)
		if err != nil {
			continue
		}
		query := u.Query()
		threadTS := strings.TrimSpace(query.Get("thread_ts"))
		if !slackurl.IsTS(threadTS) {
			continue
		}
		key := fromURL + "::" + threadTS
		if seen[key] {
			continue
		}
		seen[key] = true
		replyCount, _ := jsonx.Int(a["reply_count"])
		out = append(out, ForwardedThread{
			URL:        fromURL,
			ThreadTS:   threadTS,
			ChannelID:  strings.TrimSpace(query.Get("cid")),
			ReplyCount: replyCount,
		})
	}
	return out
}
