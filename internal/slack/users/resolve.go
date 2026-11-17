package users

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slackapi"
	"github.com/maelvls/slackcli/internal/slackid"
)

// scanPageSize is the users.list page size for directory scans. users.list
// is rate limited per call (about 20 a minute), so the largest page Slack
// accepts scans a big workspace five times faster than the recommended
// 200.
const scanPageSize = 1000

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// NotFoundError says that no workspace member matches Input.
type NotFoundError struct {
	Input string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf(`could not resolve user %q: no member of this workspace has that handle, email or display name. `+
		`Pass a user ID (U…), an email address or a @handle; "%s user list" shows them.`, e.Input, appinfo.Name)
}

// AmbiguousError says that Input is the display name of several members.
type AmbiguousError struct {
	Input string
	// Matches are the candidates.
	Matches []CompactUser
}

func (e *AmbiguousError) Error() string {
	var who []string
	for _, u := range e.Matches {
		who = append(who, fmt.Sprintf("%s (@%s)", u.ID, u.Name))
	}
	return fmt.Sprintf("user %q is ambiguous: %d members have that name: %s. Pass one of these user IDs instead.",
		e.Input, len(e.Matches), strings.Join(who, ", "))
}

// ResolveUserID turns a user reference into a user ID. It accepts, in
// order of cost:
//
//   - a user ID (U…/W…): returned as-is, without any API call;
//   - an email address: one users.lookupByEmail call;
//   - a handle, with or without "@", or failing that the exact display
//     name or real name of a single member: a scan of the user directory
//     (users.list), which is slow in large workspaces.
//
// Matching is case-insensitive, and active members win over deactivated
// ones. The error is a *NotFoundError when nobody matches and an
// *AmbiguousError when a display name designates several members.
func ResolveUserID(ctx context.Context, client *slackapi.Client, input string) (string, error) {
	found, err := lookup(ctx, client, []string{input})
	if err != nil {
		return "", err
	}
	return found[0].id, nil
}

// found is a resolved user: its ID and, when the lookup came across it,
// the raw Slack user object.
type found struct {
	id  string
	raw map[string]any
}

// want is one user being searched for in the directory.
type want struct {
	index  int    // position in the lookup inputs
	input  string // as given, for errors
	handle string // lower-cased, without "@"
	email  string // lower-cased; "" unless the input is an email address

	active   map[string]any   // handle or email match
	inactive map[string]any   // same, but a deactivated member
	named    []map[string]any // active members with that display or real name
}

// lookup resolves every input, or fails on the first that cannot be. All
// the inputs that need the user directory share a single scan of it.
func lookup(ctx context.Context, client *slackapi.Client, inputs []string) ([]found, error) {
	out := make([]found, len(inputs))
	var wants []*want
	for i, input := range inputs {
		trimmed := strings.TrimSpace(input)
		if trimmed == "" {
			return nil, errors.New("user is empty: pass a user ID (U…), an email address or a @handle")
		}
		if slackid.IsUserID(trimmed) {
			out[i] = found{id: trimmed}
			continue
		}
		w := &want{index: i, input: input, handle: strings.ToLower(strings.TrimPrefix(trimmed, "@"))}
		if emailRe.MatchString(trimmed) {
			w.email = strings.ToLower(trimmed)
			user, err := lookupByEmail(ctx, client, trimmed)
			switch {
			case user != nil:
				out[i] = found{id: jsonx.Str(user["id"]), raw: user}
				continue
			case slackapi.IsErrorCode(err, "users_not_found"):
				// Slack's answer is authoritative: scanning the whole
				// directory would only find the same nothing, slowly.
				return nil, &NotFoundError{Input: input}
			case isFatal(ctx, err):
				return nil, fmt.Errorf("looking up user %s: %w", trimmed, err)
			}
			// users.lookupByEmail unavailable (missing users:read.email
			// scope …): look for the email in the directory instead.
		}
		if w.handle == "" {
			return nil, &NotFoundError{Input: input}
		}
		wants = append(wants, w)
	}

	if len(wants) > 0 {
		if err := scanDirectory(ctx, client, wants); err != nil {
			return nil, err
		}
	}
	for _, w := range wants {
		switch {
		case w.active != nil:
			out[w.index] = found{id: jsonx.Str(w.active["id"]), raw: w.active}
		case len(w.named) == 1:
			out[w.index] = found{id: jsonx.Str(w.named[0]["id"]), raw: w.named[0]}
		case len(w.named) > 1:
			amb := &AmbiguousError{Input: w.input}
			for _, m := range w.named {
				amb.Matches = append(amb.Matches, ToCompactUser(m))
			}
			return nil, amb
		case w.inactive != nil:
			out[w.index] = found{id: jsonx.Str(w.inactive["id"]), raw: w.inactive}
		default:
			return nil, &NotFoundError{Input: w.input}
		}
	}
	return out, nil
}

func lookupByEmail(ctx context.Context, client *slackapi.Client, email string) (map[string]any, error) {
	resp, err := client.API(ctx, "users.lookupByEmail", map[string]any{"email": email})
	if err != nil {
		return nil, err
	}
	if user := jsonx.Map(resp["user"]); jsonx.Str(user["id"]) != "" {
		return user, nil
	}
	return nil, nil
}

// scanDirectory pages through users.list filling in wants, and stops as
// soon as each of them has an active handle (or email) match.
func scanDirectory(ctx context.Context, client *slackapi.Client, wants []*want) error {
	cursor := ""
	for {
		params := map[string]any{"limit": scanPageSize}
		if cursor != "" {
			params["cursor"] = cursor
		}
		resp, err := client.API(ctx, "users.list", params)
		if err != nil {
			return fmt.Errorf("searching the user directory: %w", err)
		}
		for _, m := range jsonx.Maps(resp["members"]) {
			if jsonx.Str(m["id"]) == "" {
				continue
			}
			for _, w := range wants {
				w.consider(m)
			}
		}
		done := true
		for _, w := range wants {
			done = done && w.active != nil
		}
		next := nextCursor(resp)
		if done || next == "" || next == cursor {
			return nil
		}
		cursor = next
	}
}

func (w *want) consider(m map[string]any) {
	profile := jsonx.Map(m["profile"])
	deleted := jsonx.Bool(m["deleted"])

	exact := strings.ToLower(jsonx.Str(m["name"])) == w.handle ||
		(w.email != "" && strings.ToLower(jsonx.Str(profile["email"])) == w.email)
	switch {
	case exact && !deleted:
		if w.active == nil {
			w.active = m
		}
		return
	case exact:
		if w.inactive == nil {
			w.inactive = m
		}
		return
	case deleted:
		return
	}
	for _, name := range []string{jsonx.Str(profile["display_name"]), jsonx.Str(profile["real_name"]), jsonx.Str(m["real_name"])} {
		if name != "" && strings.ToLower(name) == w.handle {
			w.named = append(w.named, m)
			return
		}
	}
}
