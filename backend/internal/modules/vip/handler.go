package vip

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
	"transithub/backend/internal/shared/secretbox"
	"transithub/backend/internal/shared/workspacescope"
)

type Handler struct{ service *Service }

func RegisterRoutes(mux *http.ServeMux, service *Service) {
	h := &Handler{service: service}
	mux.HandleFunc("GET /api/vip/summary", h.summary)
	mux.HandleFunc("GET /api/vip/search", h.search)
	mux.HandleFunc("POST /api/vip/watch", h.watch)
	mux.HandleFunc("POST /api/vip/unwatch", h.unwatch)
	mux.HandleFunc("POST /api/vip/dismissed/restore", h.restore)
	mux.HandleFunc("PUT /api/vip/settings", h.settings)
}

func publicReason(err error) string {
	var target *settings.TelegramTargetError
	if errors.As(err, &target) {
		return target.Error()
	}
	var validation *settingsValidationError
	if errors.As(err, &validation) {
		return validation.Error()
	}
	for _, known := range []error{ErrUnavailable, ErrNotFound, ErrInvalidSettings, ErrNotificationUnavailable,
		settings.ErrTelegramTargetUnavailable, settings.ErrTelegramProxyInvalid, settings.ErrTelegramChannelFormat} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	if errors.Is(err, secretbox.ErrNoKey) || errors.Is(err, secretbox.ErrUndecryptable) {
		return err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "本地处理超过允许时间；请检查本站数据库负载后重新读取。"
	}
	if errors.Is(err, context.Canceled) {
		return "本次本地处理已取消；请在服务恢复后重新读取。"
	}
	var database *pgconn.PgError
	if errors.As(err, &database) {
		switch database.Code {
		case "42P01", "42703":
			return "所需资料表或字段尚未初始化；请先用正常服务完成升级后重试。"
		case "42501":
			return "当前数据库账号没有完成这项操作所需的权限；请核查数据库表权限后重试。"
		case "40001", "40P01":
			return "并发更新导致本次数据库事务回退；请重新读取当前资料后再操作。"
		}
	}
	return "本地数据库或资料读取未完成，具体原因尚未确认；请检查本站服务日志和数据库连接后重试。"
}

func (h *Handler) failure(w http.ResponseWriter, scope Scope, step string, err error, write bool) {
	status := http.StatusInternalServerError
	reason := publicReason(err)
	impact := "本次未修改本地资料或主站。"
	action := "检查本站服务日志及数据库连接后重新读取。"
	message := "读取大户关注失败：" + reason
	retryable := true
	if write {
		message = "保存" + step + "失败：" + reason + "原设置仍保留；请核查后重试。"
		impact = "本次保存未提交，不会修改主站或发送通知。"
	}
	var commit *workspacescope.CommitError
	switch {
	case errors.As(err, &commit):
		message = "保存" + step + "的结果尚未确认；请先重新读取，确认当前设置后再操作，避免重复修改。"
		impact = "本地保存可能已经完成，但本次没有调用Telegram发送。"
		action = "先重新读取当前设置，再决定是否操作。"
		retryable = false
	case errors.Is(err, workspacescope.ErrChanged):
		status = http.StatusConflict
		message = workspacescope.ErrChanged.Error()
		impact = "本次操作已停止，旧工作区的资料不会转存到新工作区。"
		action = "重新读取当前工作区后再操作。"
	case errors.Is(err, ErrUnavailable):
		status = http.StatusServiceUnavailable
		message = ErrUnavailable.Error()
		action = "先用正常服务完成升级，再读取大户关注。"
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, settings.ErrTelegramTargetUnavailable), errors.Is(err, settings.ErrTelegramProxyInvalid), errors.Is(err, ErrNotificationUnavailable):
		status = http.StatusConflict
	case errors.Is(err, ErrInvalidSettings):
		status = http.StatusBadRequest
	}
	httpjson.Write(w, status, map[string]any{
		"message": message, "step": step, "object": scope.AdminAccountID, "impact": impact,
		"action": action, "retryable": retryable,
		"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt,
	})
}

func (h *Handler) bad(w http.ResponseWriter, scope Scope, message string) {
	httpjson.Write(w, http.StatusBadRequest, map[string]any{
		"message": message, "step": "检查请求", "object": "大户关注", "impact": "本次没有保存或发送。",
		"action": "重新读取当前工作区并检查输入后重试。", "retryable": true,
		"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt,
	})
}

func validScope(scope Scope) bool {
	_, err := time.Parse(time.RFC3339Nano, scope.CreatedAt)
	return scope.AdminAccountID != "" && utf8.RuneCountInString(scope.AdminAccountID) <= 128 && len(scope.CreatedAt) <= 64 && err == nil
}

func (h *Handler) user(w http.ResponseWriter, r *http.Request) (string, bool) {
	user, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "请先登录本站后再读取大户关注。")
	}
	return user, ok
}

func (h *Handler) getScope(w http.ResponseWriter, r *http.Request, search bool) (Scope, bool) {
	user, ok := h.user(w, r)
	if !ok {
		return Scope{}, false
	}
	query := r.URL.Query()
	scope := Scope{UserID: user, AdminAccountID: query.Get("workspaceAdminAccountId"), CreatedAt: query.Get("workspaceCreatedAt")}
	for key, values := range query {
		if (key != "workspaceAdminAccountId" && key != "workspaceCreatedAt" && !(search && key == "q")) || len(values) != 1 {
			h.bad(w, scope, "请求包含不支持或重复的查询参数；请重新打开大户关注后重试。")
			return scope, false
		}
	}
	if !validScope(scope) {
		h.bad(w, scope, "工作区编号或创建时间缺失、格式不正确；请重新读取当前工作区后重试。")
		return scope, false
	}
	if err := h.service.repo.Ready(r.Context()); err != nil {
		h.failure(w, scope, "大户关注", err, false)
		return scope, false
	}
	if err := workspacescope.Resolve(r.Context(), h.service.repo.db, scope); err != nil {
		h.failure(w, scope, "大户关注", err, false)
		return scope, false
	}
	return scope, true
}

func (h *Handler) write(w http.ResponseWriter, scope Scope, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		h.failure(w, scope, "大户关注响应", err, false)
		return
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil || result == nil {
		h.failure(w, scope, "大户关注响应", errors.New("invalid response"), false)
		return
	}
	result["workspaceAdminAccountId"], _ = json.Marshal(scope.AdminAccountID)
	result["workspaceCreatedAt"], _ = json.Marshal(scope.CreatedAt)
	// Echo the original scope. A later selection change must not misreport a committed write.
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.getScope(w, r, false)
	if !ok {
		return
	}
	result, err := h.service.Summary(r.Context(), scope)
	if err != nil {
		h.failure(w, scope, "大户关注", err, false)
		return
	}
	h.write(w, scope, result)
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.getScope(w, r, true)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(query) > 100 {
		h.bad(w, scope, "搜索文字不能超过100字；请缩短后重新查询。")
		return
	}
	result, err := h.service.repo.Search(r.Context(), scope, query)
	if err != nil {
		h.failure(w, scope, "搜索客户", err, false)
		return
	}
	h.write(w, scope, map[string]any{"items": result})
}

func (h *Handler) decode(w http.ResponseWriter, r *http.Request, target any, scope *Scope) (map[string]json.RawMessage, bool) {
	user, ok := h.user(w, r)
	if !ok {
		return nil, false
	}
	fields, err := httpjson.DecodeStrictObject(w, r, target)
	if err != nil {
		h.bad(w, *scope, err.Error())
		return nil, false
	}
	scope.UserID = user
	if !validScope(*scope) {
		h.bad(w, *scope, "工作区编号或创建时间无效；请重新读取当前工作区后重试。")
		return nil, false
	}
	if h.service.apiOnly {
		httpjson.Write(w, http.StatusConflict, map[string]any{
			"message": "当前实例不提供这项操作，请在正常服务执行。", "step": "保存大户关注", "object": scope.AdminAccountID,
			"impact": "本次没有保存或发送。", "action": "请在正常服务执行。", "retryable": false,
			"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt,
		})
		return nil, false
	}
	if err := h.service.repo.Ready(r.Context()); err != nil {
		h.failure(w, *scope, "大户关注", err, true)
		return nil, false
	}
	return fields, true
}

func (h *Handler) saved(w http.ResponseWriter, r *http.Request, scope Scope, restored int) {
	summary, err := h.service.Summary(r.Context(), scope)
	if err == nil {
		_, err = json.Marshal(summary)
	}
	response := map[string]any{"saved": true, "restored": restored}
	if err == nil {
		response["summary"] = summary
	} else {
		response["summary"] = nil
		response["refreshMessage"] = "本地设置已保存，但更新后的名单读取未完成：" + publicReason(err) + "请重新读取名单；不要重复保存。"
	}
	h.write(w, scope, response)
}

func (h *Handler) watch(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Scope
		SiteUserID string `json:"siteUserId"`
		Note       string `json:"note"`
	}
	if _, ok := h.decode(w, r, &request, &request.Scope); !ok {
		return
	}
	if request.SiteUserID == "" || utf8.RuneCountInString(request.SiteUserID) > 128 || utf8.RuneCountInString(request.Note) > 200 {
		h.bad(w, request.Scope, "客户编号必须有效且不超过128字，备注不能超过200字；请检查输入后重试。")
		return
	}
	if err := h.service.Watch(r.Context(), request.Scope, request.SiteUserID, request.Note, false); err != nil {
		h.failure(w, request.Scope, "关注名单", err, true)
		return
	}
	h.saved(w, r, request.Scope, 0)
}

func (h *Handler) unwatch(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Scope
		SiteUserID string `json:"siteUserId"`
	}
	if _, ok := h.decode(w, r, &request, &request.Scope); !ok {
		return
	}
	if request.SiteUserID == "" || utf8.RuneCountInString(request.SiteUserID) > 128 {
		h.bad(w, request.Scope, "客户编号为空或超过128字，请重新选择客户。")
		return
	}
	if err := h.service.Watch(r.Context(), request.Scope, request.SiteUserID, "", true); err != nil {
		h.failure(w, request.Scope, "移出关注", err, true)
		return
	}
	h.saved(w, r, request.Scope, 0)
}

func (h *Handler) restore(w http.ResponseWriter, r *http.Request) {
	var scope Scope
	if _, ok := h.decode(w, r, &scope, &scope); !ok {
		return
	}
	restored, err := h.service.Restore(r.Context(), scope)
	if err != nil {
		h.failure(w, scope, "恢复推荐", err, true)
		return
	}
	h.saved(w, r, scope, restored)
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Scope
		Settings
	}
	fields, ok := h.decode(w, r, &request, &request.Scope)
	if !ok {
		return
	}
	for _, key := range []string{"dailyTop", "lifetimeTop", "alertDays", "alertBalance", "alertsEnabled", "botIds", "reminderText", "emptyReminderText"} {
		if _, exists := fields[key]; !exists {
			h.bad(w, request.Scope, "大户关注设置缺少必要字段；请重新读取设置，完整填写后保存。")
			return
		}
	}
	if err := validateSettings(request.Settings); err != nil {
		h.failure(w, request.Scope, "提醒设置", err, true)
		return
	}
	if err := h.service.SaveSettings(r.Context(), request.Scope, request.Settings); err != nil {
		h.failure(w, request.Scope, "提醒设置", err, true)
		return
	}
	h.saved(w, r, request.Scope, 0)
}
