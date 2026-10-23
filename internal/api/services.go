package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
)

// servicePaths are the parts of the API that homelab-services serves (platform services).
// Homelab decides who is calling, then forwards the call with that caller signed.
//
// /api/secrets is for an app's own key only (the secrets API). Homelab refuses everyone else
// before forwarding, and homelab-services refuses them again.
var servicePaths = []string{"/api/files", "/api/db", "/api/data", "/api/secrets"}

// secretsPath is the part of the API that apps use for their secrets.
const secretsPath = "/api/secrets"

// isSecretsAPI says whether a path belongs to the secrets API.
func isSecretsAPI(path string) bool {
	return path == secretsPath || strings.HasPrefix(path, secretsPath+"/")
}

const msgSecretsAppOnly = "Secrets can only be used by the app itself, with its own key (HOMELAB_TOKEN)."

// isServicePath says whether a path belongs to homelab-services.
func isServicePath(path string) bool {
	for _, p := range servicePaths {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// storage lists the disks and how full they are, for the Files screen.
func (s *server) storage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.hub.Disks(r.Context()))
}

// personToServices forwards a companion call. The guard has already checked the person.
func (s *server) personToServices(w http.ResponseWriter, r *http.Request) {
	// People use the companion's own secrets screens, never the app's secrets API.
	if isSecretsAPI(r.URL.Path) {
		writeErr(w, http.StatusForbidden, msgSecretsAppOnly)
		return
	}
	dropCopyParam(r)
	if !s.copyBeforeWrite(w, r) {
		return
	}
	s.toServices(w, r, core.ServiceCaller{Kind: "person", Person: viewer(r).PersonID})
}

// maxQueryBody is the most of a SQL box request Homelab reads to see whether it writes.
// homelab-services refuses SQL over 100 KB, and a JSON string takes some more.
const maxQueryBody = 256 << 10

// dbQuery says whether a path is the SQL box of an app's database, and which app and
// environment: /api/db/{app}/{env}/query.
func dbQuery(path string) (app, env string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 5 || parts[0] != "api" || parts[1] != "db" || parts[4] != "query" {
		return "", "", false
	}
	return parts[2], parts[3], true
}

// dropCopyParam removes any copy= the caller sent to the databases part of the API. Only
// Homelab says which copy was made, so no one can put a made-up name in the change history.
func dropCopyParam(r *http.Request) {
	if r.URL.Path != "/api/db" && !strings.HasPrefix(r.URL.Path, "/api/db/") {
		return
	}
	if q := r.URL.Query(); q.Has("copy") {
		q.Del("copy")
		r.URL.RawQuery = q.Encode()
	}
}

const msgNoCopy = "Couldn't make a copy first, so nothing was changed."

// copyBeforeWrite makes a copy of prod's database before a person's write in the SQL box,
// and adds copy=<name> to the query so homelab-services can record which copy to go back
// to. The body goes on unchanged. It answers the request itself, and returns false, when
// the copy can't be made. Dev writes and reads get no copy.
//
// A copy= the caller sent was dropped already (dropCopyParam).
func (s *server) copyBeforeWrite(w http.ResponseWriter, r *http.Request) bool {
	app, env, ok := dbQuery(r.URL.Path)
	if !ok {
		return true
	}
	q := r.URL.Query()
	if r.Method != http.MethodPost || env != core.Prod {
		return true
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxQueryBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "That request is too big.")
		} else {
			writeErr(w, http.StatusBadRequest, msgBadRequest)
		}
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	var in struct {
		Write bool `json:"write"`
	}
	// A body that isn't JSON is not a write; homelab-services refuses it.
	if json.Unmarshal(b, &in) != nil || !in.Write || s.services == nil {
		return true
	}
	name, err := s.hub.CopyBeforeChange(actionCtx(r), app, viewer(r))
	if err != nil {
		s.log.Warn("copy before a database change", "app", app, "err", err)
		writeErr(w, http.StatusServiceUnavailable, msgNoCopy)
		return false
	}
	if name != "" {
		q.Set("copy", name)
		r.URL.RawQuery = q.Encode()
	}
	return true
}

// keyToServices forwards a call made with an API key.
func (s *server) keyToServices(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
	// Admin keys get nothing from the secrets API, whatever scopes they have. The check is
	// here, before anything is forwarded; homelab-services refuses them again.
	if isSecretsAPI(r.URL.Path) && c.Kind != hub.KeyKindApp {
		writeErr(w, http.StatusForbidden, msgSecretsAppOnly)
		return
	}
	dropCopyParam(r)
	sc := core.ServiceCaller{Kind: "admin", Person: c.PersonID, Key: c.KeyID, Scopes: c.Scopes}
	if c.Kind == hub.KeyKindApp {
		sc = core.ServiceCaller{Kind: "app", Key: c.KeyID, App: c.App, Env: c.Env, Scopes: c.Scopes}
	}
	// Logged per part of the API, so a key reading many files makes one line, not thousands.
	part := ""
	for _, p := range servicePaths {
		if r.URL.Path == p || strings.HasPrefix(r.URL.Path, p+"/") {
			part = p
		}
	}
	s.hub.LogKeyUse(r.Context(), c, r.Method+" "+part, r.Method != http.MethodGet && r.Method != http.MethodHead)
	s.toServices(w, r, sc)
}

func (s *server) toServices(w http.ResponseWriter, r *http.Request, c core.ServiceCaller) {
	if s.services == nil {
		writeErr(w, http.StatusServiceUnavailable, "Homelab's services aren't set up here.")
		return
	}
	s.services.Forward(w, r, c)
}
