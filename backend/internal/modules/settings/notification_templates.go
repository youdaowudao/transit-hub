package settings

import "strings"

// Only exact historical system defaults are projected. Custom templates stay byte-for-byte intact.
var legacyBalanceTemplateDefaults = []string{
	"【余额预警】{siteName} 站点余额（CNY）已不足 {threshold} 元，当前余额为 {balance} 元。",
	"[Balance warning] {siteName} balance (CNY) is below {threshold}; current balance is {balance}.",
	"<div style=\"border-left:4px solid #f59e0b;background:rgba(245,158,11,0.12);padding:16px;border-radius:6px\"><div style=\"font-size:18px;font-weight:700;color:#f59e0b\">🔴 余额预警</div><p style=\"margin:10px 0 0\">上游站点 <strong style=\"color:#3b82f6\">{siteName}</strong> 的可用余额已低于预警阈值。</p><p style=\"margin:10px 0 0\">💰 当前余额: <strong style=\"color:#ef4444\">¥{balance}</strong><br>⚠️ 预警阈值: <strong>¥{threshold}</strong></p><p style=\"margin:10px 0 0\">请及时检查并充值，避免服务中断。</p></div>",
	"<div style=\"border-left:4px solid #f59e0b;background:rgba(245,158,11,0.12);padding:16px;border-radius:6px\"><div style=\"font-size:18px;font-weight:700;color:#f59e0b\">🔴 Balance warning</div><p style=\"margin:10px 0 0\">The available balance for <strong style=\"color:#3b82f6\">{siteName}</strong> is below the warning threshold.</p><p style=\"margin:10px 0 0\">💰 Current balance: <strong style=\"color:#ef4444\">¥{balance}</strong><br>⚠️ Warning threshold: <strong>¥{threshold}</strong></p><p style=\"margin:10px 0 0\">Please review and recharge the upstream account to avoid service interruption.</p></div>",
	"🔴 **余额预警**\n\n🏷️ **站点：** {siteName}\n💰 **当前余额：** ¥{balance}\n⚠️ **预警阈值：** ¥{threshold}\n\n请及时检查并充值，避免服务中断。",
	"🔴 余额预警\n🏷️ 站点：{siteName}\n💰 当前余额：¥{balance}\n⚠️ 预警阈值：¥{threshold}\n请及时检查并充值，避免服务中断。",
}

func projectDefaultBalanceTemplate(template string) string {
	for _, legacy := range legacyBalanceTemplateDefaults {
		if template == legacy {
			return strings.NewReplacer("¥", "", "（CNY）", "", "(CNY) ", "", " 元", "").Replace(template)
		}
	}
	return template
}
