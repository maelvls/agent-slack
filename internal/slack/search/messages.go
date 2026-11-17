package search

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/channels"
	"github.com/maelvls/slackcli/internal/slack/messages"
	"github.com/maelvls/slackcli/internal/slack/slacktext"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
)

const (
	// maxExamined bounds how many search matches one search fetches in
	// full while looking for --limit results that pass the content-type
	// filter: each costs at least one API call.
	maxExamined = 1000
	// historyPageSize is the conversations.history page size, as Slack
	// recommends.
	historyPageSize = 200
)

// hit is a message that matched, before downloads and compaction.
type hit struct {
	msg       messages.Message
	permalink string
}

// ref points at a search.messages match to fetch in full.
type ref struct {
	slackurl.MessageRef
	permalink string
	// raw is the match itself, used when the message cannot be fetched.
	raw map[string]any
}

// searchMessages finds messages with search.messages. Matches are fetched
// in full (the search index has no blocks and incomplete files) a few at
// a time, and pages are requested until limit messages pass the
// content-type filter.
func (s *searcher) searchMessages(ctx context.Context) ([]hit, *More, error) {
	p := &pager{client: s.client, method: "search.messages", key: "messages", query: s.query, size: min(s.limit, searchPageSize)}
	if s.contentType != ContentAny {
		// Some matches will be filtered out: ask for full pages.
		p.size = searchPageSize
	}

	var (
		hits     []hit
		pending  []ref
		seen     = map[string]bool{}
		examined int
		reason   string
	)
	for len(hits) < s.limit && reason == "" {
		if len(pending) == 0 {
			if p.done {
				break
			}
			matches, err := p.next(ctx)
			if err != nil {
				if len(hits) > 0 && isRateLimit(err) {
					reason = fmt.Sprintf("The search stopped early: %v", err)
					break
				}
				return nil, nil, err
			}
			if pending, err = s.refs(ctx, matches, seen); err != nil {
				return nil, nil, err
			}
			continue
		}
		if examined >= maxExamined {
			reason = fmt.Sprintf("The search stopped after examining the %d most recent matches.", examined)
			break
		}

		n := min(len(pending), s.limit-len(hits), maxExamined-examined)
		batch := pending[:n]
		pending = pending[n:]
		examined += n
		fetched, err := s.fetch(ctx, batch)
		if err != nil {
			return nil, nil, err
		}
		for i, msg := range fetched {
			if passesMessage(msg, s.contentType) {
				hits = append(hits, hit{msg: msg, permalink: batch[i].permalink})
			}
		}
	}

	if reason == "" && len(pending) == 0 && p.done {
		return hits, nil, nil
	}
	newest, oldest := "", ""
	if len(hits) > 0 {
		newest, oldest = hits[0].msg.TS, hits[len(hits)-1].msg.TS
	}
	return hits, s.more("messages", p.total, newest, oldest, reason), nil
}

// refs turns a page of search.messages matches into messages to fetch,
// skipping those already seen: page-based pagination repeats matches when
// new messages arrive between two pages.
func (s *searcher) refs(ctx context.Context, matches []map[string]any, seen map[string]bool) ([]ref, error) {
	var out []ref
	for _, m := range matches {
		ts := strings.TrimSpace(jsonx.Str(m["ts"]))
		if ts == "" {
			continue
		}
		channelID := jsonx.GetStr(m, "channel", "id")
		if channelID == "" {
			name := jsonx.GetStr(m, "channel", "name")
			if name == "" {
				continue
			}
			id, err := channels.ResolveID(ctx, s.client, "#"+name)
			if isFatal(ctx, err) {
				return nil, err
			}
			if err != nil {
				continue
			}
			channelID = id
		}
		if key := channelID + "/" + ts; seen[key] {
			continue
		} else {
			seen[key] = true
		}

		r := ref{
			MessageRef: slackurl.MessageRef{ChannelID: channelID, MessageTS: ts},
			permalink:  jsonx.Str(m["permalink"]),
			raw:        m,
		}
		// The permalink of a thread reply names its thread, which saves
		// FetchMessage from looking for it.
		if parsed, err := slackurl.ParseMessageURL(r.permalink); err == nil {
			r.ThreadTSHint = parsed.ThreadTSHint
		}
		r.Raw = r.permalink
		out = append(out, r)
	}
	return out, nil
}

// fetch loads the messages refs point at, a few at a time. A message that
// cannot be fetched (deleted meanwhile, rate limit …) is not dropped: it
// is rendered from the search match instead.
func (s *searcher) fetch(ctx context.Context, refs []ref) ([]messages.Message, error) {
	out := make([]messages.Message, len(refs))
	err := forEach(ctx, len(refs), fetchConcurrency, func(ctx context.Context, i int) error {
		msg, err := messages.FetchMessage(ctx, s.client, refs[i].MessageRef, false)
		switch {
		case err == nil:
			out[i] = msg
		case isFatal(ctx, err):
			return err
		default:
			out[i] = messages.ParseMessage(refs[i].ChannelID, refs[i].raw)
			s.mu.Lock()
			s.degraded++
			if s.degradedErr == nil {
				s.degradedErr = err
			}
			s.mu.Unlock()
		}
		return nil
	})
	return out, err
}

// scanMessages finds messages by reading the history of each channel and
// matching locally. Channels are scanned concurrently and their matches
// merged, newest first.
func (s *searcher) scanMessages(ctx context.Context) ([]hit, *More, error) {
	found := make([][]messages.Message, len(s.channelIDs))
	cut := make([]bool, len(s.channelIDs))
	err := forEach(ctx, len(s.channelIDs), scanConcurrency, func(ctx context.Context, i int) error {
		var err error
		found[i], cut[i], err = s.scanChannel(ctx, s.channelIDs[i])
		return err
	})
	if err != nil {
		return nil, nil, err
	}

	// Each channel contributed its limit newest matches, so the limit
	// newest of the union are the newest overall.
	all := slices.Concat(found...)
	slices.SortStableFunc(all, func(a, b messages.Message) int { return messages.CompareTS(b.TS, a.TS) })
	incomplete := slices.Contains(cut, true)
	if len(all) > s.limit {
		all, incomplete = all[:s.limit], true
	}

	hits := make([]hit, len(all))
	for i, msg := range all {
		hits[i].msg = msg
		if ws := s.client.WorkspaceURL(); ws != "" {
			hits[i].permalink = slackurl.BuildMessageURL(ws, msg.ChannelID, msg.TS, msg.ThreadTS)
		}
	}
	if !incomplete {
		return hits, nil, nil
	}
	newest, oldest := "", ""
	if len(all) > 0 {
		newest, oldest = all[0].TS, all[len(all)-1].TS
	}
	return hits, s.more("messages", 0, newest, oldest, ""), nil
}

// scanChannel returns the limit most recent messages of a channel that
// match, and whether the scan stopped before the end of the channel (or
// of the --after/--before window). Only top-level messages are scanned:
// thread replies are not part of a channel's history.
func (s *searcher) scanChannel(ctx context.Context, channelID string) (out []messages.Message, cut bool, err error) {
	// The window is applied by Slack, so that a search far in the past
	// does not page through everything posted since.
	base := map[string]any{"channel": channelID, "limit": historyPageSize}
	if !s.days.oldest.IsZero() {
		base["oldest"] = unixTS(s.days.oldest)
	}
	latest, cursor := "", ""
	if !s.days.latest.IsZero() {
		latest = unixTS(s.days.latest)
	}

	for page := 0; ; page++ {
		params := maps.Clone(base)
		if latest != "" {
			params["latest"] = latest
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		resp, err := s.client.API(ctx, "conversations.history", params)
		if err != nil {
			if page > 0 && isRateLimit(err) {
				s.warnf("the scan of channel %s stopped early, older messages were not searched: %v", channelID, err)
				return out, true, nil
			}
			return nil, false, fmt.Errorf("searching the history of channel %s: %w", channelID, err)
		}
		raw := jsonx.Maps(resp["messages"])
		hasMore := jsonx.Bool(resp["has_more"])
		for i, m := range raw {
			msg := messages.ParseMessage(channelID, m)
			if !s.matchesMessage(msg) {
				continue
			}
			out = append(out, msg)
			if len(out) >= s.limit {
				return out, hasMore || i < len(raw)-1, nil
			}
		}
		if len(raw) == 0 || !hasMore {
			return out, false, nil
		}

		// Next page: Slack's cursor when it gives one, else everything
		// older than the last (oldest) message of this page.
		if next := jsonx.GetStr(resp, "response_metadata", "next_cursor"); next != "" {
			if next == cursor {
				return out, false, nil
			}
			cursor = next
			continue
		}
		last := jsonx.Str(raw[len(raw)-1]["ts"])
		if last == "" || last == latest {
			return out, false, nil
		}
		latest, cursor = last, ""
	}
}

func (s *searcher) matchesMessage(msg messages.Message) bool {
	if s.userID != "" && msg.User != s.userID {
		return false
	}
	if !passesMessage(msg, s.contentType) {
		return false
	}
	return s.match.matches(slacktext.RenderMessageContent(map[string]any{
		"text":        msg.Text,
		"blocks":      msg.Blocks,
		"attachments": msg.Attachments,
	}))
}

// compactMessages downloads the files of the matched messages and
// converts them to the shape printed.
func (s *searcher) compactMessages(ctx context.Context, hits []hit) ([]Message, error) {
	var items []download
	for _, h := range hits {
		for _, f := range h.msg.Files {
			items = append(items, download{
				id:   f.ID,
				url:  cmp.Or(f.URLPrivateDownload, f.URLPrivate),
				name: downloadName(f.ID, f.Mimetype, f.Filetype, f.Name, f.Title),
			})
		}
	}
	results, err := s.download(ctx, items)
	if err != nil {
		return nil, err
	}

	maxChars := s.opts.MaxContentChars
	switch {
	case maxChars == 0:
		maxChars = DefaultMaxContentChars
	case maxChars < 0:
		maxChars = -1
	}
	out := make([]Message, len(hits))
	for i, h := range hits {
		c := messages.ToCompact(h.msg, messages.CompactOptions{MaxBodyChars: maxChars, Downloads: results})
		out[i] = Message{
			ChannelID:        c.ChannelID,
			TS:               c.TS,
			Author:           c.Author,
			Content:          c.Content,
			Files:            c.Files,
			ForwardedThreads: c.ForwardedThreads,
			Permalink:        h.permalink,
		}
	}
	return out, nil
}
