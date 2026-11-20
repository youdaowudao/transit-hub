package user_activity

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
	"transithub/backend/internal/shared/workspacescope"
)

type Handler struct{ s *Service }

func RegisterRoutes(mux *http.ServeMux, s *Service) {
	h := &Handler{s}
	mux.HandleFunc("GET /api/user-activity/summary", h.summary)
	mux.HandleFunc("GET /api/user-activity/sync", h.sync)
	mux.HandleFunc("POST /api/user-activity/sync", h.start)
	mux.HandleFunc("GET /api/user-activity/users", h.users)
	mux.HandleFunc("GET /api/user-activity/users/{id}", h.detail)
}
func (h *Handler) error(w http.ResponseWriter, scope Scope, status int, step, object, message, impact, action string, retry bool) {
	httpjson.Write(w, status, map[string]any{"message": message, "step": step, "object": object, "impact": impact, "action": action, "retryable": retry, "workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt})
}
func (h *Handler) failure(w http.ResponseWriter, scope Scope, err error) {
	status := http.StatusBadGateway
	message := "读取本地用户分析资料失败：" + safeReason(err) + "；请核查本站服务日志和数据库连接后重试。"
	impact := "已经保存的资料仍保留。"
	action := "检查本站服务日志与管理员账号连接后重新读取。"
	retry := true
	var commit *workspacescope.CommitError
	switch {
	case errors.As(err, &commit):
		status = 500
		message = "保存用户分析同步登记的结果尚未确认；请先重新读取同步状态后再操作，避免重复修改。"
		impact = "登记可能已经保存，尚未确认；不能据此判断已经开始读取。"
		action = "先重新读取同步状态，再决定是否重试。"
		retry = false
	case errors.Is(err, workspacescope.ErrChanged):
		status = 409
		message = workspacescope.ErrChanged.Error()
		impact = "当前请求已停止，旧工作区的资料不会转存到新工作区。"
	case errors.Is(err, ErrUnavailable):
		status = 503
		message = ErrUnavailable.Error()
		impact = "本次用户分析没有执行，原功能可继续使用。"
		action = "请先用正常服务完成升级，再重新读取。"
	case errors.Is(err, ErrBusy):
		status = 409
		message = ErrBusy.Error()
		impact = "本次没有开始。"
		action = "请稍后重新同步。"
	case errors.Is(err, ErrNotFound):
		status = 404
		message = ErrNotFound.Error()
		impact = "未向主站补查或修改任何资料。"
	default:
		var b businessError
		if errors.As(err, &b) {
			status = 409
			message = string(b)
			impact = "本次没有开始新的采集。"
		}
	}
	h.error(w, scope, status, "用户分析", scope.AdminAccountID, message, impact, action, retry)
}
func validScope(s Scope) bool {
	_, e := time.Parse(time.RFC3339Nano, s.CreatedAt)
	return s.AdminAccountID != "" && utf8.RuneCountInString(s.AdminAccountID) <= 128 && len(s.CreatedAt) <= 64 && e == nil
}
func (h *Handler) scope(w http.ResponseWriter, r *http.Request, extra ...string) (Scope, bool) {
	uid, ok := authctx.UserID(r.Context())
	if !ok {
		h.error(w, Scope{}, 401, "登录", "本站", "请先登录本站后重试。", "本次未读取资料。", "请登录本站。", false)
		return Scope{}, false
	}
	s := Scope{UserID: uid, AdminAccountID: r.URL.Query().Get("workspaceAdminAccountId"), CreatedAt: r.URL.Query().Get("workspaceCreatedAt")}
	allowed := map[string]bool{"workspaceAdminAccountId": true, "workspaceCreatedAt": true}
	for _, key := range extra {
		allowed[key] = true
	}
	for key, values := range r.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			h.error(w, s, 400, "检查参数", key, "请求包含不支持或重复的参数，请重新读取页面后重试。", "本次未读取资料。", "重新打开用户分析。", true)
			return s, false
		}
	}
	if !validScope(s) {
		h.error(w, s, 400, "检查工作区", s.AdminAccountID, "工作区编号或创建时间缺失、格式不正确；请重新读取当前工作区后重试。", "本次未读取资料。", "重新打开用户分析。", true)
		return s, false
	}
	if e := h.s.repo.Ready(r.Context()); e != nil {
		h.failure(w, s, e)
		return s, false
	}
	if e := workspacescope.Resolve(r.Context(), h.s.repo.db, s); e != nil {
		h.failure(w, s, e)
		return s, false
	}
	return s, true
}
func (h *Handler) write(w http.ResponseWriter, r *http.Request, status int, s Scope, value any) {
	raw, e := json.Marshal(value)
	if e != nil {
		h.failure(w, s, e)
		return
	}
	var data map[string]json.RawMessage
	if e = json.Unmarshal(raw, &data); e != nil {
		h.failure(w, s, e)
		return
	}
	data["workspaceAdminAccountId"], _ = json.Marshal(s.AdminAccountID)
	data["workspaceCreatedAt"], _ = json.Marshal(s.CreatedAt)
	httpjson.Write(w, status, data)
}
func integer(raw string, fallback, min, max int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n < min || n > max {
		return 0, fail("页号、数量或日期范围超出允许值；请检查筛选条件后重试。")
	}
	return n, nil
}
func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scope(w, r, "days")
	if !ok {
		return
	}
	days, e := integer(r.URL.Query().Get("days"), 60, 1, 400)
	if e != nil {
		h.error(w, s, 400, "检查范围", "days", e.Error(), "未读取资料。", "选择1至400天。", true)
		return
	}
	value, e := h.s.Summary(r.Context(), s, days)
	if e != nil {
		h.failure(w, s, e)
		return
	}
	h.write(w, r, 200, s, value)
}
func (h *Handler) sync(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scope(w, r)
	if !ok {
		return
	}
	value, e := h.s.repo.Sync(r.Context(), s)
	if e != nil {
		h.failure(w, s, e)
		return
	}
	h.write(w, r, 200, s, value)
}
func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	uid, ok := authctx.UserID(r.Context())
	if !ok {
		h.error(w, Scope{}, 401, "登录", "本站", "请先登录本站后重试。", "未开始读取。", "登录本站。", false)
		return
	}
	if h.s.apiOnly {
		h.failure(w, Scope{}, fail("当前实例不提供这项操作，请在正常服务执行。"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var s Scope
	if e := decoder.Decode(&s); e != nil {
		h.error(w, s, 400, "检查请求", "同步", "同步请求格式不正确、含不支持字段或超过64KiB；请重新打开页面后重试。", "本次没有开始。", "重新读取当前工作区。", true)
		return
	}
	var tail any
	if e := decoder.Decode(&tail); e != io.EOF {
		h.error(w, s, 400, "检查请求", "同步", "同步请求只能包含一个完整JSON对象。", "本次没有开始。", "重新打开用户分析。", true)
		return
	}
	s.UserID = uid
	if !validScope(s) {
		h.error(w, s, 400, "检查工作区", s.AdminAccountID, "工作区编号或创建时间无效，请重新读取当前工作区后重试。", "本次没有开始。", "重新打开用户分析。", true)
		return
	}
	if e := h.s.repo.Ready(r.Context()); e != nil {
		h.failure(w, s, e)
		return
	}
	if e := workspacescope.Resolve(r.Context(), h.s.repo.db, s); e != nil {
		h.failure(w, s, e)
		return
	}
	value, e := h.s.StartSync(r.Context(), s, true)
	if e != nil {
		h.failure(w, s, e)
		return
	}
	h.write(w, r, 202, s, value)
}
func (h *Handler) users(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scope(w, r, "page", "pageSize", "segment", "inactiveDays", "search", "role", "status", "sort", "order")
	if !ok {
		return
	}
	v := r.URL.Query()
	q := ListQuery{Segment: v.Get("segment"), Search: strings.TrimSpace(v.Get("search")), Role: v.Get("role"), Status: v.Get("status"), Sort: v.Get("sort"), Order: v.Get("order")}
	var e error
	q.Page, e = integer(v.Get("page"), 1, 1, 1000000)
	if e == nil {
		q.PageSize, e = integer(v.Get("pageSize"), 20, 1, 100)
	}
	if e == nil {
		q.InactiveDays, e = integer(v.Get("inactiveDays"), 30, 1, 3650)
	}
	if q.Segment == "" {
		q.Segment = "all"
	}
	if q.Sort == "" {
		q.Sort = "lastUsedAt"
	}
	if q.Order == "" {
		q.Order = "desc"
	}
	segments := map[string]bool{"all": true, "active_1d": true, "active_7d": true, "active_30d": true, "inactive": true, "churned_7d": true, "declining": true, "growing": true}
	sorts := map[string]bool{"lastUsedAt": true, "cost7d": true, "cost30d": true, "balance": true, "createdAt": true}
	if e != nil || !segments[q.Segment] || !sorts[q.Sort] || (q.Order != "asc" && q.Order != "desc") || utf8.RuneCountInString(q.Search) > 100 || (q.Role != "" && q.Role != "user" && q.Role != "admin") || (q.Status != "" && q.Status != "active" && q.Status != "disabled") {
		h.error(w, s, 400, "检查筛选", "用户列表", "用户筛选、排序或分页参数无效；请检查筛选值后重试。", "本次未读取资料。", "恢复允许的筛选条件。", true)
		return
	}
	value, e := h.s.Users(r.Context(), s, q)
	if e != nil {
		h.failure(w, s, e)
		return
	}
	h.write(w, r, 200, s, value)
}
func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" || utf8.RuneCountInString(id) > 128 {
		h.error(w, s, 400, "检查编号", "用户详情", "用户编号为空或超过128字，请重新选择用户。", "未读取主站或修改资料。", "从用户列表打开详情。", true)
		return
	}
	value, e := h.s.Detail(r.Context(), s, id)
	if e != nil {
		h.failure(w, s, e)
		return
	}
	h.write(w, r, 200, s, value)
}
