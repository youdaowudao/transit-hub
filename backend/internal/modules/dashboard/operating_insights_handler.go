package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/httpjson"
	"transithub/backend/internal/shared/secretbox"
	"transithub/backend/internal/shared/workspacescope"
)

func operatingErrorReason(err error) string {
	switch {
	case errors.Is(err, ErrOperatingInsightsUnavailable):
		return ErrOperatingInsightsUnavailable.Error()
	case errors.Is(err, upstream.ErrBalanceSnapshotUnavailable):
		return upstream.ErrBalanceSnapshotUnavailable.Error()
	case errors.Is(err, secretbox.ErrNoKey):
		return secretbox.ErrNoKey.Error()
	case errors.Is(err, secretbox.ErrUndecryptable):
		return secretbox.ErrUndecryptable.Error()
	case errors.Is(err, context.Canceled):
		return "本地读取已取消；请在服务恢复后重新展开经营提示。"
	case errors.Is(err, context.DeadlineExceeded):
		return "本地读取超过允许时间；请检查本站数据库和Redis负载后重试。"
	}
	var database *pgconn.PgError
	if errors.As(err, &database) {
		switch database.Code {
		case "42P01", "42703":
			return "原结算资料表或字段尚未就绪；请先用正常服务完成升级后重试。"
		case "42501":
			return "本站数据库账号缺少读取原结算资料的权限；请核查表权限后重试。"
		}
	}
	var syntax *json.SyntaxError
	var invalidType *json.UnmarshalTypeError
	if errors.As(err, &syntax) || errors.As(err, &invalidType) {
		return "原本地资料格式不正确，无法读取；请核查本站缓存、原存储和服务日志后重试。"
	}
	var network *net.OpError
	if errors.As(err, &network) {
		return "无法连接本站数据库或Redis，原资料尚未读完；请核查本地连接和服务日志后重试。"
	}
	return "读取本站现有资料未完成，具体原因尚未确认；请核查PostgreSQL、Redis和本站服务日志后重试。"
}

func writeOperatingError(w http.ResponseWriter, scope workspacescope.Scope, status int, step, object, message string) {
	httpjson.Write(w, status, map[string]any{
		"message": message, "step": step, "object": object, "impact": "本次没有采集、保存、充值或修改主站，原有首页与分组利润仍按原方式读取。",
		"action": "核查提示中的本地资料或服务日志后重新展开经营提示。", "retryable": true,
		"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt,
	})
}

func (h *Handler) operatingInsights(w http.ResponseWriter, r *http.Request) {
	user, ok := authctx.UserID(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "请先登录本站后再读取经营提示。")
		return
	}
	query := r.URL.Query()
	scope := workspacescope.Scope{UserID: user, AdminAccountID: query.Get("workspaceAdminAccountId"), CreatedAt: query.Get("workspaceCreatedAt")}
	for key, values := range query {
		if (key != "workspaceAdminAccountId" && key != "workspaceCreatedAt") || len(values) != 1 {
			writeOperatingError(w, scope, http.StatusBadRequest, "检查经营提示请求", scope.AdminAccountID, "经营提示只接受工作区编号和原创建时间，不能包含额外或重复参数；请重新打开当前工作区后重试。")
			return
		}
	}
	_, err := time.Parse(time.RFC3339Nano, scope.CreatedAt)
	if scope.AdminAccountID == "" || utf8.RuneCountInString(scope.AdminAccountID) > 128 || len(scope.CreatedAt) > 64 || err != nil {
		writeOperatingError(w, scope, http.StatusBadRequest, "检查工作区", scope.AdminAccountID, "工作区编号或创建时间缺失、格式不正确；请重新读取当前工作区后展开经营提示。")
		return
	}
	result, err := h.metricsService.OperatingInsightsForWorkspace(r.Context(), scope)
	if err != nil {
		status := http.StatusInternalServerError
		step, object := "经营提示", scope.AdminAccountID
		reason := operatingErrorReason(err)
		var read *operatingReadError
		if errors.As(err, &read) {
			step, object = read.Step, read.Object
		}
		if errors.Is(err, workspacescope.ErrChanged) {
			status = http.StatusConflict
			reason = workspacescope.ErrChanged.Error()
		}
		var database *pgconn.PgError
		if errors.Is(err, ErrOperatingInsightsUnavailable) || errors.Is(err, upstream.ErrBalanceSnapshotUnavailable) ||
			(errors.As(err, &database) && (database.Code == "42P01" || database.Code == "42703")) {
			status = http.StatusServiceUnavailable
		}
		writeOperatingError(w, scope, status, step, object, "读取"+step+"（"+object+"）失败："+reason)
		return
	}
	httpjson.Write(w, http.StatusOK, result)
}
