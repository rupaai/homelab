package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// Versions is core.Versions backed by homelab-services, called as the system caller like
// Secrets. The GitHub token goes in the body of each call, and nowhere else.
type Versions struct{ s *Services }

var _ core.Versions = (*Versions)(nil)

// Versions returns the release versions client.
func (s *Services) Versions() *Versions { return &Versions{s: s} }

// versionsTimeout bounds one call: it makes several calls to GitHub.
const versionsTimeout = 45 * time.Second

func (v *Versions) call(path string, in any, out any) error {
	r, err := v.s.call("releases", errNoServices, versionsTimeout, http.MethodPost, path, nil, in)
	if err != nil {
		var ref *refused
		if errors.As(err, &ref) && ref.status == http.StatusForbidden {
			return fmt.Errorf("%w: %s", core.ErrGitHubRefused, ref.msg)
		}
		return err
	}
	return json.Unmarshal(r.body, out)
}

// Publish picks and makes the version of a commit. Every build gets one, so it asks for a
// release even when no change counts.
func (v *Versions) Publish(_ context.Context, repo, commit, channel, token string, dryRun bool) (core.VersionResult, error) {
	var out core.VersionResult
	err := v.call("/internal/releases/publish", map[string]any{
		"repo": repo, "commit": commit, "channel": channel, "token": token, "always": true, "dry_run": dryRun,
	}, &out)
	return out, err
}

// List gives the repo's releases.
func (v *Versions) List(_ context.Context, repo, token string) ([]core.ReleaseInfo, error) {
	var out struct {
		Releases []core.ReleaseInfo `json:"releases"`
	}
	err := v.call("/internal/releases/list", map[string]any{"repo": repo, "token": token}, &out)
	return out.Releases, err
}
