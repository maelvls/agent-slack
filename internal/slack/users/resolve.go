package users

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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
//   - a handle, with or without "@": one search.messages "from:@handle"
//     call, which finds anybody who posted a message the caller can see
//     (user and browser tokens only), confirmed by one users.info call:
//     the author of the message must really have that handle;
//   - failing that, a handle or the exact display name or real name of a
//     single member: a scan of the user directory (users.list), which is
//     slow in large workspaces and gets rate limited in huge ones.
//
// The directory scan is also the last resort for an email address that
// users.lookupByEmail does not know (or may not look up): Slack only looks
// active members up by email, a deactivated one is found by the scan.
//
// Matching is case-insensitive, and active members win over deactivated
// ones. The error is a *NotFoundError when nobody matches and an
// *AmbiguousError when a display name designates several members.
func ResolveUserID(ctx context.Context, client *slackapi.Client, input string) (string, error) {
	found, err := lookupAll(ctx, client, []string{input})
	if err != nil {
		return "", err
	}
	return found[0].id, nil
}

// Resolution is the outcome of resolving one input of ResolveUserIDs.
type Resolution struct {
	// Input is the user reference, as given.
	Input string
	// ID is the user ID; "" when Err is set.
	ID string
	// Err says why Input designates no single member: a *NotFoundError,
	// an *AmbiguousError, or a plain error for a blank input.
	Err error
}

// ResolveUserIDs resolves several user references (see ResolveUserID) and
// returns one Resolution per input, in order. The inputs that need the
// user directory share a single scan of it.
//
// An input that designates nobody (or several members) is reported in its
// Resolution and does not fail the others. The error is for failures that
// leave the outcome unknown: dead credentials, cancellation, a directory
// that cannot be scanned.
func ResolveUserIDs(ctx context.Context, client *slackapi.Client, inputs []string) ([]Resolution, error) {
	found, err := lookup(ctx, client, inputs)
	if err != nil {
		return nil, err
	}
	out := make([]Resolution, len(inputs))
	for i, f := range found {
		out[i] = Resolution{Input: inputs[i], ID: f.id, Err: f.err}
	}
	return out, nil
}

// found is the outcome for one input: the user's ID and, when the lookup
// came across it, the raw Slack user object; or err when the input
// designates no single member.
type found struct {
	id  string
	raw map[string]any
	err error
}

// want is one user being searched for in the directory.
type want struct {
	index  int    // position in the lookup inputs
	input  string // as given, for errors
	handle string // lower-cased, without "@"
	email  string // lower-cased; "" unless the input is an email address
	// deactivated is set when users.lookupByEmail knows no member with
	// that email: only a deactivated member can still have it, so the
	// scan may stop at the first one found.
	deactivated bool

	active   map[string]any   // handle or email match
	inactive map[string]any   // same, but a deactivated member
	named    []map[string]any // active members with that display or real name
}

// lookupAll is lookup for callers that need every input resolved: it
// fails on the first that is not.
func lookupAll(ctx context.Context, client *slackapi.Client, inputs []string) ([]found, error) {
	out, err := lookup(ctx, client, inputs)
	if err != nil {
		return nil, err
	}
	for _, f := range out {
		if f.err != nil {
			return nil, f.err
		}
	}
	return out, nil
}

// lookup resolves every input: out[i] is the outcome for inputs[i]. All
// the inputs that need the user directory share a single scan of it. The
// error is for operational failures only; an input matching nobody is
// reported in its found.err.
func lookup(ctx context.Context, client *slackapi.Client, inputs []string) ([]found, error) {
	out := make([]found, len(inputs))
	var wants []*want
	// searched remembers the outcome per handle, so that a handle given
	// twice costs one search.
	searched := map[string]map[string]any{}
	for i, input := range inputs {
		trimmed := strings.TrimSpace(input)
		if trimmed == "" {
			out[i].err = errors.New("user is empty: pass a user ID (U…), an email address or a @handle")
			continue
		}
		if slackid.IsUserID(trimmed) {
			out[i].id = trimmed
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
				// No active member has that address. A deactivated one
				// may: the directory is the only place to find them.
				w.deactivated = true
			case isFatal(ctx, err):
				return nil, fmt.Errorf("looking up user %s: %w", trimmed, err)
			}
			// users.lookupByEmail unavailable (missing users:read.email
			// scope …): look for the email in the directory instead.
		}
		if w.handle == "" {
			out[i].err = &NotFoundError{Input: input}
			continue
		}
		if w.email == "" && canSearch(client, w.handle) {
			user, done := searched[w.handle]
			if !done {
				var err error
				user, err = searchByHandle(ctx, client, w.handle)
				if err != nil && (ctx.Err() != nil || slackapi.IsAuthError(err)) {
					return nil, fmt.Errorf("looking up user @%s: %w", w.handle, err)
				}
				// Any other failure (missing search:read scope, rate limit
				// on the search tier, users.info refused …) leaves the
				// directory to look at. So does finding nothing: somebody
				// who never posted is invisible to search.
				searched[w.handle] = user
			}
			if user != nil {
				out[i] = found{id: jsonx.Str(user["id"]), raw: user}
				continue
			}
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
			out[w.index].err = amb
		case w.inactive != nil:
			out[w.index] = found{id: jsonx.Str(w.inactive["id"]), raw: w.inactive}
		default:
			out[w.index].err = &NotFoundError{Input: w.input}
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

// canSearch reports whether the search.messages shortcut is worth trying:
// bot tokens cannot call search.*, and something with spaces or quotes is
// a display name, not a handle, and cannot be embedded in a query.
func canSearch(client *slackapi.Client, handle string) bool {
	if strings.HasPrefix(client.Auth().BearerToken(), "xoxb-") {
		return false
	}
	return !strings.ContainsAny(handle, " \t\n\"")
}

// searchResults is how many matches searchByHandle asks for. Search is
// fuzzy about "from:": a few results give the exact handle a chance to
// show up behind a near miss.
const searchResults = 5

// searchByHandle finds a user through the messages they posted: Slack has
// no handle→ID lookup, and the user directory of a large workspace cannot
// be paged through. It returns the user (as users.info gives it), or nil
// when no message is from that handle.
//
// The ID it returns ends up being DMed, invited or searched for, so a
// match is not taken at its word. Integrations post under any `username`
// they like: their messages (those with a bot_id or a subtype) are
// ignored. And the author of the first remaining match is confirmed with
// one users.info call, whose `name` must be the handle.
func searchByHandle(ctx context.Context, client *slackapi.Client, handle string) (map[string]any, error) {
	resp, err := client.API(ctx, "search.messages", map[string]any{
		"query":    "from:@" + handle,
		"count":    searchResults,
		"sort":     "timestamp",
		"sort_dir": "desc",
	})
	if err != nil {
		return nil, err
	}
	candidate := ""
	for _, m := range jsonx.Maps(jsonx.Get(resp, "messages", "matches")) {
		if jsonx.Str(m["bot_id"]) != "" || jsonx.Str(m["subtype"]) != "" {
			continue
		}
		// "from:" also matches display names and similar handles.
		if id := jsonx.Str(m["user"]); slackid.IsUserID(id) && strings.EqualFold(jsonx.Str(m["username"]), handle) {
			candidate = id
			break
		}
	}
	if candidate == "" {
		return nil, nil
	}
	resp, err = client.API(ctx, "users.info", map[string]any{"user": candidate})
	if err != nil {
		return nil, err
	}
	user := jsonx.Map(resp["user"])
	if jsonx.Str(user["id"]) != candidate || !strings.EqualFold(jsonx.Str(user["name"]), handle) {
		return nil, nil
	}
	return user, nil
}

// scanDirectory pages through users.list filling in wants, and stops as
// soon as each of them has an active handle (or email) match, or, for an
// email that no active member has, a deactivated one.
func scanDirectory(ctx context.Context, client *slackapi.Client, wants []*want) error {
	cursor := ""
	for {
		params := map[string]any{"limit": scanPageSize}
		if cursor != "" {
			params["cursor"] = cursor
		}
		resp, err := client.API(ctx, "users.list", params)
		if err != nil {
			return scanError(err, wants)
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
			done = done && (w.active != nil || w.deactivated && w.inactive != nil)
		}
		next := nextCursor(resp)
		if done || next == "" || next == cursor {
			return nil
		}
		cursor = next
	}
}

// scanError explains a failed directory scan. Being rate limited is the
// normal outcome in a workspace with tens of thousands of members, where
// retrying does not help: the message says what to pass instead.
func scanError(err error, wants []*want) error {
	var rl *slackapi.RateLimitError
	if !errors.As(err, &rl) {
		return fmt.Errorf("searching the user directory: %w", err)
	}
	var missing []string
	onlyEmails := true
	for _, w := range wants {
		if w.active == nil {
			missing = append(missing, strconv.Quote(w.input))
			onlyEmails = onlyEmails && w.deactivated
		}
	}
	if onlyEmails && len(missing) > 0 {
		return fmt.Errorf("could not resolve user %s: no active member of this workspace has that email address (users.lookupByEmail), "+
			"and looking for a deactivated one takes a scan of the user directory, which this workspace is too large for (users.list was rate limited). "+
			"Pass the user ID (U…) instead. "+
			"Setting %s to a large value makes the scan wait and carry on, which can take very long: %w",
			strings.Join(missing, ", "), appinfo.EnvName("RATE_LIMIT_MAX_WAIT_MS"), err)
	}
	return fmt.Errorf("could not resolve user %s: this workspace is too large to scan its user directory (users.list was rate limited). "+
		"Pass the user ID (U…) or the email address instead: a handle is only found without a scan when it is the exact @handle of somebody who posted a message you can see. "+
		"Setting %s to a large value makes the scan wait and carry on, which can take very long: %w",
		strings.Join(missing, ", "), appinfo.EnvName("RATE_LIMIT_MAX_WAIT_MS"), err)
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
