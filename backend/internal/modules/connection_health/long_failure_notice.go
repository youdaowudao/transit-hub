package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"transithub/backend/internal/modules/notifications"
	"transithub/backend/internal/modules/upstream"
)

// NoticePublisher writes an immutable local notification; it never changes the upstream account.
type NoticePublisher interface {
	Publish(context.Context, notifications.NoticeInput) (string, bool, error)
}

func (s *Service) SetNoticePublisher(publisher NoticePublisher) {
	s.noticeMu.Lock()
	defer s.noticeMu.Unlock()
	s.noticePublisher = publisher
}

func modelInLongFailure(state *ConnectionHealthState, protocol TestProtocol, protocolKnown bool, judgment Policy, now time.Time) bool {
	if state == nil || state.State == StateDisabled {
		return false
	}
	if protocolKnown {
		state = probeDecisionStateForProtocol(state, protocol)
	}
	return stateHasLongFailure(*state, judgment, now)
}

func autoProbeRetryKind(configUsable bool, decision EffectiveProbeDecision) string {
	if !configUsable {
		return "config_blocked"
	}
	if decision.BlockedReason == ProbeBlockedUpstreamSchedulingDisabled {
		return "scheduling_blocked"
	}
	return "continuing"
}

type longFailureNoticeCandidate struct {
	input        notifications.NoticeInput
	unverifiable bool
}

func (s *Service) collectLongFailureNotices(scan *schedulerScan, userID, workspace string, target AdminProbeTarget, specs []probeModelSpec, states []*ConnectionHealthState, decisions []EffectiveProbeDecision, ready []bool, now time.Time) {
	if scan == nil || target.Platform != string(upstream.PlatformSub2API) || len(specs) == 0 {
		return
	}
	configUsable := target.TestConfiguration.usable()
	allSchedulingBlocked := true
	allStatesReady := true
	var latest *time.Time
	minimumAfter := 0
	for index, spec := range specs {
		preset := RulePresetForPolicy(spec.policy)
		if minimumAfter == 0 || preset.LongFailureAfterSeconds < minimumAfter {
			minimumAfter = preset.LongFailureAfterSeconds
		}
		if !ready[index] {
			allStatesReady = false
			continue
		}
		state, decision := states[index], decisions[index]
		if decision.BlockedReason != ProbeBlockedUpstreamSchedulingDisabled {
			allSchedulingBlocked = false
		}
		if state != nil && state.LastProbeAt != nil && (latest == nil || state.LastProbeAt.After(*latest)) {
			value := *state.LastProbeAt
			latest = &value
		}
		if !modelInLongFailure(state, target.TestConfiguration.Protocol, configUsable, spec.policy, now) {
			continue
		}
		input := longFailureNoticeInput(userID, workspace, target)
		input.Kind = "connection_health.long_failure"
		input.EventKey = "long_failure|" + target.TargetID + "|" + spec.modelName + "|" + state.FailingSince.UTC().Format(time.RFC3339Nano)
		input.DiscoveredAt = state.FailingSince.Add(time.Duration(preset.LongFailureAfterSeconds) * time.Second)
		retry := longFailureRetryText(autoProbeRetryKind(configUsable, decision), preset.LongFailureIntervalSeconds)
		account := noticeDisplayText(target.AccountName, 120)
		model := noticeDisplayText(spec.modelName, 120)
		since := longFailureTime(state.FailingSince)
		recentError := longFailureErrorText(state.LastErrorKey, state.LastErrorDetail)
		status := longFailureMainSiteStatus(target)
		input.Content.Title = "账号长期失败，需要人工处理"
		input.Content.Summary = fmt.Sprintf("%s 的 %s 从 %s 起连续失败超过 %s（最近错误：%s）。这个账号在主站%s。%s查看这条提醒不会修改主站。", account, model, since, longFailureDuration(preset.LongFailureAfterSeconds), recentError, status, retry)
		input.Content.Details = []notifications.Detail{{Label: "账号", Value: account}, {Label: "模型", Value: model}, {Label: "所属分组", Value: longFailureGroups(target)}, {Label: "连续失败开始", Value: since}, {Label: "最近错误", Value: recentError}, {Label: "主站状态", Value: status}, {Label: "自动重试", Value: retry}}
		scan.notices = append(scan.notices, longFailureNoticeCandidate{input: input})
	}
	// A disabled model is excluded from L above, but it does not hide an account-level U fact.
	if !allStatesReady || configUsable && !allSchedulingBlocked {
		return
	}
	for _, checkpoint := range scan.targetActionStates {
		if checkpoint.UserID != userID || checkpoint.AdminAccountID != workspace || checkpoint.TargetID != target.TargetID || normalizeTargetStatus(target.Platform, checkpoint.LastAppliedStatus) != "inactive" {
			continue
		}
		origin, key := checkpoint.UpdatedAt, "never"
		if latest != nil {
			origin, key = *latest, latest.UTC().Format(time.RFC3339Nano)
		}
		discovered := origin.Add(time.Duration(minimumAfter) * time.Second)
		if origin.IsZero() || now.Before(discovered) {
			return
		}
		input := longFailureNoticeInput(userID, workspace, target)
		input.Kind, input.EventKey = "connection_health.disabled_unverified", "unverifiable|"+target.TargetID+"|"+key
		input.DiscoveredAt = discovered
		retryKind := "config_blocked"
		if configUsable {
			retryKind = "scheduling_blocked"
		}
		account, status := noticeDisplayText(target.AccountName, 120), longFailureMainSiteStatus(target)
		input.Content.Title = "系统关停的账号无法继续验证"
		input.Content.Summary = fmt.Sprintf("%s 已被系统关停，且已超过 %s 没有新的探活结果，系统不会自动重新启用它。%s查看这条提醒不会修改主站。", account, longFailureDuration(minimumAfter), longFailureRetryText(retryKind, 0))
		input.Content.Details = []notifications.Detail{{Label: "账号", Value: account}, {Label: "所属分组", Value: longFailureGroups(target)}, {Label: "最后一次探活", Value: longFailureTime(latest)}, {Label: "主站状态", Value: status}}
		scan.notices = append(scan.notices, longFailureNoticeCandidate{input: input, unverifiable: true})
		return
	}
}

func longFailureNoticeInput(userID, workspace string, target AdminProbeTarget) notifications.NoticeInput {
	return notifications.NoticeInput{UserID: userID, AdminAccountID: workspace, SubjectType: "connection_health_target", SubjectID: target.TargetID, Level: notifications.LevelImportant, Content: notifications.Content{Action: &notifications.Action{Kind: "connection_health"}}}
}

func (s *Service) publishLongFailureNotices(ctx context.Context, scan *schedulerScan) {
	s.noticeMu.Lock()
	defer s.noticeMu.Unlock()
	if s.noticePublisher == nil {
		return
	}
	current := make(map[string]bool, len(scan.notices))
	for _, candidate := range scan.notices {
		current[candidate.input.EventKey] = s.sentLongFailureNotices[candidate.input.EventKey]
	}
	s.sentLongFailureNotices = current
	sort.SliceStable(scan.notices, func(i, j int) bool {
		left, right := scan.notices[i].input, scan.notices[j].input
		if left.DiscoveredAt.Equal(right.DiscoveredAt) {
			return left.EventKey < right.EventKey
		}
		return left.DiscoveredAt.Before(right.DiscoveredAt)
	})
	attempts := 0
	for _, candidate := range scan.notices {
		if ctx.Err() != nil || attempts >= 5 {
			return
		}
		input := candidate.input
		if current[input.EventKey] {
			continue
		}
		if candidate.unverifiable && !s.unverifiableNoticeStillStopped(ctx, scan, input) {
			delete(current, input.EventKey)
			continue
		}
		attempts++
		_, _, err := s.noticePublisher.Publish(ctx, input)
		if err == nil || errors.Is(err, notifications.ErrEventConflict) {
			current[input.EventKey] = true
			continue
		}
		log.Printf("[connection-health] publish health notice failed workspace=%s target_id=%s reason=%s", input.AdminAccountID, input.SubjectID, noticeDisplayText(err.Error(), 300))
	}
}

func (s *Service) unverifiableNoticeStillStopped(ctx context.Context, scan *schedulerScan, input notifications.NoticeInput) bool {
	state, err := s.repo.GetTargetActionState(ctx, input.UserID, input.AdminAccountID, input.SubjectID)
	if err != nil || state == nil || normalizeTargetStatus(string(upstream.PlatformSub2API), state.LastAppliedStatus) != "inactive" || state.Conflict || targetActionPending(state) || state.PendingDispatchPhase != "" {
		return false
	}
	cached := scan.inventoryCache[input.UserID+"|"+input.AdminAccountID]
	if cached.err != nil || cached.inventory == nil {
		return false
	}
	target, found := findActionInventoryTarget(input.SubjectID, *cached.inventory)
	return found && normalizeTargetStatus(target.Platform, target.AccountStatus) == "inactive"
}

func longFailureMainSiteStatus(target AdminProbeTarget) string {
	switch strings.ToLower(strings.TrimSpace(target.AccountStatus)) {
	case "inactive", "disabled", "2", "0":
		return "已停用，目前不接收请求"
	case "active", "enabled", "1":
		if target.Schedulable != nil && *target.Schedulable {
			return "仍是启用且可调度，可能还在接收请求"
		}
	}
	if target.Schedulable != nil && !*target.Schedulable {
		return "调度已关闭，目前不接收请求"
	}
	return "状态未能确认"
}

func longFailureTime(at *time.Time) string {
	if at == nil {
		return "从未探活"
	}
	return at.In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02 15:04")
}

func longFailureDuration(seconds int) string {
	if seconds%3600 == 0 {
		return fmt.Sprintf("%d 小时", seconds/3600)
	}
	return fmt.Sprintf("%d 分钟", seconds/60)
}

func longFailureGroups(target AdminProbeTarget) string {
	names := make([]string, 0, len(target.TestMemberships))
	for _, membership := range target.TestMemberships {
		names = append(names, membership.AdminGroupName)
	}
	if len(names) == 0 {
		names = append(names, target.AdminGroupName)
	}
	return noticeDisplayText(strings.Join(names, "、"), 600)
}

func longFailureRetryText(kind string, seconds int) string {
	switch kind {
	case "config_blocked":
		return "因为分组测试配置冲突或无法确认，系统目前无法探活，也不会自动恢复。请到分组设置把这个账号所在各分组的测试协议和超时统一，未被禁止探活的模型才会继续自动探活。若模型显示“健康已禁用”，请联系管理员核查该模型的禁用记录；正式探活不能解除禁用。"
	case "scheduling_blocked":
		return "因为主站调度已关闭、且策略设为调度关闭时不继续探活，系统目前不会自动探活。请在分组健康检查模型状态：若有模型显示“健康已禁用”，请先联系管理员核查禁用记录，正式探活不能解除禁用；否则可点“一键正式探活”验证。仍处于关停状态的模型要按各自预设要求连续成功，全部受控模型恢复健康后，账号才会改回原状态；调度开关仍关闭，要接收请求请在主站手动打开。"
	default:
		return fmt.Sprintf("系统已改为每 %s 自动试一次，成功后自动恢复正常探活。请检查上游 Key 或账号，修好后可在分组健康点“一键正式探活”立即验证。", longFailureDuration(seconds))
	}
}

func longFailureErrorText(key, detail string) string {
	labels := map[string]string{"ok": "正常", "slow_response": "慢响应", "delayed": "延迟", "network_fluctuation": "网络波动", "rate_limited": "触发限流", "server_error": "上游服务异常", "auth": "鉴权失败", "model_not_found": "模型不存在", "invalid_response": "响应无法解析", "unsupported": "暂不支持"}
	label := labels[key]
	if label == "" {
		label = "原因未能确认，请在分组健康查看探活记录"
	}
	if cause := noticeDisplayText(detail, 300); cause != "" {
		label += "：" + cause
	}
	return noticeLimitRunes(label, 300)
}

var (
	noticeHTML       = regexp.MustCompile(`<\s*/?\s*[a-zA-Z!][^>]*>`)
	noticeBearer     = regexp.MustCompile(`(?i)\bbearer\s+[^\s，。；;,"'<>]+`)
	noticeCredential = regexp.MustCompile(`(?i)(?:password|passwd|token|key|secret|api[_ -]?key|access[_ -]?token|refresh[_ -]?token|authorization|cookie|session|real[_ -]?connection|密码|密钥|令牌)["']?\s*[:=]\s*[^，。；\r\n]*`)
	noticeConnection = regexp.MustCompile(`(?i)(?:https?://[^\s/?#，。；]*@[^\s，。；]*|(?:postgres(?:ql)?|redis)://[^\s，。；]*)`)
)

// Keep a concrete plain-language cause, not an upstream JSON/HTML document or credentials.
func noticeDisplayText(value string, limit int) string {
	var structured any
	if json.Unmarshal([]byte(value), &structured) == nil {
		switch structured.(type) {
		case map[string]any, []any:
			value = noticeJSONCause(structured, 0)
			if value == "" {
				value = "上游返回了结构化错误，请在分组健康查看具体探活记录"
			}
		}
	}
	value = noticeConnection.ReplaceAllString(value, "连接资料已隐藏")
	value = noticeBearer.ReplaceAllString(value, "登录资料已隐藏")
	value = noticeCredential.ReplaceAllString(value, "登录资料已隐藏")
	value = noticeHTML.ReplaceAllString(value, "")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(value, ""))
	return noticeLimitRunes(strings.TrimSpace(value), limit)
}

func noticeJSONCause(value any, depth int) string {
	if depth > 4 {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	if object, ok := value.(map[string]any); ok {
		for _, key := range []string{"message", "detail", "error_description", "error"} {
			if text := noticeJSONCause(object[key], depth+1); text != "" {
				return text
			}
		}
	}
	return ""
}

func noticeLimitRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}
