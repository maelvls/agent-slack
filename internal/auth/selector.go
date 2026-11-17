package auth

import "strings"

// SelectWorkspace resolves a --workspace selector against configured
// workspaces. A selector is a workspace URL (exact match) or a
// case-insensitive substring of the URL, host, name or team domain.
//
// It returns the single match, or the list of candidates when the selector
// is ambiguous; both are nil when nothing matches.
func SelectWorkspace(workspaces []Workspace, selector string) (match *Workspace, ambiguous []Workspace) {
	raw := strings.TrimSpace(selector)
	if raw == "" {
		return nil, nil
	}
	if u, err := NormalizeURL(raw); err == nil {
		for i := range workspaces {
			if workspaces[i].URL == u {
				return &workspaces[i], nil
			}
		}
	}
	needle := strings.ToLower(raw)
	var hits []int
	for i, w := range workspaces {
		if workspaceMatches(w.URL, w.Name, w.TeamDomain, needle) {
			hits = append(hits, i)
		}
	}
	switch len(hits) {
	case 0:
		return nil, nil
	case 1:
		return &workspaces[hits[0]], nil
	}
	// Several substring hits: an exact host/name/domain match wins
	// ("acme" must select acme.slack.com even if acme-dev.slack.com exists).
	var exact []int
	for _, i := range hits {
		w := workspaces[i]
		host := hostOf(w.URL)
		for _, c := range []string{host, strings.TrimSuffix(host, ".slack.com"), strings.ToLower(w.Name), strings.ToLower(w.TeamDomain)} {
			if c == needle {
				exact = append(exact, i)
				break
			}
		}
	}
	if len(exact) == 1 {
		return &workspaces[exact[0]], nil
	}
	for _, i := range hits {
		ambiguous = append(ambiguous, workspaces[i])
	}
	return nil, ambiguous
}

// MatchesSelector reports whether a workspace described by url/name matches
// a selector by substring (see SelectWorkspace).
func MatchesSelector(workspaceURL, name, selector string) bool {
	return workspaceMatches(workspaceURL, name, "", strings.ToLower(strings.TrimSpace(selector)))
}

func workspaceMatches(workspaceURL, name, teamDomain, needle string) bool {
	if needle == "" {
		return false
	}
	host := hostOf(workspaceURL)
	for _, c := range []string{
		strings.ToLower(workspaceURL),
		host,
		strings.TrimSuffix(host, ".slack.com"),
		strings.ToLower(name),
		strings.ToLower(teamDomain),
	} {
		if c != "" && strings.Contains(c, needle) {
			return true
		}
	}
	return false
}

func hostOf(workspaceURL string) string {
	u := strings.ToLower(workspaceURL)
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	return u
}
