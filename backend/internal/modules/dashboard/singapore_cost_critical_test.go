package dashboard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

func TestSingaporeCostCriticalPGEndedQuotaDoesNotBlockAbsentCurrentCost(t *testing.T) {
	for _, scenario := range []string{"active", "active-manual-split", "ended-no-current-cost", "ended-current-cost", "ended-zero-cost", "future-manual-mode"} {
		t.Run(scenario, func(t *testing.T) {
			repo, pool, asset, _, now := singaporeAccountRepository(t)
			date := businesstime.DateAt(now)
			ctx := t.Context()
			if _, err := pool.Exec(ctx, `UPDATE dashboard_account_assets SET recognition_mode='quota',quota_total_micros=100 WHERE id=$1`, asset.ID); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(scenario, "ended-") {
				if _, err := pool.Exec(ctx, `INSERT INTO dashboard_account_events(id,user_id,admin_account_id,account_asset_id,event_type,effective_date,status,idempotency_key) VALUES('ended','user-1','workspace-1',$1,'status','2031-02-02','closed','ended')`, asset.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `UPDATE dashboard_account_assets SET current_status='closed' WHERE id=$1`, asset.ID); err != nil {
					t.Fatal(err)
				}
			}
			hasRecord := scenario == "ended-current-cost" || scenario == "ended-zero-cost" || scenario == "future-manual-mode"
			amount := 123
			if scenario == "ended-zero-cost" {
				amount = 0
			}
			if hasRecord {
				if _, err := pool.Exec(ctx, `INSERT INTO dashboard_additional_costs(id,user_id,admin_account_id,type,name,business_date,amount_cents,source_id,batch_id,account_asset_id) VALUES('current','user-1','workspace-1','account_purchase','原额度金额',$1::date,$3,'quota','batch',$2)`, date, asset.ID, amount); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "future-manual-mode" {
				if _, err := pool.Exec(ctx, `INSERT INTO dashboard_account_events(id,user_id,admin_account_id,account_asset_id,event_type,effective_date,stats_mode,idempotency_key) VALUES('mode','user-1','workspace-1',$1,'stats_mode_change','2031-02-04','manual','mode')`, asset.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `UPDATE dashboard_account_assets SET stats_mode='manual' WHERE id=$1`, asset.ID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "active-manual-split" {
				if _, err := pool.Exec(ctx, `UPDATE dashboard_account_links SET effective_from=$1::date,manual_same_day_split=true WHERE account_asset_id=$2`, date, asset.ID); err != nil {
					t.Fatal(err)
				}
			}
			invalid, err := repo.UnconfirmedQuotaRecognitionAssets(ctx, "user-1", "workspace-1", date, date)
			if err != nil {
				t.Fatal(err)
			}
			wantMissing := scenario != "ended-no-current-cost"
			if invalid[asset.ID] != wantMissing {
				t.Fatalf("当前费用确认必须按当天状态与实际保留费用判断 scenario=%s got=%v want=%v", scenario, invalid[asset.ID], wantMissing)
			}
			if hasRecord {
				ledger, err := repo.ListAccountCostLedger(ctx, "user-1", "workspace-1", AccountCostLedgerFilter{CurrentDate: date})
				if err != nil || len(ledger.Items) != 1 || ledger.Items[0].AmountCents != int64(amount) || !strings.Contains(ledger.Items[0].Note, "尚未按新加坡日期重新确认") {
					t.Fatalf("旧自动额度记录包括零须保留金额并说明尚未重新确认 scenario=%s ledger=%+v err=%v", scenario, ledger, err)
				}
			}
		})
	}
}

func TestSingaporeCostCriticalPGHistoricalRecoveryCanFillAndComplete(t *testing.T) {
	for _, scenario := range []string{"missing-row", "missing-quality"} {
		t.Run(scenario, func(t *testing.T) {
			repo, pool, asset, _, now := singaporeAccountRepository(t)
			ctx := withAccountCurrentDate(t.Context(), businesstime.DateAt(now))
			if scenario == "missing-quality" {
				if _, err := pool.Exec(ctx, `INSERT INTO dashboard_account_daily_stats(id,user_id,admin_account_id,account_asset_id,business_date,source,quality) VALUES('old','user-1','workspace-1',$1,'2031-02-02','automatic','missing')`, asset.ID); err != nil {
					t.Fatal(err)
				}
			}
			qty, revenue := int64(5), int64(100)
			stat := AccountDailyStat{ID: "history", UserID: "user-1", AdminAccountID: "workspace-1", AccountAssetID: asset.ID, BusinessDate: "2031-02-02", Source: StatsModeAutomatic, Quality: KeyCostQualityComplete, RawQuotaUsedMicros: &qty, RevenueCents: &revenue, KeyCostRunID: "historical-run", CreatedAt: now, UpdatedAt: now}
			if err := repo.SaveAutomaticAccountDailyStats(ctx, []AccountDailyStat{stat}); err != nil {
				t.Fatalf("原历史目标日允许缺行补齐或missing恢复完整：%v", err)
			}
			var quality string
			var actual int64
			if err := pool.QueryRow(ctx, `SELECT quality,revenue_cents FROM dashboard_account_daily_stats WHERE account_asset_id=$1 AND business_date='2031-02-02' AND source='automatic'`, asset.ID).Scan(&quality, &actual); err != nil || quality != KeyCostQualityComplete || actual != revenue {
				t.Fatalf("历史恢复未保存原业务结果 quality=%s revenue=%d err=%v", quality, actual, err)
			}
		})
	}
}

type singaporeDailyStatsLister struct {
	*fakeUpstreamLister
	calls int
}

func (l *singaporeDailyStatsLister) List(ctx context.Context, userID string) []upstream.Response {
	l.calls++
	return l.fakeUpstreamLister.List(ctx, userID)
}

func TestSingaporeCostCriticalDailyStatsDetailsMatchCurrentSummary(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	for _, state := range []string{"fresh", "stale", "retained"} {
		t.Run(state, func(t *testing.T) {
			at := now.Add(-time.Minute)
			if state == "stale" {
				at = now.Add(-3 * time.Hour)
			}
			m := upstream.Metrics{TodayConsume: upstream.MetricValue{Value: ptrFloat64(9)}, TodayConsumeDate: date, TodayConsumeTimezone: businesstime.Timezone, TodayConsumeAt: &at}
			if state == "retained" {
				m.TodayConsumeStatus = "unreadable"
				m.TodayConsumeErrorDetail = "读取独立总额超时；本轮费用未更新，可同步这个站点重试，不会修改主站。"
				m.TodayConsumeFailedAt = &now
			}
			lister := &singaporeDailyStatsLister{fakeUpstreamLister: &fakeUpstreamLister{cachedSites: []upstream.Response{{ID: "site", Name: "原账号", Platform: upstream.PlatformSub2API, Status: upstream.StatusConnected, RechargeRate: 2, Metrics: m}}}}
			repo := &fakeMetricsRepository{dailyStatsSnapshots: []DailySnapshot{{ID: "today", Date: now, TodayProfit: ptrFloat64(30), TodayPurchase: ptrFloat64(18)}}}
			svc := newLiveMetricsTestService(&fakePlatformClient{}, lister.fakeUpstreamLister, repo)
			svc.upstreams = lister
			svc.now = func() time.Time { return now }
			items, err := svc.DailyStats(t.Context(), "user-1", date, date, 1, 31, true)
			if err != nil || len(items) != 1 || len(items[0].SiteCosts) != 1 {
				t.Fatalf("当日展开入口：items=%+v err=%v", items, err)
			}
			detail := items[0].SiteCosts[0]
			if state == "stale" {
				if items[0].ConfirmedCost != nil || detail.AdjustedCost != nil || detail.Status != "missing" {
					t.Fatalf("过期费用不可在展开中变可用：summary=%v detail=%+v", items[0].ConfirmedCost, detail)
				}
			} else {
				if items[0].ConfirmedCost == nil || *items[0].ConfirmedCost != 18 || detail.AdjustedCost == nil || *detail.AdjustedCost != 18 {
					t.Fatalf("可用费用合计与展开不一致：%+v", items[0])
				}
			}
			if state == "retained" && (detail.LastAttemptError != m.TodayConsumeErrorDetail || detail.LastAttemptStatus == "" || detail.ObservedAt == "") {
				t.Fatalf("沿用必须保留实际失败与截至：%+v", detail)
			}
			if lister.calls != 1 {
				t.Fatalf("同响应汇总和展开须复用同次站点读取，实际%d次", lister.calls)
			}
		})
	}
}

// 主代理固定真实旧入口业务RED；实现者不得改变这些业务期望。
func TestSingaporeCostCriticalDashboardRejectsLegacyLiveCost(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	for _, history := range []bool{false, true} {
		m := upstream.Metrics{TodayConsume: upstream.MetricValue{Value: ptrFloat64(9)}, TodayConsumeDate: date, TodayConsumeAt: &now}
		site := upstream.Response{ID: "site", Name: "原账号", Status: upstream.StatusConnected, RechargeRate: 2, Metrics: m}
		h := map[string]SiteDailyCost{}
		if history {
			h["site"] = SiteDailyCost{SiteID: "site", AdjustedCost: ptrFloat64(18), ObservedAt: &now}
		}
		total, q := summarizeCachedUpstreamCostsWithHistoryAt([]upstream.Response{site}, date, 0, h, now)
		if total != 0 || q.Complete || q.Mode != "unavailable" || q.CollectedSites != 0 {
			t.Fatalf("旧当前费用/历史回退被确认为SG费用 history=%v total=%v quality=%+v", history, total, q)
		}
	}
}

func TestSingaporeCostCriticalDashboardCertifiedRetention(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	var m upstream.Metrics
	body, _ := json.Marshal(map[string]any{"todayConsume": map[string]any{"value": 3}, "todayConsumeDate": date, "todayConsumeAt": now, "todayConsumeTimezone": "Asia/Singapore", "todayConsumeStatus": "unreadable"})
	_ = json.Unmarshal(body, &m)
	total, q := summarizeCachedUpstreamCostsWithHistoryAt([]upstream.Response{{ID: "site", Status: upstream.StatusConnected, RechargeRate: 2, Metrics: m}}, date, 0, nil, now)
	if total != 6 || q.Mode != "retained" || !q.Complete {
		t.Fatalf("SG同日合格费用应沿用：%v %+v", total, q)
	}
}

func TestSingaporeCostCriticalPublishedRunCannotUseLegacyOrRange(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	for _, certified := range []bool{false, true} {
		var keys upstream.KeyUsageForDateResult
		tz := ""
		if certified {
			tz = "Asia/Singapore"
		}
		body, _ := json.Marshal(map[string]any{"businessDate": date, "expectedSites": 1, "completedSites": 1, "sites": []any{map[string]any{"siteId": "site", "complete": true, "timezone": tz, "consumeDate": date, "collectedAt": now, "syncedRawCost": 1, "collectedRawCost": 9, "items": []any{map[string]any{"siteId": "site", "keyId": "1", "rawAmount": 9, "todayAmount": 9}}}}})
		if err := json.Unmarshal(body, &keys); err != nil {
			t.Fatal(err)
		}
		runs := buildAccountKeyCostRuns("user", "workspace", "run", date, []upstream.SiteCostForDateResult{{SiteID: "site", RawCost: 5, RechargeRate: 1}}, keys, now)
		if len(runs) != 1 || runs[0].Complete {
			t.Fatalf("旧来源或同步-采集范围不可代替独立总额5：certified=%v runs=%+v", certified, runs)
		}
	}
}

func TestSingaporeCostCriticalLiveMetricsDoesNotRecoverLegacyCost(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	date := businesstime.DateAt(now)
	for _, scenario := range []string{"old-cache", "stored-site-history", "old-dashboard-snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			site := upstream.Response{ID: "site", Name: "原账号", Status: upstream.StatusConnected, RechargeRate: 2, Metrics: upstream.Metrics{TodayConsumeDate: date, TodayConsumeAt: &now}}
			repo := &fakeMetricsRepository{}
			switch scenario {
			case "old-cache":
				site.Metrics.TodayConsume.Value = ptrFloat64(9)
			case "stored-site-history":
				site.Status = upstream.StatusError
				repo.latestSiteCosts = []SiteDailyCost{{SiteID: site.ID, AdjustedCost: ptrFloat64(18), ObservedAt: &now}}
			case "old-dashboard-snapshot":
				repo.latestSnapshot = &DailySnapshot{TodayPurchase: ptrFloat64(18), NetProfit: ptrFloat64(12), OperatingCost: ptrFloat64(18), AdjustedNetProfit: ptrFloat64(12)}
			}
			svc := newLiveMetricsTestService(&fakePlatformClient{usageStats: 30}, &fakeUpstreamLister{cachedSites: []upstream.Response{site}}, repo)
			svc.now = func() time.Time { return now }
			r, err := svc.LiveMetrics(t.Context(), "user-1")
			if err != nil {
				t.Fatal(err)
			}
			if r.TodayPurchase != nil || r.NetProfit != nil || r.CostQuality == nil || r.CostQuality.Complete || r.CostQuality.Mode != "unavailable" {
				t.Fatalf("当前首页费用不能用旧记录恢复 scenario=%s purchase=%v profit=%v quality=%+v", scenario, r.TodayPurchase, r.NetProfit, r.CostQuality)
			}
			if r.TodayProfit == nil || *r.TodayProfit != 30 {
				t.Fatal("成本暂缺不应丢失已取得营收")
			}
		})
	}
}
