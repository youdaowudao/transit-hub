package reports

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	mux.HandleFunc("GET /api/reports/daily/preview", h.preview)
	mux.HandleFunc("POST /api/reports/daily/test", h.test)
}

func publicReason(err error) string {
	var target *settings.TelegramTargetError
	if errors.As(err, &target) {
		return target.Error()
	}
	var config *settings.DailyReportConfigError
	if errors.As(err, &config) {
		return config.Error()
	}
	for _, known := range []error{ErrUnavailable, ErrStopped, ErrReportStore, ErrReportMarker, ErrReportRandom,
		settings.ErrDailyReportWritesDisabled, settings.ErrTelegramTargetUnavailable, settings.ErrTelegramProxyInvalid,
		settings.ErrTelegramChannelFormat, secretbox.ErrNoKey, secretbox.ErrUndecryptable} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	if errors.Is(err, context.Canceled) {
		return "本地处理已取消；请在服务恢复后重新预览日报。"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "本地处理超过允许时间；请核查本站数据库与Redis负载后重试。"
	}
	var database *pgconn.PgError
	if errors.As(err, &database) {
		switch database.Code {
		case "42P01", "42703":
			return "经营日报所需资料表或字段尚未初始化；请先用正常服务完成升级。"
		case "42501":
			return "数据库账号没有读取或锁定本地资料所需的权限；请核查策略、通知和分析表权限后重试。"
		case "40001", "40P01":
			return "并发更新使本次本地事务回退；请重新预览和核查已保存配置后再试。"
		}
	}
	return "本地日报处理未完成，具体原因尚未确认；请检查本站服务日志、PostgreSQL和Redis连接。"
}

func (h *Handler) failure(w http.ResponseWriter, scope Scope, err error) {
	status := http.StatusInternalServerError
	step, object := "本地日报", scope.AdminAccountID
	var detail *stepError
	if errors.As(err, &detail) {
		step, object = detail.Step, detail.Object
	}
	message := "处理" + step + "（" + object + "）失败：" + publicReason(err)
	retryable := true
	var commit *workspacescope.CommitError
	switch {
	case errors.As(err, &commit):
		message = "检查已保存日报配置的事务结果尚未确认，本次没有调用Telegram；请先重新读取策略和发送记录，再决定是否测试。"
		retryable = false
	case errors.Is(err, workspacescope.ErrChanged):
		status = http.StatusConflict
		message = workspacescope.ErrChanged.Error()
	case errors.Is(err, ErrUnavailable):
		status = http.StatusServiceUnavailable
	case errors.Is(err, settings.ErrDailyReportWritesDisabled), errors.Is(err, settings.ErrTelegramTargetUnavailable), errors.Is(err, settings.ErrTelegramProxyInvalid), errors.Is(err, settings.ErrDailyReportInvalid), errors.Is(err, ErrStopped):
		status = http.StatusConflict
	}
	var database *pgconn.PgError
	if errors.As(err, &database) && (database.Code == "42P01" || database.Code == "42703") {
		status = http.StatusServiceUnavailable
	}
	httpjson.Write(w, status, map[string]any{
		"message": message, "step": step, "object": object, "impact": "本次没有调用Telegram或修改主站；已保存的设置保持原流程。",
		"action": "先核查提示的本地资料、已保存机器人和本站日志，再重新预览。", "retryable": retryable,
		"attempted": 0, "sent": 0, "failed": 0, "unknown": 0,
		"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt,
	})
}

func (h *Handler) bad(w http.ResponseWriter, scope Scope, message string) {
	httpjson.Write(w, http.StatusBadRequest, map[string]any{
		"message": message, "step": "检查日报请求", "object": scope.AdminAccountID, "impact": "本次没有保存或发送。",
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
		httpjson.WriteError(w, http.StatusUnauthorized, "请先登录本站后再预览或测试日报。")
	}
	return user, ok
}

func (h *Handler) write(w http.ResponseWriter, scope Scope, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		h.failure(w, scope, err)
		return
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil || result == nil {
		h.failure(w, scope, errors.New("invalid response"))
		return
	}
	result["workspaceAdminAccountId"], _ = json.Marshal(scope.AdminAccountID)
	result["workspaceCreatedAt"], _ = json.Marshal(scope.CreatedAt)
	httpjson.Write(w, http.StatusOK, result)
}

func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	user, ok := h.user(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	scope := Scope{UserID: user, AdminAccountID: query.Get("workspaceAdminAccountId"), CreatedAt: query.Get("workspaceCreatedAt")}
	for key, values := range query {
		if (key != "workspaceAdminAccountId" && key != "workspaceCreatedAt") || len(values) != 1 {
			h.bad(w, scope, "日报预览只接受工作区编号和创建时间，不能包含额外或重复参数；请重新打开当前工作区。")
			return
		}
	}
	if !validScope(scope) {
		h.bad(w, scope, "工作区编号或创建时间无效；请重新读取当前工作区后预览日报。")
		return
	}
	result, err := h.service.Preview(r.Context(), scope)
	if err != nil {
		h.failure(w, scope, err)
		return
	}
	h.write(w, scope, result)
}

func (h *Handler) test(w http.ResponseWriter, r *http.Request) {
	user, ok := h.user(w, r)
	if !ok {
		return
	}
	var scope Scope
	if _, err := httpjson.DecodeStrictObject(w, r, &scope); err != nil {
		h.bad(w, scope, err.Error())
		return
	}
	scope.UserID = user
	if !validScope(scope) {
		h.bad(w, scope, "工作区编号或创建时间无效；请重新读取当前工作区后测试。")
		return
	}
	if h.service.apiOnly {
		h.failure(w, scope, settings.ErrDailyReportWritesDisabled)
		return
	}
	result, err := h.service.Test(r.Context(), scope)
	if err != nil {
		h.failure(w, scope, err)
		return
	}
	// Successful/unknown sends echo the captured scope, without a post-send selection query.
	h.write(w, scope, result)
}
