// App handlers: list, add, deploy, promote, rollback, remove, and the database copy and import routes.

package api

import (
	"net/http"
	"strings"

	"github.com/rupaai/homelab/internal/core"
	"github.com/rupaai/homelab/internal/hub"
)

func (s *server) listApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.hub.Overview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if apps == nil {
		apps = []hub.AppView{}
	}
	p, err := s.store.Person(r.Context(), viewer(r).PersonID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !hasRole(p, "owner") && !hasRole(p, "admin") {
		filtered := make([]hub.AppView, 0, len(apps))
		for _, a := range apps {
			level, ok := p.Grants[a.ID]
			if !ok {
				continue
			}
			if level != "build" {
				a = productionOnly(a)
			}
			filtered = append(filtered, a)
		}
		apps = filtered
	}
	writeJSON(w, http.StatusOK, apps)
}

func (s *server) listProjects(w http.ResponseWriter, r *http.Request) {
	apps, err := s.hub.Overview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.store.Person(r.Context(), viewer(r).PersonID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !hasRole(p, "owner") && !hasRole(p, "admin") {
		filtered := make([]hub.AppView, 0, len(apps))
		for _, app := range apps {
			if p.Grants[app.ID] == "build" {
				filtered = append(filtered, app)
			}
		}
		apps = filtered
	}
	if apps == nil {
		apps = []hub.AppView{}
	}
	writeJSON(w, http.StatusOK, apps)
}

func (s *server) addApp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	ctx := actionCtx(r)
	a, err := s.hub.AddApp(ctx, in.Repo, in.Branch, viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v, err := s.hub.App(ctx, a.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) getApp(w http.ResponseWriter, r *http.Request) {
	v, err := s.hub.App(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.store.Person(r.Context(), viewer(r).PersonID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !hasRole(p, "owner") && !hasRole(p, "admin") && p.Grants[v.ID] != "build" {
		v = productionOnly(v)
	}
	writeJSON(w, http.StatusOK, v)
}

func productionOnly(a hub.AppView) hub.AppView {
	for _, e := range a.Envs {
		if e.Env == core.Prod {
			a.Envs = []hub.EnvView{e}
			break
		}
	}
	a.LatestBuild = nil
	a.Held = []hub.HoldView{}
	a.AutoPromote = nil
	a.Mirror = nil
	return a
}

func (s *server) deploy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Commit string `json:"commit"`
	}
	if !decode(w, r, &in, false) {
		return
	}
	if err := s.hub.DeployDev(actionCtx(r), r.PathValue("id"), in.Commit, viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	accepted(w)
}

func (s *server) previewPromote(w http.ResponseWriter, r *http.Request) {
	p, err := s.hub.PreviewPromote(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) promote(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	if err := s.hub.Promote(actionCtx(r), r.PathValue("id"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	accepted(w)
}

func (s *server) previewRollback(w http.ResponseWriter, r *http.Request) {
	p, err := s.hub.PreviewRollback(r.Context(), r.PathValue("id"), r.PathValue("env"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) rollback(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	if err := s.hub.Rollback(actionCtx(r), r.PathValue("id"), r.PathValue("env"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	accepted(w)
}

// appDatabase describes the app's database and its copies.
func (s *server) appDatabase(w http.ResponseWriter, r *http.Request) {
	v, err := s.hub.Database(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) makeDBCopy(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	c, err := s.hub.MakeDBCopy(actionCtx(r), r.PathValue("id"), r.PathValue("env"), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *server) restoreDBCopy(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	if err := s.hub.RestoreDBCopy(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	accepted(w)
}

func (s *server) copyProdToDev(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	if err := s.hub.CopyProdToDev(actionCtx(r), r.PathValue("id"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	accepted(w)
}

// isDBImport says whether a request uploads a file to import into an app's database.
func isDBImport(r *http.Request) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	return r.Method == http.MethodPost && len(parts) == 7 && parts[1] == "apps" && parts[3] == "envs" && parts[5] == "db" && parts[6] == "import"
}

// importDB loads the uploaded file (the whole body) into an environment's database.
func (s *server) importDB(w http.ResponseWriter, r *http.Request) {
	kind, err := s.hub.ImportDB(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.Body, viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "kind": kind})
}

func (s *server) setPromotion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	v, err := s.hub.SetPromotion(actionCtx(r), r.PathValue("id"), in.Mode, viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) removeApp(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.RemoveApp(actionCtx(r), r.PathValue("id"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}
