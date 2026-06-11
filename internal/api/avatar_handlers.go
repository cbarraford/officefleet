package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
)

const maxAvatarUploadBytes = 1 << 20 // 1 MiB

var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func (a *API) handleRegenerateAvatar(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	agent, err := a.agents.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if a.avatars == nil {
		writeError(w, http.StatusInternalServerError, "avatars not configured")
		return
	}
	a.avatars.Assign(agent)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "generating"})
}

func (a *API) handleUploadAvatar(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if _, err := a.agents.GetByID(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if a.avatars == nil {
		writeError(w, http.StatusInternalServerError, "avatars not configured")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && ct != "image/png" {
		writeError(w, http.StatusBadRequest, "avatar must be uploaded as image/png")
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxAvatarUploadBytes)
	data, err := io.ReadAll(body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusBadRequest, "avatar must be at most 1 MiB")
			return
		}
		writeError(w, http.StatusBadRequest, "could not read upload body")
		return
	}
	if len(data) < len(pngMagic) || !bytes.Equal(data[:len(pngMagic)], pngMagic) {
		writeError(w, http.StatusBadRequest, "body must be a PNG image")
		return
	}
	if _, err := a.avatars.SetUpload(r.Context(), id, data); err != nil {
		a.logf("api: upload avatar: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	agent, err := a.agents.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}
