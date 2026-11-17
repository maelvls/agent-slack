// Package channeladmin creates Slack channels and invites people to them.
package channeladmin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// MaxNameLength is Slack's limit on channel names, in characters.
const MaxNameLength = 80

// Channel is a newly created channel.
type Channel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsPrivate bool   `json:"is_private"`
}

// Created is the result of Create, in the shape the CLI prints.
type Created struct {
	Channel Channel `json:"channel"`
}

// NormalizeName validates a channel name against Slack's rules and returns
// it without surrounding blanks or a leading "#". Names may only contain
// lowercase letters, digits, hyphens and underscores, and are at most
// MaxNameLength characters long. Invalid names are rejected, with a
// suggested fix, rather than silently rewritten.
func NormalizeName(input string) (string, error) {
	name := strings.TrimPrefix(strings.TrimSpace(input), "#")
	if name == "" {
		return "", errors.New("channel name is empty: pass --name, e.g. --name project-apollo")
	}
	valid := utf8.RuneCountInString(name) <= MaxNameLength
	for _, r := range name {
		if !validNameRune(r) {
			valid = false
		}
	}
	if valid {
		return name, nil
	}
	msg := fmt.Sprintf("invalid channel name %q: Slack channel names may only contain lowercase letters, digits, hyphens and underscores (no spaces or uppercase), and are at most %d characters long", input, MaxNameLength)
	if s := suggestName(name); s != "" {
		msg += fmt.Sprintf("; try --name %s", s)
	}
	return "", errors.New(msg)
}

func validNameRune(r rune) bool {
	if r == '-' || r == '_' {
		return true
	}
	// Letters of any script are allowed, as long as they are not
	// uppercase; marks are needed by scripts that combine them.
	return unicode.IsDigit(r) || unicode.IsMark(r) || (unicode.IsLetter(r) && !unicode.IsUpper(r) && !unicode.IsTitle(r))
}

var invalidRunsRe = regexp.MustCompile(`[^\p{L}\p{M}\p{Nd}_-]+`)

func suggestName(name string) string {
	s := strings.Trim(invalidRunsRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if r := []rune(s); len(r) > MaxNameLength {
		s = strings.TrimRight(string(r[:MaxNameLength]), "-")
	}
	return s
}

// Create creates a public channel, or a private one when private is true
// (conversations.create). The name is checked with NormalizeName first.
func Create(ctx context.Context, client *slackapi.Client, name string, private bool) (Created, error) {
	name, err := NormalizeName(name)
	if err != nil {
		return Created{}, err
	}
	resp, err := client.API(ctx, "conversations.create", map[string]any{
		"name":       name,
		"is_private": private,
	})
	if slackapi.IsErrorCode(err, "name_taken") {
		return Created{}, fmt.Errorf("a channel named %q already exists (possibly archived, or private and not visible to you); pick another --name: %w", name, err)
	}
	if err != nil {
		return Created{}, fmt.Errorf("creating channel %q: %w", name, err)
	}
	ch := jsonx.Map(resp["channel"])
	created := Channel{ID: jsonx.Str(ch["id"]), Name: jsonx.Str(ch["name"]), IsPrivate: private}
	if created.ID == "" || created.Name == "" {
		return Created{}, errors.New("conversations.create succeeded but returned no channel; check whether the channel now exists before retrying")
	}
	if p, ok := ch["is_private"].(bool); ok {
		created.IsPrivate = p
	}
	return Created{Channel: created}, nil
}

// ParseUsersCSV splits a comma-separated --users value into trimmed,
// de-duplicated, non-empty targets, keeping their order.
func ParseUsersCSV(input string) []string {
	var out []string
	for _, v := range strings.Split(input, ",") {
		if v = strings.TrimSpace(v); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// SplitEmails separates the invite targets that look like email addresses
// (de-duplicated case-insensitively, first spelling kept) from the others
// (user IDs, handles), both in input order.
func SplitEmails(targets []string) (emails, others []string) {
	seen := map[string]bool{}
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if !emailRe.MatchString(t) {
			others = append(others, t)
			continue
		}
		if key := strings.ToLower(t); !seen[key] {
			seen[key] = true
			emails = append(emails, t)
		}
	}
	return emails, others
}
