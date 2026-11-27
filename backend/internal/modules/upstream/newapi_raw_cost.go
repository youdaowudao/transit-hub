package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const groupCostSourceRawLog = "raw_consumption_log"

func newAPIStatisticsPending(err error) bool {
	var detail *RequestError
	return errors.As(err, &detail) && detail.MessageKey == ErrorRequest &&
		detail.StatusCode == http.StatusOK && detail.Reason == "" &&
		strings.EqualFold(strings.TrimSpace(detail.UpstreamMessage), "Statistics are still being calculated. Try again later.")
}

type newAPIRawUsage struct {
	groups map[string]int64
	keys   map[string]int64
	// Missing Key identity does not invalidate consumption-time group attribution.
	unidentifiedKeyQuota int64
}

func (s *PlatformService) fetchNewAPIRawUsage(ctx context.Context, session Session, date string) (newAPIRawUsage, error) {
	fail := func(reason string) (newAPIRawUsage, error) {
		return newAPIRawUsage{}, datedCostError(PlatformNewAPI, "metrics", "原始消费日志："+reason)
	}
	start, end, err := businessDayUnixBounds(date)
	if err != nil {
		return fail("指定日期无效")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	result := newAPIRawUsage{groups: map[string]int64{}, keys: map[string]int64{}}
	const pageSize = 100
	const maxRows = 10000 // Upstream may cap its advertised count; equality is not proof of completeness.
	const maxExactQuota = int64(9007199254740991)
	seen := map[string]bool{}
	declared, count := -1, 0
	var totalQuota int64
	for page := 1; page <= maxRows/pageSize; page++ {
		siteID := datedUsageSiteID(ctx)
		if s.datedUsagePaused(siteID) {
			return newAPIRawUsage{}, datedUsagePauseError()
		}
		path := session.BaseURL + "/api/log/self?type=2&start_timestamp=" + strconvInt(start) + "&end_timestamp=" + strconvInt(end) + "&p=" + strconv.Itoa(page) + "&page_size=" + strconv.Itoa(pageSize)
		reqCtx, stop := context.WithTimeout(ctx, keyUsageRequestTimeout)
		response, readErr := s.httpClient.requestJSONWithContextLimit(reqCtx, path, newAPIAuthOptions(session), 8*1024*1024)
		stop()
		s.recordDatedUsageRateLimit(siteID, path, response, readErr)
		if readErr != nil {
			return newAPIRawUsage{}, readErr
		}
		envelope, ok := response.Payload.(map[string]any)
		if !ok || envelope["success"] != true {
			return fail("没有明确成功结果")
		}
		data, valid := envelope["data"].(map[string]any)
		rows, rowsOK := data["items"].([]any)
		totalID, totalOK := strictNumericID(data["total"], true)
		total, totalErr := strconv.Atoi(totalID)
		if !valid || !rowsOK || !totalOK || totalErr != nil || total >= maxRows {
			return fail("缺少完整列表或总数达到读取上限")
		}
		if declared < 0 {
			declared = total
		}
		wantCount := min(pageSize, declared-count)
		if total != declared || len(rows) != wantCount {
			return fail("分页数量不一致，不能确认完整消费记录")
		}
		if value, present := data["page"]; present {
			p, valid := strictNumericID(value, false)
			if !valid || p != strconv.Itoa(page) {
				return fail("返回页码不一致")
			}
		}
		for _, item := range rows {
			row, ok := item.(map[string]any)
			if !ok {
				return fail("消费记录格式不正确")
			}
			kind, kindOK := strictNumericID(row["type"], false)
			ts, tsOK := strictNumericID(row["created_at"], false)
			at, _ := strconv.ParseInt(ts, 10, 64)
			quota, quotaOK := strictNumericID(row["quota"], true)
			q, _ := strconv.ParseInt(quota, 10, 64)
			group, groupOK := row["group"].(string)
			id, idOK := row["request_id"].(string)
			if !kindOK || kind != "2" || !tsOK || at < start || at > end || !quotaOK || !groupOK ||
				!idOK || strings.TrimSpace(id) == "" || seen[id] || q > maxExactQuota-totalQuota {
				return fail("消费日期、金额、分组或请求标识无效或重复")
			}
			seen[id] = true
			totalQuota += q
			result.groups[strings.TrimSpace(group)] += q
			key, keyOK := strictNumericID(row["token_id"], false)
			if keyOK {
				result.keys[key] += q
			} else {
				result.unidentifiedKeyQuota += q
			}
			count++
		}
		if count == declared {
			return result, nil
		}
	}
	return fail("分页达到读取上限")
}

func rawLogAmount(quota int64, session Session) (float64, error) {
	amount := quotaToUSDWithUnit(float64Pointer(float64(quota)), session.QuotaPerUnit)
	if amount == nil || !finiteNonnegative(*amount) || quota > 0 && *amount == 0 {
		return 0, datedCostError(PlatformNewAPI, "metrics", "原始消费金额换算无效，不能确认费用")
	}
	return *amount, nil
}

func rawLogGroupStats(result newAPIRawUsage, session Session) ([]GroupDailyStat, error) {
	stats := make([]GroupDailyStat, 0, len(result.groups))
	for group, quota := range result.groups {
		amount, err := rawLogAmount(quota, session)
		if err != nil {
			return nil, err
		}
		stats = append(stats, GroupDailyStat{GroupName: group, TodayActualCost: amount})
	}
	return stats, nil
}

func rawLogKeyStats(result newAPIRawUsage, catalog []KeyUsageTodayStat, session Session) ([]KeyUsageTodayStat, error) {
	items := append([]KeyUsageTodayStat(nil), catalog...)
	known := make(map[string]bool)
	for i := range items {
		var quota int64
		for _, id := range items[i].KeyIDs {
			known[id] = true
			quota += result.keys[id]
		}
		amount, err := rawLogAmount(quota, session)
		if err != nil {
			return nil, err
		}
		items[i].TodayAmount = amount
	}
	for id, quota := range result.keys {
		if !known[id] && quota != 0 {
			return nil, datedCostError(PlatformNewAPI, "metrics", "原始消费日志包含当前Key列表无法对应的消费")
		}
	}
	if result.unidentifiedKeyQuota != 0 {
		return nil, datedCostError(PlatformNewAPI, "metrics", "原始消费日志存在缺少Key编号的消费")
	}
	return items, nil
}

func rawCostFallbackError(original, fallback error) error {
	var detail *RequestError
	result := RequestError{MessageKey: siteErrorKey(fallback), Platform: PlatformNewAPI, Stage: "metrics"}
	if errors.As(fallback, &detail) {
		result = *detail
	}
	result.Cause = fallback
	result.UpstreamMessage = safeCostReason(fmt.Sprintf("读取原始消费记录补查失败：%s；原统计未通过：%s", costFailureCause(fallback), costFailureCause(original)))
	return &result
}

func (s *PlatformService) fetchRawLogGroupCosts(ctx context.Context, session Session, groups []GroupInfo, date string, before float64, original error) (GroupCostFetchResult, error) {
	fail := func(err error) (GroupCostFetchResult, error) {
		return GroupCostFetchResult{}, &groupCostFetchError{err: rawCostFallbackError(original, err), path: "summary"}
	}
	usage, err := s.fetchNewAPIRawUsage(ctx, session, date)
	if err != nil {
		return fail(err)
	}
	after, _, err := s.fetchAccountCostForDateContext(ctx, session, date)
	if err != nil {
		return fail(err)
	}
	stats, err := rawLogGroupStats(usage, session)
	if err != nil {
		return fail(err)
	}
	result, err := reconcileGroupCosts(session.Platform, groups, stats, before, after)
	if err != nil {
		return fail(err)
	}
	result.Source = groupCostSourceRawLog
	return result, nil
}

func (s *PlatformService) fetchRawLogKeyCosts(ctx context.Context, session Session, date string, catalog []KeyUsageTodayStat, before float64, original error) ([]KeyUsageTodayStat, float64, time.Time, error) {
	fail := func(err error) ([]KeyUsageTodayStat, float64, time.Time, error) {
		return nil, 0, time.Time{}, rawCostFallbackError(original, err)
	}
	usage, err := s.fetchNewAPIRawUsage(ctx, session, date)
	if err != nil {
		return fail(err)
	}
	items, err := rawLogKeyStats(usage, catalog, session)
	if err != nil {
		return fail(err)
	}
	after, at, err := s.fetchAccountCostForDateContext(ctx, session, date)
	if err != nil {
		return fail(err)
	}
	if err = validateDatedKeyCollection(date, before, after, items); err != nil {
		return fail(err)
	}
	return items, after, at, nil
}
