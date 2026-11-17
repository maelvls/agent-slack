package update

import (
	"cmp"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// semver is a parsed semantic version (https://semver.org).
type semver struct {
	major, minor, patch uint64
	pre                 []string // pre-release identifiers, e.g. ["rc", "1"]
	build               string   // build metadata, ignored for precedence
}

var identifiersRE = regexp.MustCompile(`^[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*$`)

// parseSemver accepts "1.2.3", "v1.2.3", "1.2.3-rc.1" and "1.2.3+build".
// Like the predecessor it tolerates a missing minor or patch ("1.2").
// Anything else is an error rather than a silent zero: "dev" must never
// compare as 0.0.0.
func parseSemver(s string) (semver, error) {
	var v semver
	rest := strings.TrimPrefix(strings.TrimSpace(s), "v")
	invalid := fmt.Errorf("invalid version %q", s)

	if core, build, ok := strings.Cut(rest, "+"); ok {
		if !identifiersRE.MatchString(build) {
			return v, invalid
		}
		rest, v.build = core, build
	}
	if core, pre, ok := strings.Cut(rest, "-"); ok {
		if !identifiersRE.MatchString(pre) {
			return v, invalid
		}
		rest, v.pre = core, strings.Split(pre, ".")
	}

	parts := strings.Split(rest, ".")
	if len(parts) > 3 {
		return v, invalid
	}
	var nums [3]uint64
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return v, invalid
		}
		nums[i] = n
	}
	v.major, v.minor, v.patch = nums[0], nums[1], nums[2]
	return v, nil
}

// String renders the version without a "v" prefix.
func (v semver) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
	if len(v.pre) > 0 {
		s += "-" + strings.Join(v.pre, ".")
	}
	if v.build != "" {
		s += "+" + v.build
	}
	return s
}

// compare orders two versions by semver precedence.
func (v semver) compare(o semver) int {
	if c := cmp.Or(cmp.Compare(v.major, o.major), cmp.Compare(v.minor, o.minor), cmp.Compare(v.patch, o.patch)); c != 0 {
		return c
	}
	// A version without pre-release ranks above any of its pre-releases.
	switch {
	case len(v.pre) == 0 && len(o.pre) == 0:
		return 0
	case len(v.pre) == 0:
		return 1
	case len(o.pre) == 0:
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(o.pre); i++ {
		if c := compareIdentifier(v.pre[i], o.pre[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(v.pre), len(o.pre))
}

// compareIdentifier compares two pre-release identifiers: numeric ones
// numerically and below alphanumeric ones, the rest in ASCII order.
func compareIdentifier(a, b string) int {
	na, errA := strconv.ParseUint(a, 10, 64)
	nb, errB := strconv.ParseUint(b, 10, 64)
	switch {
	case errA == nil && errB == nil:
		return cmp.Compare(na, nb)
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}

// CompareSemver compares two semantic versions (an optional "v" prefix is
// ignored, pre-releases rank below their release, build metadata is
// ignored). It returns a negative number if a < b, zero if they are equal
// and a positive number if a > b, or an error if either is not a version.
func CompareSemver(a, b string) (int, error) {
	va, err := parseSemver(a)
	if err != nil {
		return 0, err
	}
	vb, err := parseSemver(b)
	if err != nil {
		return 0, err
	}
	return va.compare(vb), nil
}

// isNewer reports whether latest is a strictly newer version than current.
// It is false when either is not a version, so that a "dev" build or a
// corrupted cache never claims an update exists.
func isNewer(latest, current string) bool {
	c, err := CompareSemver(latest, current)
	return err == nil && c > 0
}

// pseudoVersionRE matches the timestamp-commit suffix of a Go
// pseudo-version (v0.0.0-20260102150405-0123456789ab).
var pseudoVersionRE = regexp.MustCompile(`(^|[.-])\d{14}-[0-9a-f]{12}$`)

// isReleaseVersion reports whether s names a tagged version, as opposed to
// "(devel)", "dev", a pseudo-version or a build from a dirty checkout.
func isReleaseVersion(s string) bool {
	v, err := parseSemver(s)
	if err != nil || v.build != "" {
		return false
	}
	return !pseudoVersionRE.MatchString(strings.Join(v.pre, "."))
}
