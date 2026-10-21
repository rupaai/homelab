package api

import (
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
	s.toServices(w, r, core.ServiceCaller{Kind: "person", Person: viewer(r).PersonID})
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
