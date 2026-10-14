package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	samplingCredentials = regexp.MustCompile(`(?i)(?:password|passwd|token|key|secret|api[_ -]?key|access[_ -]?token|refresh[_ -]?token|authorization|cookie|session|real[_ -]?connection|密码|密钥|令牌)["']?\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;，。；]+)`)
	samplingBearer      = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;，。；]+`)
	samplingURLs        = regexp.MustCompile(`(?i)(?:https?|postgres(?:ql)?|redis)://[^\s<>，。；]+`)
)

func safeSamplingDisplayText(value, fallback string, limit int) string {
	invalidURL := false
	value = samplingURLs.ReplaceAllStringFunc(value, func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			invalidURL = true
			return ""
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return "[存储连接已遮盖]"
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		parsed.RawFragment = ""
		return parsed.String()
	})
	if invalidURL {
		return fallback
	}
	value = samplingCredentials.ReplaceAllString(value, "[登录资料已遮盖]")
	value = samplingBearer.ReplaceAllString(value, "[登录资料已遮盖]")
	return safeSamplingText(value, fallback, limit)
}

func safeSamplingText(value, fallback string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || checkText("倍率读取状态", value, limit, true, false) != nil {
		return fallback
	}
	return value
}

func samplingWarning(state *SamplingState, detail string) {
	if state.Warning == nil {
		state.Warning = &Warning{Message: "倍率采集状态暂时无法完整确认", Next: "请稍后重新读取消息；仍失败时请维护人员核查Hub本地的自动同步设置和网站倍率读取记录"}
	}
	if state.Warning.Detail != "" {
		state.Warning.Detail += "；"
	}
	state.Warning.Detail += detail
}

func (r *Repository) loadSamplingState(ctx context.Context, user, workspace string) SamplingState {
	state := SamplingState{Sites: []SamplingSite{}}
	// Select the one local setting only. Do not load channel settings or credentials.
	var enabledJSON []byte
	var settingsType *string
	err := r.db.QueryRow(ctx, `SELECT jsonb_typeof(settings),settings->'enableRefreshInterval' FROM strategy_settings WHERE user_id=$1 AND admin_account_id=$2`, user, workspace).Scan(&settingsType, &enabledJSON)
	var enabled bool
	if errors.Is(err, pgx.ErrNoRows) {
		state.AutoSyncEnabled = &enabled // Existing empty settings default to disabled.
	} else if err != nil {
		samplingWarning(&state, "自动同步设置未能读取："+SafeStorageCause(err)+"；不能据此判断自动同步已关闭")
	} else if settingsType == nil || *settingsType != "object" {
		samplingWarning(&state, "已保存的自动同步设置格式不正确，当前启用状态未知；没有修改原设置")
	} else if len(enabledJSON) == 0 || string(enabledJSON) == "null" {
		state.AutoSyncEnabled = &enabled
	} else if err := json.Unmarshal(enabledJSON, &enabled); err != nil {
		samplingWarning(&state, "已保存的自动同步开关格式不正确，当前启用状态未知；没有修改原设置")
	} else {
		state.AutoSyncEnabled = &enabled
	}
	// Project only safe observation fields. This never reads full metrics/session.
	rows, err := r.db.Query(ctx, `WITH unconfirmed AS (
SELECT id,name,metrics#>>'{rateNoticeObservation,observedAt}' AS observed_at,
metrics#>>'{rateNoticeObservation,reason}' AS reason
FROM upstream_sites WHERE user_id=$1 AND admin_account_id=$2 AND (
jsonb_typeof(metrics->'rateNoticeObservation') IS DISTINCT FROM 'object'
OR jsonb_typeof(metrics#>'{rateNoticeObservation,confirmed}') IS DISTINCT FROM 'boolean'
OR metrics#>>'{rateNoticeObservation,confirmed}' IS DISTINCT FROM 'true'
OR COALESCE(metrics#>>'{rateNoticeObservation,reason}','')<>''
OR jsonb_typeof(metrics#>'{rateNoticeObservation,observedAt}') IS DISTINCT FROM 'string'
)) SELECT name,observed_at,reason,count(*) OVER() FROM unconfirmed ORDER BY observed_at DESC NULLS LAST,name,id LIMIT 10`, user, workspace)
	if err != nil {
		samplingWarning(&state, "网站最近的倍率读取状态未能读取："+SafeStorageCause(err)+"；已有通知仍然有效")
		return state
	}
	defer rows.Close()
	count := int64(0)
	sites := []SamplingSite{}
	for rows.Next() {
		var name string
		var observed, reason *string
		if err := rows.Scan(&name, &observed, &reason, &count); err != nil {
			samplingWarning(&state, "网站最近的倍率读取状态无法安全读取："+SafeStorageCause(err))
			return state
		}
		item := SamplingSite{SiteName: safeSamplingDisplayText(name, "名称暂时无法安全显示的网站", 512), Reason: "还没有可确认的本地倍率读取记录，请同步这个网站并核查上游分组列表"}
		if observed != nil {
			if parsed, err := time.Parse(time.RFC3339Nano, *observed); err == nil && !parsed.IsZero() {
				item.ObservedAt = &parsed
			}
		}
		if reason != nil && strings.TrimSpace(*reason) != "" {
			item.Reason = safeSamplingDisplayText(*reason, "最近的倍率读取原因包含不能安全显示的内容，请同步这个网站并核查Hub读取记录", 1000)
		}
		sites = append(sites, item)
	}
	if err := rows.Err(); err != nil {
		samplingWarning(&state, "网站最近的倍率读取状态未能完整读取："+SafeStorageCause(err))
		return state
	}
	state.UnconfirmedSiteCount = &count
	state.Sites = sites
	return state
}
