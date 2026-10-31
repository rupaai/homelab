// Secret and setting handlers: list, reveal, set, versions and restore.

package api

import (
	"net/http"
	"strconv"
)

func (s *server) appSecrets(w http.ResponseWriter, r *http.Request) {
	envs, err := s.hub.AppSecrets(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"envs": envs, "canSeeValues": s.hub.CanSeeSecretValues(r.Context(), viewer(r).PersonID)})
}

func (s *server) revealSecret(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	v, err := s.hub.RevealSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": v})
}

func (s *server) revealSavedSecret(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	v, err := s.hub.RevealSavedSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": v})
}

func (s *server) deleteSavedSecret(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.DeleteSavedSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) secretVersions(w http.ResponseWriter, r *http.Request) {
	list, err := s.hub.SecretVersions(r.Context(), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": list})
}

func (s *server) restoreSecret(w http.ResponseWriter, r *http.Request) {
	n, ok := versionParam(w, r)
	if !ok || !decode(w, r, &struct{}{}, false) {
		return
	}
	if err := s.hub.RestoreSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), n, viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func versionParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		writeErr(w, http.StatusBadRequest, "That isn't a version number.")
		return 0, false
	}
	return n, true
}

type valueIn struct {
	Value string `json:"value"`
}

func (s *server) setSecret(w http.ResponseWriter, r *http.Request) {
	var in valueIn
	if !decode(w, r, &in, true) {
		return
	}
	if err := s.hub.SetSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), in.Value, viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) regenerateSecret(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.RegenerateSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.DeleteSecret(actionCtx(r), r.PathValue("id"), r.PathValue("env"), r.PathValue("name"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) settings(w http.ResponseWriter, r *http.Request) {
	list, err := s.hub.PlatformSettings(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) setSetting(w http.ResponseWriter, r *http.Request) {
	var in valueIn
	if !decode(w, r, &in, true) {
		return
	}
	if err := s.hub.SetPlatformSetting(actionCtx(r), r.PathValue("name"), in.Value, viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) deleteSetting(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.DeletePlatformSetting(actionCtx(r), r.PathValue("name"), viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}

func (s *server) revealSetting(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}, false) {
		return
	}
	v, err := s.hub.RevealPlatformSetting(actionCtx(r), r.PathValue("name"), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": v})
}

func (s *server) settingVersions(w http.ResponseWriter, r *http.Request) {
	list, err := s.hub.PlatformSettingVersions(r.Context(), r.PathValue("name"), viewer(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": list})
}

func (s *server) restoreSetting(w http.ResponseWriter, r *http.Request) {
	n, ok := versionParam(w, r)
	if !ok || !decode(w, r, &struct{}{}, false) {
		return
	}
	if err := s.hub.RestorePlatformSetting(actionCtx(r), r.PathValue("name"), n, viewer(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	noContent(w)
}
