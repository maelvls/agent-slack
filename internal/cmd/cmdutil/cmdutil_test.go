package cmdutil

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/maelvls/slackcli/internal/auth"
	"github.com/maelvls/slackcli/internal/clictx"
	"github.com/maelvls/slackcli/internal/slack/slackurl"
	"github.com/maelvls/slackcli/internal/slackapi"
)

const (
	acme       = "https://acme.slack.com"
	acmeDev    = "https://acme-dev.slack.com"
	messageURL = acme + "/archives/C12345678/p1700000000000100"
	messageTS  = "1700000000.000100"
)

type call struct {
	method string
	params map[string]any
}

// env is a runtime with stored browser credentials for the given
// workspaces (the first one is the default) and a fake Slack.
type env struct {
	rt     *clictx.Context
	stderr *bytes.Buffer
	calls  []call
	// reply answers API calls; nil answers {}.
	reply func(method string, params map[string]any) (map[string]any, error)
}

func newEnv(t *testing.T, workspaces ...string) *env {
	t.Helper()
	for _, name := range []string{"SLACK_TOKEN", "SLACK_COOKIE_D", "SLACK_COOKIE", "SLACK_WORKSPACE_URL"} {
		t.Setenv(name, "")
	}
	e := &env{stderr: &bytes.Buffer{}}
	store := &auth.Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
	for _, u := range workspaces {
		ws := auth.Workspace{URL: u, Auth: slackapi.Auth{Type: slackapi.AuthBrowser, XoxcToken: "xoxc-1", XoxdCookie: "xoxd-1"}}
		if u == acme {
			ws.Name = "Rocket Skates"
		}
		if err := store.Upsert(ws); err != nil {
			t.Fatal(err)
		}
	}
	e.rt = &clictx.Context{
		Store:  store,
		Stdout: &bytes.Buffer{},
		Stderr: e.stderr,
		ClientOptions: []slackapi.Option{slackapi.WithHandler(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
			e.calls = append(e.calls, call{method, params})
			if e.reply != nil {
				return e.reply(method, params)
			}
			return map[string]any{}, nil
		})},
	}
	return e
}

func wantErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want one containing %q", err, want)
	}
}

func TestWorkspacePrecedence(t *testing.T) {
	e := newEnv(t, acme, acmeDev)
	t.Setenv("SLACK_WORKSPACE_URL", "https://env.slack.com")

	for _, tt := range []struct{ name, named, flag, want string }{
		{"the URL wins over SLACK_WORKSPACE_URL", acme, "", acme},
		{"then --workspace", "", "other", "other"},
		{"then SLACK_WORKSPACE_URL", "", "", "https://env.slack.com"},
		{"--workspace repeating the URL", acme, acme, acme},
		{"--workspace repeating the URL without its scheme", acme, "acme.slack.com", acme},
		{"--workspace selecting the URL's workspace by substring", acmeDev, "dev", acmeDev},
		{"--workspace selecting the URL's workspace by name", acme, "rocket", acme},
		// "acme" is a substring of both; the exact host match wins.
		{"--workspace selecting the URL's workspace exactly", acme, "acme", acme},
		{"--workspace matching a workspace that is not stored", "https://new.slack.com", "new", "https://new.slack.com"},
	} {
		got, err := Workspace(e.rt, tt.named, tt.flag)
		if err != nil || got != tt.want {
			t.Errorf("%s: Workspace(%q, %q) = %q, %v, want %q", tt.name, tt.named, tt.flag, got, err, tt.want)
		}
	}

	t.Setenv("SLACK_WORKSPACE_URL", "")
	if got, err := Workspace(e.rt, "", " "); err != nil || got != "" {
		t.Errorf("nothing given: got %q, %v, want the default workspace", got, err)
	}
}

func TestWorkspaceRefusesAFlagThatContradictsTheURL(t *testing.T) {
	e := newEnv(t, acme, acmeDev)
	for _, flag := range []string{acmeDev, "https://other.slack.com", "dev", "other", "acme-dev.slack.com"} {
		_, err := Workspace(e.rt, acme, flag)
		wantErr(t, err, "contradicts the URL, which is in workspace "+acme)
	}
	// "acme" selects acme.slack.com, not acme-dev.
	_, err := Workspace(e.rt, acmeDev, "acme")
	wantErr(t, err, "contradicts the URL")
}

func TestParseTarget(t *testing.T) {
	e := newEnv(t, acme)

	got, err := ParseTarget(e.rt, messageURL, "")
	if err != nil || got.Kind != slackurl.TargetURL || got.Workspace != acme || got.Ref.MessageTS != messageTS {
		t.Errorf("message URL: %+v, %v", got, err)
	}
	got, err = ParseTarget(e.rt, "general", "acme")
	if err != nil || got.Kind != slackurl.TargetChannel || got.Channel != "#general" || got.Workspace != "acme" {
		t.Errorf("channel name: %+v, %v", got, err)
	}
	got, err = ParseTarget(e.rt, "U12345678", "")
	if err != nil || got.Kind != slackurl.TargetUser || got.Workspace != "" {
		t.Errorf("user ID: %+v, %v", got, err)
	}
	// An app.slack.com link names no workspace: --workspace applies.
	got, err = ParseTarget(e.rt, "https://app.slack.com/client/T12345678/C12345678/thread/C12345678-"+messageTS, "acme")
	if err != nil || got.Kind != slackurl.TargetURL || got.Workspace != "acme" {
		t.Errorf("app.slack.com URL: %+v, %v", got, err)
	}
	if e.stderr.Len() != 0 || len(e.calls) != 0 {
		t.Errorf("stderr = %q, calls = %v", e.stderr, e.calls)
	}

	_, err = ParseTarget(e.rt, "", "")
	wantErr(t, err, "missing target")
	_, err = ParseTarget(e.rt, messageURL, "other")
	wantErr(t, err, `--workspace "other" contradicts the URL`)
}

func TestParseTargetWarnsAboutATruncatedURL(t *testing.T) {
	e := newEnv(t, acme)
	if _, err := ParseTarget(e.rt, messageURL+"?thread_ts=1700000000.000001", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.stderr.String(), "truncated") {
		t.Errorf("stderr = %q, want a truncation hint", e.stderr)
	}
}

func TestParseTargetNeedsAWorkspaceForChannelNames(t *testing.T) {
	// Two workspaces and no default: only a hand-written file has that.
	e := newEnv(t)
	e.rt.Store = &auth.Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
	const creds = `{"version": 1, "workspaces": [
		{"workspace_url": "https://acme.slack.com", "auth": {"auth_type": "browser", "xoxc_token": "xoxc-1", "xoxd_cookie": "xoxd-1"}},
		{"workspace_url": "https://other.slack.com", "auth": {"auth_type": "browser", "xoxc_token": "xoxc-2", "xoxd_cookie": "xoxd-2"}}]}`
	if err := os.WriteFile(e.rt.Store.Path, []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ParseTarget(e.rt, "#general", "")
	wantErr(t, err, "ambiguous channel name across multiple workspaces")
	for _, ok := range [][2]string{{"#general", "other"}, {"C12345678", ""}, {messageURL, ""}, {"U12345678", ""}} {
		if _, err := ParseTarget(e.rt, ok[0], ok[1]); err != nil {
			t.Errorf("ParseTarget(%q, %q): %v", ok[0], ok[1], err)
		}
	}
}

func TestParseChannel(t *testing.T) {
	e := newEnv(t, acme)
	for arg, want := range map[string]string{
		"#general":                   "#general",
		"general":                    "#general",
		"C12345678":                  "C12345678",
		acme + "/archives/C12345678": "C12345678",
	} {
		got, err := ParseChannel(e.rt, arg, "")
		if err != nil || got.Channel != want {
			t.Errorf("ParseChannel(%q) = %+v, %v, want channel %q", arg, got, err, want)
		}
	}
	for arg, want := range map[string]string{
		"":          "the channel is empty",
		" # ":       "the channel is empty",
		"U12345678": `"U12345678" is not a channel`,
		messageURL:  "is not a channel",
	} {
		_, err := ParseChannel(e.rt, arg, "")
		wantErr(t, err, want)
	}
}

func TestMessageTS(t *testing.T) {
	e := newEnv(t, acme)
	parse := func(arg string) Target {
		t.Helper()
		target, err := ParseTarget(e.rt, arg, "")
		if err != nil {
			t.Fatal(err)
		}
		return target
	}
	url, channel := parse(messageURL), parse("C12345678")

	for _, tt := range []struct {
		name   string
		target Target
		flag   string
		want   string
	}{
		{"the ts of the URL", url, "", messageTS},
		{"--ts repeating the URL", url, " " + messageTS + " ", messageTS},
		{"--ts for a channel", channel, "1700000000.000200", "1700000000.000200"},
	} {
		if got, err := tt.target.MessageTS(tt.flag); err != nil || got != tt.want {
			t.Errorf("%s: got %q, %v, want %q", tt.name, got, err, tt.want)
		}
	}
	for _, tt := range []struct {
		name   string
		target Target
		flag   string
		want   string
	}{
		{"--ts contradicting the URL", url, "1700000000.000200", "--ts 1700000000.000200 contradicts the message URL, which points at " + messageTS},
		{"a channel without --ts", channel, "", "--ts is required when the target is a channel"},
		{"a channel with a blank --ts", channel, "  ", "--ts is required"},
		{"a channel URL without --ts", parse(acme + "/archives/C12345678"), "", "--ts is required"},
		{"garbage", channel, "yesterday", `invalid --ts value "yesterday"`},
		{"garbage with a URL", url, "12abc", "invalid --ts"},
	} {
		_, err := tt.target.MessageTS(tt.flag)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want one containing %q", tt.name, err, tt.want)
		}
	}
}

func TestParseMessageTarget(t *testing.T) {
	e := newEnv(t, acme)
	target, ts, err := ParseMessageTarget(e.rt, "app later save", "#general", "acme", messageTS)
	if err != nil || target.Channel != "#general" || target.Workspace != "acme" || ts != messageTS {
		t.Errorf("got %+v, %q, %v", target, ts, err)
	}
	_, _, err = ParseMessageTarget(e.rt, "app later save", "U12345678", "", messageTS)
	wantErr(t, err, `"app later save" does not support user ID targets`)
	wantErr(t, err, "user dm-open U12345678")
	_, _, err = ParseMessageTarget(e.rt, "app later save", "#general", "", "")
	wantErr(t, err, "--ts is required")
	_, _, err = ParseMessageTarget(e.rt, "app later save", "not/a/target", "", "")
	wantErr(t, err, "unsupported target")
}

func TestWithTargetResolvesTheConversation(t *testing.T) {
	e := newEnv(t, acme)
	e.reply = func(method string, _ map[string]any) (map[string]any, error) {
		switch method {
		case "conversations.open":
			return map[string]any{"channel": map[string]any{"id": "D99999999"}}, nil
		case "search.messages":
			return map[string]any{"messages": map[string]any{"matches": []any{
				map[string]any{"channel": map[string]any{"id": "C000000001", "name": "general"}},
			}}}, nil
		}
		return map[string]any{}, nil
	}
	for arg, want := range map[string]string{
		messageURL:  "C12345678",
		"C12345678": "C12345678",
		"#general":  "C000000001",
		"U12345678": "D99999999",
	} {
		target, err := ParseTarget(e.rt, arg, "")
		if err != nil {
			t.Fatal(err)
		}
		e.calls = nil
		got, err := WithTarget(context.Background(), e.rt, target, func(loc Located) (string, error) {
			if loc.Client == nil || loc.WorkspaceURL != acme {
				t.Errorf("%s: located in %q", arg, loc.WorkspaceURL)
			}
			return loc.ChannelID, nil
		})
		if err != nil || got != want {
			t.Errorf("WithTarget(%q) = %q, %v, want %q", arg, got, err, want)
		}
		if isID := arg == messageURL || arg == "C12345678"; isID && len(e.calls) != 0 {
			t.Errorf("%s: unexpected API calls %v", arg, e.calls)
		}
	}

	// Errors of the work, of the lookup and of the credentials come back.
	target, _ := ParseTarget(e.rt, "C12345678", "")
	boom := errors.New("boom")
	if _, err := WithTarget(context.Background(), e.rt, target, func(Located) (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
	other, _ := ParseTarget(e.rt, "https://other.slack.com/archives/C12345678/p1700000000000100", "")
	_, err := WithTarget(context.Background(), e.rt, other, func(Located) (int, error) { return 0, nil })
	wantErr(t, err, `no Slack credentials for workspace "https://other.slack.com"`)
}

func TestReplyThreadTS(t *testing.T) {
	e := newEnv(t, acme)
	message := map[string]any{"ts": messageTS, "text": "hi"}
	e.reply = func(method string, _ map[string]any) (map[string]any, error) {
		if method == "conversations.history" {
			return map[string]any{"messages": []any{message}}, nil
		}
		return map[string]any{}, nil
	}
	client := slackapi.NewFake(func(_ context.Context, method string, params map[string]any) (map[string]any, error) {
		return e.reply(method, params)
	})
	url, _ := ParseTarget(e.rt, messageURL, "")

	// A message outside a thread starts one; a reply stays in its thread.
	if got, err := url.ReplyThreadTS(context.Background(), client); err != nil || got != messageTS {
		t.Errorf("top-level message: %q, %v", got, err)
	}
	message["thread_ts"] = "1700000000.000001"
	if got, err := url.ReplyThreadTS(context.Background(), client); err != nil || got != "1700000000.000001" {
		t.Errorf("reply: %q, %v", got, err)
	}
	channel, _ := ParseTarget(e.rt, "C12345678", "")
	if got, err := channel.ReplyThreadTS(context.Background(), client); err != nil || got != "" {
		t.Errorf("channel: %q, %v", got, err)
	}
}

func TestParseTS(t *testing.T) {
	for raw, want := range map[string]string{"": "", "  ": "", " 1770165109.628379 ": "1770165109.628379"} {
		if got, err := ParseTS("--thread-ts", raw); err != nil || got != want {
			t.Errorf("ParseTS(%q) = %q, %v, want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"yesterday", "1770165109", "p1770165109628379", "1770165109.628379x"} {
		_, err := ParseTS("--thread-ts", raw)
		wantErr(t, err, "invalid --thread-ts value")
	}
}

// --thread-ts may repeat the thread a message URL designates, but not
// contradict it; for the other targets it is the thread.
func TestThreadTS(t *testing.T) {
	e := newEnv(t, acme)
	const root = "1700000000.000001"
	client := slackapi.NewFake(func(_ context.Context, method string, _ map[string]any) (map[string]any, error) {
		if method == "conversations.history" {
			return map[string]any{"messages": []any{map[string]any{"ts": messageTS, "thread_ts": root, "text": "hi"}}}, nil
		}
		return map[string]any{}, nil
	})
	ctx := context.Background()
	url, _ := ParseTarget(e.rt, messageURL, "")
	for _, flag := range []string{"", root} {
		if got, err := url.ThreadTS(ctx, client, flag); err != nil || got != root {
			t.Errorf("URL, --thread-ts %q: %q, %v, want %q", flag, got, err, root)
		}
	}
	// Not even the ts of the linked reply itself: it is not its thread.
	for _, flag := range []string{"1699999999.000001", messageTS} {
		_, err := url.ThreadTS(ctx, client, flag)
		wantErr(t, err, "--thread-ts "+flag+" contradicts the message URL, whose thread is "+root)
	}
	for _, arg := range []string{"C12345678", "U12345678"} {
		target, _ := ParseTarget(e.rt, arg, "")
		for _, flag := range []string{"", root} {
			if got, err := target.ThreadTS(ctx, client, flag); err != nil || got != flag {
				t.Errorf("%s, --thread-ts %q: %q, %v", arg, flag, got, err)
			}
		}
	}
}

func TestCheckOptionalLimit(t *testing.T) {
	run := func(args ...string) error {
		var limit int
		cmd := &cobra.Command{Use: "list", RunE: func(cmd *cobra.Command, _ []string) error {
			return CheckOptionalLimit(cmd, limit)
		}}
		cmd.Flags().IntVar(&limit, "limit", 0, "")
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		return cmd.Execute()
	}
	for _, args := range [][]string{nil, {"--limit", "1"}, {"--limit=20"}} {
		if err := run(args...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	for _, value := range []string{"0", "-1"} {
		wantErr(t, run("--limit", value), "invalid --limit value "+value+": must be a positive integer")
	}
	// Not an integer: refused by the flag itself, as on every other
	// command ("12abc" was once accepted as 12).
	for _, value := range []string{"12abc", "abc", "1.5", ""} {
		wantErr(t, run("--limit", value), `invalid argument "`+value+`" for "--limit" flag`)
	}
}

type printed struct {
	OK     bool             `json:"ok"`
	Items  []map[string]any `json:"items"`
	Files  []string         `json:"files,omitempty"`
	Cursor string           `json:"next_cursor"`
	Hidden string           `json:"-"`
}

// A list command's collection is printed as [] when empty, in its place;
// everything else is pruned as usual.
func TestPrintList(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
		keys []string
		want string
	}{
		{"empty collection is kept in field order", printed{OK: true}, []string{"items"},
			"{\n  \"ok\": true,\n  \"items\": []\n}\n"},
		{"between its neighbours, omitempty included", &printed{OK: true, Cursor: "c1"}, []string{"items", "files"},
			"{\n  \"ok\": true,\n  \"items\": [],\n  \"files\": [],\n  \"next_cursor\": \"c1\"\n}\n"},
		{"a collection whose entries all prune away", printed{OK: true, Items: []map[string]any{{"a": ""}}}, []string{"items"},
			"{\n  \"ok\": true,\n  \"items\": []\n}\n"},
		{"non-empty output is untouched", printed{OK: true, Items: []map[string]any{{"id": "1", "tags": []string{"<b>&"}, "blank": ""}}}, []string{"items"},
			"{\n  \"ok\": true,\n  \"items\": [\n    {\n      \"id\": \"1\",\n      \"tags\": [\n        \"<b>&\"\n      ]\n    }\n  ]\n}\n"},
		{"nested values keep their indentation", printed{OK: true, Items: []map[string]any{{"id": "1"}}}, []string{"files"},
			"{\n  \"ok\": true,\n  \"items\": [\n    {\n      \"id\": \"1\"\n    }\n  ],\n  \"files\": []\n}\n"},
		{"everything pruned", struct {
			Channels []string `json:"channels"`
		}{}, []string{"channels"}, "{\n  \"channels\": []\n}\n"},
		{"map: sorted position", map[string]any{"ok": true, "zebra": 1, "items": nil}, []string{"items"},
			"{\n  \"items\": [],\n  \"ok\": true,\n  \"zebra\": 1\n}\n"},
		{"map without the key: last", map[string]any{"ok": true}, []string{"items"},
			"{\n  \"ok\": true,\n  \"items\": []\n}\n"},
		{"no keys: plain Print", printed{OK: true}, nil, "{\n  \"ok\": true\n}\n"},
		{"not an object", []string{"a"}, []string{"items"}, "[\n  \"a\"\n]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, acme)
			if err := PrintList(e.rt, tc.v, tc.keys...); err != nil {
				t.Fatal(err)
			}
			if got := e.rt.Stdout.(*bytes.Buffer).String(); got != tc.want {
				t.Errorf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestCheckLimit(t *testing.T) {
	for _, n := range []int{1, 10, 1000} {
		if err := CheckLimit(n, 1000, "page with --cursor"); err != nil {
			t.Errorf("CheckLimit(%d) = %v", n, err)
		}
	}
	for _, n := range []int{0, -5, 1001} {
		wantErr(t, CheckLimit(n, 1000, "page with --cursor"), "must be between 1 and 1000 (page with --cursor)")
	}
}

func TestCheckMaxChars(t *testing.T) {
	for _, n := range []int{-1, 1, 8000} {
		if err := CheckMaxChars("--max-body-chars", n); err != nil {
			t.Errorf("CheckMaxChars(%d) = %v", n, err)
		}
	}
	for _, n := range []int{0, -2} {
		wantErr(t, CheckMaxChars("--max-body-chars", n), "invalid --max-body-chars value")
	}
}

func TestAttachPaths(t *testing.T) {
	got := AttachPaths([]string{" a.txt ", "", "b.txt", "a.txt", "  "})
	if want := []string{"a.txt", "b.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := AttachPaths(nil); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestIsDashText(t *testing.T) {
	for arg, want := range map[string]bool{
		"- item":                   true,
		"- one\n- two":             true,
		"-- note":                  true,
		"-\tindented":              true,
		"-> see a=b":               true,
		"-":                        false,
		"--":                       false,
		"-x":                       false,
		"--thread-ts":              false,
		"--blocks=/path with/b.js": false,
		"plain - text":             false,
		"":                         false,
	} {
		if got := isDashText(arg); got != want {
			t.Errorf("isDashText(%q) = %v, want %v", arg, got, want)
		}
	}
}

// dashCommand is a command with the shape of those that take text, and
// what its RunE last received.
type dashCommand struct {
	args            []string
	thread          string
	loud, quiet     bool
	needed          string
	ran             bool
	stdout          bytes.Buffer
	requireTheFlags bool
}

func (d *dashCommand) run(args ...string) error {
	d.args, d.thread, d.loud, d.quiet, d.ran = nil, "", false, false, false
	cmd := &cobra.Command{
		Use:  "write <target> [text]",
		Long: "Writes text.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			d.args, d.ran = args, true
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().StringVar(&d.thread, "thread-ts", "", "")
	cmd.Flags().BoolVar(&d.loud, "loud", false, "")
	cmd.Flags().BoolVar(&d.quiet, "quiet", false, "")
	cmd.MarkFlagsMutuallyExclusive("loud", "quiet")
	if d.requireTheFlags {
		cmd.Flags().StringVar(&d.needed, "needed", "", "")
		_ = cmd.MarkFlagRequired("needed")
	}
	d.stdout.Reset()
	cmd.SetOut(&d.stdout)
	cmd.SetErr(&d.stdout)
	cmd.SetArgs(args)
	return AllowDashText(cmd).Execute()
}

func TestAllowDashText(t *testing.T) {
	const list = "- one\n- two"
	d := &dashCommand{}
	for _, tt := range []struct {
		name   string
		args   []string
		want   []string
		thread string
		loud   bool
	}{
		{"plain text", []string{"#general", "hello"}, []string{"#general", "hello"}, "", false},
		{"a list", []string{"#general", list}, []string{"#general", list}, "", false},
		{"a list among flags", []string{"--loud", "#general", list, "--thread-ts", "1.2"}, []string{"#general", list}, "1.2", true},
		{"an arrow", []string{"#general", "-> see a=b"}, []string{"#general", "-> see a=b"}, "", false},
		{"dash text as a flag value", []string{"#general", "--thread-ts", "- x", "hi"}, []string{"#general", "hi"}, "- x", false},
		{"after --", []string{"--", "#general", "--not-a-flag"}, []string{"#general", "--not-a-flag"}, "", false},
		{"--flag=value", []string{"#general", "hi", "--thread-ts=1.2"}, []string{"#general", "hi"}, "1.2", false},
	} {
		if err := d.run(tt.args...); err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if !reflect.DeepEqual(d.args, tt.want) || d.thread != tt.thread || d.loud != tt.loud {
			t.Errorf("%s: args = %q, thread = %q, loud = %v", tt.name, d.args, d.thread, d.loud)
		}
	}

	// Mistakes are reported as cobra reports them, and nothing runs.
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"an unknown flag", []string{"#general", "--not-a-flag"}, "unknown flag: --not-a-flag"},
		{"an unknown shorthand", []string{"#general", "-x"}, "unknown shorthand flag"},
		{"a flag without its value", []string{"#general", "hi", "--thread-ts"}, "flag needs an argument: --thread-ts"},
		{"too few arguments", nil, "accepts between 1 and 2 arg(s), received 0"},
		{"too many arguments", []string{"a", "b", "- c"}, "accepts between 1 and 2 arg(s), received 3"},
		{"mutually exclusive flags", []string{"#general", "hi", "--loud", "--quiet"}, "none of the others can be"},
	} {
		err := d.run(tt.args...)
		if err == nil || !strings.Contains(err.Error(), tt.want) || d.ran {
			t.Errorf("%s: err = %v (ran: %v), want one containing %q", tt.name, err, d.ran, tt.want)
		}
	}

	d.requireTheFlags = true
	wantErr(t, d.run("#general", list), `required flag(s) "needed" not set`)
	if err := d.run("#general", list, "--needed", "x"); err != nil || d.needed != "x" {
		t.Errorf("required flag given: %v", err)
	}
}

func TestAllowDashTextPrintsHelp(t *testing.T) {
	d := &dashCommand{}
	for _, flag := range []string{"--help", "-h"} {
		if err := d.run("#general", flag); err != nil || d.ran {
			t.Errorf("%s: err = %v, ran = %v", flag, err, d.ran)
		}
		if out := d.stdout.String(); !strings.Contains(out, "Writes text.") || !strings.Contains(out, "--thread-ts") {
			t.Errorf("%s printed %q", flag, out)
		}
	}
}
