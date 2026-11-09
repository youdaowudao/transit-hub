package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/businesstime"
)

// Exercise the public route so the old missing DELETE is a business-result RED,
// rather than a compile failure caused by referencing a proposed repository API.
func manualCostRequest(mux *http.ServeMux, method, sourceID, body, user string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/dashboard/additional-costs/"+sourceID, strings.NewReader(body))
	if user != "" {
		r = r.WithContext(authctx.WithUserID(r.Context(), user))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestManualCostDeletionRejectsUnauthenticated(t *testing.T) {
	s := NewMetricsService(nil, nil, nil, nil, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
	s.additionalCosts = &fakeAdditionalCostRepository{}
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, s)
	if w := manualCostRequest(mux, http.MethodDelete, "source-1", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("DELETE without Hub login = %d, want 401", w.Code)
	}
}

func TestManualCostDeletionPostgresWholeSourceAndRollback(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL); INSERT INTO admin_accounts VALUES('workspace-1','user-1'),('workspace-2','user-1')`); err != nil {
		t.Fatal(err)
	}
	s := NewMetricsService(nil, nil, nil, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1", "user-2": "workspace-2"}})
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, s)
	dates := []string{"2026-08-20", "2026-08-21", "2026-08-22"}
	for _, day := range dates {
		date, _ := time.ParseInLocation("2006-01-02", day, businesstime.Location())
		revenue, direct, profit, feeRate := 100.0, 20.0, 80.0, 0.02
		if err := repo.Upsert(ctx, DailySnapshot{ID: day, UserID: "user-1", AdminAccountID: "workspace-1", Date: date,
			TodayProfit: &revenue, TodayPurchase: &direct, NetProfit: &profit, RechargeFeeRate: &feeRate,
			SettlementStatus: SettlementStatusFinal, SnapshotSource: SnapshotSourceLiveCache, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(id, kind string, value float64, days int) {
		t.Helper()
		rows, err := buildAdditionalCostRecords("user-1", "workspace-1", AdditionalCostInput{Type: kind, Name: "同名费用", Amount: value, Days: days, BusinessDate: dates[0], UsageRate: 0.8}, id)
		if err != nil {
			t.Fatal(err)
		}
		if err = repo.InsertAdditionalCosts(ctx, rows); err != nil {
			t.Fatal(err)
		}
	}
	insert("remove", AdditionalCostFixed, 100, 3)
	insert("keep", AdditionalCostFixed, 6, 3)
	if w := manualCostRequest(mux, http.MethodDelete, "remove", "", "user-1"); w.Code != http.StatusNoContent {
		t.Fatalf("whole-source DELETE = %d %s, want 204", w.Code, w.Body.String())
	}
	for _, day := range dates {
		snap, err := repo.LatestDashboardSnapshot(ctx, "user-1", "workspace-1", day)
		if err != nil || snap == nil || snap.FixedCost == nil || *snap.FixedCost != 2 || snap.OperatingCost == nil || *snap.OperatingCost != 24 || snap.AdjustedNetProfit == nil || *snap.AdjustedNetProfit != 76 {
			t.Fatalf("deleted source residue on %s: snapshot=%+v err=%v", day, snap, err)
		}
		if *snap.TodayProfit != 100 || *snap.TodayPurchase != 20 || *snap.RechargeFeeRate != 0.02 || snap.SettlementStatus != SettlementStatusFinal || snap.SnapshotSource != SnapshotSourceLiveCache || len(snap.AdditionalCostRecords) != 1 || snap.AdditionalCostRecords[0].SourceID != "keep" {
			t.Fatalf("base fields/history/same-name source changed: %+v", snap)
		}
	}
	for _, user := range []string{"user-1", "user-2"} {
		id := "remove"
		if user == "user-2" {
			id = "keep"
		}
		if w := manualCostRequest(mux, http.MethodDelete, id, "", user); w.Code != http.StatusNotFound {
			t.Fatalf("missing or other-workspace DELETE = %d, want404", w.Code)
		}
	}
	// Failure after deleting rows must roll the rows and all projections back.
	insert("rollback", AdditionalCostFixed, 9, 3)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_manual_cost_projection() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated projection failure'; END $$; CREATE TRIGGER reject_manual_cost_projection BEFORE UPDATE ON dashboard_daily_stats FOR EACH ROW EXECUTE FUNCTION reject_manual_cost_projection()`); err != nil {
		t.Fatal(err)
	}
	if w := manualCostRequest(mux, http.MethodDelete, "rollback", "", "user-1"); w.Code != http.StatusInternalServerError {
		t.Fatalf("projection failure = %d, want500", w.Code)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_additional_costs WHERE source_id='rollback'`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("failed transaction removed source: count=%d err=%v", count, err)
	}
	for _, day := range dates {
		snap, err := repo.LatestDashboardSnapshot(ctx, "user-1", "workspace-1", day)
		if err != nil || snap == nil || snap.FixedCost == nil || *snap.FixedCost != 5 {
			t.Fatalf("failed deletion changed projection: %+v %v", snap, err)
		}
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_manual_cost_projection ON dashboard_daily_stats`); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{AdditionalCostPromotion, AdditionalCostAdjustment} {
		value := 10.0
		if kind == AdditionalCostAdjustment {
			value = -5
		}
		insert(kind, kind, value, 1)
		if w := manualCostRequest(mux, http.MethodDelete, kind, "", "user-1"); w.Code != http.StatusNoContent {
			t.Fatalf("DELETE %s = %d", kind, w.Code)
		}
	}
}

func TestManualCostDeletionPostgresProtectsAndSerializes(t *testing.T) {
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	s := NewMetricsService(nil, nil, nil, repo, &fakeAdminAccounts{current: map[string]string{"user-1": "workspace-1"}})
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil, s)
	for i, record := range []AdditionalCostRecord{
		{Type: AdditionalCostAccountPurchase}, {Type: AdditionalCostAccountRefund}, {Type: AdditionalCostFixed, BatchID: "batch"}, {Type: AdditionalCostFixed, AccountAssetID: "asset"},
	} {
		id := []string{"purchase", "refund", "batch", "asset"}[i]
		if _, err := pool.Exec(ctx, `INSERT INTO dashboard_additional_costs(id,user_id,admin_account_id,type,name,business_date,amount_cents,source_id,batch_id,account_asset_id) VALUES($1,'user-1','workspace-1',$2,'protected','2026-08-20',100,$1,$3,$4)`, id, record.Type, record.BatchID, record.AccountAssetID); err != nil {
			t.Fatal(err)
		}
		if w := manualCostRequest(mux, http.MethodDelete, id, "", "user-1"); w.Code != http.StatusConflict {
			t.Fatalf("protected %s = %d", id, w.Code)
		}
	}
	rows, _ := buildAdditionalCostRecords("user-1", "workspace-1", AdditionalCostInput{Type: AdditionalCostFixed, Name: "concurrent", BusinessDate: "2026-08-20", Amount: 30, Days: 3}, "concurrent")
	if err := repo.InsertAdditionalCosts(ctx, rows); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	statuses := make(chan int, 2)
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		group.Add(1)
		go func(method string) {
			defer group.Done()
			statuses <- manualCostRequest(mux, method, "concurrent", `{"type":"fixed","name":"concurrent","businessDate":"2026-08-21","amount":50,"days":2}`, "user-1").Code
		}(method)
	}
	group.Wait()
	close(statuses)
	var deleted bool
	for status := range statuses {
		if status == http.StatusNoContent {
			deleted = true
		} else if status != http.StatusOK && status != http.StatusNotFound {
			t.Fatalf("concurrent status=%d", status)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dashboard_additional_costs WHERE source_id='concurrent'`).Scan(&count); err != nil || count != 0 || !deleted {
		t.Fatalf("edit resurrected deletion: count=%d deleted=%v err=%v", count, deleted, err)
	}
}
