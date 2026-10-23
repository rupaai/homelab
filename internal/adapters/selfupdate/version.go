package selfupdate

import "strings"

// Release versions are "build-<n>" (the old numbering) and "v<major>.<minor>.<patch>". Every
// v version is newer than every build, builds go by their number, v versions by major, minor
// and patch. Anything else (a test version, "dev") is not a release version.

// relVer is a parsed release version.
type relVer struct {
	semver              bool // v<major>.<minor>.<patch>; false means build-<n>
	major, minor, patch int  // for a build, major holds <n>
}

// number reads a whole number written in digits, with no leading zero (except 0 itself).
func number(s string) (int, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') || len(s) > 9 {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// parseRelease reads "build-12" or "v1.2.3".
func parseRelease(v string) (relVer, bool) {
	if s, ok := strings.CutPrefix(v, "build-"); ok {
		n, ok := number(s)
		return relVer{major: n}, ok && n > 0
	}
	s, ok := strings.CutPrefix(v, "v")
	if !ok {
		return relVer{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return relVer{}, false
	}
	var nums [3]int
	for i, p := range parts {
		if nums[i], ok = number(p); !ok {
			return relVer{}, false
		}
	}
	return relVer{semver: true, major: nums[0], minor: nums[1], patch: nums[2]}, true
}

// compare is -1, 0 or 1 as a is older than, the same as, or newer than b.
func (a relVer) compare(b relVer) int {
	if a.semver != b.semver {
		if a.semver {
			return 1
		}
		return -1
	}
	for _, p := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Compare orders two release versions: -1, 0 or 1 as a is older than, the same as, or newer
// than b. It says false when either isn't a release version.
func Compare(a, b string) (int, bool) {
	ra, ok := parseRelease(a)
	rb, ok2 := parseRelease(b)
	if !ok || !ok2 {
		return 0, false
	}
	return ra.compare(rb), true
}

// Newer says whether latest should replace current. A Homelab that isn't on a release
// version (built by hand, or a test version) always takes a release, and a build never
// replaces a v version.
func Newer(latest, current string) bool {
	l, ok := parseRelease(latest)
	if !ok {
		return false
	}
	c, ok := parseRelease(current)
	return !ok || l.compare(c) > 0
}

// testPR reads a test version's pull request number from "pr-<number>-<commit>" or
// "v<major>.<minor>.<patch>-pr.<number>[.<commit>]".
func testPR(v string) (int, bool) {
	if s, ok := strings.CutPrefix(v, testPrefix); ok {
		num, commit, ok := strings.Cut(s, "-")
		if !ok || commit == "" {
			return 0, false
		}
		return prNumber(num)
	}
	base, s, ok := strings.Cut(v, "-pr.")
	if !ok {
		return 0, false
	}
	if _, ok := parseRelease(base); !ok || !strings.HasPrefix(base, "v") {
		return 0, false
	}
	num, commit, hasCommit := strings.Cut(s, ".")
	if hasCommit && (commit == "" || strings.Contains(commit, ".")) {
		return 0, false
	}
	return prNumber(num)
}

func prNumber(s string) (int, bool) {
	n, ok := number(s)
	return n, ok && n > 0
}
