package channeladmin

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/maelvls/slackcli/internal/slackapi"
)

type call struct {
	method string
	params map[string]any
}

// fakeClient records calls and answers with respond's error code ("" for
// success) or transport error.
func fakeClient(calls *[]call, respond func(params map[string]any) (code string, err error)) *slackapi.Client {
	return slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		*calls = append(*calls, call{method, params})
		code, err := respond(params)
		if err != nil {
			return nil, err
		}
		if code != "" {
			return map[string]any{"ok": false, "error": code}, nil
		}
		return nil, nil
	})
}

func TestParseUsersCSV(t *testing.T) {
	got := ParseUsersCSV(" U01AAAA , @alice, bob@example.com, @alice ,, ")
	if want := []string{"U01AAAA", "@alice", "bob@example.com"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := ParseUsersCSV(" , , "); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

func TestSplitEmails(t *testing.T) {
	emails, others := SplitEmails([]string{"alice@example.com", "@alice", "U01AAAA", "bob@example.com"})
	if want := []string{"alice@example.com", "bob@example.com"}; !reflect.DeepEqual(emails, want) {
		t.Errorf("emails = %v, want %v", emails, want)
	}
	if want := []string{"@alice", "U01AAAA"}; !reflect.DeepEqual(others, want) {
		t.Errorf("others = %v, want %v", others, want)
	}

	emails, _ = SplitEmails([]string{"alice@example.com", "alice@example.com", "Alice@Example.com"})
	if want := []string{"alice@example.com"}; !reflect.DeepEqual(emails, want) {
		t.Errorf("emails = %v, want %v", emails, want)
	}
}

func TestNormalizeName(t *testing.T) {
	ok := map[string]string{
		"project-apollo":        "project-apollo",
		" #project_apollo-2 ":   "project_apollo-2",
		"プロジェクト":                "プロジェクト",
		"équipe-café":           "équipe-café",
		strings.Repeat("a", 80): strings.Repeat("a", 80),
	}
	for in, want := range ok {
		if got, err := NormalizeName(in); err != nil || got != want {
			t.Errorf("NormalizeName(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	bad := map[string]string{
		"":                      "channel name is empty",
		" # ":                   "channel name is empty",
		"Project Apollo":        "try --name project-apollo",
		"Team":                  "try --name team",
		"release.notes":         "try --name release-notes",
		"what?!":                "try --name what",
		strings.Repeat("a", 81): "try --name " + strings.Repeat("a", 80),
		"!!!":                   "at most 80 characters long",
	}
	for in, want := range bad {
		if got, err := NormalizeName(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("NormalizeName(%q) = %q, %v, want error containing %q", in, got, err, want)
		}
	}
}

func TestCreate(t *testing.T) {
	var calls []call
	client := slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		calls = append(calls, call{method, params})
		return map[string]any{"ok": true, "channel": map[string]any{"id": "C0NEW1234", "name": "project-apollo", "is_private": true}}, nil
	})
	got, err := Create(context.Background(), client, " #project-apollo ", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Created{Channel{ID: "C0NEW1234", Name: "project-apollo", IsPrivate: true}}); got != want {
		t.Errorf("Create = %+v, want %+v", got, want)
	}
	want := call{"conversations.create", map[string]any{"name": "project-apollo", "is_private": true}}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
}

func TestCreateFailures(t *testing.T) {
	var calls []call
	client := fakeClient(&calls, func(params map[string]any) (string, error) {
		if params["name"] == "taken" {
			return "name_taken", nil
		}
		return "", nil // ok:true without a channel
	})
	if _, err := Create(context.Background(), client, "Bad Name", false); err == nil || len(calls) != 0 {
		t.Errorf("invalid name: err = %v, API calls = %d (want an error before any call)", err, len(calls))
	}
	_, err := Create(context.Background(), client, "taken", false)
	if !slackapi.IsErrorCode(err, "name_taken") || !strings.Contains(err.Error(), "pick another --name") {
		t.Errorf("err = %v", err)
	}
	if _, err := Create(context.Background(), client, "ghost", false); err == nil || !strings.Contains(err.Error(), "returned no channel") {
		t.Errorf("err = %v", err)
	}
}

func TestInviteUsersReportsEveryOutcome(t *testing.T) {
	var calls []call
	client := fakeClient(&calls, func(params map[string]any) (string, error) {
		return map[string]string{
			"U0MEMBER1": "already_in_channel",
			"U0MISSING": "user_not_found",
			"U0GUEST12": "user_is_restricted",
		}[params["users"].(string)], nil
	})
	got, err := InviteUsers(context.Background(), client, "C1", []string{"U0NEW0001", "U0MEMBER1", "U0MISSING", "U0NEW0001", "U0GUEST12", "U0NEW0002"})
	if err != nil {
		t.Fatal(err)
	}
	want := InviteResult{
		InvitedUserIDs:          []string{"U0NEW0001", "U0NEW0002"},
		AlreadyInChannelUserIDs: []string{"U0MEMBER1"},
		// A failure for one user used to abort and lose the whole report.
		Failed: []Failure{{"U0MISSING", "user_not_found"}, {"U0GUEST12", "user_is_restricted"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("InviteUsers =\n%+v, want\n%+v", got, want)
	}
	if len(calls) != 5 {
		t.Errorf("calls = %d, want 5 (one per distinct user)", len(calls))
	}
	if p := calls[0].params; calls[0].method != "conversations.invite" || p["channel"] != "C1" || p["users"] != "U0NEW0001" {
		t.Errorf("first call = %+v", calls[0])
	}
	if err := got.Err(); err == nil || !strings.Contains(err.Error(), "U0MISSING (user_not_found), U0GUEST12 (user_is_restricted)") {
		t.Errorf("Err() = %v", err)
	}
	if err := (InviteResult{InvitedUserIDs: []string{"U1"}}).Err(); err != nil {
		t.Errorf("Err() without failures = %v", err)
	}
}

func TestInviteUsersAbortsOnChannelLevelErrors(t *testing.T) {
	boom := errors.New("connection reset")
	tests := []struct {
		name  string
		code  string
		err   error
		check func(error) bool
	}{
		{"channel not found", "channel_not_found", nil, func(e error) bool { return slackapi.IsErrorCode(e, "channel_not_found") }},
		{"expired credentials", "invalid_auth", nil, slackapi.IsAuthError},
		{"transport", "", boom, func(e error) bool { return errors.Is(e, boom) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []call
			client := fakeClient(&calls, func(params map[string]any) (string, error) {
				if params["users"] == "U0NEW0001" {
					return "", nil
				}
				return tt.code, tt.err
			})
			got, err := InviteUsers(context.Background(), client, "C1", []string{"U0NEW0001", "U0NEW0002", "U0NEW0003"})
			if !tt.check(err) {
				t.Errorf("err = %v", err)
			}
			// The invite that did go through is still reported.
			if want := (InviteResult{InvitedUserIDs: []string{"U0NEW0001"}}); !reflect.DeepEqual(got, want) {
				t.Errorf("partial result = %+v, want %+v", got, want)
			}
			if len(calls) != 2 {
				t.Errorf("calls = %d, want 2 (no point retrying for the third user)", len(calls))
			}
		})
	}
}

func TestInviteUsersMatchesErrorCodesExactly(t *testing.T) {
	// The TypeScript matched "already_in_channel" as a substring of the
	// error message, which this made-up code would have satisfied.
	var calls []call
	client := fakeClient(&calls, func(map[string]any) (string, error) { return "not_already_in_channel_x", nil })
	got, err := InviteUsers(context.Background(), client, "C1", []string{"U0NEW0001"})
	if err != nil || len(got.AlreadyInChannelUserIDs) != 0 || len(got.Failed) != 1 {
		t.Errorf("InviteUsers = %+v, %v", got, err)
	}
}

func TestInviteExternal(t *testing.T) {
	var calls []call
	client := fakeClient(&calls, func(params map[string]any) (string, error) {
		return map[string]string{
			"member@example.com":  "already_in_channel",
			"pending@example.com": "already_invited",
			"bad@example.com":     "invalid_email",
		}[params["emails"].([]string)[0]], nil
	})
	emails := []string{"new@example.com", "member@example.com", "pending@example.com", "bad@example.com", "new@example.com"}
	got, err := InviteExternal(context.Background(), client, "C1", emails, true)
	if err != nil {
		t.Fatal(err)
	}
	want := ExternalInviteResult{
		InvitedEmails:        []string{"new@example.com"},
		AlreadyInvitedEmails: []string{"member@example.com", "pending@example.com"},
		Failed:               []Failure{{"bad@example.com", "invalid_email"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("InviteExternal =\n%+v, want\n%+v", got, want)
	}
	if got.Err() == nil {
		t.Error("Err() = nil, want the failure")
	}
	wantCall := call{"conversations.inviteShared", map[string]any{"channel": "C1", "emails": []string{"new@example.com"}, "external_limited": true}}
	if len(calls) != 4 || !reflect.DeepEqual(calls[0], wantCall) {
		t.Errorf("calls = %d, first = %+v, want %+v", len(calls), calls[0], wantCall)
	}

	calls = nil
	client = fakeClient(&calls, func(map[string]any) (string, error) { return "not_in_channel", nil })
	got, err = InviteExternal(context.Background(), client, "C1", []string{"a@example.com", "b@example.com"}, false)
	if !slackapi.IsErrorCode(err, "not_in_channel") || len(calls) != 1 || calls[0].params["external_limited"] != false {
		t.Errorf("abort: result = %+v, err = %v, calls = %v", got, err, calls)
	}
}
