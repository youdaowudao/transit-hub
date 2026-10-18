package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

func TestGroupRateAccountConsumptionIncludeZeroPreservesHomeDrilldown(t *testing.T) {
	now := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	u := &stageASnapshotUpstreams{fakeUpstreamLister: &fakeUpstreamLister{}, result: stageASnapshotResult(t, map[string]any{
		"BusinessDate": "2031-01-01", "Sites": []map[string]any{{"SiteID": "site", "Complete": true, "Status": "ok", "Items": []upstream.KeyUsageTodayItem{{SiteID: "site", KeyID: "zero", TodayAmount: 0}, {SiteID: "site", KeyID: "paid", TodayAmount: 2}}}},
	})}
	svc := NewMetricsService(nil, nil, u, nil, &fakeAdminAccounts{current: map[string]string{"user": "workspace"}})
	svc.now = func() time.Time { return now }
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, svc)
	for _, tc := range []struct {
		query string
		count int
	}{{"", 1}, {"?includeZero=1", 2}, {"?includeZero=0", 1}} {
		t.Run(tc.query, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/dashboard/upstream-key-usage-today"+tc.query, nil)
			req = req.WithContext(authctx.WithUserID(req.Context(), "user"))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			var data struct {
				Workspace string                      `json:"workspaceAdminAccountId"`
				Keys      []UpstreamKeyUsageTodayItem `json:"keys"`
				Total     float64                     `json:"total"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || len(data.Keys) != tc.count || data.Total != 2 || data.Workspace != "workspace" {
				t.Errorf("response=%d %s", w.Code, w.Body.String())
			}
		})
	}
	if u.keyUsageCalls != 0 || u.syncCalls != 0 || u.cachedCalls != 3 {
		t.Fatalf("unexpected reads: %#v", u)
	}
}

// Invoke the new repository entry without requiring its types in the RED build.
func readRecordedTotalsForTest(t *testing.T, repo *MetricsRepository, user, workspace string) []map[string]any {
	t.Helper()
	method := reflect.ValueOf(repo).MethodByName("UpstreamKeyRecordedTotals")
	if !method.IsValid() {
		t.Fatal("UpstreamKeyRecordedTotals is missing: daily account totals cannot be read")
	}
	result := method.Call([]reflect.Value{reflect.ValueOf(context.Background()), reflect.ValueOf(user), reflect.ValueOf(workspace)})
	if !result[1].IsNil() {
		t.Fatal(result[1].Interface())
	}
	raw, err := json.Marshal(result[0].Interface())
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err = json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func TestGroupRateAccountConsumptionRecordedTotalsLatestCompleteSingaporeRuns(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	add := func(id, user, ws, date, site, tz string, complete bool, offset int, cost int64, status string) {
		t.Helper()
		run := UpstreamKeyCostRun{ID: id, SnapshotRunID: id, UserID: user, AdminAccountID: ws, BusinessDate: date, SiteID: site, UsageTimezone: tz, Complete: complete, Quality: KeyCostQualityComplete, ObservedAt: now.Add(time.Duration(offset) * time.Minute), Items: []UpstreamKeyDailyCost{{RunID: id, UserID: user, AdminAccountID: ws, BusinessDate: date, SiteID: site, KeyID: "key", Status: status, AdjustedCostCents: cost, ObservedAt: now}}}
		if err := repo.SaveUpstreamKeyCostRuns(ctx, []UpstreamKeyCostRun{run}); err != nil {
			t.Fatal(err)
		}
	}
	add("old", "user", "workspace", "2030-12-30", "site", "Asia/Singapore", true, 1, 100, "ok")
	add("latest", "user", "workspace", "2030-12-30", "site", "Asia/Singapore", true, 2, 250, "ok")
	add("incomplete", "user", "workspace", "2030-12-30", "site", "Asia/Singapore", false, 3, 9000, "ok")
	add("next", "user", "workspace", "2030-12-31", "site", "Asia/Singapore", true, 4, 50, "ok")
	add("legacy", "user", "workspace", "2030-12-29", "site", "", true, 5, 9000, "ok")
	add("other-user", "other", "workspace", "2030-12-31", "site", "Asia/Singapore", true, 6, 9000, "ok")
	add("other-workspace", "user", "other", "2030-12-31", "site", "Asia/Singapore", true, 7, 9000, "ok")
	add("merged", "user", "workspace", "2030-12-31", "merged-site", "Asia/Singapore", true, 8, 300, "merged")
	add("other-account", "user", "workspace", "2030-12-30", "merged-site", "Asia/Singapore", true, 9, 200, "ok")
	// Different non-merged key on that site must receive the whole-site flag too.
	if _, err := pool.Exec(ctx, `UPDATE dashboard_upstream_key_daily_costs SET key_id='other-key' WHERE run_id='other-account'`); err != nil {
		t.Fatal(err)
	}
	items := readRecordedTotalsForTest(t, repo, "user", "workspace")
	if len(items) != 3 {
		t.Fatalf("items=%#v", items)
	}
	for _, item := range items {
		if item["siteId"] == "site" {
			if item["totalAmount"] != float64(3) || item["days"] != float64(2) || item["firstDate"] != "2030-12-30" || item["lastDate"] != "2030-12-31" || item["siteMergedDays"] != float64(0) {
				t.Errorf("wrong daily latest sum=%#v", item)
			}
		} else if item["siteMergedDays"] != float64(1) {
			t.Errorf("site merged flag lost=%#v", item)
		}
	}
	if items := readRecordedTotalsForTest(t, repo, "absent", "workspace"); len(items) != 0 {
		t.Fatal(items)
	}
	svc := NewMetricsService(nil, nil, nil, repo, &fakeAdminAccounts{current: map[string]string{"user": "workspace"}})
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, svc)
	req := httptest.NewRequest("GET", "/api/dashboard/upstream-key-recorded-totals", nil)
	req = req.WithContext(authctx.WithUserID(ctx, "user"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var body struct {
		Workspace string           `json:"workspaceAdminAccountId"`
		Items     []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || body.Workspace != "workspace" || len(body.Items) != 3 {
		t.Fatalf("endpoint=%d %s", w.Code, w.Body.String())
	}
}
