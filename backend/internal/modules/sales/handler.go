package sales

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/httpjson"
	"transithub/backend/internal/shared/workspacescope"
)

type Handler struct{ service *Service }

func RegisterRoutes(mux *http.ServeMux, service *Service) {
	h := &Handler{service: service}
	mux.HandleFunc("GET /api/sales/summary", h.summary)
	mux.HandleFunc("GET /api/sales/entries", h.entries)
	mux.HandleFunc("POST /api/sales/exclusions", h.exclude)
	mux.HandleFunc("POST /api/sales/exclusions/delete", h.restore)
	mux.HandleFunc("PUT /api/sales/settings", h.settings)
}

func publicReason(err error) string {
	var request *requestError
	if errors.As(err, &request) {
		return request.Error()
	}
	if errors.Is(err, workspacescope.ErrChanged) {
		return "原请求工作区已被删除、重建或改变，无法继续读取该次资料；请重新打开当前工作区。"
	}
	if errors.Is(err, ErrUnavailable) {
		return ErrUnavailable.Error()
	}
	if errors.Is(err, context.Canceled) {
		return "本地处理已取消；请在服务恢复后重新读取销售明细。"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "本地处理超过允许时间；请核查本站数据库负载后重新读取。"
	}
	var database *pgconn.PgError
	if errors.As(err, &database) {
		switch database.Code {
		case "42P01", "42703":
			return "所需资料表或字段尚未初始化；请先用正常服务完成升级。"
		case "42501":
			return "数据库账号没有完成这项操作所需的权限；请核查销售资料表权限后重试。"
		case "40001", "40P01":
			return "并发更新导致本次数据库事务回退；请重新读取销售明细后重试。"
		}
	}
	return "本地数据库处理未完成，具体原因尚未确认；请核查本站服务日志和数据库连接。"
}

func (h *Handler) failure(w http.ResponseWriter, scope Scope, step string, err error, write bool) {
	status := http.StatusInternalServerError
	reason := publicReason(err)
	message := "读取" + step + "失败：" + reason
	impact := "本次只读本地资料，没有修改主站或销售设置。"
	action := "检查本站服务日志及数据库连接后重新读取。"
	retryable := true
	if write {
		message = "保存" + step + "失败：" + reason + "原设置仍保留；请核查后重试。"
		impact = "本次保存未提交；原排除和设置保留，没有修改主站、首页营收或赠送记录。"
	}
	var commit *workspacescope.CommitError
	switch {
	case errors.As(err, &commit):
		message = "保存" + step + "的结果尚未确认：" + publicReason(commit.Cause) + "请先重新读取，确认当前设置后再操作，避免重复修改。"
		impact = "本地保存可能已经完成；没有修改主站、首页营收或赠送记录。"
		action = "先重新读取明细和设置，再决定是否重试。"
		retryable = false
	case errors.Is(err, workspacescope.ErrChanged):
		status = http.StatusConflict
		message = workspacescope.ErrChanged.Error()
		impact = "本次操作已停止，旧工作区的选择不会写入新工作区。"
		action = "重新读取当前工作区后再操作。"
	case errors.Is(err, ErrInvalid):
		status = http.StatusBadRequest
		action = "修正输入或重新读取销售明细后选择。"
	case errors.Is(err, ErrUnavailable):
		status = http.StatusServiceUnavailable
		action = "请先用正常服务完成升级，再读取销售分析。"
	}
	httpjson.Write(w, status, map[string]any{
		"message": message, "step": step, "object": scope.AdminAccountID, "impact": impact, "action": action, "retryable": retryable,
		"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt,
	})
}

func (h *Handler) bad(w http.ResponseWriter, scope Scope, message string) {
	h.failure(w, scope, "销售请求", invalid(message), false)
}

func validScope(scope Scope) bool {
	_, err := time.Parse(time.RFC3339Nano, scope.CreatedAt)
	return scope.AdminAccountID != "" && utf8.RuneCountInString(scope.AdminAccountID) <= 128 && len(scope.CreatedAt) <= 64 && err == nil
}

func (h *Handler) user(w http.ResponseWriter, r *http.Request) (string, bool) {
	user, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "请先登录本站后再读取销售分析。")
	}
	return user, ok
}

func (h *Handler) getScope(w http.ResponseWriter, r *http.Request, entries bool) (Scope, bool) {
	user, ok := h.user(w, r)
	if !ok {
		return Scope{}, false
	}
	q := r.URL.Query()
	scope := Scope{UserID: user, AdminAccountID: q.Get("workspaceAdminAccountId"), CreatedAt: q.Get("workspaceCreatedAt")}
	for key, values := range q {
		allowed := key == "workspaceAdminAccountId" || key == "workspaceCreatedAt" || key == "from" || key == "to" || key == "days"
		if entries {
			allowed = allowed || key == "page" || key == "pageSize" || key == "channel" || key == "q" || key == "excluded"
		}
		if !allowed || len(values) != 1 {
			h.bad(w, scope, "请求包含不支持或重复的查询参数；请重新打开销售分析后操作。")
			return scope, false
		}
	}
	if !validScope(scope) {
		h.bad(w, scope, "工作区编号或创建时间缺失、格式不正确；请重新读取当前工作区。")
		return scope, false
	}
	if err := h.service.repo.Ready(r.Context()); err != nil {
		h.failure(w, scope, "销售分析", err, false)
		return scope, false
	}
	if err := workspacescope.Resolve(r.Context(), h.service.repo.db, scope); err != nil {
		h.failure(w, scope, "销售分析", err, false)
		return scope, false
	}
	return scope, true
}

func parseInteger(r *http.Request, key string, fallback, min, max int) (int, error) {
	values, exists := r.URL.Query()[key]
	if !exists {
		return fallback, nil
	}
	value, err := strconv.Atoi(values[0])
	if err != nil || value < min || value > max {
		return 0, invalid("参数" + key + "超出允许范围或不是整数；请重新选择日期、页码或每页数量。")
	}
	return value, nil
}

func (h *Handler) getRange(w http.ResponseWriter, r *http.Request, scope Scope) (DayRange, bool) {
	q := r.URL.Query()
	if q.Has("from") != q.Has("to") || (q.Has("from") && (q.Get("from") == "" || q.Get("to") == "")) || (q.Has("days") && q.Has("from")) {
		h.bad(w, scope, "开始和结束日期须同时填写，且不能同时提交天数；请只使用一种日期选择方式。")
		return DayRange{}, false
	}
	days := 0
	if !q.Has("from") {
		var err error
		days, err = parseInteger(r, "days", 30, 1, 400)
		if err != nil {
			h.bad(w, scope, err.Error())
			return DayRange{}, false
		}
	}
	span, err := ResolveRange(RangeQuery{From: q.Get("from"), To: q.Get("to"), Days: days}, businesstime.Today())
	if err != nil {
		h.bad(w, scope, err.Error())
		return DayRange{}, false
	}
	return span, true
}

func (h *Handler) write(w http.ResponseWriter, scope Scope, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		h.failure(w, scope, "销售资料响应", err, false)
		return
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil || result == nil {
		h.failure(w, scope, "销售资料响应", errors.New("invalid response"), false)
		return
	}
	result["workspaceAdminAccountId"], _ = json.Marshal(scope.AdminAccountID)
	result["workspaceCreatedAt"], _ = json.Marshal(scope.CreatedAt)
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.getScope(w, r, false)
	if !ok {
		return
	}
	span, ok := h.getRange(w, r, scope)
	if !ok {
		return
	}
	result, err := h.service.Summary(r.Context(), scope, span)
	if err != nil {
		h.failure(w, scope, "销售概览", err, false)
		return
	}
	h.write(w, scope, result)
}

func (h *Handler) entries(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.getScope(w, r, true)
	if !ok {
		return
	}
	span, ok := h.getRange(w, r, scope)
	if !ok {
		return
	}
	page, err := parseInteger(r, "page", 1, 1, int(^uint(0)>>1)/100)
	if err != nil {
		h.bad(w, scope, err.Error())
		return
	}
	size, err := parseInteger(r, "pageSize", 20, 1, 100)
	if err != nil {
		h.bad(w, scope, err.Error())
		return
	}
	channel := r.URL.Query().Get("channel")
	rawSearch := r.URL.Query().Get("q")
	search := strings.TrimSpace(rawSearch)
	excluded := r.URL.Query().Get("excluded")
	if (channel != "" && !contains(channels, channel)) || utf8.RuneCountInString(rawSearch) > 100 || (r.URL.Query().Has("excluded") && excluded != "0" && excluded != "1") {
		h.bad(w, scope, "渠道不受支持、搜索超过100字或排除筛选不是0/1；请修正筛选后重试。")
		return
	}
	result, err := h.service.Entries(r.Context(), scope, span, EntryQuery{Page: page, PageSize: size, Channel: channel, Search: search, ExcludedOnly: excluded == "1"})
	if err != nil {
		h.failure(w, scope, "销售明细", err, false)
		return
	}
	h.write(w, scope, result)
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
			"message": "当前实例不提供这项操作，请在正常服务执行。", "step": "保存销售设置", "object": scope.AdminAccountID,
			"impact": "本次没有保存或修改主站。", "action": "请在正常服务执行。", "retryable": false,
			"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt,
		})
		return nil, false
	}
	if err := h.service.repo.Ready(r.Context()); err != nil {
		h.failure(w, *scope, "销售设置", err, true)
		return nil, false
	}
	return fields, true
}

func (h *Handler) change(w http.ResponseWriter, r *http.Request, restore bool) {
	var request struct {
		Scope
		Entries []EntryKey `json:"entries"`
	}
	if _, ok := h.decode(w, r, &request, &request.Scope); !ok {
		return
	}
	result, err := h.service.ChangeExclusions(r.Context(), request.Scope, request.Entries, restore)
	if err != nil {
		h.failure(w, request.Scope, "入账排除选择", err, true)
		return
	}
	if _, err := json.Marshal(result); err != nil {
		result.Exclusions = nil
		result.RefreshMessage = "本地选择已保存，但排除概览的金额无法完整显示；请重新读取明细并检查本站服务日志，不要重复提交。"
	}
	h.write(w, request.Scope, result)
}

func (h *Handler) exclude(w http.ResponseWriter, r *http.Request) {
	h.change(w, r, false)
}

func (h *Handler) restore(w http.ResponseWriter, r *http.Request) {
	h.change(w, r, true)
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
	if _, exists := fields["excludedChannels"]; !exists {
		h.bad(w, request.Scope, "缺少排除渠道列表；如不排除任何渠道，请明确提交空数组。")
		return
	}
	saved, err := h.service.SaveSettings(r.Context(), request.Scope, request.ExcludedChannels)
	if err != nil {
		h.failure(w, request.Scope, "销售渠道设置", err, true)
		return
	}
	h.write(w, request.Scope, map[string]any{"saved": true, "settings": saved})
}
