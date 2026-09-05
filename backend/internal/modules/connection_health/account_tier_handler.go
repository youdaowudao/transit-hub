package connection_health

import (
	"net/http"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

func (h *Handler) getAccountTier(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	result, err := h.service.GetAccountTier(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) putAccountTier(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var input struct {
		AccountTier *int `json:"accountTier"`
	}
	if err := httpjson.Decode(r, &input); err != nil || input.AccountTier == nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.service.SaveAccountTier(r.Context(), userID, r.PathValue("id"), *input.AccountTier)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
