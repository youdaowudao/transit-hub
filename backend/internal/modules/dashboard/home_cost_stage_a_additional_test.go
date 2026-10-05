package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/businesstime"
)

type stageASnapshotUpstreams struct {
	*fakeUpstreamLister
	result                 upstream.KeyUsageForDateResult
	cachedCalls, syncCalls int
}

func (f *stageASnapshotUpstreams) CachedKeyUsageForDate(context.Context, string, string, string) (upstream.KeyUsageForDateResult, error) {
	f.cachedCalls++
	return f.result, nil
}
func (f *stageASnapshotUpstreams) KeyUsageForDate(context.Context, string, string, string) (upstream.KeyUsageForDateResult, error) {
	f.keyUsageCalls++
	return f.result, nil
}
func (f *stageASnapshotUpstreams) SyncAndCollectKeyUsage(context.Context, string, string, string, time.Time) (upstream.KeyUsageForDateResult, error) {
	f.syncCalls++
	return f.result, nil
}

func stageASnapshotResult(t *testing.T, value map[string]any) upstream.KeyUsageForDateResult {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result upstream.KeyUsageForDateResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestHomeCostStageADrilldownUsesSnapshotStatesAndNoLiveRequests(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := businesstime.DateAt(now)
	upstreams := &stageASnapshotUpstreams{fakeUpstreamLister: &fakeUpstreamLister{}, result: stageASnapshotResult(t, map[string]any{
		"BusinessDate": date, "ExpectedSites": 3, "CompletedSites": 2, "AutoRefreshEnabled": false,
		"Sites": []map[string]any{
			{"SiteID": "ok", "SiteName": "ok", "Complete": true, "Status": "ok", "CollectedAt": now, "Items": []upstream.KeyUsageTodayItem{{SiteID: "ok", KeyID: "a", TodayAmount: 3}}},
			{"SiteID": "retained", "SiteName": "retained", "Complete": true, "Status": "retained", "CollectedAt": now.Add(-time.Hour), "Error": upstream.ErrorRequest, "Items": []upstream.KeyUsageTodayItem{{SiteID: "retained", KeyID: "b", TodayAmount: 4}}},
			{"SiteID": "missing", "Status": "missing", "Error": upstream.ErrorRequest},
		},
	})}
	service := NewMetricsService(nil, nil, upstreams, nil, nil)
	service.now = func() time.Time { return now }
	response, err := service.upstreamKeyUsageTodayForDate(context.Background(), "user", date, false)
	if err != nil {
		t.Fatal(err)
	}
	if response.Total != 7 || response.FailedSites != 1 || response.TotalSites != 3 || upstreams.keyUsageCalls != 0 || upstreams.cachedCalls != 1 {
		t.Fatalf("snapshot response=%#v live=%d cache=%d", response, upstreams.keyUsageCalls, upstreams.cachedCalls)
	}
	data, _ := json.Marshal(response)
	var decoded map[string]any
	_ = json.Unmarshal(data, &decoded)
	sites, ok := decoded["sites"].([]any)
	if !ok || len(sites) != 3 {
		t.Fatalf("missing snapshot states: %s", data)
	}
	for i, status := range []string{"ok", "retained", "missing"} {
		if sites[i].(map[string]any)["status"] != status {
			t.Fatalf("site state=%#v", sites[i])
		}
	}
}

func TestHomeCostStageALiveCostIgnoresPublishedAccountSnapshot(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	repository := &fakeMetricsRepository{latestSnapshot: &DailySnapshot{TodayPurchase: ptrF64(9), AccountSnapshotRunID: "published", AccountStatsQuality: KeyCostQualityComplete, CostExpectedCount: intPtr(1), CostCollectedCount: intPtr(1), CostFreshCount: intPtr(1), CostRetainedCount: intPtr(0), CostMissingCount: intPtr(0), CostQualityMode: "exact"}}
	metrics := upstream.Metrics{TodayConsume: upstream.MetricValue{Value: ptrF64(3)}, TodayConsumeDate: businesstime.DateAt(now), TodayConsumeAt: &now}
	service := newLiveMetricsTestService(&fakePlatformClient{usageStats: 20}, &fakeUpstreamLister{cachedSites: []upstream.Response{{ID: "site", Status: upstream.StatusConnected, RechargeRate: 1, Metrics: metrics}}}, repository)
	service.now = func() time.Time { return now }
	result, err := service.LiveMetrics(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.TodayPurchase == nil || *result.TodayPurchase != 3 || result.CostQuality.ConfirmedCost != 3 {
		t.Fatalf("published account run replaced current sync cost: %#v", result)
	}
}

func TestHomeCostStageARefreshUsesSyncSnapshotInsteadOfLiveQueries(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := businesstime.DateAt(now)
	upstreams := &stageASnapshotUpstreams{fakeUpstreamLister: &fakeUpstreamLister{siteCostResults: []upstream.SiteCostForDateResult{{SiteID: "site", RechargeRate: 1, RawCost: 1}}}, result: stageASnapshotResult(t, map[string]any{
		"BusinessDate": date, "ExpectedSites": 1, "CompletedSites": 1, "Sites": []map[string]any{{"SiteID": "site", "RechargeRate": 1, "Complete": true, "Status": "ok", "StartedAt": now.Add(time.Minute), "CollectedAt": now.Add(time.Minute), "SyncedRawCost": 1, "CollectedRawCost": 1, "ConsumeDate": date, "Items": []upstream.KeyUsageTodayItem{{SiteID: "site", KeyID: "key", RawAmount: 1, TodayAmount: 1, RechargeRate: 1}}}},
	})}
	repository := &fakeAccountKeyRunRepository{}
	service := NewMetricsService(nil, nil, upstreams, nil, &fakeAdminAccounts{current: map[string]string{"user": "workspace"}})
	service.keyCostRuns = repository
	service.now = func() time.Time { return now }
	result, err := service.RefreshAccountStats(context.Background(), "user", date, "same-batch")
	if err != nil {
		t.Fatal(err)
	}
	if upstreams.syncCalls != 1 || upstreams.keyUsageCalls != 0 || result.Quality != KeyCostQualityComplete || !repository.published {
		t.Fatalf("refresh=%#v sync=%d live=%d published=%v", result, upstreams.syncCalls, upstreams.keyUsageCalls, repository.published)
	}
}

type stageABlockedSnapshotUpstreams struct {
	*stageASnapshotUpstreams
	release chan struct{}
}

func (f *stageABlockedSnapshotUpstreams) SyncAndCollectKeyUsage(context.Context, string, string, string, time.Time) (upstream.KeyUsageForDateResult, error) {
	<-f.release
	return f.result, nil
}

func TestHomeCostStageARefreshTimeoutKeepsBackgroundJobRunning(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := businesstime.DateAt(now)
	base := &stageASnapshotUpstreams{fakeUpstreamLister: &fakeUpstreamLister{}, result: stageASnapshotResult(t, map[string]any{"BusinessDate": date, "ExpectedSites": 1, "CompletedSites": 1, "Sites": []map[string]any{{"SiteID": "site", "RechargeRate": 1, "Complete": true, "Status": "ok", "StartedAt": now.Add(time.Second), "ConsumeDate": date, "SyncedRawCost": 1, "CollectedRawCost": 1, "Items": []upstream.KeyUsageTodayItem{{SiteID: "site", KeyID: "key", RawAmount: 1, TodayAmount: 1}}}}})}
	upstreams := &stageABlockedSnapshotUpstreams{stageASnapshotUpstreams: base, release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(upstreams.release)
		}
	}()
	repository := &fakeAccountKeyRunRepository{}
	service := NewMetricsService(nil, nil, upstreams, nil, &fakeAdminAccounts{current: map[string]string{"user": "workspace"}})
	service.keyCostRuns = repository
	service.now = func() time.Time { return now }
	service.accountRefreshWait = time.Millisecond
	result, err := service.RefreshAccountStats(context.Background(), "user", date, "timeout")
	if err != nil || result.Status != "in_progress" {
		t.Fatalf("timeout response=%#v err=%v", result, err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/dashboard/account-stats/refresh", strings.NewReader(`{"date":"`+date+`"}`))
	request = request.WithContext(authctx.WithUserID(context.Background(), "user"))
	request.Header.Set("Idempotency-Key", "timeout")
	response := httptest.NewRecorder()
	handler := &Handler{metricsService: service}
	handler.refreshAccountStats(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"status":"in_progress"`) {
		t.Fatalf("HTTP wait response=%d %s", response.Code, response.Body.String())
	}
	service.accountRefreshMu.Lock()
	job := service.accountRefreshJobs[accountRefreshRunID("user", "workspace", date, "timeout")]
	service.accountRefreshMu.Unlock()
	if job == nil {
		t.Fatal("background job disappeared while collection is blocked")
	}
	close(upstreams.release)
	released = true
	select {
	case <-job.done:
	case <-time.After(time.Second):
		t.Fatal("background job did not finish")
	}
	if job.err != nil || !repository.published || job.result.Quality != KeyCostQualityComplete {
		t.Fatalf("background result=%#v err=%v published=%v", job.result, job.err, repository.published)
	}
}

func TestHomeCostStageARefreshExcludesOldAndSyncFailedSnapshots(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := businesstime.DateAt(now)
	one := 1.0
	upstreams := &stageASnapshotUpstreams{fakeUpstreamLister: &fakeUpstreamLister{}, result: upstream.KeyUsageForDateResult{BusinessDate: date, ExpectedSites: 3, CompletedSites: 2, Sites: []upstream.KeyUsageSiteResult{
		{SiteID: "current", RechargeRate: 1, Complete: true, Status: "ok", StartedAt: now.Add(time.Second), SyncedRawCost: &one, CollectedRawCost: &one, ConsumeDate: date, Items: []upstream.KeyUsageTodayItem{{SiteID: "current", KeyID: "one", TodayAmount: 1}}},
		{SiteID: "old", RechargeRate: 1, Complete: true, Status: "retained", StartedAt: now.Add(-time.Second), SyncedRawCost: &one, CollectedRawCost: &one, ConsumeDate: date, Items: []upstream.KeyUsageTodayItem{{SiteID: "old", KeyID: "two", TodayAmount: 1}}},
		{SiteID: "sync-failed", RechargeRate: 1, Status: "missing", Error: upstream.ErrorRequest},
	}}}
	repository := &fakeAccountKeyRunRepository{}
	service := NewMetricsService(nil, nil, upstreams, nil, &fakeAdminAccounts{current: map[string]string{"user": "workspace"}})
	service.keyCostRuns = repository
	service.now = func() time.Time { return now }
	response, err := service.RefreshAccountStats(context.Background(), "user", date, "fresh-only")
	if err != nil || response.ExpectedSites != 3 || response.CompletedSites != 1 || response.Quality != KeyCostQualityMissing || repository.published || upstreams.keyUsageCalls != 0 {
		t.Fatalf("fresh-only refresh=%#v err=%v published=%v live=%d", response, err, repository.published, upstreams.keyUsageCalls)
	}
}

func TestHomeCostStageAGroupProfitReadsSnapshotsAndCannotSplitMergedKeys(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := businesstime.DateAt(now)
	fast, early := now.Add(-time.Minute), now.Add(-time.Hour)
	upstreams := &stageASnapshotUpstreams{fakeUpstreamLister: &fakeUpstreamLister{}, result: upstream.KeyUsageForDateResult{BusinessDate: date, ExpectedSites: 2, CompletedSites: 2, Sites: []upstream.KeyUsageSiteResult{
		{SiteID: "fast", Complete: true, Status: "ok", CollectedAt: &fast, Items: []upstream.KeyUsageTodayItem{{SiteID: "fast", KeyID: "one", TodayAmount: 4}, {SiteID: "fast", KeyID: "merged", KeyIDs: []string{"merged", "merged-two"}, Merged: true, TodayAmount: 8}}},
		{SiteID: "early", Complete: true, Status: "retained", CollectedAt: &early, Items: []upstream.KeyUsageTodayItem{{SiteID: "early", KeyID: "two", TodayAmount: 3}}},
	}}}
	service := NewMetricsService(nil, &fakePlatformClient{scopeUsage: map[string]upstream.AdminUsageStats{"admin-one|g1": {TotalActualCost: 10}, "admin-two|g2": {TotalActualCost: 10}, "admin-three|g3": {TotalActualCost: 10}}}, upstreams, &fakeMetricsRepository{}, &fakeAdminAccounts{current: map[string]string{"user": "workspace"}})
	service.now = func() time.Time { return now }
	service.realConnections = fakeRealConnectionReader{connections: []my_sites.RealConnection{
		{ID: "one", Status: "active", UpstreamSiteID: "fast", UpstreamKeyID: "one", AdminAccountID: "admin-one", OwnGroupIDs: []string{"g1"}},
		{ID: "two", Status: "active", UpstreamSiteID: "early", UpstreamKeyID: "two", AdminAccountID: "admin-two", OwnGroupIDs: []string{"g2"}},
		{ID: "three", Status: "active", UpstreamSiteID: "fast", UpstreamKeyID: "merged", AdminAccountID: "admin-three", OwnGroupIDs: []string{"g3"}},
	}}
	response, err := service.realGroupProfitToday(context.Background(), "user", "workspace", upstream.Session{}, date)
	if err != nil || response.TotalProfit != 13 || response.UnavailableGroups != 1 || response.UnsplittableGroups != 1 || response.CollectedAt == nil || !response.CollectedAt.Equal(early) || upstreams.keyUsageCalls != 0 {
		t.Fatalf("group snapshots=%#v err=%v live=%d", response, err, upstreams.keyUsageCalls)
	}
	for _, group := range response.Groups {
		if group.GroupID == "g3" {
			t.Fatal("merged group was attributed a numeric cost/profit")
		}
	}
}

func TestHomeCostStageADayEndSiteRoundingTolerance(t *testing.T) {
	deduction := int64(10)
	reconciled := int64(300)
	components := AccountCostComponents{RequiresReplacementDeduction: true, ReplacementDeductionCents: &deduction, ReconciledUpstreamDirectCostCents: &reconciled}
	// Three nonzero site totals can differ by up to two cents.
	data, _ := json.Marshal(map[string]any{"ReconciledSiteCount": 3})
	_ = json.Unmarshal(data, &components)
	for _, tc := range []struct {
		cost float64
		ok   bool
	}{{3.02, true}, {3.03, false}} {
		summary := &AdditionalCostSummary{Total: ptrF64(0)}
		operating, _, _ := projectOperatingCost(ptrF64(tc.cost), ptrF64(10), summary, components, nil)
		if (operating != nil) != tc.ok {
			t.Fatalf("cost %v operating=%v want available=%v", tc.cost, operating, tc.ok)
		}
	}
}

func TestHomeCostStageALiveReplacementStillRejectsDeductionAboveDirectCost(t *testing.T) {
	deduction := int64(301)
	components := AccountCostComponents{RequiresReplacementDeduction: true, ReplacementDeductionCents: &deduction, LiveKeySnapshot: true}
	summary := &AdditionalCostSummary{Total: ptrF64(0)}
	operating, profit, margin := projectOperatingCost(ptrF64(3), ptrF64(10), summary, components, nil)
	if operating != nil || profit != nil || margin != nil || summary.AccountQuality != KeyCostQualityMissing {
		t.Fatalf("excess live deduction accepted: %v %v %v %#v", operating, profit, margin, summary)
	}
}

func TestHomeCostStageAReconciliationRejectsCrossBusinessDay(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := businesstime.DateAt(now)
	value := 1.0
	keys := upstream.KeyUsageForDateResult{BusinessDate: date, Sites: []upstream.KeyUsageSiteResult{{SiteID: "site", Complete: true, ConsumeDate: businesstime.DateAt(now.AddDate(0, 0, -1)), SyncedRawCost: &value, CollectedRawCost: &value, Items: []upstream.KeyUsageTodayItem{{SiteID: "site", KeyID: "key", TodayAmount: 1, RawAmount: 1}}}}}
	runs := buildAccountKeyCostRuns("user", "workspace", "run", date, []upstream.SiteCostForDateResult{{SiteID: "site", RawCost: 1, RechargeRate: 1}}, keys, now)
	if len(runs) != 1 || runs[0].Complete || runs[0].Quality != KeyCostQualityMismatch {
		t.Fatalf("cross-day costs were reconciled: %#v", runs)
	}
}

func TestHomeCostStageAReconciliationAllowsCollectionInterval(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	date := businesstime.DateAt(now)
	keys := stageASnapshotResult(t, map[string]any{"BusinessDate": date, "Sites": []map[string]any{{"SiteID": "site", "Complete": true, "ConsumeDate": date, "SyncedRawCost": 1, "CollectedRawCost": 2, "Items": []upstream.KeyUsageTodayItem{{SiteID: "site", KeyID: "key", RawAmount: 1.5, TodayAmount: 1.5, RechargeRate: 1}}}}})
	runs := buildAccountKeyCostRuns("user", "workspace", "run", date, []upstream.SiteCostForDateResult{{SiteID: "site", RawCost: 1, RechargeRate: 1}}, keys, now)
	if len(runs) != 1 || !runs[0].Complete {
		t.Fatalf("same-batch collection interval incorrectly mismatched: %#v", runs)
	}
}
