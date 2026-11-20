package dashboard

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

var ErrOperatingInsightsUnavailable = errors.New("经营提示所需的本地只读服务当前不可用；请检查本站数据库、Redis和服务启动日志后重试。")

type operatingScopeVerifier interface {
	verifyOperatingScope(context.Context, workspacescope.Scope) error
}

type operatingBalanceReader interface {
	BalanceSnapshotForWorkspace(context.Context, string, string) ([]upstream.BalanceSnapshot, error)
}

type operatingReadError struct {
	Step   string
	Object string
	Cause  error
}

func (e *operatingReadError) Error() string {
	return "读取" + e.Step + "未完成；请核查本站已有资料后重试。"
}
func (e *operatingReadError) Unwrap() error { return e.Cause }

// Only this new read path checks scope before and after its multiple original readers.
// No locks, writes, migrations or alternative data source are introduced.
func (r *MetricsRepository) verifyOperatingScope(ctx context.Context, scope workspacescope.Scope) error {
	if r == nil || r.db == nil {
		return ErrOperatingInsightsUnavailable
	}
	if pool, ok := r.db.(*pgxpool.Pool); ok && pool == nil {
		return ErrOperatingInsightsUnavailable
	}
	return workspacescope.Resolve(ctx, r.db, scope)
}

func operatingShift(day string, days int) string {
	start, _, _ := businesstime.Bounds(day)
	return start.AddDate(0, 0, days).Format("2006-01-02")
}

func finiteOperating(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0)
}

// OperatingInsightsForWorkspace reads existing closed-day rows and the original site cache.
// It never calls the live metrics, settlement, account refresh or upstream request paths.
func (s *MetricsService) OperatingInsightsForWorkspace(ctx context.Context, scope workspacescope.Scope) (OperatingInsights, error) {
	if s == nil || s.metricsRepo == nil {
		return OperatingInsights{}, ErrOperatingInsightsUnavailable
	}
	verifier, ok := s.metricsRepo.(operatingScopeVerifier)
	if !ok {
		return OperatingInsights{}, ErrOperatingInsightsUnavailable
	}
	balances, ok := s.upstreams.(operatingBalanceReader)
	if !ok {
		return OperatingInsights{}, ErrOperatingInsightsUnavailable
	}
	if err := verifier.verifyOperatingScope(ctx, scope); err != nil {
		return OperatingInsights{}, err
	}
	today := businesstime.DateAt(s.currentTime())
	span := OperatingInsightsRange{From: operatingShift(today, -7), To: operatingShift(today, -1), Days: 7}
	result := OperatingInsights{
		WorkspaceAdminAccountID: scope.AdminAccountID, WorkspaceCreatedAt: scope.CreatedAt, Range: span,
		MissingDates: []string{}, SiteEstimates: []OperatingSiteEstimate{}, Warnings: []string{},
	}
	snapshots, err := s.metricsRepo.ListDailyStats(ctx, scope.UserID, scope.AdminAccountID, span.From, span.To)
	if err != nil {
		return OperatingInsights{}, &operatingReadError{Step: "近7日原结算", Object: scope.AdminAccountID, Cause: err}
	}
	costs := map[string]map[string][]SiteDailyCost{}
	for day := span.From; day <= span.To; day = operatingShift(day, 1) {
		rows, err := s.metricsRepo.ListSiteCosts(ctx, scope.UserID, scope.AdminAccountID, day)
		if err != nil {
			return OperatingInsights{}, &operatingReadError{Step: "逐日原始成本", Object: day, Cause: err}
		}
		bySite := map[string][]SiteDailyCost{}
		for _, row := range rows {
			bySite[row.SiteID] = append(bySite[row.SiteID], row)
		}
		costs[day] = bySite
	}
	sites, err := balances.BalanceSnapshotForWorkspace(ctx, scope.UserID, scope.AdminAccountID)
	if err != nil {
		return OperatingInsights{}, &operatingReadError{Step: "原上游站点余额缓存", Object: scope.AdminAccountID, Cause: err}
	}
	result.SnapshotTotals, result.MissingDates, result.Warnings = operatingSnapshotTotals(snapshots, span)
	result.ObservedAt = s.currentTime()
	for _, site := range sites {
		result.SiteEstimates = append(result.SiteEstimates, operatingSiteEstimate(site, costs, span, result.ObservedAt))
	}
	// A selection change or delete/recreate during the reads discards all gathered values.
	if err := verifier.verifyOperatingScope(ctx, scope); err != nil {
		return OperatingInsights{}, err
	}
	return result, nil
}

func operatingSnapshotTotals(rows []DailySnapshot, span OperatingInsightsRange) (*OperatingSnapshotTotals, []string, []string) {
	byDay := map[string]DailySnapshot{}
	duplicates := map[string]bool{}
	for _, row := range rows {
		day := row.Date.Format("2006-01-02")
		if _, exists := byDay[day]; exists {
			duplicates[day] = true
		}
		byDay[day] = row
	}
	missing := []string{}
	warnings := []string{}
	totals := OperatingSnapshotTotals{}
	for day := span.From; day <= span.To; day = operatingShift(day, 1) {
		row, exists := byDay[day]
		reason := ""
		switch {
		case !exists:
			reason = "没有原结算记录"
		case duplicates[day]:
			reason = "原结算记录重复，无法确定唯一值"
		case row.SettlementStatus != "final":
			reason = "原结算尚未完成"
		case row.CostQualityMode != "exact" && row.CostQualityMode != "retained":
			reason = "原成本资料不完整或尚未核实"
		case !finiteOperating(row.TodayProfit):
			reason = "原营收缺失或不是有限数值"
		case !finiteOperating(row.TodayPurchase):
			reason = "原总成本缺失或不是有限数值"
		case !finiteOperating(row.OperatingCost):
			reason = "原经营成本缺失或不是有限数值"
		case !finiteOperating(row.AdjustedNetProfit):
			reason = "原调整后净利润缺失或不是有限数值"
		}
		if reason != "" {
			missing = append(missing, day)
			warnings = append(warnings, day+"："+reason+"；请到原逐日明细核查，本区暂不汇总部分金额。")
			continue
		}
		totals.TodayProfit += *row.TodayProfit
		totals.TodayPurchase += *row.TodayPurchase
		totals.OperatingCost += *row.OperatingCost
		totals.AdjustedNetProfit += *row.AdjustedNetProfit
	}
	if len(missing) > 0 {
		return nil, missing, warnings
	}
	if !finiteOperating(&totals.TodayProfit) || !finiteOperating(&totals.TodayPurchase) ||
		!finiteOperating(&totals.OperatingCost) || !finiteOperating(&totals.AdjustedNetProfit) {
		warnings = append(warnings, "7日金额合计超出可显示范围，暂不能汇总；请核查原逐日结算金额。")
		return nil, missing, warnings
	}
	if totals.TodayProfit > 0 {
		margin := totals.AdjustedNetProfit / totals.TodayProfit
		if finiteOperating(&margin) {
			totals.ProfitMargin = &margin
		} else {
			warnings = append(warnings, "7日利润率超出可显示范围，暂不显示比例；请核查原营收和调整后净利润。")
		}
	}
	return &totals, missing, warnings
}

func operatingCostReason(row SiteDailyCost) string {
	switch {
	case !finiteOperating(row.RawCost):
		return "原始成本缺失或不是有限数值"
	case *row.RawCost < 0:
		return "原始成本为负，不能作为消耗速度"
	case row.Status != "ok":
		return "这天的原始成本没有完整核实"
	case row.Source != "dated_query" && row.Source != "backfill":
		return "这天的成本不是按业务日期确认的资料"
	case row.ObservedAt == nil || row.ObservedAt.IsZero():
		return "这天的成本缺少原观察时间"
	}
	return ""
}

func operatingSiteEstimate(site upstream.BalanceSnapshot, costs map[string]map[string][]SiteDailyCost, span OperatingInsightsRange, now time.Time) OperatingSiteEstimate {
	result := OperatingSiteEstimate{SiteID: site.SiteID, Name: site.Name, Enabled: site.Enabled, Status: string(site.Status), MissingDates: []string{}, Reasons: []string{}}
	if finiteOperating(site.RawBalance) {
		balance := *site.RawBalance
		result.RawBalance = &balance
	} else {
		result.Reasons = append(result.Reasons, "原缓存余额缺失或不是有限数值；请核查原上游站点同步结果，本区不会补采。")
	}
	if site.LastSyncedAt != nil {
		observed := time.UnixMilli(*site.LastSyncedAt)
		if observed.Year() >= 1 && observed.Year() <= 9999 {
			result.LastSyncedAt = &observed
		}
	}
	balanceUsable := result.RawBalance != nil
	switch {
	case site.Status == upstream.StatusConnecting:
		result.Reasons = append(result.Reasons, "站点仍在连接，原缓存余额暂不用于估计；请先到上游站点查看连接结果。")
		balanceUsable = false
	case site.Status == upstream.StatusError:
		result.Reasons = append(result.Reasons, "站点当前同步报错，原缓存余额暂不用于估计；请到上游站点核查具体错误。")
		balanceUsable = false
	}
	switch {
	case result.LastSyncedAt == nil && site.LastSyncedAt != nil:
		result.Reasons = append(result.Reasons, "原同步观察时间超出有效日期范围，暂不使用这份余额；请核查原站点缓存和服务时钟。")
		balanceUsable = false
	case result.LastSyncedAt == nil:
		result.Reasons = append(result.Reasons, "没有原同步观察时间，无法判断余额新旧；请核查上游站点资料。")
		balanceUsable = false
	case result.LastSyncedAt.After(now):
		result.Reasons = append(result.Reasons, "原同步观察时间晚于当前时间，暂不使用这份余额；请核查服务时钟。")
		balanceUsable = false
	case now.Sub(*result.LastSyncedAt) > 2*time.Hour:
		result.Reasons = append(result.Reasons, "原缓存余额已超过2小时，暂不用于估计；请到上游站点核查并按需手动同步。")
		balanceUsable = false
	}
	average := float64(0)
	confirmedDays := 0
	for day := span.From; day <= span.To; day = operatingShift(day, 1) {
		rows := costs[day][site.SiteID]
		var found *SiteDailyCost
		if len(rows) > 0 {
			found = &rows[0]
		}
		reason := ""
		switch {
		case found == nil:
			reason = "没有这天的原始成本"
		case len(rows) > 1:
			reason = "这天的原始成本重复，无法确定唯一值"
		case found.Date.Format("2006-01-02") != day:
			reason = "原始成本的日期与所查日期不一致"
		default:
			reason = operatingCostReason(*found)
		}
		if reason != "" {
			result.MissingDates = append(result.MissingDates, day)
			result.Reasons = append(result.Reasons, day+"："+reason+"；请到原逐日成本明细核查。")
			continue
		}
		confirmedDays++
		// The online mean avoids overflowing a valid seven-day mean or rounding each tiny cost to zero.
		average += (*found.RawCost - average) / float64(confirmedDays)
	}
	if len(result.MissingDates) == 0 {
		if finiteOperating(&average) {
			result.AverageRawCost = &average
			if average <= 0 {
				result.Reasons = append(result.Reasons, "近7日原始成本日均为0，没有可用的消耗速度，暂不能估计天数。")
			}
		} else {
			result.Reasons = append(result.Reasons, "近7日原始成本日均超出可显示范围，暂不能估计；请核查原成本金额。")
		}
	}
	if balanceUsable && result.AverageRawCost != nil && average > 0 {
		days := *result.RawBalance / average
		if finiteOperating(&days) {
			result.DaysLeft = &days
			if site.Enabled && days <= 2 {
				suggested := math.Max(7*average-*result.RawBalance, 0)
				if finiteOperating(&suggested) {
					result.SuggestedTopUp = &suggested
				} else {
					result.Reasons = append(result.Reasons, "建议补充量超出可显示范围，暂不提供金额；请核查原始余额和成本。")
				}
			}
		} else {
			result.Reasons = append(result.Reasons, "原始余额与日均成本的比值超出可显示范围，暂不能估计天数。")
		}
	}
	if !site.Enabled {
		result.Reasons = append(result.Reasons, "站点已停用，仅展示已有资料，不建议补款。")
	}
	return result
}
