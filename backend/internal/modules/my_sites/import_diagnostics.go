package my_sites

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"transithub/backend/internal/modules/upstream"
)

type importValidationError struct{ key, detail string }

func (e *importValidationError) Error() string            { return e.key }
func (e *importValidationError) SafeImportDetail() string { return e.detail }
func (e *importValidationError) Unwrap() error            { return requestError(e.key) }

func importInvalid(key, detail string) error { return &importValidationError{key: key, detail: detail} }

func safeImportReason(cause error) string {
	if reason := safeManagedDeleteReason(cause); reason != "" {
		return reason
	}
	var remote *upstream.RequestError
	if errors.As(cause, &remote) {
		for _, key := range []string{upstream.ErrorAuth, upstream.ErrorForbidden, upstream.ErrorNotFound, upstream.ErrorRateLimited, upstream.ErrorUpstreamServer, upstream.ErrorNetworkTimeout, upstream.ErrorNetworkUnreachable, upstream.ErrorTLSFailed, upstream.ErrorInvalidResponse, upstream.ErrorBusinessRejected, upstream.ErrorRefreshTokenRejected, upstream.ErrorAccessTokenRejected, upstream.ErrorLoginIncomplete, upstream.ErrorAnnouncementAckRequired, upstream.ErrorUpstreamInsufficientBalance, upstream.ErrorUpstreamKeyQuotaExhausted, upstream.ErrorUpstreamKeyExpired, ErrorImportModelListEmpty, ErrorImportModelListInvalid} {
			if remote.Reason == key || remote.Reason == "" && remote.MessageKey == key {
				return key
			}
		}
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return ErrorImportCancelled
	}
	return ""
}

func newImportDiagnostic(stage, target, message string, cause error, localDetail string) ImportDiagnostic {
	d := ImportDiagnostic{Stage: stage, Target: target, Message: message, Reason: safeImportReason(cause)}
	var remote *upstream.RequestError
	if target != "local" && errors.As(cause, &remote) {
		if remote.StatusCode >= 100 && remote.StatusCode <= 599 {
			d.HTTPStatus = remote.StatusCode
		}
		d.Detail = remote.UpstreamMessage
	}
	var safe interface{ SafeImportDetail() string }
	if errors.As(cause, &safe) {
		d.Detail = safe.SafeImportDetail()
	}
	if localDetail != "" {
		d.Detail = localDetail
	}
	if d.Detail == "" && target == "local" {
		switch stage {
		case "persistence":
			if message == ErrorImportSourceGroupUnavailable {
				d.Detail = "上游站点或分组来源已变化，账号和 Key 保留；请刷新上游分组并检查已有资源"
			} else {
				d.Detail = "Hub 未能确认完整关联已保存，具体数据库原因无法安全确认；请查看 Hub 后端保存记录"
			}
		case "validation":
			switch message {
			case "admin.adminAccounts.errors.noCurrentAccount":
				d.Detail = "尚未选择当前工作区，本次未创建账号或 Key；请先选择工作区，然后重新打开导入窗口"
			case ErrorAuthRequired, "auth.errors.unauthorized":
				d.Detail = "登录资料不可用，本次未创建账号或 Key；请登录或到我的站点重新连接主站，再重新打开导入窗口"
			case ErrorImportInProgress:
				d.Detail = "相同目标已有导入正在处理，请等待已有操作完成后查看连接列表"
			case ErrorConnectionExists:
				d.Detail = "当前目标已有连接，本次未创建账号或 Key；请查看已有连接"
			default:
				d.Detail = "导入前检查未通过，本次未创建账号或 Key；请检查设置、上游分组和工作区"
			}
		}
	}
	if d.Reason == ErrorImportCancelled {
		d.Detail = "导入请求已取消或到期，已停止后续操作；请按下方账号和 Key 状态检查资源，不要直接重复导入"
	}
	d.Detail = upstream.ImportDiagnosticHint(d.Detail)
	d.Detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, d.Detail)
	// Keep local explanations comfortably within the UTF-16 display contract.
	runes := []rune(d.Detail)
	units, end := 0, len(runes)
	for i, r := range runes {
		units++
		if r > 0xffff {
			units++
		}
		if units > 460 {
			end = i
			break
		}
	}
	if end < len(runes) {
		d.Detail = string(runes[:end]) + "；差异较多，请到主站查看完整设置"
	}
	return d
}

func appendImportWarning(failure *ImportFailure, warning *ImportDiagnostic) {
	if warning == nil {
		return
	}
	// Initial cause stays first; cleanup is always the last entry.
	diagnostics := append([]ImportDiagnostic{}, failure.Diagnostics[:1]...)
	diagnostics = append(diagnostics, *warning)
	if len(failure.Diagnostics) > 1 {
		diagnostics = append(diagnostics, failure.Diagnostics[1:]...)
	}
	failure.Diagnostics = diagnostics
}
