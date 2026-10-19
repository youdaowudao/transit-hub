package upstream

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

const oldSingaporeCostMessage = "旧费用尚未按新加坡日期重新读取，请同步这个站点；此操作不会修改主站。"

// UsableSingaporeTodayCost is shared by runtime responses and dashboard reads.
func UsableSingaporeTodayCost(metrics Metrics, date string) bool {
	return metrics.TodayConsumeTimezone == businesstime.Timezone && metrics.TodayConsumeDate == date &&
		metrics.TodayConsume.Value != nil && finiteNonnegative(*metrics.TodayConsume.Value)
}
func finiteNonnegative(amount float64) bool {
	return amount >= 0 && !math.IsNaN(amount) && !math.IsInf(amount, 0)
}
func datedCostError(platform Platform, stage, reason string) error {
	return &RequestError{MessageKey: ErrorInvalidResponse, Platform: platform, Stage: stage, UpstreamMessage: safeCostReason(reason)}
}
func safeCostReason(reason string) string {
	// Stored cost messages never keep request URLs, even their nonsecret paths.
	return safeUpstreamMessage(messageURL.ReplaceAllString(reason, "[地址已遮盖]"))
}
func costFailureCause(err error) string {
	var detail *RequestError
	if errors.As(err, &detail) {
		if text := safeCostReason(detail.UpstreamMessage); text != "" {
			return text
		}
		switch siteErrorKey(err) {
		case ErrorNetworkTimeout:
			return "上游读取超时"
		case ErrorNetworkUnreachable:
			return "无法连接上游"
		case ErrorTLSFailed:
			return "上游安全连接验证失败"
		case ErrorForbidden:
			return "上游拒绝访问，具体原因未说明，请查看上游提示"
		case ErrorAccessTokenRejected, ErrorAuth:
			return "上游未接受当前登录资料"
		case ErrorNotFound:
			return "上游没有提供这个查询接口"
		case ErrorRateLimited:
			return "上游要求稍后再读取"
		case ErrorInvalidResponse:
			return "上游返回缺少必要字段或金额格式不正确"
		}
		if detail.StatusCode > 0 {
			return fmt.Sprintf("上游读取未成功（HTTP %d），原因尚未确认，请查看上游当天消费记录", detail.StatusCode)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "读取超时"
	}
	if errors.Is(err, context.Canceled) {
		return "读取已取消"
	}
	return "读取未成功，原因尚未确认，请核查上游当天消费记录和存储连接"
}
func formatCostFailure(step string, err error) string {
	return step + "失败：" + costFailureCause(err) + "；本轮没有更新费用，请在上游核查当天消费记录后同步这个站点重试；不会修改主站。"
}
func (s *PlatformService) fetchAccountCostForDateContext(ctx context.Context, session Session, date string) (float64, time.Time, error) {
	if _, _, err := businessDayUnixBounds(date); err != nil {
		return 0, time.Time{}, datedCostError(session.Platform, "metrics", "指定的新加坡日期格式不正确")
	}
	reqCtx, cancel := context.WithTimeout(ctx, keyUsageRequestTimeout)
	defer cancel()
	var endpoint string
	var options requestOptions
	switch session.Platform {
	case PlatformSub2API:
		if strings.TrimSpace(session.AccessToken) == "" {
			return 0, time.Time{}, datedCostError(session.Platform, "metrics", "当前登录资料没有用户访问令牌，无法读取新加坡指定日费用")
		}
		endpoint = session.BaseURL + "/api/v1/usage/stats?start_date=" + url.QueryEscape(date) + "&end_date=" + url.QueryEscape(date) + "&timezone=Asia%2FSingapore"
		options = sub2APIUserAuthOptions(session)
	case PlatformNewAPI:
		if !session.IsAuthenticated() {
			return 0, time.Time{}, datedCostError(session.Platform, "metrics", "当前登录资料没有用户编号或用户登录凭据，无法读取新加坡指定日费用；请核查上游登录资料后同步这个站点重试")
		}
		start, end, _ := businessDayUnixBounds(date)
		endpoint = session.BaseURL + "/api/log/self/stat?type=2&start_timestamp=" + strconvInt(start) + "&end_timestamp=" + strconvInt(end)
		options = newAPIAuthOptions(session)
	default:
		return 0, time.Time{}, newRequestError(ErrorUnsupported, session.Platform)
	}
	response, err := s.httpClient.requestJSONWithContext(reqCtx, endpoint, options)
	if err != nil {
		return 0, time.Time{}, withFailureStage(err, "metrics", session.Platform)
	}
	var amount *float64
	if session.Platform == PlatformSub2API {
		amount = firstNumber(dataRecord(response.Payload), []string{"total_actual_cost"})
	} else {
		amount = quotaToUSDWithUnit(firstNumber(dataRecord(response.Payload), []string{"quota", "used_quota", "usedQuota"}), session.QuotaPerUnit)
	}
	if amount == nil || !finiteNonnegative(*amount) {
		return 0, time.Time{}, datedCostError(session.Platform, "metrics", "上游指定日总额缺少实际费用字段，或金额不是有限的非负数")
	}
	return *amount, s.keyUsageNow().UTC(), nil
}
func (s *PlatformService) FetchMetricsForDate(session Session, date string) (Metrics, error) {
	switch session.Platform {
	case PlatformNewAPI:
		return s.fetchNewAPIMetrics(session, nil, date)
	case PlatformSub2API:
		return s.fetchSub2APIMetrics(session, date)
	default:
		return Metrics{}, newRequestError(ErrorUnsupported, session.Platform)
	}
}
func (s *PlatformService) readLoginPrincipal(ctx context.Context, session Session) (string, string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, keyUsageRequestTimeout)
	defer cancel()
	path := "/api/v1/auth/me"
	options := sub2APIUserAuthOptions(session)
	if session.Platform == PlatformNewAPI {
		path = "/api/user/self"
		options = newAPIAuthOptions(session)
	}
	response, err := s.httpClient.requestJSONWithContext(reqCtx, session.BaseURL+path, options)
	if err != nil {
		statusCode := 0
		var requestErr *RequestError
		if errors.As(err, &requestErr) {
			statusCode = requestErr.StatusCode
		}
		if contextErr := reqCtx.Err(); contextErr != nil {
			err = contextErr
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", "", &RequestError{MessageKey: ErrorNetwork, Reason: ErrorNetworkTimeout, Timeout: true, Platform: session.Platform, Stage: "verify", StatusCode: statusCode, Cause: err}
		}
		if errors.Is(err, context.Canceled) {
			return "", "", &RequestError{MessageKey: ErrorNetwork, Platform: session.Platform, Stage: "verify", StatusCode: statusCode, UpstreamMessage: "读取已取消，本次没有保存", Cause: err}
		}
		return "", "", withFailureStage(err, "verify", session.Platform)
	}
	record := dataRecord(response.Payload)
	email := ""
	if value, ok := record["email"].(string); ok {
		email = strings.TrimSpace(value)
	}
	for _, key := range []string{"id", "user_id", "userId"} {
		if id, ok := strictNumericID(record[key], false); ok {
			return id, email, nil
		}
	}
	return "", email, nil
}
func strictNumericID(value any, allowZero bool) (string, bool) {
	var number float64
	switch v := value.(type) {
	case float64:
		number = v
	case string:
		if strings.TrimSpace(v) == "" {
			return "", false
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return "", false
		}
		number = parsed
	default:
		return "", false
	}
	if !finiteNonnegative(number) || math.Trunc(number) != number || number > 9007199254740991 || (!allowZero && number == 0) {
		return "", false
	}
	return strconv.FormatInt(int64(number), 10), true
}
func validateDatedKeyCollection(date string, a, b float64, items []KeyUsageTodayStat) error {
	if _, _, err := businessDayUnixBounds(date); err != nil {
		return datedCostError(PlatformSub2API, "metrics", "指定日期格式不正确")
	}
	var total float64
	for _, item := range items {
		if !finiteNonnegative(item.TodayAmount) {
			return datedCostError(PlatformSub2API, "metrics", "Key实际费用不是有限的非负数")
		}
		total += item.TodayAmount
	}
	if !finiteNonnegative(a) || !finiteNonnegative(b) || !finiteNonnegative(total) {
		return datedCostError(PlatformSub2API, "metrics", "独立总额或Key合计不是有限的非负数")
	}
	if math.Abs(a-b) > 1e-9 {
		return datedCostError(PlatformSub2API, "metrics", fmt.Sprintf("读取期间费用变化，读取前 %.12g、读取后 %.12g、Key合计 %.12g，暂未更新", a, b, total))
	}
	if math.Abs(total-b) > 1e-9 || math.Abs(total-a) > 1e-9 {
		return datedCostError(PlatformSub2API, "metrics", fmt.Sprintf("独立总额 %.12g 与Key合计 %.12g 不同，差额 %.12g，缺失原因尚未确认", b, total, b-total))
	}
	return nil
}
func (s *PlatformService) fetchDatedKeyCollection(ctx context.Context, session Session, groups []GroupInfo, date string) ([]KeyUsageTodayStat, float64, time.Time, error) {
	a, _, err := s.fetchAccountCostForDateContext(ctx, session, date)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	items, err := s.fetchKeyUsageForDate(ctx, session, groups, date)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	b, observedAt, err := s.fetchAccountCostForDateContext(ctx, session, date)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	if err = validateDatedKeyCollection(date, a, b, items); err != nil {
		return nil, 0, time.Time{}, err
	}
	return items, b, observedAt, nil
}
func (s *PlatformService) fetchSub2APIUserGroupCostsForDateContext(ctx context.Context, session Session, date string) ([]GroupDailyStat, error) {
	reqCtx, cancel := context.WithTimeout(ctx, keyUsageRequestTimeout)
	defer cancel()
	response, err := s.httpClient.requestJSONWithContext(reqCtx, session.BaseURL+"/api/v1/usage/dashboard/snapshot-v2?start_date="+url.QueryEscape(date)+"&end_date="+url.QueryEscape(date)+"&timezone=Asia%2FSingapore&include_trend=false&include_model_stats=false&include_group_stats=true", sub2APIUserAuthOptions(session))
	if err != nil {
		var detail *RequestError
		if errors.As(err, &detail) && (detail.StatusCode == http.StatusNotFound || detail.StatusCode == http.StatusMethodNotAllowed) {
			return nil, datedCostError(session.Platform, "metrics", "当前上游不支持用户分组费用查询，请联系上游维护人员检查此接口")
		}
		return nil, withFailureStage(err, "metrics", session.Platform)
	}
	data := dataRecord(response.Payload)
	if data["start_date"] != date || data["end_date"] != date {
		return nil, datedCostError(session.Platform, "metrics", "上游返回的分组费用日期与指定的新加坡日期不一致")
	}
	rows, ok := data["groups"].([]any)
	if !ok {
		return nil, datedCostError(session.Platform, "metrics", "上游分组费用响应缺少groups数组，不能视为零消费")
	}
	result := make([]GroupDailyStat, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		record, ok := row.(map[string]any)
		if !ok {
			return nil, datedCostError(session.Platform, "metrics", "上游分组费用包含格式不正确的记录")
		}
		id, ok := strictNumericID(record["group_id"], true)
		if !ok || seen[id] {
			return nil, datedCostError(session.Platform, "metrics", "上游分组编号缺失、无效或重复")
		}
		seen[id] = true
		amount := firstNumber(record, []string{"actual_cost"})
		if amount == nil || !finiteNonnegative(*amount) {
			return nil, datedCostError(session.Platform, "metrics", "上游分组实际费用缺失或不是有限的非负数")
		}
		result = append(result, GroupDailyStat{GroupID: id, GroupName: safeString(record, "name"), TodayActualCost: *amount})
	}
	return result, nil
}
func reconcileGroupCosts(platform Platform, groups []GroupInfo, stats []GroupDailyStat, a, b float64) (GroupCostFetchResult, error) {
	visible := map[string]GroupInfo{}
	order := []string{}
	for _, group := range groups {
		id := strings.TrimSpace(group.ID)
		if platform == PlatformNewAPI {
			id = strings.TrimSpace(group.Name)
			if id == "" {
				id = strings.TrimSpace(group.ID)
			}
		}
		if id == "" {
			return GroupCostFetchResult{}, datedCostError(platform, "metrics", "当前可用分组没有合法编号")
		}
		if _, exists := visible[id]; exists {
			return GroupCostFetchResult{}, datedCostError(platform, "metrics", "当前可用分组编号重复，无法对应费用")
		}
		visible[id] = group
		order = append(order, id)
	}
	var total, attributed, unattributed float64
	amounts := map[string]float64{}
	for _, stat := range stats {
		if !finiteNonnegative(stat.TodayActualCost) {
			return GroupCostFetchResult{}, datedCostError(platform, "metrics", "分组费用金额无效")
		}
		key := strings.TrimSpace(stat.GroupID)
		if platform == PlatformNewAPI {
			key = strings.TrimSpace(stat.GroupName)
		}
		if _, duplicate := amounts[key]; duplicate {
			return GroupCostFetchResult{}, datedCostError(platform, "metrics", "分组费用编号重复")
		}
		amounts[key] = stat.TodayActualCost
		total += stat.TodayActualCost
		if _, ok := visible[key]; ok && (platform != PlatformSub2API || key != "0") {
			attributed += stat.TodayActualCost
		} else {
			unattributed += stat.TodayActualCost
		}
	}
	if !finiteNonnegative(a) || !finiteNonnegative(b) || !finiteNonnegative(total) {
		return GroupCostFetchResult{}, datedCostError(platform, "metrics", "独立总额或分组合计无效")
	}
	if math.Abs(a-b) > 1e-9 {
		return GroupCostFetchResult{}, &RequestError{MessageKey: ErrorInvalidResponse, Platform: platform, Stage: "metrics", UpstreamMessage: fmt.Sprintf("读取期间费用变化，读取前 %.12g、读取后 %.12g，暂未更新", a, b), Cause: &costChangedError{Before: a, After: b}}
	}
	if math.Abs(total-b) > 1e-9 {
		return GroupCostFetchResult{}, &RequestError{MessageKey: ErrorInvalidResponse, Platform: platform, Stage: "metrics", UpstreamMessage: fmt.Sprintf("独立总额 %.12g、分组合计 %.12g、差额 %.12g，缺失原因尚未确认，本轮未更新", b, total, b-total), Cause: &groupCostComparisonError{Before: a, After: b, Total: total}}
	}
	result := GroupCostFetchResult{Source: GroupCostSourceSummary, Complete: unattributed == 0, SiteReportedRawCost: float64Pointer(b), GroupAttributedRawCost: float64Pointer(attributed), UnattributedRawCost: float64Pointer(unattributed), Stats: make([]GroupDailyStat, 0, len(groups))}
	for _, id := range order {
		g := visible[id]
		result.Stats = append(result.Stats, GroupDailyStat{GroupID: g.ID, GroupName: g.Name, TodayActualCost: amounts[id]})
	}
	return result, nil
}

func (s *PlatformService) metricsDate(dates []string) string {
	if len(dates) > 0 {
		return dates[0]
	}
	return businesstime.DateAt(s.keyUsageNow())
}
func strictKeyArray(payload any) ([]any, bool) {
	record, ok := payload.(map[string]any)
	if !ok {
		return nil, false
	}
	if rows, ok := record["data"].([]any); ok {
		return rows, true
	}
	data := dataRecord(payload)
	for _, key := range []string{"items", "list", "records"} {
		if rows, ok := data[key].([]any); ok {
			return rows, true
		}
	}
	return nil, false
}
func strictKeyTotal(payload any, platforms ...Platform) (int, bool, error) {
	platform := PlatformSub2API
	if len(platforms) > 0 {
		platform = platforms[0]
	}
	record := dataRecord(payload)
	for _, key := range []string{"total", "count"} {
		if value, exists := record[key]; exists {
			id, valid := strictNumericID(value, true)
			if !valid {
				return 0, true, datedCostError(platform, "metrics", "Key列表总数不是合法的非负整数")
			}
			total, _ := strconv.Atoi(id)
			if total > 10000 {
				return 0, true, datedCostError(platform, "metrics", "Key列表总数超过10000条上限")
			}
			return total, true, nil
		}
	}
	return 0, false, nil
}
