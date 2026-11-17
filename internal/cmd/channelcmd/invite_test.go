package channelcmd

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/authextract"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// desktopRefresh makes rt re-import credentials when a call fails with an
// auth error, as Slack Desktop being installed does.
func (h *harness) desktopRefresh() {
	h.rt.Desktop = func(context.Context) (*authextract.Extracted, error) {
		return &authextract.Extracted{
			CookieD: "xoxd-fresh",
			Teams:   []authextract.Team{{URL: "https://acme.slack.com/", Token: "xoxc-fresh"}},
		}, nil
	}
}

// subjects lists who the invite calls were for.
func subjects(calls []call) []string {
	var out []string
	for _, c := range calls {
		if emails, ok := c.params["emails"].([]string); ok {
			out = append(out, emails...)
		} else {
			out = append(out, c.params["users"].(string))
		}
	}
	return out
}

// Credentials expiring in the middle of an invite are re-imported and the
// invite carries on: nobody is invited twice, and what happened before the
// expiry is still reported as it happened.
func TestInviteResumesAfterAnAuthRefresh(t *testing.T) {
	t.Run("external", func(t *testing.T) {
		h := newHarness(t)
		h.desktopRefresh()
		h.failOnce = map[string]string{"conversations.inviteShared:dave@example.com": "invalid_auth"}
		h.fail["conversations.inviteShared:erin@example.com"] = "invalid_email"
		out, err := h.run("invite", "--channel", "C000000001", "--external", "--users", "bob@example.com,dave@example.com,erin@example.com")
		if err == nil || !strings.Contains(err.Error(), "could not invite 1 of the targets: erin@example.com (invalid_email)") {
			t.Errorf("err = %v", err)
		}
		// bob is invited once, and is not reported as already invited.
		if got, want := subjects(h.only("conversations.inviteShared")), []string{"bob@example.com", "dave@example.com", "dave@example.com", "erin@example.com"}; !reflect.DeepEqual(got, want) {
			t.Errorf("invitations sent to %v, want %v", got, want)
		}
		want := map[string]any{
			"channel_id": "C000000001", "external": true, "external_limited": true,
			"invited_emails": []any{"bob@example.com", "dave@example.com"},
			"failed":         []any{map[string]any{"target": "erin@example.com", "error": "invalid_email"}},
		}
		if !reflect.DeepEqual(out, want) {
			t.Errorf("printed %s", h.stdout)
		}
	})
	t.Run("members", func(t *testing.T) {
		h := newHarness(t)
		h.desktopRefresh()
		h.failOnce = map[string]string{"conversations.invite:U000000002": "token_expired"}
		h.fail["conversations.invite:U000000001"] = "user_is_restricted"
		out, err := h.run("invite", "--channel", "C000000001", "--users", "U000000001,@ghost,U000000002,U000000003")
		if err == nil || !strings.Contains(err.Error(), "U000000001 (user_is_restricted)") || !strings.Contains(err.Error(), "could not resolve 1 of the users: @ghost") {
			t.Errorf("err = %v", err)
		}
		if got, want := subjects(h.only("conversations.invite")), []string{"U000000001", "U000000002", "U000000002", "U000000003"}; !reflect.DeepEqual(got, want) {
			t.Errorf("invited %v, want %v", got, want)
		}
		// The users are resolved, and the reason given, once.
		if n := len(h.only("users.list")); n != 1 {
			t.Errorf("users.list calls = %d, want 1", n)
		}
		if n := strings.Count(h.stderr.String(), `could not resolve user "@ghost"`); n != 1 {
			t.Errorf("the reason @ghost is unresolved is printed %d times: %q", n, h.stderr)
		}
		want := map[string]any{
			"channel_id":       "C000000001",
			"invited_user_ids": []any{"U000000002", "U000000003"},
			"unresolved_users": []any{"@ghost"},
			"failed":           []any{map[string]any{"target": "U000000001", "error": "user_is_restricted"}},
		}
		if !reflect.DeepEqual(out, want) {
			t.Errorf("printed %s", h.stdout)
		}
	})
	t.Run("credentials that stay dead abort with what was done", func(t *testing.T) {
		h := newHarness(t)
		h.desktopRefresh()
		h.fail["conversations.inviteShared:dave@example.com"] = "invalid_auth"
		out, err := h.run("invite", "--channel", "C000000001", "--external", "--users", "bob@example.com,dave@example.com,erin@example.com")
		if !slackapi.IsErrorCode(err, "invalid_auth") {
			t.Errorf("err = %v", err)
		}
		if got, want := subjects(h.only("conversations.inviteShared")), []string{"bob@example.com", "dave@example.com", "dave@example.com"}; !reflect.DeepEqual(got, want) {
			t.Errorf("invitations sent to %v, want %v", got, want)
		}
		if !reflect.DeepEqual(out["invited_emails"], []any{"bob@example.com"}) || out["already_invited_emails"] != nil {
			t.Errorf("printed %s", h.stdout)
		}
	})
}

// --external sends nothing unless every target is an email address, and
// says which ones are not.
func TestInviteExternalNamesEveryBadTarget(t *testing.T) {
	h := newHarness(t)
	out, err := h.run("invite", "--channel", "C000000001", "--external", "--users", "bob@example.com,@alice,U000000001,carol")
	if err == nil || !strings.Contains(err.Error(), "not an email address: @alice, U000000001, carol. Nothing was sent") {
		t.Errorf("err = %v", err)
	}
	if out != nil || len(h.calls) != 0 {
		t.Errorf("printed %v, made calls %+v", out, h.calls)
	}
}

// Inviting (possibly people outside the workspace) and creating channels
// reach other people: safe mode refuses them before anything is looked up.
func TestSafeModeRefusesInviteAndNew(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
	}{
		{"channel invite", []string{"invite", "--channel", "C000000001", "--users", "U000000001"}},
		{"channel invite", []string{"invite", "--channel", "#general", "--external", "--users", "bob@example.com"}},
		{"channel new", []string{"new", "--name", "incident-123"}},
	} {
		h := newHarness(t)
		h.rt.SafeModeFlag = true
		out, err := h.run(tc.args...)
		if err == nil || !strings.Contains(err.Error(), "safe mode is active") || !strings.Contains(err.Error(), `"`+tc.command+`" is blocked`) {
			t.Errorf("%v: err = %v", tc.args, err)
		}
		if out != nil || len(h.calls) != 0 {
			t.Errorf("%v: printed %v, made calls %+v", tc.args, out, h.calls)
		}
		for _, c := range New(h.rt).Commands() {
			if c.Name() == tc.args[0] && !strings.Contains(c.Long, "refused in safe mode") {
				t.Errorf("channel %s: the help does not say that safe mode refuses it", c.Name())
			}
		}
	}
	// Reading and the read marker stay available.
	h := newHarness(t)
	h.rt.SafeModeFlag = true
	for _, args := range [][]string{{"list"}, {"mark", messageURL}} {
		if _, err := h.run(args...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
}

// "channels" is printed even when there are none.
func TestListWithNoChannels(t *testing.T) {
	h := newHarness(t)
	h.empty = true
	for _, args := range [][]string{{"list"}, {"list", "--all"}, {"list", "--via-counts"}} {
		if _, err := h.run(args...); err != nil {
			t.Fatal(err)
		}
		if got, want := h.stdout.String(), "{\n  \"channels\": []\n}\n"; got != want {
			t.Errorf("%v printed %s, want %s", args, got, want)
		}
	}
}
