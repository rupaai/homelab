package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b   string
		want   int
		wantOK bool
	}{
		{"build-2", "build-10", -1, true},
		{"build-10", "build-2", 1, true},
		{"build-7", "build-7", 0, true},
		{"build-19", "v0.20.0", -1, true},
		{"v0.20.0", "build-25", 1, true},
		{"v0.9.0", "v0.10.0", -1, true},
		{"v0.10.0", "v0.9.0", 1, true},
		{"v1.0.0", "v0.99.99", 1, true},
		{"v1.2.3", "v1.2.4", -1, true},
		{"v1.2.3", "v1.2.3", 0, true},
		{"v0.0.0", "build-1", 1, true},
		{"dev", "build-1", 0, false},
		{"build-1", "pr-5-abc1234", 0, false},
		{"v0.20.0-pr.27", "v0.20.0", 0, false},
	}
	for _, c := range cases {
		got, ok := Compare(c.a, c.b)
		if got != c.want || ok != c.wantOK {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d, %v", c.a, c.b, got, ok, c.want, c.wantOK)
		}
	}
}

func TestReleaseVersionShapes(t *testing.T) {
	good := []string{"build-1", "build-12", "v0.0.0", "v0.20.0", "v10.2.30"}
	bad := []string{"", "dev", "build-0", "build-", "build-01", "build-1a", "build--1", "build-+1",
		"v1", "v1.2", "v1.2.3.4", "v01.2.3", "v1.02.3", "v1.2.03", "v1.2.x", "v1.2.-3", "v1.2.3-rc1",
		"v1.2.3-pr.5", "1.2.3", "V1.2.3", "v 1.2.3", "pr-5-abc1234", "v99999999999.0.0"}
	for _, v := range good {
		if _, ok := parseRelease(v); !ok {
			t.Errorf("%q should be a release version", v)
		}
	}
	for _, v := range bad {
		if _, ok := parseRelease(v); ok {
			t.Errorf("%q should not be a release version", v)
		}
	}
}

func TestBuildNumber(t *testing.T) {
	for v, want := range map[string]int{"build-12": 12, "build-0": 0, "v1.2.3": 0, "dev": 0} {
		if n, ok := BuildNumber(v); n != want || ok != (want > 0) {
			t.Errorf("BuildNumber(%q) = %d, %v", v, n, ok)
		}
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"build-3", "build-2", true},
		{"build-2", "build-2", false},
		{"build-1", "build-2", false},
		{"build-3", "70200a1-dirty", true},
		{"v1", "build-2", false},
		{"", "dev", false},
		// from builds to v versions
		{"v0.20.0", "build-19", true},
		{"v0.20.0", "build-9999", true},
		{"build-25", "v0.20.0", false},
		{"build-25", "v0.0.1", false},
		// v versions
		{"v0.10.0", "v0.9.0", true},
		{"v0.9.0", "v0.10.0", false},
		{"v0.20.0", "v0.20.0", false},
		{"v0.20.1", "v0.20.0", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.20.0", "v0.20.1", false},
		// a Homelab that isn't on a release version takes any release
		{"v0.20.0", "dev", true},
		{"v0.20.0", "pr-5-abc1234", true},
		{"v0.20.0", "v0.19.0-pr.5.abc1234", true},
		{"build-3", "v0.19.0-pr.5", true},
		// only release versions are offered
		{"pr-5-abc1234", "build-2", false},
		{"v0.21.0-pr.27", "v0.20.0", false},
		{"v0.21.0-pr.27", "dev", false},
		{"v0.21", "build-2", false},
	}
	for _, c := range cases {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestTestPR(t *testing.T) {
	cases := map[string]int{
		"pr-12-1a2b3c4":         12,
		"pr-7-abc":              7,
		"v0.20.0-pr.27":         27,
		"v0.20.0-pr.27.abc1234": 27,
		"build-12-pr.3":         0,
		"pr-12":                 0,
		"pr-12-":                0,
		"pr-x-1":                0,
		"pr-0-a":                0,
		"pr-+5-a":               0,
		"build-3":               0,
		"v0.20.0":               0,
		"dev":                   0,
		"v0.20.0-pr.":           0,
		"v0.20.0-pr.0":          0,
		"v0.20.0-pr.x":          0,
		"v0.20.0-pr.27.":        0,
		"v0.20.0-pr.27.a.b":     0,
		"v0.20-pr.27":           0,
		"v01.2.3-pr.27":         0,
		"0.20.0-pr.27":          0,
		"v0.20.0-rc.27":         0,
	}
	for v, want := range cases {
		n, ok := TestPR(v)
		if n != want || ok != (want > 0) {
			t.Errorf("TestPR(%q) = %d, %v; want %d", v, n, ok, want)
		}
		if _, isRelease := parseRelease(v); isRelease && want > 0 {
			t.Errorf("%q is a test version and must not be a release version", v)
		}
	}
}

func TestPruneOrder(t *testing.T) {
	home := installed(t, "build-1")
	bin := BinDir(home)
	// Old builds, v versions out of numeric order, and a version nothing can rank.
	all := []string{"build-30", "build-2", "v0.9.0", "v0.10.0", "v0.2.0", "build-100", "dev", "pr-5-abc1234"}
	for _, v := range all {
		if _, err := Install(bin, v, script(v)); err != nil {
			t.Fatal(err)
		}
	}
	prune(bin)
	kept := map[string]bool{"v0.10.0": true, "v0.9.0": true, "v0.2.0": true, "build-1": true}
	for _, v := range append(all, "build-1") {
		_, err := os.Stat(filepath.Join(bin, VersionFile(v)))
		if kept[v] && err != nil {
			t.Errorf("%s was deleted", v)
		}
		if !kept[v] && err == nil {
			t.Errorf("%s was kept", v)
		}
	}
}
