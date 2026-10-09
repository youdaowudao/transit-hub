package connection_health

import (
	"errors"
	"net/http"
	"strconv"
	"transithub/backend/internal/shared/httpjson"
)

func registerModelControlRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("GET /api/connection-health/model-control/rules", h.listModelControlRules)
	mux.HandleFunc("PUT /api/connection-health/model-control/rules", h.putModelControlRule)
	mux.HandleFunc("DELETE /api/connection-health/model-control/rules", h.deleteModelControlRule)
	mux.HandleFunc("GET /api/connection-health/model-control/targets/{targetId}", h.getModelControlTarget)
	mux.HandleFunc("GET /api/connection-health/model-control/items", h.listModelControlItems)
	mux.HandleFunc("POST /api/connection-health/model-control/managed", h.addManagedModel)
	mux.HandleFunc("DELETE /api/connection-health/model-control/managed", h.removeManagedModel)
	mux.HandleFunc("POST /api/connection-health/model-control/preview", h.previewModelControl)
	for _, op := range []string{"close", "restore", "close-account"} {
		route := op
		mux.HandleFunc("POST /api/connection-health/model-control/"+route, func(w http.ResponseWriter, r *http.Request) { h.executeModelControl(w, r, route) })
	}
	mux.HandleFunc("GET /api/connection-health/model-control/verify-targets", h.getModelControlVerifyTargets)
	mux.HandleFunc("POST /api/connection-health/model-control/verify", h.verifyModelControl)
	mux.HandleFunc("GET /api/connection-health/model-control/events", h.listModelControlEvents)
}
func writeModelControlError(w http.ResponseWriter, err error) {
	if errors.Is(err, requestError(ErrorProbeTargetNotFound)) || errors.Is(err, requestError(ErrorNotFound)) {
		httpjson.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	var conflict *ModelControlConflictError
	if errors.As(err, &conflict) {
		httpjson.Write(w, http.StatusConflict, map[string]any{"error": conflict.Key, "message": conflict.Key, "current": conflict.Current})
		return
	}
	if errors.Is(err, ErrRemoteActionLeaseLost) || errors.Is(err, ErrRemoteActionPending) {
		httpjson.WriteError(w, http.StatusConflict, modelControlError("LeaseLost"))
		return
	}
	writeQuestionAnswerScheduleError(w, err)
}
func (h *Handler) listModelControlRules(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	items, err := h.service.ListModelControlRules(r.Context(), user)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, map[string]any{"items": items})
}
func (h *Handler) putModelControlRule(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input struct {
		ModelName          string `json:"modelName"`
		MinAccuracyPercent *int   `json:"minAccuracyPercent"`
		MinJudgedAnswers   *int   `json:"minJudgedAnswers"`
		IncludeManual      *bool  `json:"includeManual"`
		IncludeScheduled   *bool  `json:"includeScheduled"`
		ExpectedVersion    *int64 `json:"expectedVersion"`
	}
	if err := httpjson.Decode(r, &input); err != nil || input.MinAccuracyPercent == nil || input.MinJudgedAnswers == nil || input.IncludeManual == nil || input.IncludeScheduled == nil || input.ExpectedVersion == nil {
		httpjson.WriteError(w, 400, ErrorRequest)
		return
	}
	rule, err := h.service.SaveModelControlRule(r.Context(), user, ModelControlRule{ModelName: input.ModelName, MinAccuracyPercent: *input.MinAccuracyPercent, MinJudgedAnswers: *input.MinJudgedAnswers, IncludeManual: *input.IncludeManual, IncludeScheduled: *input.IncludeScheduled}, *input.ExpectedVersion)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, rule)
}
func (h *Handler) deleteModelControlRule(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input struct {
		ModelName       string `json:"modelName"`
		ExpectedVersion *int64 `json:"expectedVersion"`
	}
	if err := httpjson.Decode(r, &input); err != nil || !validModelControlModel(input.ModelName) || input.ExpectedVersion == nil || *input.ExpectedVersion < 1 {
		httpjson.WriteError(w, 400, ErrorRequest)
		return
	}
	if err := h.service.DeleteModelControlRule(r.Context(), user, input.ModelName, *input.ExpectedVersion); err != nil {
		writeModelControlError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *Handler) getModelControlTarget(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	target := r.PathValue("targetId")
	ws, err := h.service.modelControlScope(r.Context(), user, target)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	all, _, err := h.service.modelControlItems(r.Context(), user, ws)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	items := []ModelControlItem{}
	for _, i := range all {
		if i.TargetID == target {
			items = append(items, i)
		}
	}
	candidates, err := h.service.modelControls.ListRecentQuestionAnswerModels(r.Context(), user, target)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, map[string]any{"targetId": target, "items": items, "candidates": candidates})
}
func modelControlPage(r *http.Request) (int, error) {
	p := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 1000000 {
			return 0, requestError(ErrorRequest)
		}
		p = value
	}
	return p, nil
}
func (h *Handler) listModelControlItems(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	ws, err := h.service.modelControlScope(r.Context(), user, "")
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	page, err := modelControlPage(r)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	view := r.URL.Query().Get("view")
	if view == "" {
		view = "attention"
	}
	if view != "attention" && view != "all" {
		httpjson.WriteError(w, 400, ErrorRequest)
		return
	}
	all, _, err := h.service.modelControlItems(r.Context(), user, ws)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	model := r.URL.Query().Get("modelName")
	items := []ModelControlItem{}
	for _, i := range all {
		if model != "" && i.ModelName != model {
			continue
		}
		if view == "attention" && !modelControlNeedsAttention(i) {
			continue
		}
		items = append(items, i)
	}
	pages := (len(items) + 49) / 50
	start := (page - 1) * 50
	if start > len(items) {
		start = len(items)
	}
	end := start + 50
	if end > len(items) {
		end = len(items)
	}
	httpjson.Write(w, 200, map[string]any{"items": items[start:end], "page": page, "totalPages": pages})
}
func (h *Handler) addManagedModel(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input struct {
		TargetID  string `json:"targetId"`
		ModelName string `json:"modelName"`
	}
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, 400, ErrorRequest)
		return
	}
	item, err := h.service.AddManagedModel(r.Context(), user, input.TargetID, input.ModelName)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, item)
}
func (h *Handler) removeManagedModel(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input struct {
		TargetID        string `json:"targetId"`
		ModelName       string `json:"modelName"`
		ExpectedVersion *int64 `json:"expectedVersion"`
		AbandonClosed   bool   `json:"abandonClosed"`
	}
	if err := httpjson.Decode(r, &input); err != nil || !validModelControlModel(input.ModelName) || input.ExpectedVersion == nil || *input.ExpectedVersion < 1 {
		httpjson.WriteError(w, 400, ErrorRequest)
		return
	}
	if err := h.service.RemoveManagedModel(r.Context(), user, input.TargetID, input.ModelName, *input.ExpectedVersion, input.AbandonClosed); err != nil {
		writeModelControlError(w, err)
		return
	}
	w.WriteHeader(204)
}

type modelControlActionInput struct {
	TargetID               string             `json:"targetId"`
	ModelName              string             `json:"modelName"`
	Operation              string             `json:"operation"`
	Basis                  *modelControlBasis `json:"basis"`
	PlanFingerprint        string             `json:"planFingerprint"`
	ConfirmWithoutEvidence bool               `json:"confirmWithoutEvidence"`
}

func (h *Handler) previewModelControl(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input modelControlActionInput
	if err := httpjson.Decode(r, &input); err != nil || input.Basis == nil {
		httpjson.WriteError(w, 400, ErrorRequest)
		return
	}
	result, err := h.service.PreviewModelControl(r.Context(), user, input.TargetID, input.ModelName, input.Operation, *input.Basis)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, result)
}
func (h *Handler) executeModelControl(w http.ResponseWriter, r *http.Request, op string) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input modelControlActionInput
	if err := httpjson.Decode(r, &input); err != nil || input.Basis == nil || input.PlanFingerprint == "" {
		httpjson.WriteError(w, 400, ErrorRequest)
		return
	}
	var result ModelControlResult
	var err error
	if op == "close-account" {
		result, err = h.service.CloseAccountForModel(r.Context(), user, input.TargetID, input.ModelName, *input.Basis, input.PlanFingerprint)
	} else {
		result, err = h.service.ExecuteModelControl(r.Context(), user, input.TargetID, input.ModelName, op, *input.Basis, input.PlanFingerprint, input.ConfirmWithoutEvidence)
	}
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, result)
}
func (h *Handler) getModelControlVerifyTargets(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	ids, err := h.service.ModelControlVerifyTargets(r.Context(), user)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, map[string]any{"targetIds": ids})
}
func (h *Handler) verifyModelControl(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input struct {
		TargetIDs []string `json:"targetIds"`
	}
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, 400, ErrorRequest)
		return
	}
	result, err := h.service.VerifyModelControl(r.Context(), user, input.TargetIDs)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, result)
}
func (h *Handler) listModelControlEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	ws, err := h.service.modelControlScope(r.Context(), user, "")
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	page, err := modelControlPage(r)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	target := r.URL.Query().Get("targetId")
	if target != "" {
		if err = questionAnswerScheduleOwnedTarget(user, ws, target); err != nil {
			writeModelControlError(w, err)
			return
		}
	}
	items, pages, err := h.service.modelControls.ListModelControlEvents(r.Context(), user, ws, target, r.URL.Query().Get("modelName"), page)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, map[string]any{"items": items, "page": page, "totalPages": pages})
}
