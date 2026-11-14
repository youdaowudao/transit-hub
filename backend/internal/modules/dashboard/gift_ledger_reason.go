package dashboard

import (
	"errors"
	"fmt"
	"strings"

	"transithub/backend/internal/modules/upstream"
)

// giftLedgerUpstreamReason 把主站请求错误写成白话原因：失败步骤 + 失败类型 + 脱敏提示。
// 只有网络错误才写“没有收到主站响应”；内容不对时写实际状态码和具体问题。
func giftLedgerUpstreamReason(step string, err error, date string) string {
	var requestErr *upstream.RequestError
	if !errors.As(err, &requestErr) || requestErr == nil {
		return fmt.Sprintf("%s：原因未知，请查看本站后端日志中 %s 的 dashboard 记录", step, date)
	}
	switch requestErr.MessageKey {
	case upstream.ErrorAuth:
		var b strings.Builder
		b.WriteString(step)
		b.WriteString("：主站登录已失效")
		if requestErr.StatusCode > 0 {
			fmt.Fprintf(&b, "（HTTP %d", requestErr.StatusCode)
			if msg := strings.TrimSpace(requestErr.UpstreamMessage); msg != "" {
				fmt.Fprintf(&b, "，主站提示“%s”", msg)
			}
			b.WriteString("）")
		}
		b.WriteString("，请在仪表盘重新登录主站管理员后重试")
		return b.String()
	case upstream.ErrorUnsupported:
		return step + "：当前主站不是 Sub2API，不能按客户查询消费"
	case upstream.ErrorNetwork:
		if requestErr.Timeout {
			return step + "：没有收到主站响应（超时）"
		}
		return step + "：没有收到主站响应（网络不通）"
	case upstream.ErrorInvalidResponse:
		problem := "内容格式不对"
		switch requestErr.Reason {
		case upstream.GiftUsageMissingCost:
			problem = "缺少消费金额"
		case upstream.GiftUsageNegativeCost:
			problem = "消费金额为负数"
		}
		if requestErr.StatusCode > 0 {
			return fmt.Sprintf("%s：主站返回 HTTP %d，但%s", step, requestErr.StatusCode, problem)
		}
		return fmt.Sprintf("%s：主站返回的%s", step, problem)
	}
	var b strings.Builder
	b.WriteString(step)
	b.WriteString("：")
	if requestErr.StatusCode > 0 {
		fmt.Fprintf(&b, "HTTP %d", requestErr.StatusCode)
	} else {
		b.WriteString("主站请求失败")
	}
	if msg := strings.TrimSpace(requestErr.UpstreamMessage); msg != "" {
		fmt.Fprintf(&b, "，主站提示“%s”", msg)
	}
	return b.String()
}
