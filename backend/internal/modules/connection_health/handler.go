package connection_health

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

type Handler struct {
	service *Service
}

const (
	questionAnswerContractHeaderName = "X-TransitHub-Question-Answer-Contract"
	questionAnswerContractVersion    = "2"
)

// RegisterRoutes 注册链路健康探活模块的全部路由。响应体一律不含 upstream_key。
func RegisterRoutes(mux *http.ServeMux, service *Service) {
	handler := &Handler{service: service}
	mux.HandleFunc("GET /api/connection-health/overview", handler.overview)
	mux.HandleFunc("GET /api/connection-health/stored-summary", handler.storedSummary)
	mux.HandleFunc("GET /api/connection-health/groups", handler.groups)
	mux.HandleFunc("GET /api/connection-health/admin-groups", handler.adminGroups)
	mux.HandleFunc("GET /api/connection-health/admin-groups/refresh", handler.refreshAdminGroups)
	mux.HandleFunc("POST /api/connection-health/admin-groups/refresh", handler.refreshAdminGroups)
	mux.HandleFunc("GET /api/connection-health/priority-sync-status", handler.prioritySyncStatus)
	mux.HandleFunc("GET /api/connection-health/events", handler.events)
	mux.HandleFunc("POST /api/connection-health/connections/{id}/probe", handler.probe)
	mux.HandleFunc("POST /api/connection-health/targets/{id}/probe", handler.probeTarget)
	mux.HandleFunc("POST /api/connection-health/targets/{id}/probe-stream", handler.probeTargetStream)
	mux.HandleFunc("PUT /api/connection-health/targets/{id}/intelligence-weight", handler.setTargetIntelligenceWeight)
	mux.HandleFunc("POST /api/connection-health/connections/{id}/disable", handler.disable)
	mux.HandleFunc("POST /api/connection-health/connections/{id}/restore", handler.restore)
	mux.HandleFunc("GET /api/connection-health/policies", handler.listPolicies)
	mux.HandleFunc("POST /api/connection-health/policies", handler.createPolicy)
	mux.HandleFunc("PUT /api/connection-health/policies/{id}", handler.updatePolicy)
	mux.HandleFunc("DELETE /api/connection-health/policies/{id}", handler.deletePolicy)
	mux.HandleFunc("GET /api/connection-health/targets/{id}/models", handler.discoverTargetModels)
	mux.HandleFunc("POST /api/connection-health/targets/{id}/manual-probe", handler.manualProbeTarget)
	mux.HandleFunc("GET /api/connection-health/test-questions", handler.listTestQuestions)
	mux.HandleFunc("POST /api/connection-health/test-questions", handler.createTestQuestion)
	mux.HandleFunc("PUT /api/connection-health/test-questions/{questionId}", handler.updateTestQuestion)
	mux.HandleFunc("POST /api/connection-health/test-questions/{questionId}/enabled", handler.setTestQuestionEnabled)
	mux.HandleFunc("POST /api/connection-health/test-questions/{questionId}/default", handler.setDefaultTestQuestion)
	mux.HandleFunc("DELETE /api/connection-health/test-questions/{questionId}", handler.deleteTestQuestion)
	mux.HandleFunc("POST /api/connection-health/targets/{id}/question-answers/batches", handler.startQuestionAnswerBatch)
	mux.HandleFunc("GET /api/connection-health/targets/{id}/question-answers/batches/latest", handler.latestQuestionAnswerBatch)
	mux.HandleFunc("GET /api/connection-health/targets/{id}/question-answers/batches/{batchId}", handler.getQuestionAnswerBatch)
	mux.HandleFunc("POST /api/connection-health/targets/{id}/question-answers/batches/{batchId}/cancel", handler.stopQuestionAnswerBatch)
	mux.HandleFunc("GET /api/connection-health/targets/{id}/question-answers/history", handler.questionAnswerHistory)
	mux.HandleFunc("PUT /api/connection-health/targets/{id}/question-answers/records/{recordId}/manual-error", handler.setQuestionAnswerManualError)
	mux.HandleFunc("PUT /api/connection-health/targets/{id}/question-answers/records/{recordId}/judgment", handler.setQuestionAnswerJudgment)
	mux.HandleFunc("POST /api/connection-health/targets/{id}/schedulable", handler.setTargetSchedulable)
	mux.HandleFunc("GET /api/connection-health/targets/{id}/policy-assignments", handler.getPolicyAssignments)
	mux.HandleFunc("PUT /api/connection-health/targets/{id}/policy-assignments", handler.putPolicyAssignments)
	mux.HandleFunc("GET /api/connection-health/admin-groups/{id}/policy-configuration", handler.getAdminGroupPolicyConfiguration)
	mux.HandleFunc("PUT /api/connection-health/admin-groups/{id}/policy-configuration", handler.putAdminGroupPolicyConfiguration)
}

func (h *Handler) prioritySyncStatus(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	status, err := h.service.PrioritySyncStatus(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, status)
}

func (h *Handler) setTargetSchedulable(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var input struct {
		Schedulable *bool `json:"schedulable"`
	}
	if err := httpjson.Decode(r, &input); err != nil || input.Schedulable == nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.service.SetTargetSchedulable(r.Context(), userID, r.PathValue("id"), *input.Schedulable)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) setTargetIntelligenceWeight(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var input UpdateTargetIntelligenceWeightInput
	if err := httpjson.Decode(r, &input); err != nil || !input.IntelligenceWeight.Set {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorIntelligenceWeightInvalid)
		return
	}
	result, err := h.service.SetTargetIntelligenceWeight(
		r.Context(), userID, r.PathValue("id"), input.IntelligenceWeight.Value,
	)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) storedSummary(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	response, err := h.service.StoredSummary(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, response)
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	resp, err := h.service.Overview(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, resp)
}

func (h *Handler) groups(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	groups, err := h.service.Groups(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	if groups == nil {
		groups = []OwnGroupHealth{}
	}
	httpjson.Write(w, http.StatusOK, groups)
}

// adminGroups 返回当前 admin workspace 下的 admin 全量分组健康主列表（含账号/渠道与探活叠加）。
// 与旧的 /groups 路由并存，不破坏旧调用方。
func (h *Handler) adminGroups(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	groups, err := h.service.AdminGroups(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	if groups == nil {
		groups = []AdminGroupHealth{}
	}
	httpjson.Write(w, http.StatusOK, groups)
}

func (h *Handler) refreshAdminGroups(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	wantsSSE := strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
	var flusher http.Flusher
	if wantsSSE {
		var supported bool
		flusher, supported = w.(http.Flusher)
		if !supported {
			httpjson.WriteError(w, http.StatusInternalServerError, ErrorUnknown)
			return
		}
	}

	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	var run *adminGroupsRefreshRun
	disposition := adminGroupsRefreshRunJoined
	var err error
	if runID != "" {
		if r.Method != http.MethodGet {
			httpjson.WriteError(w, http.StatusBadRequest, "refresh_run_invalid_reconnect")
			return
		}
		var found bool
		run, found = h.service.adminGroupsRefreshRunByID(r.Context(), userID, runID)
		if !found {
			httpjson.WriteError(w, http.StatusNotFound, "refresh_run_not_found")
			return
		}
	} else {
		mode := adminGroupsRefreshModeAutomatic
		if r.Method == http.MethodPost {
			mode = adminGroupsRefreshModeManual
		}
		run, disposition, err = h.service.startOrJoinAdminGroupsRefreshRun(r.Context(), userID, mode)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if disposition == adminGroupsRefreshRunConflict {
		httpjson.Write(w, http.StatusConflict, struct {
			ErrorKey string `json:"errorKey"`
			RunID    string `json:"runId"`
		}{ErrorKey: "refresh_run_conflict", RunID: run.id})
		return
	}

	if wantsSSE {
		h.streamAdminGroupsRefreshRun(w, r, flusher, run, disposition == adminGroupsRefreshRunStarted)
		return
	}
	h.waitAdminGroupsRefreshRunJSON(w, r, run)
}

func (h *Handler) streamAdminGroupsRefreshRun(w http.ResponseWriter, r *http.Request, flusher http.Flusher, run *adminGroupsRefreshRun, started bool) {
	subscription, current := run.subscribe()
	defer run.unsubscribe(subscription.ID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	lastRevision := int64(0)
	emitSnapshot := func(snapshot adminGroupsRefreshSnapshot) bool {
		if snapshot.Revision <= lastRevision {
			return snapshot.Terminal == nil
		}
		eventName := "snapshot"
		payload := any(snapshot)
		if snapshot.Terminal != nil {
			eventName = "terminal"
			payload = snapshot.Terminal
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, data); err != nil {
			return false
		}
		flusher.Flush()
		lastRevision = snapshot.Revision
		return snapshot.Terminal == nil
	}

	if started {
		if !emitSnapshot(run.initialSnapshot()) {
			return
		}
	}
	if !emitSnapshot(current) {
		return
	}
	for {
		select {
		case _, open := <-subscription.Signals:
			latest := run.latest()
			if !emitSnapshot(latest) || !open {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (h *Handler) waitAdminGroupsRefreshRunJSON(w http.ResponseWriter, r *http.Request, run *adminGroupsRefreshRun) {
	subscription, snapshot := run.subscribe()
	defer run.unsubscribe(subscription.ID)
	for snapshot.Terminal == nil {
		select {
		case <-subscription.Signals:
			snapshot = run.latest()
		case <-r.Context().Done():
			return
		}
	}
	terminal := snapshot.Terminal
	if terminal.Status != "success" || terminal.Groups == nil {
		// JSON callers keep the pre-streaming failure contract; detailed safe run failures
		// are exposed only by the SSE terminal union.
		httpjson.WriteError(w, http.StatusInternalServerError, ErrorUnknown)
		return
	}
	result := AdminGroupsFreshResult{
		Groups:  append([]AdminGroupHealth{}, (*terminal.Groups)...),
		Refresh: normalizeAdminGroupsRefreshSummary(terminal.Refresh),
	}
	if result.Groups == nil {
		result.Groups = []AdminGroupHealth{}
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	connectionID := r.URL.Query().Get("connectionId")
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	events, err := h.service.Events(r.Context(), userID, connectionID, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	if events == nil {
		events = []EventView{}
	}
	httpjson.Write(w, http.StatusOK, events)
}

func (h *Handler) probe(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	connectionID := r.PathValue("id")

	// 请求体是可选的：旧调用不带 body（或带空 body）时，Decode 返回 io.EOF，视为
	// "未指定 models"，保持旧行为（探活全部匹配模型），不当作请求错误处理。
	var input ProbeConnectionInput
	if err := httpjson.Decode(r, &input); err != nil && !errors.Is(err, io.EOF) {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}

	results, err := h.service.ProbeConnection(r.Context(), userID, connectionID, input)
	if err != nil {
		writeError(w, err)
		return
	}
	if results == nil {
		results = []ModelHealth{}
	}
	httpjson.Write(w, http.StatusOK, results)
}

// probeTarget 手动探活一个独立 admin 目标：路径参数是 targetId（不是 connectionId）。
// 请求体可选携带 models 限定探活模型。不可探活时返回结构化 i18n 错误 key。
func (h *Handler) probeTarget(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	targetID := r.PathValue("id")

	var input ProbeConnectionInput
	if err := httpjson.Decode(r, &input); err != nil && !errors.Is(err, io.EOF) {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}

	results, err := h.service.ProbeTarget(r.Context(), userID, targetID, input.Models)
	if err != nil {
		writeError(w, err)
		return
	}
	if results == nil {
		results = []ModelHealth{}
	}
	httpjson.Write(w, http.StatusOK, results)
}

type probeTargetStreamEvent struct {
	Type     string        `json:"type"`
	Phase    string        `json:"phase,omitempty"`
	Results  []ModelHealth `json:"results,omitempty"`
	ErrorKey string        `json:"errorKey,omitempty"`
}

// probeTargetStream 以 SSE 推送正式手动探活的排队/执行阶段，结果仍与 JSON 接口一致。
func (h *Handler) probeTargetStream(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	targetID := r.PathValue("id")
	var input ProbeConnectionInput
	if err := httpjson.Decode(r, &input); err != nil && !errors.Is(err, io.EOF) {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpjson.WriteError(w, http.StatusInternalServerError, ErrorUnknown)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	emit := func(event probeTargetStreamEvent) {
		data, err := json.Marshal(event)
		if err != nil || r.Context().Err() != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	results, err := h.service.ProbeTargetWithProgress(r.Context(), userID, targetID, input.Models, func(phase ProbeTargetPhase) {
		emit(probeTargetStreamEvent{Type: "phase", Phase: string(phase)})
	})
	if err != nil {
		if r.Context().Err() == nil {
			errorKey := ErrorUnknown
			var requestErr requestError
			if errors.As(err, &requestErr) {
				errorKey = requestErr.Error()
			}
			emit(probeTargetStreamEvent{Type: "error", ErrorKey: errorKey})
		}
		return
	}
	if results == nil {
		results = []ModelHealth{}
	}
	emit(probeTargetStreamEvent{Type: "result", Results: results})
}

func (h *Handler) disable(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	connectionID := r.PathValue("id")
	if err := h.service.DisableConnection(r.Context(), userID, connectionID); err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) restore(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	connectionID := r.PathValue("id")
	if err := h.service.RestoreConnection(r.Context(), userID, connectionID); err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) listPolicies(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	policies, err := h.service.ListPolicies(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	if policies == nil {
		policies = []Policy{}
	}
	httpjson.Write(w, http.StatusOK, policies)
}

func (h *Handler) createPolicy(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var req PolicyInput
	if err := httpjson.Decode(r, &req); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	req.ID = ""
	policy, err := h.service.SavePolicy(r.Context(), userID, req)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, policy)
}

func (h *Handler) updatePolicy(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var req PolicyInput
	if err := httpjson.Decode(r, &req); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	req.ID = r.PathValue("id")
	policy, err := h.service.SavePolicy(r.Context(), userID, req)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, policy)
}

func (h *Handler) deletePolicy(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if err := h.service.DeletePolicy(r.Context(), userID, r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]bool{"ok": true})
}

// discoverTargetModels 是手动一次性探活弹窗打开时调用的 server-only 模型发现接口：
// 后端用当前 admin session 临时解析该 target 的 base_url + key，请求上游 /v1/models，
// 只把安全字段（id/name/ownedBy/providerFamily）返回前端，不回传/落库任何凭据。
func (h *Handler) discoverTargetModels(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	targetID := r.PathValue("id")
	models, err := h.service.DiscoverTargetModels(r.Context(), userID, targetID)
	if err != nil {
		writeError(w, err)
		return
	}
	if models == nil {
		models = []DiscoveredModel{}
	}
	httpjson.Write(w, http.StatusOK, models)
}

// manualProbeTarget 一次性手动探活：不写状态/事件、不消耗策略预算、不触发状态机或远端动作，
// 结果仅用于弹窗内即时展示。models 必须非空。
func (h *Handler) manualProbeTarget(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	targetID := r.PathValue("id")

	var input ProbeConnectionInput
	if err := httpjson.Decode(r, &input); err != nil && !errors.Is(err, io.EOF) {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}

	results, err := h.service.ManualProbeTarget(r.Context(), userID, targetID, input.Models)
	if err != nil {
		writeError(w, err)
		return
	}
	if results == nil {
		results = []ManualProbeResult{}
	}
	httpjson.Write(w, http.StatusOK, results)
}

func (h *Handler) listTestQuestions(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	questions, err := h.service.ListTestQuestions(r.Context(), userID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, questions)
}

func (h *Handler) createTestQuestion(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var input TestQuestionInput
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	question, err := h.service.CreateTestQuestion(r.Context(), userID, input)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, question)
}

func (h *Handler) updateTestQuestion(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var input TestQuestionInput
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	question, err := h.service.UpdateTestQuestion(r.Context(), userID, r.PathValue("questionId"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, question)
}

func (h *Handler) setTestQuestionEnabled(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err := httpjson.Decode(r, &input); err != nil || input.Enabled == nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	question, err := h.service.SetTestQuestionEnabled(r.Context(), userID, r.PathValue("questionId"), *input.Enabled)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, question)
}

func (h *Handler) setDefaultTestQuestion(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	question, err := h.service.SetDefaultTestQuestion(r.Context(), userID, r.PathValue("questionId"))
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, question)
}

func (h *Handler) deleteTestQuestion(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if err := h.service.DeleteTestQuestion(r.Context(), userID, r.PathValue("questionId")); err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) startQuestionAnswerBatch(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if rejectQuestionAnswerContractMismatch(w, r) {
		return
	}
	var input QuestionAnswerStartInput
	if err := httpjson.Decode(r, &input); err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	batch, err := h.service.StartQuestionAnswerBatch(r.Context(), userID, r.PathValue("id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusAccepted, batch)
}

func (h *Handler) latestQuestionAnswerBatch(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if rejectQuestionAnswerContractMismatch(w, r) {
		return
	}
	batch, err := h.service.LatestQuestionAnswerBatch(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, batch)
}

func (h *Handler) getQuestionAnswerBatch(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if rejectQuestionAnswerContractMismatch(w, r) {
		return
	}
	batch, err := h.service.GetQuestionAnswerBatch(r.Context(), userID, r.PathValue("id"), r.PathValue("batchId"))
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, batch)
}

func (h *Handler) stopQuestionAnswerBatch(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if rejectQuestionAnswerContractMismatch(w, r) {
		return
	}
	batch, err := h.service.StopQuestionAnswerBatch(r.Context(), userID, r.PathValue("id"), r.PathValue("batchId"))
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, batch)
}

func (h *Handler) questionAnswerHistory(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if rejectQuestionAnswerContractMismatch(w, r) {
		return
	}
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
			return
		}
		page = parsed
	}
	history, err := h.service.QuestionAnswerHistory(r.Context(), userID, r.PathValue("id"), page)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, history)
}

func (h *Handler) setQuestionAnswerManualError(w http.ResponseWriter, r *http.Request) {
	_, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	httpjson.WriteError(w, http.StatusConflict, ErrorQuestionAnswerContractMismatch)
}

func (h *Handler) setQuestionAnswerJudgment(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	if rejectQuestionAnswerContractMismatch(w, r) {
		return
	}
	var input struct {
		Judgment QuestionAnswerJudgment `json:"judgment"`
	}
	if err := httpjson.Decode(r, &input); err != nil || !validQuestionAnswerJudgment(input.Judgment) {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	record, err := h.service.SetQuestionAnswerJudgment(r.Context(), userID, r.PathValue("id"), r.PathValue("recordId"), input.Judgment)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, record)
}

func rejectQuestionAnswerContractMismatch(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get(questionAnswerContractHeaderName) == questionAnswerContractVersion {
		return false
	}
	httpjson.WriteError(w, http.StatusConflict, ErrorQuestionAnswerContractMismatch)
	return true
}

func (h *Handler) getPolicyAssignments(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	targetID := r.PathValue("id")
	result, err := h.service.GetTargetPolicyAssignments(r.Context(), userID, targetID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

// PolicyAssignmentInput 是策略分配 PUT 接口的请求体：policyIds 为空表示清空该 target 的分配。
type PolicyAssignmentInput struct {
	PolicyIDs []string `json:"policyIds"`
}

func (h *Handler) putPolicyAssignments(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	targetID := r.PathValue("id")

	var input PolicyAssignmentInput
	if err := httpjson.Decode(r, &input); err != nil && !errors.Is(err, io.EOF) {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}

	result, err := h.service.SetTargetPolicyAssignments(r.Context(), userID, targetID, input.PolicyIDs)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) getAdminGroupPolicyConfiguration(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	result, err := h.service.GetAdminGroupPolicyConfiguration(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) putAdminGroupPolicyConfiguration(w http.ResponseWriter, r *http.Request) {
	userID, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "auth.errors.unauthorized")
		return
	}
	var input AdminGroupPolicyConfigurationInput
	if err := httpjson.Decode(r, &input); err != nil && !errors.Is(err, io.EOF) {
		httpjson.WriteError(w, http.StatusBadRequest, ErrorRequest)
		return
	}
	result, err := h.service.SetAdminGroupPolicyConfiguration(r.Context(), userID, r.PathValue("id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func writeError(w http.ResponseWriter, err error) {
	var requestErr requestError
	if errors.As(err, &requestErr) {
		status := http.StatusBadRequest
		if requestErr == requestError(ErrorNotFound) || requestErr == requestError(ErrorTestQuestionNotFound) || requestErr == requestError(ErrorQuestionAnswerBatchNotFound) {
			status = http.StatusNotFound
		}
		if requestErr == requestError(ErrorNoCurrentAccount) || requestErr == requestError(ErrorQuestionAnswerActive) || requestErr == requestError(ErrorQuestionAnswerServiceStopped) {
			status = http.StatusConflict
		}
		if requestErr == requestError(ErrorQuestionAnswerContractMismatch) || requestErr == requestError(ErrorQuestionAnswerJudgmentForbidden) {
			status = http.StatusConflict
		}
		if requestErr == requestError(ErrorSub2APIGroupLastUsable) || requestErr == requestError(ErrorSub2APIInventoryIncomplete) {
			status = http.StatusConflict
		}
		if requestErr == requestError(ErrorQuestionAnswerStorage) {
			status = http.StatusInternalServerError
		}
		httpjson.WriteError(w, status, requestErr.Error())
		return
	}
	httpjson.WriteError(w, http.StatusInternalServerError, ErrorUnknown)
}
