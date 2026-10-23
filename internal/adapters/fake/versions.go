package fake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/rupaai/homelab/internal/core"
)

// Versions is a fake GitHub that makes release versions. The first prod release is First
// (v0.3.0 unless set); every later prod release is the next minor (v0.4.0, v0.5.0...), and
// dev builds are -dev.N pre-releases of the next prod version. Asking again for a commit
// gives the version it has.
type Versions struct {
	mu sync.Mutex
	// First is the first prod version.
	First string
	// Releases is what GitHub lists, newest first. A test may fill it in.
	Releases []core.ReleaseInfo
	// Err is what every call fails with, e.g. core.ErrGitHubRefused.
	Err error
	// Calls records "publish dev <commit>", "preview prod <commit>" and "list".
	Calls []string
	// Tokens are the tokens the calls came with.
	Tokens []string

	byCommit map[string]string // "channel/commit" -> version
}

func NewVersions() *Versions {
	return &Versions{First: "v0.3.0", byCommit: map[string]string{}}
}

// Set makes a call fail (or work again, with nil).
func (v *Versions) Set(err error) {
	v.mu.Lock()
	v.Err = err
	v.mu.Unlock()
}

// Called says how many calls started with prefix.
func (v *Versions) Called(prefix string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := 0
	for _, c := range v.Calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (v *Versions) lastProd() string {
	for _, r := range v.Releases {
		if !r.Prerelease {
			return r.Tag
		}
	}
	return ""
}

func (v *Versions) Publish(_ context.Context, _, commit, channel, token string, dryRun bool) (core.VersionResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	kind := "publish"
	if dryRun {
		kind = "preview"
	}
	v.Calls = append(v.Calls, fmt.Sprintf("%s %s %s", kind, channel, commit))
	v.Tokens = append(v.Tokens, token)
	if v.Err != nil {
		return core.VersionResult{}, v.Err
	}
	if token == "" {
		return core.VersionResult{}, errors.New("no token")
	}
	last := v.lastProd()
	res := core.VersionResult{Released: true, Previous: last, Summary: "1 new", Notes: "## New\n\n- something (#1)\n"}
	next := v.First
	if last != "" {
		var major, minor, patch int
		fmt.Sscanf(last, "v%d.%d.%d", &major, &minor, &patch)
		next = fmt.Sprintf("v%d.%d.0", major, minor+1)
	}
	if known, ok := v.byCommit[channel+"/"+commit]; ok {
		res.Version, res.Existing = known, true
		return res, nil
	}
	res.Version = next
	if channel == core.Dev {
		n := 1
		for _, r := range v.Releases {
			if strings.HasPrefix(r.Tag, next+"-dev.") {
				n++
			}
		}
		res.Version = fmt.Sprintf("%s-dev.%d", next, n)
	}
	if !dryRun {
		v.byCommit[channel+"/"+commit] = res.Version
		v.Releases = append([]core.ReleaseInfo{{Tag: res.Version, Prerelease: channel == core.Dev, Commit: commit,
			Summary: res.Summary, Notes: res.Notes}}, v.Releases...)
	}
	return res, nil
}

func (v *Versions) List(_ context.Context, _, token string) ([]core.ReleaseInfo, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.Calls = append(v.Calls, "list")
	v.Tokens = append(v.Tokens, token)
	if v.Err != nil {
		return nil, v.Err
	}
	return append([]core.ReleaseInfo(nil), v.Releases...), nil
}

var _ core.Versions = (*Versions)(nil)
