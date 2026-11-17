package authextract

import (
	"strings"
	"testing"
)

func TestParseCurl(t *testing.T) {
	for _, tc := range []struct {
		name, input, wantURL string
	}{
		{
			name: "Chrome, bash, -b cookie and multipart body",
			input: `curl 'https://acme.slack.com/api/client.counts?_x_id=abc&slack_route=T1' \
  -H 'accept: */*' \
  -H 'content-type: multipart/form-data; boundary=----WebKitFormBoundaryX' \
  -b 'b=.123; x=1; d=xoxd-abc%2Bdef%2Fghi%3D%3D; d-s=1700000000; lc=1' \
  -H 'origin: https://app.slack.com' \
  --data-raw $'------WebKitFormBoundaryX\r\nContent-Disposition: form-data; name="token"\r\n\r\nxoxc-1-2-3-abcdef\r\n------WebKitFormBoundaryX--\r\n'`,
			wantURL: "https://acme.slack.com",
		},
		{
			name: "Firefox, Cookie header, options before the URL",
			input: `curl -X POST --compressed 'https://acme.slack.com/api/conversations.history' ` +
				`-H 'User-Agent: Mozilla/5.0' -H 'Cookie: d=xoxd-abc%2Bdef%2Fghi%3D%3D; b=1' ` +
				`--data-raw 'token=xoxc-1-2-3-abcdef&channel=C1'`,
			wantURL: "https://acme.slack.com",
		},
		{
			name: "double quotes, lower-case header, JSON body",
			input: `curl "https://acme.slack.com/api/x" -H "cookie: utm=1; d=xoxd-abc%2Bdef%2Fghi%3D%3D" ` +
				`--data '{"token":"xoxc-1-2-3-abcdef"}'`,
			wantURL: "https://acme.slack.com",
		},
		{
			name: "Windows cmd flavour",
			input: `curl ^"https://acme.slack.com/api/client.counts^" ^
  -H ^"cookie: b=1; d=xoxd-abc^%2Bdef^%2Fghi^%3D^%3D; lc=2^" ^
  --data-raw ^"token=xoxc-1-2-3-abcdef^"`,
			wantURL: "https://acme.slack.com",
		},
		{
			name:    "Enterprise Grid host",
			input:   `curl 'https://acme.enterprise.slack.com/api/x' -b 'd=xoxd-abc%2Bdef%2Fghi%3D%3D' --data 'token=xoxc-1-2-3-abcdef'`,
			wantURL: "https://acme.enterprise.slack.com",
		},
		{
			name:    "already decoded cookie",
			input:   `curl 'https://acme.slack.com/api/x' -b 'd=xoxd-abc+def/ghi==' --data 'token=xoxc-1-2-3-abcdef'`,
			wantURL: "https://acme.slack.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCurl(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			assertTeams(t, got.Teams, Team{URL: tc.wantURL, Token: "xoxc-1-2-3-abcdef"})
			if got.CookieD != testCookie {
				t.Errorf("CookieD = %q, want %q", got.CookieD, testCookie)
			}
		})
	}
}

func TestParseCurlErrors(t *testing.T) {
	const cookie = ` -b 'd=xoxd-abc' `
	const token = ` --data 'token=xoxc-1-2-3'`
	for _, tc := range []struct{ name, input, want string }{
		{"empty", "", "request URL"},
		{"not Slack", `curl 'https://example.com/api/x'` + cookie + token, "not an https request to Slack"},
		{"look-alike host", `curl 'https://evil.example/x.slack.com/api'` + cookie + token, "not an https request to Slack"},
		{"plain http", `curl 'http://acme.slack.com/api/x'` + cookie + token, "not an https request to Slack"},
		{"shared host", `curl 'https://edgeapi.slack.com/cache/T1/users/info'` + cookie + token, "does not identify a workspace"},
		{"web client host", `curl 'https://app.slack.com/client/T1'` + cookie + token, "does not identify a workspace"},
		{"no cookie", `curl 'https://acme.slack.com/api/x' -b 'b=1; lc=2'` + token, "xoxd cookie"},
		{"cookie of another name", `curl 'https://acme.slack.com/api/x' -b 'ssid=xoxd-abc'` + token, "xoxd cookie"},
		{"no token", `curl 'https://acme.slack.com/api/x'` + cookie, "xoxc token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCurl(tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			// Errors are shown to the user and may be logged.
			for _, secret := range []string{"xoxd-abc", "xoxc-1-2-3"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaks a secret: %v", err)
				}
			}
		})
	}
}
