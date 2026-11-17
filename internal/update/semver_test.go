package update

import "testing"

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		// Ported from test/update.test.ts.
		{"equal versions return 0", "1.2.3", "1.2.3", 0},
		{"strips v prefix", "v1.2.3", "1.2.3", 0},
		{"newer major", "2.0.0", "1.9.9", 1},
		{"newer minor", "1.3.0", "1.2.9", 1},
		{"newer patch", "1.2.4", "1.2.3", 1},
		{"older version returns negative", "0.1.0", "0.2.0", -1},
		{"handles 0.x versions (greater)", "0.2.10", "0.2.9", 1},
		{"handles 0.x versions (equal)", "0.2.10", "0.2.10", 0},
		{"handles 0.x versions (less)", "0.2.10", "0.3.0", -1},

		// The TypeScript turned "1-rc" into NaN and called it equal.
		{"v prefix on both", "v1.2.4", "v1.2.3", 1},
		{"missing patch is zero", "1.2", "1.2.0", 0},
		{"pre-release is older than its release", "1.0.0-rc.1", "1.0.0", -1},
		{"release is newer than its pre-release", "v1.0.0", "v1.0.0-rc.1", 1},
		{"pre-release of a newer version wins", "1.1.0-rc.0", "1.0.9", 1},
		{"numeric identifiers compare numerically", "1.0.0-rc.10", "1.0.0-rc.9", 1},
		{"numeric identifier is below alphanumeric", "1.0.0-1", "1.0.0-alpha", -1},
		{"alphanumeric identifiers compare in ASCII order", "1.0.0-alpha", "1.0.0-beta", -1},
		{"more identifiers win on a tie", "1.0.0-alpha.1", "1.0.0-alpha", 1},
		{"build metadata is ignored", "1.0.0+abc", "1.0.0+def", 0},
		{"large components do not lose precision", "1.0.9007199254740993", "1.0.9007199254740992", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompareSemver(tt.a, tt.b)
			if err != nil {
				t.Fatalf("CompareSemver(%q, %q): %v", tt.a, tt.b, err)
			}
			if sign(got) != tt.want {
				t.Errorf("CompareSemver(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
			}
			back, _ := CompareSemver(tt.b, tt.a)
			if sign(back) != -tt.want {
				t.Errorf("CompareSemver(%q, %q) = %d, want sign %d", tt.b, tt.a, back, -tt.want)
			}
		})
	}
}

func TestCompareSemverRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "dev", "(devel)", "v", "1.2.3.4", "1..3", "1.x.3", "-1.2.3", "1.2.3-", "1.2.3-rc..1", "1.2.3+", "1.2.3-rc_1", "99999999999999999999.0.0", "1.2.3\x1b[31m"} {
		if _, err := CompareSemver(s, "1.0.0"); err == nil {
			t.Errorf("CompareSemver(%q, …) succeeded, want an error", s)
		}
		if isNewer(s, "1.0.0") || isNewer("1.0.0", s) {
			t.Errorf("isNewer with %q reported an update", s)
		}
	}
}

func TestIsReleaseVersion(t *testing.T) {
	tests := map[string]bool{
		"v1.2.3":                               true,
		"1.2.3":                                true,
		"v1.2.3-rc.1":                          true,
		"dev":                                  false,
		"(devel)":                              false,
		"":                                     false,
		"v1.2.3+dirty":                         false,
		"v0.0.0-20260102150405-0123456789ab":   false,
		"v1.2.4-0.20260102150405-0123456789ab": false,
		"v1.2.4-rc.1.0.20260102150405-0123456789ab": false,
	}
	for s, want := range tests {
		if got := isReleaseVersion(s); got != want {
			t.Errorf("isReleaseVersion(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestSemverString(t *testing.T) {
	for in, want := range map[string]string{"v1.2.3": "1.2.3", " 1.2 ": "1.2.0", "v1.0.0-rc.1+b5": "1.0.0-rc.1+b5"} {
		v, err := parseSemver(in)
		if err != nil || v.String() != want {
			t.Errorf("parseSemver(%q).String() = %q, %v; want %q", in, v, err, want)
		}
	}
}
