package users

import (
	"log"
	"net/http"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

type Handler struct {
	service *Service
}

func RegisterRoutes(mux *http.ServeMux, service *Service) {
	handler := &Handler{service: service}
	mux.HandleFunc("GET /api/users", handler.findAll)
}

func (h *Handler) findAll(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	users, err := h.service.FindAll(r.Context(), userID)
	if err != nil {
		log.Printf("list users: %v", err)
		httpjson.WriteError(w, http.StatusInternalServerError, "Failed to list users")
		return
	}
	httpjson.Write(w, http.StatusOK, users)
}
