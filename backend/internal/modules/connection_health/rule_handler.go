package connection_health

import (
	"encoding/json"
	"io"
	"net/http"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

func registerRuleRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("GET /api/connection-health/rule-presets", h.listRulePresets)
	mux.HandleFunc("POST /api/connection-health/rule-presets", h.saveRulePreset)
	mux.HandleFunc("PUT /api/connection-health/rule-presets/{id}", h.saveRulePreset)
	mux.HandleFunc("DELETE /api/connection-health/rule-presets/{id}", h.deleteRulePreset)
	mux.HandleFunc("POST /api/connection-health/rule-presets/{id}/apply-all", h.applyRulePresetToAll)
	mux.HandleFunc("POST /api/connection-health/rule-version/restore-legacy", h.restoreLegacyRule)
	mux.HandleFunc("POST /api/connection-health/rule-version/switch-v2", h.switchV2Rule)
	mux.HandleFunc("GET /api/connection-health/workspace-settings", h.getWorkspaceHealthSettings)
	mux.HandleFunc("PUT /api/connection-health/workspace-settings", h.putWorkspaceHealthSettings)
}
func ruleRequestUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
	}
	return userID, ok
}
func decodeRuleInput(w http.ResponseWriter, r *http.Request, value any) bool {
	d := json.NewDecoder(io.LimitReader(r.Body, 32769))
	if err := d.Decode(value); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return false
	}
	return true
}
func (h *Handler) listRulePresets(w http.ResponseWriter, r *http.Request) {
	userID, ok := ruleRequestUser(w, r)
	if !ok {
		return
	}
	out, err := h.service.ListRulePresets(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, out)
}
func (h *Handler) saveRulePreset(w http.ResponseWriter, r *http.Request) {
	userID, ok := ruleRequestUser(w, r)
	if !ok {
		return
	}
	var p RulePreset
	if !decodeRuleInput(w, r, &p) {
		return
	}
	p.ID = r.PathValue("id")
	out, err := h.service.SaveRulePreset(r.Context(), userID, p)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, out)
}
func (h *Handler) deleteRulePreset(w http.ResponseWriter, r *http.Request) {
	userID, ok := ruleRequestUser(w, r)
	if !ok {
		return
	}
	if err := h.service.DeleteRulePreset(r.Context(), userID, r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) applyRulePresetToAll(w http.ResponseWriter, r *http.Request) {
	userID, ok := ruleRequestUser(w, r)
	if !ok {
		return
	}
	out, err := h.service.ApplyRulePresetToAll(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, out)
}
func (h *Handler) restoreLegacyRule(w http.ResponseWriter, r *http.Request) {
	h.switchRule(w, r, RuleVersionLegacy)
}
func (h *Handler) switchV2Rule(w http.ResponseWriter, r *http.Request) {
	h.switchRule(w, r, RuleVersionV2)
}
func (h *Handler) switchRule(w http.ResponseWriter, r *http.Request, rule string) {
	userID, ok := ruleRequestUser(w, r)
	if !ok {
		return
	}
	out, err := h.service.SwitchWorkspaceRule(r.Context(), userID, rule)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, out)
}
func (h *Handler) getWorkspaceHealthSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := ruleRequestUser(w, r)
	if !ok {
		return
	}
	out, err := h.service.WorkspaceHealthSettings(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, out)
}
func (h *Handler) putWorkspaceHealthSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := ruleRequestUser(w, r)
	if !ok {
		return
	}
	var input struct {
		ProbeConcurrency        int   `json:"probeConcurrency"`
		ProbeConcurrencyVersion int64 `json:"probeConcurrencyVersion"`
	}
	if !decodeRuleInput(w, r, &input) {
		return
	}
	out, err := h.service.SaveWorkspaceProbeConcurrency(r.Context(), userID, input.ProbeConcurrency, input.ProbeConcurrencyVersion)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, out)
}
