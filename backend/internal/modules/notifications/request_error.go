package notifications

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

type RequestError struct {
	Status                  int    `json:"-"`
	Message                 string `json:"message"`
	Detail                  string `json:"detail"`
	Next                    string `json:"next"`
	Retryable               bool   `json:"retryable"`
	WorkspaceAdminAccountID string `json:"workspaceAdminAccountId"`
}

func (e *RequestError) Error() string { return e.Message + "：" + e.Detail + "；" + e.Next }

func requestFailure(status int, workspace, message, detail, next string, retryable bool) *RequestError {
	if checkText("工作区编号", workspace, 512, false, true) != nil {
		workspace = ""
	}
	return &RequestError{status, message, detail, next, retryable, workspace}
}

func invalidRequest(workspace, detail string) *RequestError {
	return requestFailure(http.StatusBadRequest, workspace, "通知请求的内容不正确，本次没有保存或改变消息", detail, "请核对当前工作区和请求内容后重试", false)
}

func storageFailure(workspace, step string, err error) *RequestError {
	return requestFailure(http.StatusServiceUnavailable, workspace, step+"失败", SafeStorageCause(err)+"；本次操作没有确认成功，原有通知和设置不会被当作空数据返回", "请稍后重试；仍失败时请维护人员核查Hub后端的通知读取或保存步骤", true)
}

func commitFailure(workspace, object string, err error) *RequestError {
	return requestFailure(http.StatusServiceUnavailable, workspace, object+"的保存结果无法确认", "提交时"+SafeStorageCause(err)+"；没有取得明确保存结果，可能已保存，也可能未保存，本次不会显示保存成功", "请重新读取这次涉及的消息编号或当前工作区的检查间隔，核对结果后再重试相同操作", true)
}

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func verifyWorkspace(ctx context.Context, q queryer, user, workspace string, lock bool) error {
	if workspace == "" {
		return invalidRequest(workspace, "没有提供工作区编号，无法确定要读取或保存哪个工作区的通知")
	}
	if err := checkText("工作区编号", workspace, 512, true, true); err != nil {
		return invalidRequest("", "工作区编号不符合纯文字格式或超过512个字符")
	}
	if user == "" {
		return requestFailure(http.StatusUnauthorized, workspace, "登录状态已失效", "无法确认这次请求的登录用户，本次未读取或保存通知", "请重新登录后重试", false)
	}
	sql := `SELECT current_admin_account_id FROM users WHERE id=$1`
	if lock {
		sql += ` FOR SHARE`
	}
	var current *string
	if err := q.QueryRow(ctx, sql, user).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return requestFailure(http.StatusUnauthorized, workspace, "登录用户已不存在", "本次未读取或保存通知", "请重新登录后重试", false)
		}
		return storageFailure(workspace, "核查通知所属的当前工作区", err)
	}
	if current == nil || *current != workspace {
		return requestFailure(http.StatusConflict, workspace, "工作区已变化，本次通知操作没有执行", "请求中的工作区与这个用户当前选择的工作区不同", "请等待工作区切换完成，重新读取当前工作区后重试", true)
	}
	sql = `SELECT id FROM admin_accounts WHERE user_id=$1 AND id=$2`
	if lock {
		sql += ` FOR KEY SHARE`
	}
	var account string
	if err := q.QueryRow(ctx, sql, user, workspace).Scan(&account); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return requestFailure(http.StatusConflict, workspace, "这个工作区已不可访问，本次通知操作没有执行", "工作区已删除或不属于当前登录用户", "请重新选择可访问的工作区后重试", false)
		}
		return storageFailure(workspace, "核查通知工作区的用户归属", err)
	}
	return nil
}
