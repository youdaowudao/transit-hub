package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

func giftLedgerPGRepo(t *testing.T) (*MetricsRepository, context.Context) {
	t.Helper()
	pool := accountAssetTestPool(t)
	ctx := context.Background()
	repo := NewMetricsRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL); INSERT INTO admin_accounts VALUES('workspace-1','user-1'),('workspace-2','user-2')`); err != nil {
		t.Fatal(err)
	}
	return repo, ctx
}

func TestGiftLedgerPostgresRecordsAndUsage(t *testing.T) {
	repo, ctx := giftLedgerPGRepo(t)
	base := time.Date(2026, 11, 14, 1, 0, 0, 0, time.UTC)
	record := func(id, workspace, user string, mainUser int64, date string, created time.Time) GiftLedgerRecord {
		return GiftLedgerRecord{ID: id, UserID: user, AdminAccountID: workspace, MainUserID: mainUser, MainUserEmail: "x@example.com",
			RechargeDate: date, PaidCents: 10000, GiftCents: 2000, Note: "n", CreatedAt: created, UpdatedAt: created}
	}
	for _, item := range []GiftLedgerRecord{
		record("b", "workspace-1", "user-1", 5, "2026-11-10", base.Add(2*time.Second)),
		record("a", "workspace-1", "user-1", 5, "2026-11-10", base.Add(2*time.Second)),
		record("c", "workspace-1", "user-1", 5, "2026-11-09", base.Add(5*time.Second)),
		record("d", "workspace-1", "user-1", 3, "2026-11-12", base),
		record("other", "workspace-2", "user-2", 5, "2026-11-01", base),
	} {
		if err := repo.CreateGiftLedgerRecord(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.CreateGiftLedgerRecord(ctx, record("ghost", "workspace-9", "user-1", 1, "2026-11-10", base)); !errors.Is(err, ErrGiftLedgerNotFound) {
		t.Fatalf("create in missing workspace err = %v", err)
	}
	items, err := repo.ListGiftLedgerRecords(ctx, "user-1", "workspace-1")
	if err != nil || len(items) != 4 {
		t.Fatalf("items = %+v err=%v", items, err)
	}
	order := ""
	for _, item := range items {
		order += item.ID
	}
	if order != "dcab" || items[0].RechargeDate != "2026-11-12" || items[1].PaidCents != 10000 || items[1].GiftCents != 2000 {
		t.Fatalf("order = %s items=%+v", order, items)
	}
	if _, err := repo.GetGiftLedgerRecord(ctx, "user-2", "workspace-2", "a"); !errors.Is(err, ErrGiftLedgerNotFound) {
		t.Fatalf("cross-workspace get err = %v", err)
	}
	updated := items[2]
	updated.RechargeDate, updated.PaidCents, updated.GiftCents, updated.Note, updated.UpdatedAt = "2026-11-11", 5000, 0, "改", base.Add(time.Hour)
	if err := repo.UpdateGiftLedgerRecord(ctx, updated); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetGiftLedgerRecord(ctx, "user-1", "workspace-1", "a")
	if err != nil || got.RechargeDate != "2026-11-11" || got.PaidCents != 5000 || got.GiftCents != 0 || got.Note != "改" || !got.CreatedAt.Equal(base.Add(2*time.Second)) || got.MainUserID != 5 {
		t.Fatalf("updated = %+v err=%v", got, err)
	}
	updated.UserID = "user-2"
	if err := repo.UpdateGiftLedgerRecord(ctx, updated); !errors.Is(err, ErrGiftLedgerNotFound) {
		t.Fatalf("cross-workspace update err = %v", err)
	}
	if err := repo.DeleteGiftLedgerRecord(ctx, "user-1", "workspace-1", "a"); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteGiftLedgerRecord(ctx, "user-1", "workspace-1", "a"); !errors.Is(err, ErrGiftLedgerNotFound) {
		t.Fatalf("repeat delete err = %v", err)
	}

	// 每日消费：第一次保存为准，返回库里的值。
	if value, err := repo.SaveGiftCustomerUsage(ctx, "user-1", "workspace-1", 5, "2026-11-12", 60e6, base); err != nil || value != 60e6 {
		t.Fatalf("first save = %d err=%v", value, err)
	}
	if value, err := repo.SaveGiftCustomerUsage(ctx, "user-1", "workspace-1", 5, "2026-11-12", 90e6, base.Add(time.Hour)); err != nil || value != 60e6 {
		t.Fatalf("second save = %d err=%v, want the first value", value, err)
	}
	if _, err := repo.SaveGiftCustomerUsage(ctx, "user-1", "workspace-1", 5, "2026-11-13", 1, base); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveGiftCustomerUsage(ctx, "user-2", "workspace-2", 5, "2026-11-12", 7, base); err != nil {
		t.Fatal(err)
	}
	usage, err := repo.ListGiftCustomerUsage(ctx, "user-1", "workspace-1", []int64{5, 3}, "2026-11-12", "2026-11-12")
	if err != nil || len(usage) != 1 || len(usage[5]) != 1 || usage[5]["2026-11-12"] != 60e6 {
		t.Fatalf("usage = %v err=%v", usage, err)
	}
}

func giftPGSnapshot(day string, source string, observed *time.Time, discount float64, detail []byte) DailySnapshot {
	date, _ := time.ParseInLocation("2006-01-02", day, businesstime.Location())
	revenue := 200.0
	return DailySnapshot{ID: day + source, UserID: "user-1", AdminAccountID: "workspace-1", Date: date, TodayProfit: &revenue,
		RevenueDiscount: &discount, RevenueDiscountDetail: detail, SettlementStatus: SettlementStatusPartial,
		SnapshotSource: source, ObservedAt: observed, CreatedAt: time.Now()}
}

func giftPGDetail(t *testing.T, gift int64, general float64) []byte {
	t.Helper()
	raw, err := (&RevenueDiscountDetail{Version: 1, GeneralDiscount: general, Customers: []RevenueDiscountCustomer{{MainUserID: "5", Email: "x@example.com", ConsumptionMicros: 60e6, CoveredMicros: 60e6, GiftCents: gift}}}).marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestGiftLedgerPostgresSnapshotGuard(t *testing.T) {
	repo, ctx := giftLedgerPGRepo(t)
	day := "2026-11-13"
	afterEnd := time.Date(2026, 11, 14, 0, 5, 0, 0, businesstime.Location())
	beforeEnd := time.Date(2026, 11, 13, 23, 59, 0, 0, businesstime.Location())
	late := time.Date(2026, 11, 14, 0, 10, 0, 0, businesstime.Location())
	read := func() DailySnapshot {
		t.Helper()
		latest, err := repo.LatestDashboardSnapshot(ctx, "user-1", "workspace-1", day)
		if err != nil || latest == nil {
			t.Fatalf("latest = %+v err=%v", latest, err)
		}
		return *latest
	}
	customers := func(snapshot DailySnapshot) string {
		detail, err := parseRevenueDiscountDetail(snapshot.RevenueDiscountDetail)
		if err != nil {
			t.Fatalf("detail broken: %s", snapshot.RevenueDiscountDetail)
		}
		if detail == nil {
			return "null"
		}
		raw, _ := json.Marshal(detail.Customers)
		return string(raw)
	}
	settledDetail := giftPGDetail(t, 1000, 2)
	if err := repo.Upsert(ctx, giftPGSnapshot(day, SnapshotSourceDatedQuery, &afterEnd, 12, settledDetail)); err != nil {
		t.Fatal(err)
	}
	frozen := customers(read())
	for name, write := range map[string]DailySnapshot{
		"S23 live write":                     giftPGSnapshot(day, SnapshotSourceLiveCache, &late, 99, giftPGDetail(t, 2000, 0)),
		"S25 old dated_query same customers": giftPGSnapshot(day, SnapshotSourceDatedQuery, &beforeEnd, 99, settledDetail),
		"settled different customers":        giftPGSnapshot(day, SnapshotSourceBackfill, &late, 99, giftPGDetail(t, 1385, 2)),
		"settled cleared detail":             giftPGSnapshot(day, SnapshotSourceBackfill, &late, 99, nil),
		"old row without observed_at":        giftPGSnapshot(day, SnapshotSourceDatedQuery, nil, 99, settledDetail),
	} {
		if err := repo.Upsert(ctx, write); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		current := read()
		if *current.RevenueDiscount != 12 || customers(current) != frozen || current.SnapshotSource != SnapshotSourceDatedQuery {
			t.Fatalf("%s overwrote the settled row: discount=%v source=%s customers=%s", name, *current.RevenueDiscount, current.SnapshotSource, customers(current))
		}
	}
	// 客户明细相同、整站部分不同的日结重跑：写入成功。
	rerun := giftPGSnapshot(day, SnapshotSourceBackfill, &late, 14, giftPGDetail(t, 1000, 4))
	if err := repo.Upsert(ctx, rerun); err != nil {
		t.Fatal(err)
	}
	if current := read(); *current.RevenueDiscount != 14 || customers(current) != frozen || current.SnapshotSource != SnapshotSourceBackfill {
		t.Fatalf("rerun rejected: %+v", current)
	}
	// 手工成本重算后来源、时间、明细不变。
	records, err := buildAdditionalCostRecords("user-1", "workspace-1", AdditionalCostInput{Type: AdditionalCostFixed, Name: "服务器", Amount: 10, Days: 1, BusinessDate: day}, "fixed-"+day)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertAdditionalCosts(ctx, records); err != nil {
		t.Fatal(err)
	}
	if current := read(); customers(current) != frozen || current.SnapshotSource != SnapshotSourceBackfill || current.FixedCost == nil || *current.FixedCost != 10 || !giftLedgerSettled(current) {
		t.Fatalf("reprojection changed the settled row: %+v", current)
	}
	// 读回：ListDailyStats、ListRange。
	listed, err := repo.ListDailyStats(ctx, "user-1", "workspace-1", day, day)
	if err != nil || len(listed) != 1 || customers(listed[0]) != frozen {
		t.Fatalf("ListDailyStats = %+v err=%v", listed, err)
	}
	ranged, err := repo.ListRange(ctx, "user-1", "workspace-1", 7, "2026-11-14")
	if err != nil || len(ranged) != 1 || customers(ranged[0]) != frozen {
		t.Fatalf("ListRange = %+v err=%v", ranged, err)
	}
}

func TestGiftLedgerPostgresGuardEmptyAndUnsettledRows(t *testing.T) {
	repo, ctx := giftLedgerPGRepo(t)
	afterEnd := time.Date(2026, 11, 14, 0, 5, 0, 0, businesstime.Location())
	late := time.Date(2026, 11, 14, 0, 10, 0, 0, businesstime.Location())
	read := func(day string) DailySnapshot {
		t.Helper()
		latest, err := repo.LatestDashboardSnapshot(ctx, "user-1", "workspace-1", day)
		if err != nil || latest == nil {
			t.Fatalf("latest = %+v err=%v", latest, err)
		}
		return *latest
	}
	// S26：已日结、明细为空的行：实时写入、带明细的重跑都被跳过；明细仍为空的重跑可以写。
	day := "2026-11-13"
	if err := repo.Upsert(ctx, giftPGSnapshot(day, SnapshotSourceDatedQuery, &afterEnd, 5, nil)); err != nil {
		t.Fatal(err)
	}
	for name, write := range map[string]DailySnapshot{
		"live":        giftPGSnapshot(day, SnapshotSourceLiveCache, &late, 9, nil),
		"with detail": giftPGSnapshot(day, SnapshotSourceBackfill, &late, 9, giftPGDetail(t, 1000, 0)),
	} {
		if err := repo.Upsert(ctx, write); err != nil {
			t.Fatal(err)
		}
		if current := read(day); *current.RevenueDiscount != 5 || len(current.RevenueDiscountDetail) != 0 {
			t.Fatalf("%s overwrote the empty settled row: %+v", name, current)
		}
	}
	if err := repo.Upsert(ctx, giftPGSnapshot(day, SnapshotSourceBackfill, &late, 6, nil)); err != nil {
		t.Fatal(err)
	}
	if current := read(day); *current.RevenueDiscount != 6 {
		t.Fatalf("empty rerun rejected: %v", *current.RevenueDiscount)
	}
	// 未日结行：与 V3.1.6 相同，实时可以覆盖非 final 行；今天被提前写成 dated_query（早于日末）的行也可被覆盖。
	today := "2026-11-14"
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, businesstime.Location())
	for _, first := range []DailySnapshot{
		giftPGSnapshot(today, SnapshotSourceLiveCache, &now, 1, nil),
		giftPGSnapshot(today, SnapshotSourceDatedQuery, &now, 2, giftPGDetail(t, 100, 0)),
	} {
		if err := repo.Upsert(ctx, first); err != nil {
			t.Fatal(err)
		}
		later := now.Add(time.Minute)
		if err := repo.Upsert(ctx, giftPGSnapshot(today, SnapshotSourceLiveCache, &later, 3, giftPGDetail(t, 300, 0))); err != nil {
			t.Fatal(err)
		}
		if current := read(today); *current.RevenueDiscount != 3 {
			t.Fatalf("unsettled row (%s) not overwritten: %v", first.SnapshotSource, *current.RevenueDiscount)
		}
	}
	// observed_at 为空的旧 dated_query 行：手工成本重算和日结升级都能写入。
	old := "2026-11-01"
	if err := repo.Upsert(ctx, giftPGSnapshot(old, SnapshotSourceDatedQuery, nil, 7, nil)); err != nil {
		t.Fatal(err)
	}
	records, err := buildAdditionalCostRecords("user-1", "workspace-1", AdditionalCostInput{Type: AdditionalCostFixed, Name: "服务器", Amount: 4, Days: 1, BusinessDate: old}, "fixed-"+old)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertAdditionalCosts(ctx, records); err != nil {
		t.Fatal(err)
	}
	if current := read(old); current.FixedCost == nil || *current.FixedCost != 4 {
		t.Fatalf("reprojection on NULL observed_at row failed: %+v", current)
	}
	if err := repo.Upsert(ctx, giftPGSnapshot(old, SnapshotSourceBackfill, &late, 8, giftPGDetail(t, 100, 0))); err != nil {
		t.Fatal(err)
	}
	if current := read(old); *current.RevenueDiscount != 8 || current.SnapshotSource != SnapshotSourceBackfill {
		t.Fatalf("settlement upgrade of NULL observed_at row failed: %+v", current)
	}
}
