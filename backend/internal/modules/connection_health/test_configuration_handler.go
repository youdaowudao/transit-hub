package connection_health

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

func (h *Handler) getAdminGroupTestConfiguration(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	result, err := h.service.GetAdminGroupTestConfiguration(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) putAdminGroupTestConfiguration(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var input struct {
		Configuration json.RawMessage `json:"configuration"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || len(input.Configuration) == 0 {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	var configuration *GroupTestConfiguration
	if string(input.Configuration) != "null" {
		var value GroupTestConfiguration
		inner := json.NewDecoder(bytes.NewReader(input.Configuration))
		inner.DisallowUnknownFields()
		if err := inner.Decode(&value); err != nil || !validGroupTestConfiguration(value) {
			httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
			return
		}
		configuration = &value
	}
	result, err := h.service.SetAdminGroupTestConfiguration(r.Context(), userID, r.PathValue("id"), configuration)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
