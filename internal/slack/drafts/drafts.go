// Package drafts manages Slack-native message drafts through the
// undocumented drafts.* session endpoints, the ones the official Slack
// clients use: a draft created here shows up in the user's Slack client.
//
// These endpoints only accept browser session credentials (xoxc token +
// xoxd cookie). Every function fails with ErrBrowserAuthRequired, before
// calling Slack, when the client holds a bot or user token.
package drafts

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/maelvls/slackcli/internal/appinfo"
	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slack/slacktext"
	"github.com/maelvls/slackcli/internal/slackapi"
	"github.com/maelvls/slackcli/internal/slackid"
)

// ErrBrowserAuthRequired is returned when the client does not hold browser
// session credentials.
var ErrBrowserAuthRequired = fmt.Errorf(
	`Slack-native drafts require browser auth (xoxc/xoxd): Slack rejects bot and user tokens (xoxb/xoxp) on the drafts.* endpoints. `+
		`Import browser credentials with "%[1]s auth import-desktop" (also: import-chrome, import-brave, import-firefox), `+
		`or set SLACK_TOKEN to an xoxc- token together with SLACK_COOKIE_D and SLACK_WORKSPACE_URL`, appinfo.Name)

// RequireBrowserAuth returns ErrBrowserAuthRequired unless client holds
// browser session credentials. Commands call it before doing any other
// work (uploading attachments, resolving channels).
func RequireBrowserAuth(client *slackapi.Client) error {
	if !client.Auth().IsBrowser() {
		return ErrBrowserAuthRequired
	}
	return nil
}

// Destination is where a draft is addressed.
type Destination struct {
	ChannelID string `json:"channel_id"`
	// ThreadTS, when set, makes the draft a reply in that thread.
	ThreadTS string `json:"thread_ts,omitempty"`
	// Broadcast also sends the thread reply to the channel when the draft
	// is posted. Only meaningful with ThreadTS.
	Broadcast bool `json:"broadcast,omitempty"`
	// ChannelName is a display name for ChannelID. Slack does not return
	// it: callers that want it fill it in (see `draft list`).
	ChannelName string `json:"channel_name,omitempty"`
}

// Draft is a Slack-native draft in the compact shape the CLI prints.
type Draft struct {
	ID string `json:"id"`
	// Text is the draft body rendered back to the mrkdwn-like text a user
	// would type.
	Text         string        `json:"text,omitempty"`
	Destinations []Destination `json:"destinations"`
	// LastUpdatedTS is what Update and Delete send back for conflict
	// detection.
	LastUpdatedTS string `json:"last_updated_ts,omitempty"`
	DateCreated   int64  `json:"date_created,omitempty"`
	// DateScheduled is the Unix time of a scheduled send (0 when the draft
	// is not scheduled).
	DateScheduled int64    `json:"date_scheduled,omitempty"`
	FileIDs       []string `json:"file_ids,omitempty"`
}

// NotFoundError says that no draft has the requested ID.
type NotFoundError struct{ ID string }

func (e *NotFoundError) Error() string {
	return fmt.Sprintf(`draft not found: %s. Run "%s message draft list --all" to see draft ids`, e.ID, appinfo.Name)
}

var (
	draftIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	draftTSRe = regexp.MustCompile(`^\d+(\.\d+)?$`)
)

// ValidateID rejects values that cannot be a draft ID (blank, a C… or G…
// channel ID, a user ID, a channel name, a URL), which otherwise only
// surface as an opaque Slack error or a "not found".
//
// Draft IDs are "Dr" followed by upper-case letters and digits
// ("Dr0C98G04RNC"). Nothing that starts with "D" is refused for looking
// like a DM channel ID: telling the two apart is Slack's business, and a
// wrong guess here would lock a real draft out.
func ValidateID(id string) error {
	hint := fmt.Sprintf(`draft ids look like "Dr0123ABCD" and are printed by "%s message draft list" and "draft create"`, appinfo.Name)
	switch {
	case strings.TrimSpace(id) == "":
		return fmt.Errorf("missing draft id: %s", hint)
	case slackid.IsChannelID(id) && !strings.HasPrefix(id, "D"), slackid.IsUserID(id):
		return fmt.Errorf("%q is a channel or user id, not a draft id: %s", id, hint)
	case !draftIDRe.MatchString(id):
		return fmt.Errorf("invalid draft id %q: %s", id, hint)
	}
	return nil
}

// ValidateTS rejects a last-updated timestamp that is not a number of
// seconds with an optional fraction ("1700000000.123").
func ValidateTS(ts string) error {
	if !draftTSRe.MatchString(ts) {
		return fmt.Errorf(`invalid draft timestamp %q: expected the last_updated_ts printed by "%s message draft list", such as 1700000000.1230000`, ts, appinfo.Name)
	}
	return nil
}

// PadTS pads the fractional part of a draft timestamp to 7 digits: Slack
// rejects client_last_updated_ts values with fewer places
// ("1700000000.123" must be sent as "1700000000.1230000"). A timestamp
// without a fractional part is returned unchanged.
func PadTS(ts string) string {
	secs, frac, ok := strings.Cut(ts, ".")
	if !ok || len(frac) >= 7 {
		return ts
	}
	return secs + "." + frac + strings.Repeat("0", 7-len(frac))
}

// SameTS reports whether two draft timestamps denote the same instant,
// whatever their padding.
func SameTS(a, b string) bool {
	norm := func(ts string) string {
		if strings.Contains(ts, ".") {
			ts = strings.TrimRight(ts, "0")
		}
		return strings.TrimSuffix(ts, ".")
	}
	return norm(a) == norm(b)
}

// TextToBlocks converts draft text to rich_text blocks. Drafts have no
// plain-text fallback parameter, so unformatted text is wrapped in a single
// section. Blank text gives no block.
func TextToBlocks(text string) []any {
	if strings.TrimSpace(text) == "" {
		return []any{}
	}
	if blocks := slacktext.TextToRichTextBlocksInline(text); blocks != nil {
		return blocks
	}
	return []any{map[string]any{
		"type": "rich_text",
		"elements": []any{map[string]any{
			"type":     "rich_text_section",
			"elements": slacktext.ParseInlineElements(text),
		}},
	}}
}

// ParseRecord converts a raw draft object. ok is false when raw is not an
// object with an ID.
func ParseRecord(raw any) (draft Draft, ok bool) {
	rec := jsonx.Map(raw)
	id := jsonx.Str(rec["id"])
	if id == "" {
		return Draft{}, false
	}
	draft = Draft{
		ID:            id,
		Destinations:  []Destination{},
		LastUpdatedTS: jsonx.Stringish(rec["last_updated_ts"]),
	}
	for _, dest := range jsonx.Maps(rec["destinations"]) {
		channelID := jsonx.Str(dest["channel_id"])
		if channelID == "" {
			continue
		}
		draft.Destinations = append(draft.Destinations, Destination{
			ChannelID: channelID,
			ThreadTS:  jsonx.Stringish(dest["thread_ts"]),
			Broadcast: jsonx.Bool(dest["broadcast"]),
		})
	}
	var parts []string
	for _, block := range jsonx.Maps(rec["blocks"]) {
		if t := slacktext.MrkdwnFromRichTextBlock(block); strings.TrimSpace(t) != "" {
			parts = append(parts, t)
		}
	}
	draft.Text = strings.TrimSpace(strings.Join(parts, "\n\n"))
	if n, ok := jsonx.Num(rec["date_created"]); ok {
		draft.DateCreated = int64(n)
	}
	if n, ok := jsonx.Num(rec["date_scheduled"]); ok && n > 0 {
		draft.DateScheduled = int64(n)
	}
	for _, f := range jsonx.Slice(rec["file_ids"]) {
		if s := jsonx.Str(f); s != "" {
			draft.FileIDs = append(draft.FileIDs, s)
		}
	}
	return draft, true
}

// ListOptions selects drafts.
type ListOptions struct {
	// Limit is the most drafts to return; 0 means no limit of ours (Slack
	// applies its own).
	Limit int
	// IncludeInactive also lists sent and otherwise inactive drafts.
	IncludeInactive bool
}

// List returns the user's drafts (drafts.list).
func List(ctx context.Context, client *slackapi.Client, opts ListOptions) ([]Draft, error) {
	if err := RequireBrowserAuth(client); err != nil {
		return nil, err
	}
	params := map[string]any{}
	if !opts.IncludeInactive {
		params["is_active"] = true
	}
	if opts.Limit > 0 {
		params["limit"] = opts.Limit
	}
	resp, err := client.API(ctx, "drafts.list", params)
	if err != nil {
		return nil, err
	}
	drafts := []Draft{}
	for _, raw := range jsonx.Slice(resp["drafts"]) {
		if d, ok := ParseRecord(raw); ok {
			drafts = append(drafts, d)
		}
	}
	// The endpoint is undocumented: do not rely on it honouring limit.
	if opts.Limit > 0 && len(drafts) > opts.Limit {
		drafts = drafts[:opts.Limit]
	}
	return drafts, nil
}

// Find returns the draft with the given ID, active or not. It fails with a
// *NotFoundError when there is none.
func Find(ctx context.Context, client *slackapi.Client, draftID string) (Draft, error) {
	if err := ValidateID(draftID); err != nil {
		return Draft{}, err
	}
	all, err := List(ctx, client, ListOptions{IncludeInactive: true})
	if err != nil {
		return Draft{}, err
	}
	var found *Draft
	for i := range all {
		if all[i].ID != draftID {
			continue
		}
		// Update and Delete need last_updated_ts: prefer a usable record
		// should the response hold a malformed duplicate.
		if all[i].LastUpdatedTS != "" {
			return all[i], nil
		}
		if found == nil {
			found = &all[i]
		}
	}
	if found == nil {
		return Draft{}, &NotFoundError{ID: draftID}
	}
	return *found, nil
}

// Content is what Create and Update write: a draft is always rewritten as
// a whole, addressed to one destination.
type Content struct {
	// ChannelID is the conversation the draft is addressed to. Required.
	ChannelID string
	// Text is the draft body (mrkdwn as a user types it). It may only be
	// blank when FileIDs is not empty.
	Text string
	// ThreadTS makes the draft a thread reply; Broadcast (thread replies
	// only) also sends it to the channel.
	ThreadTS  string
	Broadcast bool
	// FileIDs are the already-uploaded files attached to the draft (see
	// upload.ForDraft).
	FileIDs []string
}

// body is the wire payload shared by drafts.create and drafts.update.
// blocks, destinations and file_ids are JSON-encoded by the client, which
// is what these endpoints expect.
func (c Content) body() (map[string]any, error) {
	if c.ChannelID == "" {
		return nil, errors.New("a draft needs a destination channel")
	}
	if strings.TrimSpace(c.Text) == "" && len(c.FileIDs) == 0 {
		return nil, errors.New("draft text is empty: pass some text (or attach a file)")
	}
	dest := map[string]any{"channel_id": c.ChannelID}
	if c.ThreadTS != "" {
		dest["thread_ts"] = c.ThreadTS
		dest["broadcast"] = c.Broadcast
	}
	fileIDs := c.FileIDs
	if fileIDs == nil {
		fileIDs = []string{}
	}
	return map[string]any{
		"blocks":        TextToBlocks(c.Text),
		"destinations":  []any{dest},
		"client_msg_id": newUUID(),
		"file_ids":      fileIDs,
	}, nil
}

// Create creates a draft (drafts.create). The returned draft is nil when
// Slack accepted the call but did not send the draft back.
func Create(ctx context.Context, client *slackapi.Client, content Content) (*Draft, error) {
	if err := RequireBrowserAuth(client); err != nil {
		return nil, err
	}
	params, err := content.body()
	if err != nil {
		return nil, err
	}
	// Sent on create only, like the official client.
	params["is_from_composer"] = true
	resp, err := client.API(ctx, "drafts.create", params)
	if err != nil {
		return nil, err
	}
	return draftOf(resp), nil
}

// Update replaces the content of a draft (drafts.update). lastUpdatedTS is
// the draft's last_updated_ts as last read: Slack refuses the update when
// the draft changed since. Anything not in content (other destinations, a
// scheduled send time) is dropped by Slack.
func Update(ctx context.Context, client *slackapi.Client, draftID, lastUpdatedTS string, content Content) (*Draft, error) {
	if err := RequireBrowserAuth(client); err != nil {
		return nil, err
	}
	if err := validateRef(draftID, lastUpdatedTS); err != nil {
		return nil, err
	}
	params, err := content.body()
	if err != nil {
		return nil, err
	}
	params["draft_id"] = draftID
	params["client_last_updated_ts"] = PadTS(lastUpdatedTS)
	resp, err := client.API(ctx, "drafts.update", params)
	if err != nil {
		return nil, err
	}
	return draftOf(resp), nil
}

// Delete deletes a draft (drafts.delete). lastUpdatedTS is the draft's
// last_updated_ts as last read; when it is "", the current one is looked
// up first.
func Delete(ctx context.Context, client *slackapi.Client, draftID, lastUpdatedTS string) error {
	if err := RequireBrowserAuth(client); err != nil {
		return err
	}
	if lastUpdatedTS == "" {
		draft, err := Find(ctx, client, draftID)
		if err != nil {
			return err
		}
		if draft.LastUpdatedTS == "" {
			return fmt.Errorf("draft %s has no last_updated_ts, which Slack needs to delete it: delete it in the Slack client", draftID)
		}
		lastUpdatedTS = draft.LastUpdatedTS
	}
	if err := validateRef(draftID, lastUpdatedTS); err != nil {
		return err
	}
	_, err := client.API(ctx, "drafts.delete", map[string]any{
		"draft_id":               draftID,
		"client_last_updated_ts": PadTS(lastUpdatedTS),
	})
	return err
}

func validateRef(draftID, lastUpdatedTS string) error {
	if err := ValidateID(draftID); err != nil {
		return err
	}
	return ValidateTS(lastUpdatedTS)
}

func draftOf(resp map[string]any) *Draft {
	if d, ok := ParseRecord(resp["draft"]); ok {
		return &d
	}
	return nil
}

// newUUID returns a random (version 4) UUID, the client_msg_id Slack uses
// to de-duplicate draft writes.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
