package search

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/users"
	"github.com/maelvls/slackcli/internal/slackid"
)

const dayLayout = "2006-01-02"

// errSearchNotAllowed replaces Slack's not_allowed_token_type.
var errSearchNotAllowed = errors.New("Slack's search API cannot be called with a bot token (xoxb-). " +
	"Either pass --channel <channel> (repeatable) to scan specific channels instead, " +
	"or use a user token (xoxp-) or browser credentials (`" + appinfo.Name + " auth import-desktop`)")

// dayRange is the --after/--before window. Both days are exclusive, as
// Slack's after: and before: operators are.
type dayRange struct {
	// after and before are the validated days, "" when unset.
	after, before string
	// oldest is the first instant in the window (the start of the day
	// following after) and latest the first instant past it (the start of
	// the before day); zero when unbounded.
	oldest, latest time.Time
}

func parseDayRange(after, before string, loc *time.Location) (dayRange, error) {
	var r dayRange
	if strings.TrimSpace(after) != "" {
		day, err := parseDay("after", after, loc)
		if err != nil {
			return r, err
		}
		r.after, r.oldest = day.Format(dayLayout), day.AddDate(0, 0, 1)
	}
	if strings.TrimSpace(before) != "" {
		day, err := parseDay("before", before, loc)
		if err != nil {
			return r, err
		}
		r.before, r.latest = day.Format(dayLayout), day
	}
	if r.after != "" && r.before != "" && !r.oldest.Before(r.latest) {
		return r, fmt.Errorf("--after %s and --before %s leave no day in between: both are exclusive (results are dated strictly after the --after day and strictly before the --before day)", r.after, r.before)
	}
	return r, nil
}

func parseDay(flag, value string, loc *time.Location) (time.Time, error) {
	day, err := time.ParseInLocation(dayLayout, strings.TrimSpace(value), loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --%s date %q: expected a calendar day as YYYY-MM-DD, e.g. 2026-01-31", flag, value)
	}
	return day, nil
}

// handleRe matches what can go straight into from:@…: a Slack handle.
// Anything else (an email address, a display name with spaces) has to be
// resolved first, and must not be spliced into the query.
var handleRe = regexp.MustCompile(`^@?[A-Za-z0-9][A-Za-z0-9._-]*$`)

// buildQuery assembles the Slack search query from the query text and
// the filters.
func (s *searcher) buildQuery(ctx context.Context) (string, error) {
	var parts []string
	if q := strings.TrimSpace(s.opts.Query); q != "" {
		parts = append(parts, q)
	}
	if s.days.after != "" {
		parts = append(parts, "after:"+s.days.after)
	}
	if s.days.before != "" {
		parts = append(parts, "before:"+s.days.before)
	}
	if user := strings.TrimSpace(s.opts.User); user != "" {
		token, err := s.fromToken(ctx, user)
		if err != nil {
			return "", err
		}
		parts = append(parts, token)
	}
	if len(parts) == 0 {
		return "", errors.New("the search query is empty: pass search terms, or filter with --user, --after, --before or --channel")
	}
	return strings.Join(parts, " "), nil
}

// fromToken returns the from: operator for a user reference.
func (s *searcher) fromToken(ctx context.Context, user string) (string, error) {
	id := user
	if !slackid.IsUserID(user) {
		if handleRe.MatchString(user) {
			return "from:@" + strings.TrimPrefix(user, "@"), nil
		}
		var err error
		if id, err = users.ResolveUserID(ctx, s.client, user); err != nil {
			return "", err
		}
	}
	resp, err := s.client.API(ctx, "users.info", map[string]any{"user": id})
	if isFatal(ctx, err) {
		return "", fmt.Errorf("looking up user %s: %w", id, err)
	}
	if name := strings.TrimSpace(jsonx.GetStr(resp, "user", "name")); name != "" {
		return "from:@" + name, nil
	}
	// The handle is unknown (users.info failed or is not permitted):
	// filter by ID rather than drop the filter and return everybody's
	// messages.
	return "from:<@" + id + ">", nil
}

// operatorRe matches Slack search operators, which only Slack's search
// engine can evaluate.
var operatorRe = regexp.MustCompile(`(?i)^-?(in|from|to|with|before|after|on|during|has|is):.`)

// matcher is the local stand-in for Slack's search engine used when
// scanning channels: every term must appear in the text.
type matcher struct {
	// terms are lower-cased words and phrases.
	terms []string
}

// newMatcher splits a query into words and "quoted phrases". It refuses
// search operators: matching them as literal text would silently find
// nothing.
func newMatcher(query string) (matcher, error) {
	var (
		m       matcher
		cur     strings.Builder
		inQuote bool
		quoted  bool
	)
	flush := func() error {
		term := strings.TrimSpace(cur.String())
		cur.Reset()
		wasQuoted := quoted
		quoted = false
		if term == "" {
			return nil
		}
		if !wasQuoted && operatorRe.MatchString(term) {
			return fmt.Errorf("the search operator %q cannot be combined with --channel, which scans the channels and matches the text itself: "+
				"use --user, --after and --before instead, or drop --channel and write in:#channel in the query (put the term in double quotes to search for it literally)", term)
		}
		m.terms = append(m.terms, strings.ToLower(term))
		return nil
	}
	for _, r := range query {
		switch {
		case r == '"':
			inQuote = !inQuote
			quoted = true
		case unicode.IsSpace(r) && !inQuote:
			if err := flush(); err != nil {
				return m, err
			}
		default:
			cur.WriteRune(r)
		}
	}
	return m, flush()
}

// matches reports whether text contains every term, ignoring case. An
// empty query matches everything.
func (m matcher) matches(text string) bool {
	text = strings.ToLower(text)
	for _, term := range m.terms {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}
