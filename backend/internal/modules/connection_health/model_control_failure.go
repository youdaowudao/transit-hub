package connection_health

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"transithub/backend/internal/modules/upstream"
)

// These hints are response-only. Upstream's request boundary has already
// reduced messages to safe metadata; mask this session's credentials again.
func modelControlSafeFailureCause(err error, session upstream.Session) string {
	var detail *upstream.RequestError
	if !errors.As(err, &detail) {
		if errors.Is(err, context.DeadlineExceeded) {
			return "等待结果超时"
		}
		return "目前无法确认具体原因，请查看主站这个账号的操作记录和 TransitHub 服务日志"
	}
	message := detail.UpstreamMessage
	if message == "" {
		message = detail.RemoteMessage
	}
	message = upstream.ImportDiagnosticHint(message)
	for _, secret := range []string{session.AccessToken, session.AdminAPIKey, session.RefreshToken, session.Cookie} {
		message = redact(message, secret)
	}
	for _, cookie := range strings.Split(session.Cookie, ";") {
		if _, value, ok := strings.Cut(strings.TrimSpace(cookie), "="); ok {
			message = redact(message, value)
		}
	}
	// Never return a URL or raw response body, even if a custom actioner supplies one.
	if strings.Contains(strings.ToLower(message), "http://") || strings.Contains(strings.ToLower(message), "https://") {
		message = ""
	}
	if message == "" {
		switch detail.Reason {
		case upstream.ErrorNetworkTimeout:
			message = "连接主站或等待主站答复超时"
		case upstream.ErrorNetworkUnreachable:
			message = "无法连接主站"
		case upstream.ErrorTLSFailed:
			message = "连接主站时证书校验失败"
		default:
			if detail.MessageKey == upstream.ErrorInvalidResponse {
				message = "主站返回的内容无法读取"
			} else {
				message = "主站没有提供可确认的具体原因，请查看主站这个账号的操作记录"
			}
		}
	}
	if utf8.RuneCountInString(message) > 120 {
		message = string([]rune(message)[:120])
	}
	if detail.StatusCode > 0 {
		message += fmt.Sprintf("（HTTP %d）", detail.StatusCode)
	}
	return message
}
func modelControlFailureDetail(model, step string, err error, outcome string, session upstream.Session) string {
	message := fmt.Sprintf("%s模型的%s步骤未完成：%s。", model, step, modelControlSafeFailureCause(err, session))
	switch outcome {
	case "unknown":
		message += "操作请求已发送，暂时无法确认主站是否修改。请点“重新读取主站结果”，不要直接重试。"
	case "partial":
		message += "已读回并确认部分条目生效，实际结果见下方条目。请先处理原因，再重新读取、预览并确认，避免重复处理。"
	default:
		message += "已读回主站，预览要求的修改没有全部生效，实际结果见下方条目。请先处理原因，再重新读取、预览并确认。"
	}
	return message
}

func modelControlPreparationFailureDetail(model string, err error, session upstream.Session) string {
	return fmt.Sprintf("读取%s模型的主站白名单时未完成：%s。本次尚未发送模型开关。请先点“重新读取主站结果”，确认可以读取后重新预览；仍出现时检查主站这个账号的操作记录和 TransitHub 服务日志。", model, modelControlSafeFailureCause(err, session))
}
