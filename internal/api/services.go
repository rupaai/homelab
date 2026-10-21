package api

import (
	"net/http"
	"strings"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
)

// servicePaths are the parts of the API that homelab-services serves (platform services).
// Homelab decides who is calling, then forwards the call with that caller signed.
var servicePaths = []string{"/api/files", "/api/db", "/api/data"}

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
	s.toServices(w, r, core.ServiceCaller{Kind: "person", Person: viewer(r).PersonID})
}

// keyToServices forwards a call made with an API key.
func (s *server) keyToServices(w http.ResponseWriter, r *http.Request) {
	c, _ := callerFrom(r.Context())
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
