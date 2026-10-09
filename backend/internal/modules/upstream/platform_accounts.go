package upstream

import (
	"context"
	"log"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// AdminGroupAccountInfo 是「某个 admin 分组下的账号(sub2api) / 渠道(new-api)」的平台中性信息，
// 供 connection_health 分组健康主列表的账号弹窗展示。
//
// 安全约束：这里只保留展示所需的基础字段与探活策略相关字段，绝不包含 credentials / key /
// token / cookie 等敏感明文——上游响应里的敏感字段在解析阶段就被丢弃，不会进入本结构。
//
// 字段可空性：不同平台/不同上游版本返回的字段并不一致，凡是「缺省时应展示为占位符」的
// 数值/布尔字段一律用指针，nil 表示上游未提供，由前端决定展示 "-" 还是隐藏，避免把
// 「上游没给」误当成「值为 0」。
type AdminGroupAccountInfo struct {
	ModelMapping map[string]string `json:"-"`
	ModelMappingKnown bool `json:"-"`
	OpenAIPassthrough bool `json:"-"`
	ID             string     // sub2api account id / new-api channel id
	Name           string     // 账号或渠道名称
	Platform       string     // 上游平台标识（openai / anthropic / ...），可能为空
	Type           string     // sub2api 账号类型 / new-api channel 类型（数值转字符串）
	Status         string     // 状态（字符串或数值转字符串）
	ErrorMessage   string     `json:"-"` // Sub2API 主站运行错误原因；只保留脱敏后的短文本
	Priority       *int       // 优先级
	Concurrency    *int       // 并发（sub2api）
	RateMultiplier *float64   // Sub2API admin 转发账号记录自身的 rate_multiplier，不代表上游 API Key 所属分组倍率。
	LoadFactor     *int       // 负载因子（sub2api）
	Weight         *int       // 权重（仅 new-api channel 有；sub2api 为 nil）
	Models         string     // 模型列表（new-api channel.models 等）
	GroupIDs       []string   // 所属分组 ID/名称列表
	Schedulable    *bool      // 是否可调度（sub2api）
	UpdatedAt      *time.Time // 上游账号记录最后更新时间；用于区分本地用户动作与后续主站直接修改
	// BaseURL 是 new-api channel 转发到的上游 provider 地址（channel.base_url）。
	// 独立探活需要用它 + channel key 直接对上游发起 OpenAI 兼容请求。sub2api 账号在列表阶段
	// 拿不到 base_url，探活前再从单账号导出凭据里解析，故此处可能为空。
	BaseURL                 string
	TempUnschedulableUntil  *time.Time
	TempUnschedulableKnown  bool
	TempUnschedulableReason string
	RateLimitResetAt        *time.Time
	RateLimitKnown          bool
	OverloadUntil           *time.Time
	OverloadKnown           bool
	ExpiresAt               *time.Time
	ExpiresAtKnown          bool
	AutoPauseOnExpired      *bool
	ModelRateLimits         []Sub2APIModelRateLimit
	ModelRateLimitsKnown    bool
	QuotaLimit              *float64
	QuotaUsed               *float64
	QuotaDailyLimit         *float64
	QuotaDailyUsed          *float64
	QuotaWeeklyLimit        *float64
	QuotaWeeklyUsed         *float64
	QuotaKnown              bool
	InventoryResponseTimes  []InventoryResponseTime `json:"-"`
}

// ListAdminGroupAccounts 平台中性地读取某个 admin 分组下的账号/渠道列表。
// sub2api 走 /api/v1/admin/accounts?group=<groupID>，new-api 走 channel 查询。
// 返回的每个条目都不含敏感字段。
func (s *PlatformService) ListAdminGroupAccounts(session Session, group AdminGroupInfo) ([]AdminGroupAccountInfo, error) {
	return s.ListAdminGroupAccountsContext(context.Background(), session, group)
}

func (s *PlatformService) ListAdminGroupAccountsContext(ctx context.Context, session Session, group AdminGroupInfo) ([]AdminGroupAccountInfo, error) {
	switch session.Platform {
	case PlatformNewAPI:
		return s.listNewAPIGroupChannelsContext(ctx, session, group)
	default:
		return s.listSub2APIGroupAccountsContext(ctx, session, group)
	}
}

// ListSub2APIAdminAccountsContext reads the complete Sub2API admin account
// inventory without applying a group filter. Callers use this authoritative
// inventory to distinguish a deleted main-site account from an account that
// merely moved to another group.
func (s *PlatformService) ListSub2APIAdminAccountsContext(ctx context.Context, session Session) ([]AdminGroupAccountInfo, error) {
	if session.Platform != PlatformSub2API || !session.IsAuthenticated() {
		return nil, newRequestError(ErrorAuth, PlatformSub2API)
	}

	const pageSize = 100
	const maxPages = 100
	authOptions := adminAuthOptions(session)
	accounts := make([]AdminGroupAccountInfo, 0)
	seenAccountIDs := make(map[string]struct{})
	expectedTotal := -1
	responseTimes := make([]InventoryResponseTime, 0)
	for page := 1; page <= maxPages; page++ {
		pageURL := session.BaseURL + "/api/v1/admin/accounts?page=" + strconvInt(int64(page)) +
			"&page_size=" + strconvInt(pageSize)
		response, err := s.httpClient.requestJSONWithContext(ctx, pageURL, authOptions)
		if err != nil {
			return nil, err
		}
		responseTimes = append(responseTimes, InventoryResponseTime{HTTPDate: response.Header.Get("Date"), ReceivedAt: response.ReceivedAt})
		items, validItems := sub2APIAccountPageItems(response.Payload)
		if !validItems {
			return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
		total, hasTotal, validTotal := sub2APIAccountPaginationTotal(response.Payload)
		if !validTotal || !hasTotal {
			return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
		if len(items) > pageSize {
			return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
		if expectedTotal < 0 {
			expectedTotal = total
		} else if total != expectedTotal {
			return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
		for _, item := range items {
			record, ok := item.(map[string]any)
			if !ok {
				return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
			account := parseSub2APIAccount(record)
			accountID := strings.TrimSpace(account.ID)
			if accountID == "" {
				return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
			if _, duplicate := seenAccountIDs[accountID]; duplicate {
				return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
			seenAccountIDs[accountID] = struct{}{}
			accounts = append(accounts, account)
		}
		if len(accounts) > expectedTotal {
			return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
		if len(accounts) == expectedTotal {
			GuardSub2APIAccountTimes(session.BaseURL, accounts, responseTimes)
			return accounts, nil
		}
		if len(items) < pageSize {
			return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
	}
	return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
}

// listSub2APIGroupAccounts 分页拉取 sub2api 某分组下的账号。
// 注意 query 参数是 group=<分组ID>（不是 group_id）。逐页拉取直到没有下一页或达到 total。
func (s *PlatformService) listSub2APIGroupAccounts(session Session, group AdminGroupInfo) ([]AdminGroupAccountInfo, error) {
	return s.listSub2APIGroupAccountsContext(context.Background(), session, group)
}

func (s *PlatformService) listSub2APIGroupAccountsContext(ctx context.Context, session Session, group AdminGroupInfo) ([]AdminGroupAccountInfo, error) {
	if session.Platform != PlatformSub2API || !session.IsAuthenticated() {
		return nil, newRequestError(ErrorAuth, PlatformSub2API)
	}
	if strings.TrimSpace(group.ID) == "" {
		return []AdminGroupAccountInfo{}, nil
	}
	authOptions := adminAuthOptions(session)

	const pageSize = 100
	const maxPages = 100 // 安全上限，防止上游分页字段异常导致死循环
	accounts := make([]AdminGroupAccountInfo, 0)
	seenAccountIDs := make(map[string]struct{})
	expectedTotal := 0
	hasExpectedTotal := false
	responseTimes := make([]InventoryResponseTime, 0)
	for page := 1; page <= maxPages; page++ {
		pageURL := session.BaseURL + "/api/v1/admin/accounts?group=" + url.QueryEscape(group.ID) +
			"&page=" + strconvInt(int64(page)) + "&page_size=" + strconvInt(pageSize)
		response, err := s.httpClient.requestJSONWithContext(ctx, pageURL, authOptions)
		if err != nil {
			return nil, err
		}
		responseTime := InventoryResponseTime{HTTPDate: response.Header.Get("Date"), ReceivedAt: response.ReceivedAt}
		responseTimes = append(responseTimes, responseTime)
		group.InventoryTimeEvidence.add(responseTime)
		items, validItems := sub2APIAccountPageItems(response.Payload)
		if !validItems {
			return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
		total, hasTotal, validTotal := sub2APIAccountPaginationTotal(response.Payload)
		if !validTotal {
			return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
		if hasExpectedTotal {
			if !hasTotal || total != expectedTotal {
				return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
		} else if hasTotal {
			expectedTotal = total
			hasExpectedTotal = true
		}
		for _, item := range items {
			record, ok := item.(map[string]any)
			if !ok {
				return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
			accountID, validID := strictSub2APIInventoryID(record["id"])
			if !validID {
				return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
			if _, duplicate := seenAccountIDs[accountID]; duplicate {
				return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
			seenAccountIDs[accountID] = struct{}{}
			account := parseSub2APIAccount(record)
			account.ID = accountID
			accounts = append(accounts, account)
		}
		finished, validPage := sub2APIInventoryPageFinished(response.Payload, page, pageSize, len(items), len(accounts))
		if !validPage {
			return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
		if finished {
			GuardSub2APIAccountTimes(session.BaseURL, accounts, responseTimes)
			return accounts, nil
		}
	}
	return nil, newRequestError(ErrorInvalidResponse, PlatformSub2API)
}

// Sub2API 的账号接口在不同版本中使用过多种容器与分页字段。这里允许兼容字段并存，
// 但只在其值完全一致时接受；任何歧义都必须让完整库存轮次失败，不能静默挑选一个字段。
func sub2APIAccountPageItems(value any) ([]any, bool) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}

	candidates := make([][]any, 0, 4)
	appendCandidate := func(raw any) bool {
		items, ok := raw.([]any)
		if !ok {
			return false
		}
		candidates = append(candidates, items)
		return true
	}

	if rawData, exists := record["data"]; exists {
		switch data := rawData.(type) {
		case []any:
			candidates = append(candidates, data)
		case map[string]any:
			for _, key := range []string{"items", "list", "records"} {
				if rawItems, exists := data[key]; exists && !appendCandidate(rawItems) {
					return nil, false
				}
			}
		default:
			return nil, false
		}
	}
	if rawItems, exists := record["items"]; exists && !appendCandidate(rawItems) {
		return nil, false
	}
	if len(candidates) == 0 {
		return nil, false
	}
	for _, candidate := range candidates[1:] {
		if !reflect.DeepEqual(candidates[0], candidate) {
			return nil, false
		}
	}
	return candidates[0], true
}

func sub2APIAccountPaginationTotal(value any) (int, bool, bool) {
	record, ok := value.(map[string]any)
	if !ok {
		return 0, false, false
	}

	totals := make([]int, 0, 4)
	appendTotal := func(raw any) bool {
		number := readNumber(raw)
		if number == nil || *number < 0 || math.Trunc(*number) != *number || *number > float64(int(^uint(0)>>1)) {
			return false
		}
		totals = append(totals, int(*number))
		return true
	}
	appendRecordTotals := func(candidate map[string]any) bool {
		for _, key := range []string{"total", "count"} {
			if raw, exists := candidate[key]; exists && !appendTotal(raw) {
				return false
			}
		}
		return true
	}

	if !appendRecordTotals(record) {
		return 0, true, false
	}
	if data, ok := record["data"].(map[string]any); ok {
		if !appendRecordTotals(data) {
			return 0, true, false
		}
	}
	if len(totals) == 0 {
		return 0, false, true
	}
	for _, total := range totals[1:] {
		if total != totals[0] {
			return 0, true, false
		}
	}
	return totals[0], true, true
}

// parseSub2APIAccount 把 sub2api 账号原始记录解析为平台中性结构，主动丢弃 credentials 等敏感字段。
func parseSub2APIAccount(record map[string]any) AdminGroupAccountInfo {
	var errorMessage string
	if value := firstString(record, []string{"error_message", "errorMessage"}); value != nil {
		errorMessage = *value
	}
	account := AdminGroupAccountInfo{
		ID:             groupID2(record),
		Name:           safeString(record, "name"),
		Type:           stringOrNumberField(record, []string{"type"}),
		Status:         stringOrNumberField(record, []string{"status"}),
		ErrorMessage:   sanitizeSub2APIErrorMessage(errorMessage),
		Priority:       firstInt(record, []string{"priority"}),
		Concurrency:    firstInt(record, []string{"concurrency"}),
		RateMultiplier: firstNumber(record, []string{"rate_multiplier", "rateMultiplier"}),
		LoadFactor:     firstInt(record, []string{"load_factor", "loadFactor"}),
		GroupIDs:       parseGroupIDList(record),
		Schedulable:    firstBoolValue(record, []string{"schedulable"}),
		UpdatedAt:      parseFlexibleTime(firstAny(record, []string{"updated_at", "updatedAt"})),
	}
	account.ModelMapping, account.ModelMappingKnown = parseSub2APIModelMapping(record)
	account.OpenAIPassthrough = parseSub2APIOpenAIPassthrough(record)
	parseSub2APIRestrictions(record, &account)
	if p := firstString(record, []string{"platform"}); p != nil {
		account.Platform = *p
	}
	if m := firstString(record, []string{"models"}); m != nil {
		account.Models = *m
	}
	return account
}

func sanitizeSub2APIErrorMessage(raw string) string {
	value := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"access_token=",
		"access_token:",
		"access_token",
		"refresh_token=",
		"refresh_token:",
		"refresh_token",
		"api_key=",
		"api_key:",
		"api_key",
		"api key",
		"apikey=",
		"apikey:",
		"apikey",
		"api-key=",
		"api-key:",
		"api-key",
		"authorization=",
		"authorization:",
		"authorization ",
		"bearer ",
		"cookie=",
		"cookie:",
		"cookie ",
		"credentials=",
		"credentials:",
		"credentials",
		"client_secret=",
		"client_secret:",
		"client_secret",
		"password=",
		"password:",
		"password",
		"secret=",
		"secret:",
		"secret",
		"token=",
		"token:",
		"token ",
		"x-api-key=",
		"x-api-key:",
		"x-api-key",
	} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	if strings.Contains(value, "://") || strings.Contains(value, "?") || strings.Contains(value, "&") {
		return ""
	}
	runes := []rune(value)
	if len(runes) > 160 {
		runes = runes[:160]
	}
	return string(runes)
}

// listNewAPIGroupChannels 读取 new-api 某分组下的 channel 列表。
// 优先使用 /api/channel/search?group=<分组名>（server 端已按分组过滤，兼容较老部署也普遍支持）；
// search 失败时兜底 /api/channel/ 分页拉取后在本地按「逗号分组精确匹配」过滤。
func (s *PlatformService) listNewAPIGroupChannels(session Session, group AdminGroupInfo) ([]AdminGroupAccountInfo, error) {
	return s.listNewAPIGroupChannelsContext(context.Background(), session, group)
}

func (s *PlatformService) listNewAPIGroupChannelsContext(ctx context.Context, session Session, group AdminGroupInfo) ([]AdminGroupAccountInfo, error) {
	if session.Platform != PlatformNewAPI || !session.IsAuthenticated() {
		return nil, newRequestError(ErrorAuth, PlatformNewAPI)
	}
	groupName := strings.TrimSpace(group.Name)
	if groupName == "" {
		return []AdminGroupAccountInfo{}, nil
	}

	channels, err := s.searchNewAPIGroupChannelsContext(ctx, session, groupName)
	if err == nil {
		return channels, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	log.Printf("[connection-health] new-api /api/channel/search 拉取失败，回退 /api/channel/ 本地过滤 base_url=%s group=%s err=%v", session.BaseURL, groupName, err)
	return s.listNewAPIChannelsWithLocalFilterContext(ctx, session, groupName)
}

// searchNewAPIGroupChannels 通过 /api/channel/search?group= 分页读取指定分组的 channel。
func (s *PlatformService) searchNewAPIGroupChannels(session Session, groupName string) ([]AdminGroupAccountInfo, error) {
	return s.searchNewAPIGroupChannelsContext(context.Background(), session, groupName)
}

func (s *PlatformService) searchNewAPIGroupChannelsContext(ctx context.Context, session Session, groupName string) ([]AdminGroupAccountInfo, error) {
	cookieOptions := newAPIAuthOptions(session)
	const pageSize = 100
	const maxPages = 100
	channels := make([]AdminGroupAccountInfo, 0)
	for page := 1; page <= maxPages; page++ {
		pageURL := session.BaseURL + "/api/channel/search?group=" + url.QueryEscape(groupName) +
			"&p=" + strconvInt(int64(page)) + "&page_size=" + strconvInt(pageSize)
		response, err := s.httpClient.requestJSONWithContext(ctx, pageURL, cookieOptions)
		if err != nil {
			return nil, err
		}
		items := dataArray(response.Payload)
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			record, ok := item.(map[string]any)
			if !ok {
				continue
			}
			channels = append(channels, parseNewAPIChannel(record))
		}
		total, hasTotal := paginationTotal(response.Payload)
		if hasTotal && page*pageSize >= total {
			break
		}
		if !hasTotal && len(items) < pageSize {
			break
		}
	}
	return channels, nil
}

// listNewAPIChannelsWithLocalFilter 兜底：分页拉取 /api/channel/ 全量 channel，
// 再在本地按「逗号分组精确匹配」过滤出属于 groupName 的 channel。
// 精确匹配：channel.group 按逗号拆分后逐段 TrimSpace 比较，避免 "vip" 命中 "vip2"（substring）。
func (s *PlatformService) listNewAPIChannelsWithLocalFilter(session Session, groupName string) ([]AdminGroupAccountInfo, error) {
	return s.listNewAPIChannelsWithLocalFilterContext(context.Background(), session, groupName)
}

func (s *PlatformService) listNewAPIChannelsWithLocalFilterContext(ctx context.Context, session Session, groupName string) ([]AdminGroupAccountInfo, error) {
	cookieOptions := newAPIAuthOptions(session)
	const pageSize = 100
	const maxPages = 100
	channels := make([]AdminGroupAccountInfo, 0)
	for page := 1; page <= maxPages; page++ {
		pageURL := session.BaseURL + "/api/channel/?p=" + strconvInt(int64(page)) + "&page_size=" + strconvInt(pageSize)
		response, err := s.httpClient.requestJSONWithContext(ctx, pageURL, cookieOptions)
		if err != nil {
			return nil, err
		}
		items := dataArray(response.Payload)
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			record, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if !channelBelongsToGroup(record, groupName) {
				continue
			}
			channels = append(channels, parseNewAPIChannel(record))
		}
		total, hasTotal := paginationTotal(response.Payload)
		if hasTotal && page*pageSize >= total {
			break
		}
		if !hasTotal && len(items) < pageSize {
			break
		}
	}
	return channels, nil
}

// channelBelongsToGroup 判断 channel 是否属于指定分组：channel.group 是逗号分隔的分组名字符串，
// 拆分后按段精确匹配，不做 substring 匹配。
func channelBelongsToGroup(record map[string]any, groupName string) bool {
	raw := firstString(record, []string{"group"})
	if raw == nil {
		return false
	}
	for _, part := range strings.Split(*raw, ",") {
		if strings.TrimSpace(part) == groupName {
			return true
		}
	}
	return false
}

// parseNewAPIChannel 把 new-api channel 原始记录解析为平台中性结构，主动丢弃 key 等敏感字段。
func parseNewAPIChannel(record map[string]any) AdminGroupAccountInfo {
	channel := AdminGroupAccountInfo{
		ID:       groupID2(record),
		Name:     safeString(record, "name"),
		Type:     stringOrNumberField(record, []string{"type"}),
		Status:   stringOrNumberField(record, []string{"status"}),
		Priority: firstInt(record, []string{"priority"}),
		Weight:   firstInt(record, []string{"weight"}),
	}
	if m := firstString(record, []string{"models"}); m != nil {
		channel.Models = *m
	}
	if b := firstString(record, []string{"base_url", "baseUrl"}); b != nil {
		channel.BaseURL = strings.TrimSpace(*b)
	}
	// channel.group 是逗号分隔的分组名字符串，拆成列表方便前端展示。
	if raw := firstString(record, []string{"group"}); raw != nil {
		parts := make([]string, 0)
		for _, part := range strings.Split(*raw, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
		channel.GroupIDs = parts
	}
	return channel
}

// stringOrNumberField 依次尝试把字段解析成字符串；不是字符串时回退按数值解析并转成字符串。
// 用于 status/type 这类上游可能返回字符串也可能返回数值枚举的字段。
func stringOrNumberField(record map[string]any, keys []string) string {
	if v := firstString(record, keys); v != nil {
		return *v
	}
	if n := firstNumber(record, keys); n != nil {
		return strconv.FormatInt(int64(*n), 10)
	}
	return ""
}

// firstInt 复用 firstNumber 读取整数字段，缺省或非法时返回 nil（区分「未提供」和「值为 0」）。
func firstInt(record map[string]any, keys []string) *int {
	if n := firstNumber(record, keys); n != nil {
		v := int(*n)
		return &v
	}
	return nil
}

// firstBoolValue 读取布尔字段，仅当上游明确给出 bool 时返回指针，否则返回 nil。
func firstBoolValue(record map[string]any, keys []string) *bool {
	for _, key := range keys {
		if b, ok := record[key].(bool); ok {
			return &b
		}
	}
	return nil
}

// parseGroupIDList 解析账号所属分组 ID 列表，兼容数值数组、字符串数组和逗号分隔字符串三种形态。
func parseGroupIDList(record map[string]any) []string {
	for _, key := range []string{"group_ids", "groupIds"} {
		value, ok := record[key]
		if !ok {
			continue
		}
		if arr, ok := value.([]any); ok {
			ids := make([]string, 0, len(arr))
			for _, item := range arr {
				switch typed := item.(type) {
				case float64:
					ids = append(ids, strconv.FormatInt(int64(typed), 10))
				case string:
					if trimmed := strings.TrimSpace(typed); trimmed != "" {
						ids = append(ids, trimmed)
					}
				}
			}
			return ids
		}
		if str, ok := value.(string); ok {
			ids := make([]string, 0)
			for _, part := range strings.Split(str, ",") {
				if trimmed := strings.TrimSpace(part); trimmed != "" {
					ids = append(ids, trimmed)
				}
			}
			return ids
		}
	}
	return nil
}
