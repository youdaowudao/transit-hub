package connection_health

import (
	"errors"
	"net/http"
	"strconv"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

func registerQuestionAnswerScheduleRoutes(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("GET /api/connection-health/question-answer-runtime-settings", h.getQuestionAnswerRuntimeSettings)
	mux.HandleFunc("PUT /api/connection-health/question-answer-runtime-settings", h.putQuestionAnswerRuntimeSettings)
	mux.HandleFunc("GET /api/connection-health/question-answer-schedule-limits", h.getQuestionAnswerScheduleLimits)
	mux.HandleFunc("PUT /api/connection-health/question-answer-schedule-limits", h.putQuestionAnswerScheduleLimits)
	mux.HandleFunc("GET /api/connection-health/question-answer-schedules", h.listQuestionAnswerSchedules)
	mux.HandleFunc("POST /api/connection-health/question-answer-schedules", h.createQuestionAnswerSchedule)
	mux.HandleFunc("POST /api/connection-health/question-answer-schedules/preview", h.previewQuestionAnswerSchedule)
	mux.HandleFunc("GET /api/connection-health/question-answer-schedules/{id}", h.getQuestionAnswerSchedule)
	mux.HandleFunc("PUT /api/connection-health/question-answer-schedules/{id}", h.updateQuestionAnswerSchedule)
	for _, action := range []string{"enable", "disable", "revalidate"} {
		action := action
		mux.HandleFunc("POST /api/connection-health/question-answer-schedules/{id}/"+action, func(w http.ResponseWriter, r *http.Request) { h.setQuestionAnswerScheduleState(w, r, action) })
	}
	mux.HandleFunc("DELETE /api/connection-health/question-answer-schedules/{id}", h.deleteQuestionAnswerSchedule)
	mux.HandleFunc("POST /api/connection-health/question-answer-schedules/{id}/run", h.runQuestionAnswerSchedule)
	mux.HandleFunc("GET /api/connection-health/question-answer-schedules/{id}/executions", h.listQuestionAnswerScheduleExecutions)
	mux.HandleFunc("GET /api/connection-health/question-answer-schedule-executions/{executionId}", h.getQuestionAnswerScheduleExecution)
	mux.HandleFunc("POST /api/connection-health/question-answer-schedule-executions/{executionId}/cancel", h.cancelQuestionAnswerScheduleExecution)
	mux.HandleFunc("GET /api/connection-health/question-answer-recent-summaries", h.listQuestionAnswerRecentSummaries)
}
func (h *Handler) questionAnswerScheduleUser(w http.ResponseWriter, r *http.Request, write bool) (string, bool) {
	user, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return "", false
	}
	if write {
		if err := h.service.questionAnswerScheduleWriteGuard(); err != nil {
			writeError(w, err)
			return "", false
		}
	}
	return user, true
}
func writeQuestionAnswerScheduleError(w http.ResponseWriter, err error) {
	var conflict *QuestionAnswerScheduleConflictError
	if errors.As(err, &conflict) {
		payload := map[string]any{"error": conflict.Key, "message": conflict.Key, "current": conflict.Current}
		if conflict.ActiveExecutionID != "" {
			payload["activeExecutionId"] = conflict.ActiveExecutionID
		}
		httpjson.Write(w, http.StatusConflict, payload)
		return
	}
	var request requestError
	if errors.As(err, &request) {
		switch request.Error() {
		case ErrorQuestionAnswerScheduleNotFound:
			httpjson.WriteError(w, http.StatusNotFound, request.Error())
			return
		case ErrorQuestionAnswerScheduleDeleted, ErrorQuestionAnswerScheduleInvalid, ErrorQuestionAnswerScheduleActive, ErrorQuestionAnswerScheduleVersionConflict, ErrorQuestionAnswerExecutionVersionConflict:
			httpjson.WriteError(w, http.StatusConflict, request.Error())
			return
		}
	}
	writeError(w, err)
}
func parseQuestionAnswerSchedulePagination(r *http.Request) (int, int, error) {
	page, size := 1, 20
	for _, entry := range []struct {
		name  string
		value *int
	}{{"page", &page}, {"pageSize", &size}} {
		if raw := r.URL.Query().Get(entry.name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				return 0, 0, requestError(ErrorRequest)
			}
			*entry.value = n
		}
	}
	if size > 100 {
		return 0, 0, requestError(ErrorRequest)
	}
	return page, size, nil
}
func (h *Handler) getQuestionAnswerRuntimeSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	result, err := h.service.GetQuestionAnswerRuntimeSettings(r.Context(), user)
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
func (h *Handler) putQuestionAnswerRuntimeSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input struct {
		QuestionAnswerConcurrency *int   `json:"questionAnswerConcurrency"`
		ExpectedVersion           *int64 `json:"expectedVersion"`
	}
	if err := httpjson.Decode(r, &input); err != nil || input.QuestionAnswerConcurrency == nil || input.ExpectedVersion == nil || *input.ExpectedVersion < 0 {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.service.SaveQuestionAnswerRuntimeSettings(r.Context(), user, *input.QuestionAnswerConcurrency, *input.ExpectedVersion)
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
func (h *Handler) getQuestionAnswerScheduleLimits(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	result, err := h.service.GetQuestionAnswerScheduleLimits(r.Context(), user)
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
func (h *Handler) putQuestionAnswerScheduleLimits(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input struct {
		MaxEnabledQuestionAnswerSchedules *int   `json:"maxEnabledQuestionAnswerSchedules"`
		MaxScheduleTargets                *int   `json:"maxScheduleTargets"`
		MaxScheduleRequestsPerExecution   *int   `json:"maxScheduleRequestsPerExecution"`
		DailyScheduledRequestLimit        *int   `json:"dailyScheduledRequestLimit"`
		MaxActiveScheduleExecutions       *int   `json:"maxActiveScheduleExecutions"`
		MaxQueuedScheduledRequests        *int   `json:"maxQueuedScheduledRequests"`
		ScheduleExecutionTimeoutMinutes   *int   `json:"scheduleExecutionTimeoutMinutes"`
		ScheduleLateGraceMinutes          *int   `json:"scheduleLateGraceMinutes"`
		ExpectedVersion                   *int64 `json:"expectedVersion"`
	}
	if err := httpjson.Decode(r, &input); err != nil || input.ExpectedVersion == nil || *input.ExpectedVersion < 0 || input.MaxEnabledQuestionAnswerSchedules == nil || input.MaxScheduleTargets == nil || input.MaxScheduleRequestsPerExecution == nil || input.DailyScheduledRequestLimit == nil || input.MaxActiveScheduleExecutions == nil || input.MaxQueuedScheduledRequests == nil || input.ScheduleExecutionTimeoutMinutes == nil || input.ScheduleLateGraceMinutes == nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	limits := QuestionAnswerScheduleLimits{MaxEnabledQuestionAnswerSchedules: *input.MaxEnabledQuestionAnswerSchedules, MaxScheduleTargets: *input.MaxScheduleTargets, MaxScheduleRequestsPerExecution: *input.MaxScheduleRequestsPerExecution, DailyScheduledRequestLimit: *input.DailyScheduledRequestLimit, MaxActiveScheduleExecutions: *input.MaxActiveScheduleExecutions, MaxQueuedScheduledRequests: *input.MaxQueuedScheduledRequests, ScheduleExecutionTimeoutMinutes: *input.ScheduleExecutionTimeoutMinutes, ScheduleLateGraceMinutes: *input.ScheduleLateGraceMinutes}
	result, err := h.service.SaveQuestionAnswerScheduleLimits(r.Context(), user, limits, *input.ExpectedVersion)
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
func (h *Handler) listQuestionAnswerSchedules(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	page, size, err := parseQuestionAnswerSchedulePagination(r)
	if err != nil {
		writeError(w, err)
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "active"
	}
	result, err := h.service.ListQuestionAnswerSchedules(r.Context(), user, status, page, size)
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
func (h *Handler) getQuestionAnswerSchedule(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	result, err := h.service.GetQuestionAnswerSchedule(r.Context(), user, r.PathValue("id"))
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
func (h *Handler) createQuestionAnswerSchedule(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input QuestionAnswerScheduleInput
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.service.CreateQuestionAnswerSchedule(r.Context(), user, input)
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, result)
}
func (h *Handler) updateQuestionAnswerSchedule(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input QuestionAnswerScheduleInput
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.service.UpdateQuestionAnswerSchedule(r.Context(), user, r.PathValue("id"), input)
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
func (h *Handler) previewQuestionAnswerSchedule(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input QuestionAnswerSchedulePreviewInput
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.service.PreviewQuestionAnswerSchedule(r.Context(), user, input)
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
func (h *Handler) setQuestionAnswerScheduleState(w http.ResponseWriter, r *http.Request, action string) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion *int64 `json:"expectedVersion"`
	}
	if err := httpjson.Decode(r, &input); err != nil || input.ExpectedVersion == nil || *input.ExpectedVersion < 1 {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.service.SetQuestionAnswerScheduleState(r.Context(), user, r.PathValue("id"), action, *input.ExpectedVersion)
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
func (h *Handler) deleteQuestionAnswerSchedule(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	version, err := strconv.ParseInt(r.URL.Query().Get("expectedVersion"), 10, 64)
	if err != nil || version < 1 {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.service.SetQuestionAnswerScheduleState(r.Context(), user, r.PathValue("id"), "delete", version)
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
func (h *Handler) runQuestionAnswerSchedule(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input struct {
		RequestID string `json:"requestId"`
	}
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.service.RunQuestionAnswerSchedule(r.Context(), user, r.PathValue("id"), input.RequestID)
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusAccepted, result)
}
func (h *Handler) listQuestionAnswerScheduleExecutions(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	page, size, err := parseQuestionAnswerSchedulePagination(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := h.service.ListQuestionAnswerScheduleExecutions(r.Context(), user, r.PathValue("id"), page, size)
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
func (h *Handler) getQuestionAnswerScheduleExecution(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	result, err := h.service.GetQuestionAnswerScheduleExecution(r.Context(), user, r.PathValue("executionId"))
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
func (h *Handler) cancelQuestionAnswerScheduleExecution(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, true)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion *int64 `json:"expectedVersion"`
	}
	if err := httpjson.Decode(r, &input); err != nil || input.ExpectedVersion == nil || *input.ExpectedVersion < 1 {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.service.CancelQuestionAnswerScheduleExecution(r.Context(), user, r.PathValue("executionId"), *input.ExpectedVersion)
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	status := http.StatusOK
	if result.Accepted {
		status = http.StatusAccepted
	}
	if result.Conflict {
		httpjson.Write(w, http.StatusConflict, map[string]any{"error": ErrorQuestionAnswerExecutionVersionConflict, "message": ErrorQuestionAnswerExecutionVersionConflict, "current": result.Execution})
		return
	}
	httpjson.Write(w, status, result.Execution)
}
func (h *Handler) listQuestionAnswerRecentSummaries(w http.ResponseWriter, r *http.Request) {
	user, ok := h.questionAnswerScheduleUser(w, r, false)
	if !ok {
		return
	}
	result, err := h.service.ListQuestionAnswerRecentSummaries(r.Context(), user, r.URL.Query()["targetId"])
	if err != nil {
		writeQuestionAnswerScheduleError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
