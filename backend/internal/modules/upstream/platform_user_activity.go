package upstream

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

type Sub2APIUserUsageTrendPoint struct {
	Date, UserID     string
	Requests, Tokens int64
	ActualCost       float64
}
type Sub2APIActivityUsageStats struct {
	Requests   int64
	ActualCost float64
}
type Sub2APIRedeemRecord struct {
	ID, Code, Type, UsedBy string
	Value                  float64
	UsedAt                 *time.Time
}
type Sub2APIRedeemRecordsPage struct {
	Items      []Sub2APIRedeemRecord
	Total      int
	TotalKnown bool
}
type Sub2APIAffiliateTransfer struct {
	ID, Action, UserID string
	Amount             float64
	CreatedAt          *time.Time
}
type Sub2APIAffiliateTransfersPage struct {
	Items      []Sub2APIAffiliateTransfer
	Total      int
	TotalKnown bool
}

func activityFormat(reason string) error {
	e := newRequestError(ErrorInvalidResponse, PlatformSub2API)
	e.UpstreamMessage = reason
	return e
}
func activityNumber(m map[string]any, keys ...string) (float64, error) {
	var value float64
	found := false
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		n, ok := raw.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, activityFormat("字段 " + key + " 缺少有效数字")
		}
		if found && n != value {
			return 0, activityFormat("同一数值的多个字段不一致")
		}
		value = n
		found = true
	}
	if !found {
		return 0, activityFormat("缺少必要数字字段 " + strings.Join(keys, "/"))
	}
	return value, nil
}
func activityCount(m map[string]any, keys ...string) (int64, error) {
	n, e := activityNumber(m, keys...)
	if e != nil {
		return 0, e
	}
	if n < 0 || n > 9007199254740991 || math.Trunc(n) != n {
		return 0, activityFormat("计数字段不是可完整读取的非负整数")
	}
	return int64(n), nil
}
func activityID(m map[string]any, keys ...string) (string, error) {
	var id string
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		var v string
		switch n := raw.(type) {
		case string:
			v = n
		case float64:
			if n <= 0 || n > 9007199254740991 || math.Trunc(n) != n {
				return "", activityFormat("记录编号不是正整数")
			}
			v = strconv.FormatInt(int64(n), 10)
		default:
			return "", activityFormat("记录编号类型不正确")
		}
		num, e := strconv.ParseInt(v, 10, 64)
		if e != nil || num <= 0 || strconv.FormatInt(num, 10) != v {
			return "", activityFormat("记录编号不是完整的正整数")
		}
		if id != "" && id != v {
			return "", activityFormat("同一编号的多个字段不一致")
		}
		id = v
	}
	if id == "" {
		return "", activityFormat("缺少记录或用户编号")
	}
	return id, nil
}
func activityTime(m map[string]any, keys ...string) (*time.Time, error) {
	var value *time.Time
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		parsed := parseFlexibleTime(raw)
		if parsed == nil {
			return nil, activityFormat("时间字段 " + key + " 无效或缺失")
		}
		if value != nil && !value.Equal(*parsed) {
			return nil, activityFormat("同一时间的多个字段不一致")
		}
		value = parsed
	}
	if value == nil {
		return nil, activityFormat("缺少必要记录时间")
	}
	return value, nil
}
func activityItems(payload any) ([]any, int, bool, error) {
	root, ok := payload.(map[string]any)
	if !ok {
		return nil, 0, false, activityFormat("入账响应不是完整对象")
	}
	var candidates [][]any
	add := func(raw any) error {
		a, ok := raw.([]any)
		if !ok {
			return activityFormat("入账条目缺失、为null或不是数组")
		}
		candidates = append(candidates, a)
		return nil
	}
	if raw, ok := root["items"]; ok {
		if e := add(raw); e != nil {
			return nil, 0, false, e
		}
	}
	if raw, ok := root["data"]; ok {
		switch v := raw.(type) {
		case []any:
			if e := add(v); e != nil {
				return nil, 0, false, e
			}
		case map[string]any:
			for _, key := range []string{"items", "list", "records"} {
				if x, yes := v[key]; yes {
					if e := add(x); e != nil {
						return nil, 0, false, e
					}
				}
			}
		default:
			return nil, 0, false, activityFormat("入账data不是有效对象或数组")
		}
	}
	if len(candidates) == 0 {
		return nil, 0, false, activityFormat("入账响应没有明确的记录数组")
	}
	for _, a := range candidates[1:] {
		if !reflect.DeepEqual(a, candidates[0]) {
			return nil, 0, false, activityFormat("入账响应的多种记录数组不一致")
		}
	}
	total := 0
	known := false
	for _, m := range []map[string]any{root, dataRecord(payload)} {
		if _, ok := m["total"]; ok {
			n, e := activityCount(m, "total")
			if e != nil {
				return nil, 0, false, e
			}
			if known && total != int(n) {
				return nil, 0, false, activityFormat("入账分页总数不一致")
			}
			total = int(n)
			known = true
		}
	}
	return candidates[0], total, known, nil
}
func (s *PlatformService) activityRead(ctx context.Context, session Session, path string, q url.Values) (any, error) {
	if session.Platform != PlatformSub2API || !session.IsAuthenticated() {
		return nil, newRequestError(ErrorAuth, PlatformSub2API)
	}
	r, e := s.httpClient.requestJSONWithContext(ctx, session.BaseURL+path+"?"+q.Encode(), adminAuthOptions(session))
	if e != nil {
		return nil, e
	}
	return r.Payload, nil
}
func (s *PlatformService) FetchSub2APIUserUsageTrendContext(ctx context.Context, session Session, from, to string, limit int) ([]Sub2APIUserUsageTrendPoint, error) {
	q := url.Values{"start_date": {from}, "end_date": {to}, "granularity": {"day"}, "metric": {"actual_cost"}, "limit": {strconv.Itoa(limit)}, "timezone": {businesstime.Timezone}}
	payload, e := s.activityRead(ctx, session, "/api/v1/admin/dashboard/users-trend", q)
	if e != nil {
		return nil, e
	}
	root, ok := payload.(map[string]any)
	if !ok {
		return nil, activityFormat("用量响应不是完整对象")
	}
	data, ok := root["data"].(map[string]any)
	if !ok {
		return nil, activityFormat("用量响应缺少data对象")
	}
	rows, ok := data["trend"].([]any)
	if !ok {
		return nil, activityFormat("用量响应缺少data.trend数组")
	}
	if len(rows) >= limit {
		return nil, activityFormat("用量结果触及数量上限，无法确认完整")
	}
	out := make([]Sub2APIUserUsageTrendPoint, 0, len(rows))
	seen := map[string]bool{}
	for i, raw := range rows {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, activityFormat(fmt.Sprintf("第%d条用量不是对象", i+1))
		}
		p := Sub2APIUserUsageTrendPoint{}
		p.UserID, e = activityID(m, "user_id", "userId")
		if e != nil {
			return nil, e
		}
		p.Date, ok = m["date"].(string)
		if !ok {
			return nil, activityFormat("用量缺少日期")
		}
		if _, _, e = businesstime.Bounds(p.Date); e != nil || p.Date < from || p.Date > to {
			return nil, activityFormat("用量日期不在请求范围")
		}
		p.Requests, e = activityCount(m, "requests")
		if e != nil {
			return nil, e
		}
		p.Tokens, e = activityCount(m, "tokens", "total_tokens")
		if e != nil {
			return nil, e
		}
		p.ActualCost, e = activityNumber(m, "actual_cost", "actualCost")
		if e != nil || p.ActualCost < 0 {
			return nil, activityFormat("用量缺少有效的实际消费")
		}
		key := p.UserID + "/" + p.Date
		if seen[key] {
			return nil, activityFormat("用量中有重复用户日期")
		}
		seen[key] = true
		out = append(out, p)
	}
	return out, nil
}
func (s *PlatformService) FetchSub2APIActivityUsageStatsContext(ctx context.Context, session Session, day string) (Sub2APIActivityUsageStats, error) {
	p, e := s.activityRead(ctx, session, "/api/v1/admin/usage/stats", url.Values{"start_date": {day}, "end_date": {day}, "timezone": {businesstime.Timezone}})
	if e != nil {
		return Sub2APIActivityUsageStats{}, e
	}
	m := dataRecord(p)
	requests, e := activityCount(m, "total_requests")
	if e != nil {
		return Sub2APIActivityUsageStats{}, e
	}
	cost, e := activityNumber(m, "total_actual_cost")
	if e != nil || cost < 0 {
		return Sub2APIActivityUsageStats{}, activityFormat("用量汇总缺少有效实际消费")
	}
	return Sub2APIActivityUsageStats{requests, cost}, nil
}
func (s *PlatformService) FetchSub2APIUsedRedeemRecordsContext(ctx context.Context, session Session, kind string, page, pageSize int) (Sub2APIRedeemRecordsPage, error) {
	result := Sub2APIRedeemRecordsPage{}
	if (kind != "balance" && kind != "admin_balance") || page < 1 || pageSize < 1 || pageSize > 500 {
		return result, activityFormat("入账分页或类型参数无效")
	}
	p, e := s.activityRead(ctx, session, "/api/v1/admin/redeem-codes", url.Values{"type": {kind}, "status": {"used"}, "sort_by": {"used_at"}, "sort_order": {"desc"}, "page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(pageSize)}})
	if e != nil {
		return result, e
	}
	rows, total, known, e := activityItems(p)
	if e != nil {
		return result, e
	}
	if len(rows) > pageSize {
		return result, activityFormat("入账单页超过请求数量")
	}
	result.Total, result.TotalKnown = total, known
	result.Items = []Sub2APIRedeemRecord{}
	for _, raw := range rows {
		m, ok := raw.(map[string]any)
		if !ok {
			return result, activityFormat("入账记录不是对象")
		}
		r := Sub2APIRedeemRecord{}
		r.ID, e = activityID(m, "id")
		if e != nil {
			return result, e
		}
		r.UsedBy, e = activityID(m, "used_by", "usedBy")
		if e != nil {
			return result, e
		}
		r.UsedAt, e = activityTime(m, "used_at", "usedAt")
		if e != nil {
			return result, e
		}
		r.Value, e = activityNumber(m, "value")
		if e != nil {
			return result, e
		}
		r.Type, ok = m["type"].(string)
		if !ok || r.Type != kind {
			return result, activityFormat("入账记录类型与请求不一致")
		}
		r.Code, ok = m["code"].(string)
		if !ok {
			return result, activityFormat("入账记录缺少兑换编号")
		}
		result.Items = append(result.Items, r)
	}
	return result, nil
}
func (s *PlatformService) FetchSub2APIAffiliateTransfersContext(ctx context.Context, session Session, from, to string, page, pageSize int) (Sub2APIAffiliateTransfersPage, error) {
	result := Sub2APIAffiliateTransfersPage{}
	if page < 1 || pageSize < 1 || pageSize > 100 {
		return result, activityFormat("返利分页参数无效")
	}
	p, e := s.activityRead(ctx, session, "/api/v1/admin/affiliates/transfers", url.Values{"start_at": {from}, "end_at": {to}, "timezone": {businesstime.Timezone}, "sort_by": {"created_at"}, "sort_order": {"desc"}, "page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(pageSize)}})
	if e != nil {
		return result, e
	}
	rows, total, known, e := activityItems(p)
	if e != nil {
		return result, e
	}
	if len(rows) > pageSize {
		return result, activityFormat("返利单页超过请求数量")
	}
	result.Total, result.TotalKnown = total, known
	result.Items = []Sub2APIAffiliateTransfer{}
	for _, raw := range rows {
		m, ok := raw.(map[string]any)
		if !ok {
			return result, activityFormat("返利记录不是对象")
		}
		r := Sub2APIAffiliateTransfer{}
		r.ID, e = activityID(m, "ledger_id", "id")
		if e != nil {
			return result, e
		}
		r.UserID, e = activityID(m, "user_id", "userId")
		if e != nil {
			return result, e
		}
		r.CreatedAt, e = activityTime(m, "created_at", "createdAt")
		if e != nil {
			return result, e
		}
		r.Amount, e = activityNumber(m, "amount")
		if e != nil {
			return result, activityFormat("返利金额不是有效非负数")
		}
		r.Action, ok = m["action"].(string)
		if !ok || (r.Action != "transfer" && r.Action != "withdraw") {
			return result, activityFormat("返利操作类型无效")
		}
		if r.Action == "transfer" && r.Amount < 0 {
			return result, activityFormat("返利转入金额为负数，不能确认为余额入账")
		}
		result.Items = append(result.Items, r)
	}
	return result, nil
}
