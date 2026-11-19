package connection_health

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"transithub/backend/internal/shared/httpjson"
)

func registerModelControlRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("GET /api/connection-health/model-control/auto-settings", h.getModelControlAutoSettings)
	mux.HandleFunc("PUT /api/connection-health/model-control/auto-settings", h.putModelControlAutoSettings)
	mux.HandleFunc("GET /api/connection-health/model-control/settings", h.getModelControlSettings)
	mux.HandleFunc("PUT /api/connection-health/model-control/settings", h.putModelControlSettings)
	mux.HandleFunc("GET /api/connection-health/model-control/targets/{targetId}", h.getModelControlTarget)
	mux.HandleFunc("GET /api/connection-health/model-control/items", h.listModelControlItems)
	mux.HandleFunc("POST /api/connection-health/model-control/managed", h.addManagedModel)
	mux.HandleFunc("DELETE /api/connection-health/model-control/managed", h.removeManagedModel)
	mux.HandleFunc("POST /api/connection-health/model-control/preview", h.previewModelControl)
	for _, op := range []string{"close", "restore", "add", "close-account"} {
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
func (h *Handler) getModelControlSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	settings, err := h.service.GetModelControlSettings(r.Context(), user)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, settings)
}
func (h *Handler) putModelControlSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input struct {
		MinAccuracyPercent *int   `json:"minAccuracyPercent"`
		MinJudgedAnswers   *int   `json:"minJudgedAnswers"`
		ExpectedVersion    *int64 `json:"expectedVersion"`
	}
	if err := httpjson.Decode(r, &input); err != nil || input.MinAccuracyPercent == nil || input.MinJudgedAnswers == nil || input.ExpectedVersion == nil {
		httpjson.WriteError(w, 400, ErrorRequest)
		return
	}
	settings, err := h.service.SaveModelControlSettings(r.Context(), user, ModelControlSettings{MinAccuracyPercent: *input.MinAccuracyPercent, MinJudgedAnswers: *input.MinJudgedAnswers}, *input.ExpectedVersion)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, settings)
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
	ws, ok := h.modelControlRequestScope(w, r, user)
	if !ok {
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
	// excludeTargetIds：页面按屏蔽清单和左侧隐藏分组算出的“不显示账号”，条目与计数都去掉。
	if excluded := modelControlExcludedTargetIDs(r); len(excluded) > 0 {
		kept := make([]ModelControlItem, 0, len(all))
		for _, i := range all {
			if !excluded[i.TargetID] {
				kept = append(kept, i)
			}
		}
		all = kept
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
	if r.URL.Query().Get("all") == "1" {
		httpjson.Write(w, 200, map[string]any{"items": items, "page": 1, "totalPages": 1, "counts": modelControlCounts(all)})
		return
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
	httpjson.Write(w, 200, map[string]any{"items": items[start:end], "page": page, "totalPages": pages, "counts": modelControlCounts(all)})
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
	ws, ok := h.modelControlRequestScope(w, r, user)
	if !ok {
		return
	}
	ids, err := h.service.modelControlVerifyTargetsFor(r.Context(), user, ws)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, map[string]any{"targetIds": ids})
}

// modelControlRequestScope：请求带 workspace 时先与当前工作区比对（另一页面已切换，包括切到非 Sub2API 工作区，都返回 409），
// 一致后再对同一工作区做会话和平台校验；校验期间又被切换时再比对一次。不带 workspace 的旧调用保持原行为。
func (h *Handler) modelControlRequestScope(w http.ResponseWriter, r *http.Request, user string) (string, bool) {
	if strings.TrimSpace(r.URL.Query().Get("workspace")) != "" {
		if current, err := h.service.currentAdminAccountID(r.Context(), user); err == nil && rejectModelControlWorkspaceMismatch(w, r, current) {
			return "", false
		}
	}
	ws, err := h.service.modelControlScope(r.Context(), user, "")
	if err != nil {
		// 校验期间另一页面切走（例如切到非 Sub2API 工作区）时，按工作区已切换返回 409，而不是平台或会话错误。
		if strings.TrimSpace(r.URL.Query().Get("workspace")) != "" {
			if current, currentErr := h.service.currentAdminAccountID(r.Context(), user); currentErr == nil && rejectModelControlWorkspaceMismatch(w, r, current) {
				return "", false
			}
		}
		writeModelControlError(w, err)
		return "", false
	}
	if rejectModelControlWorkspaceMismatch(w, r, ws) {
		return "", false
	}
	return ws, true
}

// rejectModelControlWorkspaceMismatch：请求带了 workspace 但与服务器当前工作区不同（另一页面已切换）时返回 409。
// 不带 workspace 的旧调用保持原行为。
func rejectModelControlWorkspaceMismatch(w http.ResponseWriter, r *http.Request, current string) bool {
	workspace := strings.TrimSpace(r.URL.Query().Get("workspace"))
	if workspace == "" || workspace == current {
		return false
	}
	httpjson.Write(w, http.StatusConflict, map[string]string{"message": ErrorWorkspaceChanged, "detail": "读取模型管理失败：当前工作区已在其他页面切换，请刷新页面后再打开模型管理；这次只读取，没有改动任何设置。"})
	return true
}

func modelControlExcludedTargetIDs(r *http.Request) map[string]bool {
	raw := strings.TrimSpace(r.URL.Query().Get("excludeTargetIds"))
	if raw == "" {
		return nil
	}
	result := map[string]bool{}
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			result[id] = true
		}
	}
	return result
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

func (h *Handler) getModelControlAutoSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	settings, err := h.service.GetModelControlAutoSettings(r.Context(), user)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, settings)
}
func (h *Handler) putModelControlAutoSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input struct {
		AutoCloseEnabled          *bool  `json:"autoCloseEnabled"`
		AutoRestoreEnabled        *bool  `json:"autoRestoreEnabled"`
		HealthCoordinationEnabled *bool  `json:"healthCoordinationEnabled"`
		VerifyIntervalMinutes     *int   `json:"verifyIntervalMinutes"`
		RetryIntervalMinutes      *int   `json:"retryIntervalMinutes"`
		ExpectedVersion           *int64 `json:"expectedVersion"`
	}
	if err := httpjson.Decode(r, &input); err != nil || input.AutoCloseEnabled == nil || input.AutoRestoreEnabled == nil || input.HealthCoordinationEnabled == nil || input.VerifyIntervalMinutes == nil || input.RetryIntervalMinutes == nil || input.ExpectedVersion == nil {
		httpjson.WriteError(w, 400, ErrorRequest)
		return
	}
	settings, err := h.service.SaveModelControlAutoSettings(r.Context(), user, ModelControlAutoSettings{AutoCloseEnabled: *input.AutoCloseEnabled, AutoRestoreEnabled: *input.AutoRestoreEnabled, HealthCoordinationEnabled: *input.HealthCoordinationEnabled, VerifyIntervalMinutes: *input.VerifyIntervalMinutes, RetryIntervalMinutes: *input.RetryIntervalMinutes}, *input.ExpectedVersion)
	if err != nil {
		writeModelControlError(w, err)
		return
	}
	httpjson.Write(w, 200, settings)
}
