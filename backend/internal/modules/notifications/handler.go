package notifications

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
)

type Handler struct{ service *Service }

func RegisterRoutes(mux *http.ServeMux, service *Service) {
	h := &Handler{service: service}
	mux.HandleFunc("GET /api/notifications/summary", h.summary)
	mux.HandleFunc("POST /api/notifications/summary", h.summaryRead)
	mux.HandleFunc("GET /api/notifications/{$}", h.history)
	mux.HandleFunc("GET /api/notifications", h.history)
	mux.HandleFunc("GET /api/notifications/preferences", h.preferences)
	mux.HandleFunc("PUT /api/notifications/preferences", h.savePreferences)
	mux.HandleFunc("GET /api/notifications/signup-risk-preferences", h.signupRiskPreferences)
	mux.HandleFunc("PUT /api/notifications/signup-risk-preferences", h.saveSignupRiskPreferences)
	mux.HandleFunc("POST /api/notifications/progress", h.progress)
	mux.HandleFunc("GET /api/notifications/{id}", h.detail)
}

func (h *Handler) signupRiskPreferences(w http.ResponseWriter, r *http.Request) {
	user, workspace, ok := requestScope(w, r, r.URL.Query().Get("workspaceAdminAccountId"))
	if !ok {
		return
	}
	result, err := h.service.GetSignupRiskPreferences(r.Context(), user, workspace)
	if err != nil {
		writeRequestError(w, workspace, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) saveSignupRiskPreferences(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceAdminAccountID string `json:"workspaceAdminAccountId"`
		Enabled                 *bool  `json:"enabled"`
		WorkStart               string `json:"workStart"`
		WorkEnd                 string `json:"workEnd"`
		TimeZone                string `json:"timeZone"`
	}
	if !decodeRequest(w, r, &body, func() string { return body.WorkspaceAdminAccountID }) {
		return
	}
	user, workspace, ok := requestScope(w, r, body.WorkspaceAdminAccountID)
	if !ok {
		return
	}
	if body.Enabled == nil {
		writeRequestError(w, workspace, invalidRequest(workspace, "必须明确选择是否开启高风险注册提醒（布尔值）；原设置没有改变"))
		return
	}
	result, err := h.service.SaveSignupRiskPreferences(r.Context(), user, workspace, *body.Enabled, SignupRiskSchedule{WorkStart: body.WorkStart, WorkEnd: body.WorkEnd, TimeZone: body.TimeZone})
	if err != nil {
		writeRequestError(w, workspace, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func writeRequestError(w http.ResponseWriter, workspace string, err error) {
	var failure *RequestError
	if !errors.As(err, &failure) {
		failure = storageFailure(workspace, "处理这次通知请求", err)
	}
	httpjson.Write(w, failure.Status, failure)
}

func requestScope(w http.ResponseWriter, r *http.Request, workspace string) (string, string, bool) {
	user, ok := authctx.UserID(r.Context())
	if !ok {
		writeRequestError(w, workspace, requestFailure(http.StatusUnauthorized, workspace, "登录状态已失效", "本次未读取或保存通知", "请重新登录后重试", false))
		return "", "", false
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" || checkText("工作区编号", workspace, 512, true, true) != nil {
		writeRequestError(w, "", invalidRequest("", "必须明确提供当前工作区编号；编号须是有效纯文字且不超过512个字符"))
		return "", "", false
	}
	return user, workspace, true
}

func decodeRequest(w http.ResponseWriter, r *http.Request, target any, workspace func() string) bool {
	return decodeRequestIntent(w, r, target, workspace, false)
}

func decodeRequestIntent(w http.ResponseWriter, r *http.Request, target any, workspace func() string, readOnly bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(target)
	if err == nil {
		var extra any
		err = decoder.Decode(&extra)
		if err == io.EOF {
			return true
		}
		if err == nil {
			writeRequestError(w, workspace(), invalidRequest(workspace(), "请求正文在第一份JSON后还有其他内容，只能发送一份完整JSON；本次没有改变任何消息或设置"))
			return false
		}
	}
	reason := "请求正文包含不允许的字段或不是通知接口要求的结构；本次没有保存"
	var typeError *json.UnmarshalTypeError
	var syntaxError *json.SyntaxError
	var sizeError *http.MaxBytesError
	switch {
	case errors.As(err, &typeError):
		switch typeError.Field {
		case "displayedIds":
			reason = "已显示的消息编号必须是一组纯文字编号；本次没有改变任何消息"
		case "intervalMinutes":
			reason = "检查间隔必须使用1到60的整数分钟，小数、文字和其他类型无法保存；原有间隔没有改变"
		case "ids":
			reason = "消息编号必须是一组纯文字编号；本组消息没有保存"
		case "workspaceAdminAccountId":
			reason = "工作区编号必须是纯文字；本次没有保存"
		case "action":
			reason = "消息操作名称必须是纯文字；本组消息没有保存"
		default:
			reason = "请求字段类型与通知接口要求不一致；本次没有保存"
		}
	case errors.As(err, &sizeError):
		reason = "请求正文超过64KiB上限，请减少本次消息编号或正文长度；本次没有保存"
	case errors.As(err, &syntaxError), errors.Is(err, io.ErrUnexpectedEOF):
		reason = "请求正文的JSON格式不完整或格式不正确；本次没有保存"
	case errors.Is(err, io.EOF):
		reason = "请求正文为空，没有提供要保存的消息操作或检查间隔；本次没有保存"
	}
	if readOnly {
		reason = strings.ReplaceAll(reason, "本次没有保存", "本次未读取通知，也没有改变任何消息或设置")
		if errors.Is(err, io.EOF) {
			reason = "请求正文为空，请提供当前工作区和需要核查的消息编号后重试读取；本次没有改变任何消息或设置"
		}
	}
	writeRequestError(w, workspace(), invalidRequest(workspace(), reason))
	return false
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	user, workspace, ok := requestScope(w, r, query.Get("workspaceAdminAccountId"))
	if !ok {
		return
	}
	result, err := h.service.Summary(r.Context(), user, workspace, SummaryQuery{AttentionCursor: query.Get("attentionCursor"), ImportantCursor: query.Get("importantCursor"), CriticalCursor: query.Get("criticalCursor")})
	if err != nil {
		writeRequestError(w, workspace, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) summaryRead(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceAdminAccountID string   `json:"workspaceAdminAccountId"`
		AttentionCursor         string   `json:"attentionCursor"`
		ImportantCursor         string   `json:"importantCursor"`
		CriticalCursor          string   `json:"criticalCursor"`
		DisplayedIDs            []string `json:"displayedIds"`
	}
	if !decodeRequestIntent(w, r, &body, func() string { return body.WorkspaceAdminAccountID }, true) {
		return
	}
	user, workspace, ok := requestScope(w, r, body.WorkspaceAdminAccountID)
	if !ok {
		return
	}
	result, err := h.service.Summary(r.Context(), user, workspace, SummaryQuery{AttentionCursor: body.AttentionCursor, ImportantCursor: body.ImportantCursor, CriticalCursor: body.CriticalCursor, DisplayedIDs: body.DisplayedIDs})
	if err != nil {
		writeRequestError(w, workspace, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	user, workspace, ok := requestScope(w, r, query.Get("workspaceAdminAccountId"))
	if !ok {
		return
	}
	limit := 20
	if raw := query.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 {
			writeRequestError(w, workspace, invalidRequest(workspace, "每页消息数量须是正整数，默认20条，最多50条"))
			return
		}
	}
	result, err := h.service.History(r.Context(), user, workspace, HistoryQuery{Limit: limit, Cursor: query.Get("cursor"), Filter: query.Get("filter")})
	if err != nil {
		writeRequestError(w, workspace, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	user, workspace, ok := requestScope(w, r, r.URL.Query().Get("workspaceAdminAccountId"))
	if !ok {
		return
	}
	result, err := h.service.Detail(r.Context(), user, workspace, r.PathValue("id"))
	if err != nil {
		writeRequestError(w, workspace, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) progress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceAdminAccountID string   `json:"workspaceAdminAccountId"`
		IDs                     []string `json:"ids"`
		Action                  string   `json:"action"`
	}
	if !decodeRequest(w, r, &body, func() string { return body.WorkspaceAdminAccountID }) {
		return
	}
	user, workspace, ok := requestScope(w, r, body.WorkspaceAdminAccountID)
	if !ok {
		return
	}
	result, err := h.service.Progress(r.Context(), user, workspace, body.IDs, body.Action)
	if err != nil {
		writeRequestError(w, workspace, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) preferences(w http.ResponseWriter, r *http.Request) {
	user, workspace, ok := requestScope(w, r, r.URL.Query().Get("workspaceAdminAccountId"))
	if !ok {
		return
	}
	result, err := h.service.Preferences(r.Context(), user, workspace)
	if err != nil {
		writeRequestError(w, workspace, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) savePreferences(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceAdminAccountID string `json:"workspaceAdminAccountId"`
		IntervalMinutes         int    `json:"intervalMinutes"`
	}
	if !decodeRequest(w, r, &body, func() string { return body.WorkspaceAdminAccountID }) {
		return
	}
	user, workspace, ok := requestScope(w, r, body.WorkspaceAdminAccountID)
	if !ok {
		return
	}
	result, err := h.service.SavePreferences(r.Context(), user, workspace, body.IntervalMinutes)
	if err != nil {
		writeRequestError(w, workspace, err)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
