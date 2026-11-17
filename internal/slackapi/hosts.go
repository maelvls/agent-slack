package slackapi

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// IsWorkspaceHost reports whether host is a Slack workspace host, i.e. one
// whose /api/ endpoints may receive a token and the "d" cookie: slack.com,
// slack-gov.com and their subdomains. It is the single definition of "a
// workspace URL points at Slack" (see WorkspaceOrigin).
func IsWorkspaceHost(host string) bool {
	return hostIn(host, "slack.com", "slack-gov.com")
}

// IsSlackHost reports whether host is owned by Slack: a workspace host
// (IsWorkspaceHost) or one of the hosts that serve files to an
// authenticated request (slack-edge.com, slack-files.com).
func IsSlackHost(host string) bool {
	return IsWorkspaceHost(host) || hostIn(host, "slack-edge.com", "slack-files.com")
}

func hostIn(host string, domains ...string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, domain := range domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// WorkspaceOrigin reduces a workspace URL to its origin
// ("https://acme.slack.com"), whatever its path. It refuses anything that
// credentials must not be sent to: a scheme other than https, a host that
// is not a Slack workspace host (IsWorkspaceHost), a port or user info.
func WorkspaceOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid workspace URL %q: expected a URL such as https://acme.slack.com", raw)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("invalid workspace URL %q: it must use https", raw)
	}
	if u.User != nil || u.Port() != "" {
		return "", fmt.Errorf("invalid workspace URL %q: it must not carry user info or a port", raw)
	}
	if !IsWorkspaceHost(u.Hostname()) {
		return "", fmt.Errorf("invalid workspace URL %q: %s is not a Slack host (expected <workspace>.slack.com or <workspace>.slack-gov.com); credentials are only sent to Slack", raw, u.Hostname())
	}
	return "https://" + strings.ToLower(strings.TrimSuffix(u.Hostname(), ".")), nil
}

// isLoopbackURL reports whether u points at this machine (an httptest
// server).
func isLoopbackURL(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
