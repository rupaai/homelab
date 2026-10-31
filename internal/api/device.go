// Device handlers: logs, activity, push, resources, updates and test versions.

package api

import (
	"net/http"
	"strconv"

	"github.com/rupaai/homelab/internal/core"
)

func (s *server) logs(w http.ResponseWriter, r *http.Request) {
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	text, err := s.hub.Logs(r.Context(), r.PathValue("id"), r.PathValue("env"), lines)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"lines": text})
}

func (s *server) activity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	ev, err := s.hub.Events(r.Context(), q.Get("app"), before, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *server) pushKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": s.hub.PushPublicKey()})
}

type pushInput struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
	Device   string `json:"device"`
}

func (s *server) subscribePush(w http.ResponseWriter, r *http.Request) {
	var in pushInput
	if !decode(w, r, &in, true) {
		return
	}
	sub := core.PushSubscription{Endpoint: in.Endpoint, P256dh: in.P256dh, Auth: in.Auth}
	if err := s.hub.SubscribePush(r.Context(), viewer(r).PersonID, in.Device, sub); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) unsubscribePush(w http.ResponseWriter, r *http.Request) {
	var in pushInput
	if !decode(w, r, &in, true) {
		return
	}
	if err := s.hub.UnsubscribePush(r.Context(), viewer(r).PersonID, in.Endpoint); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) testPush(w http.ResponseWriter, r *http.Request) {
	n, err := s.hub.TestPush(r.Context(), viewer(r).PersonID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"sent": n})
}

func (s *server) resources(w http.ResponseWriter, r *http.Request) {
	v, err := s.hub.Resources(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) device(w http.ResponseWriter, r *http.Request) {
	// ?check=1 is the companion's Refresh: ask GitHub for the newest Homelab now.
	v, err := s.hub.Device(r.Context(), r.URL.Query().Get("check") == "1")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) updateHomelab(w http.ResponseWriter, r *http.Request) {
	version, err := s.hub.UpdateHomelab(actionCtx(r), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"version": version})
}

func (s *server) testVersions(w http.ResponseWriter, r *http.Request) {
	tests, err := s.hub.TestVersions(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tests)
}

func (s *server) tryTestVersion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version string `json:"version"`
	}
	if !decode(w, r, &in, true) {
		return
	}
	t, err := s.hub.TryTestVersion(actionCtx(r), viewer(r), in.Version)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, t)
}

func (s *server) endTest(keep bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		version, err := s.hub.EndTest(actionCtx(r), viewer(r), keep)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"version": version})
	}
}
