package dashboard

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type nullableCostQualityRows struct {
	values [][]any
	index  int
}

func (r *nullableCostQualityRows) Close()                                       {}
func (r *nullableCostQualityRows) Err() error                                   { return nil }
func (r *nullableCostQualityRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *nullableCostQualityRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *nullableCostQualityRows) Next() bool {
	if r.index >= len(r.values) {
		return false
	}
	r.index++
	return true
}
func (r *nullableCostQualityRows) Values() ([]any, error) {
	if r.index == 0 || r.index > len(r.values) {
		return nil, errors.New("rows not positioned")
	}
	return r.values[r.index-1], nil
}
func (r *nullableCostQualityRows) RawValues() [][]byte { return nil }
func (r *nullableCostQualityRows) Conn() *pgx.Conn     { return nil }
func (r *nullableCostQualityRows) Scan(dest ...any) error {
	values, _ := r.Values()
	if len(dest) != len(values) {
		return fmt.Errorf("scan destination count = %d, values = %d", len(dest), len(values))
	}
	for index, target := range dest {
		if err := assignNullableRowsValue(target, values[index]); err != nil {
			return fmt.Errorf("column %d: %w", index, err)
		}
	}
	return nil
}

func assignNullableRowsValue(target any, value any) error {
	if scanner, ok := target.(interface{ Scan(any) error }); ok {
		return scanner.Scan(value)
	}
	destination := reflect.ValueOf(target)
	if destination.Kind() != reflect.Pointer || destination.IsNil() {
		return errors.New("scan destination must be a non-nil pointer")
	}
	if value == nil {
		if destination.Elem().Kind() == reflect.Pointer || destination.Elem().Kind() == reflect.Slice || destination.Elem().Kind() == reflect.Map {
			destination.Elem().Set(reflect.Zero(destination.Elem().Type()))
			return nil
		}
		return errors.New("cannot scan NULL into non-pointer destination")
	}
	source := reflect.ValueOf(value)
	if destination.Elem().Kind() == reflect.Pointer && source.Type().AssignableTo(destination.Elem().Type().Elem()) {
		pointer := reflect.New(source.Type())
		pointer.Elem().Set(source)
		destination.Elem().Set(pointer)
		return nil
	}
	if source.Type().AssignableTo(destination.Elem().Type()) {
		destination.Elem().Set(source)
		return nil
	}
	if source.Type().ConvertibleTo(destination.Elem().Type()) {
		destination.Elem().Set(source.Convert(destination.Elem().Type()))
		return nil
	}
	return fmt.Errorf("cannot assign %T to %T", value, target)
}

type nullableCostQualityDB struct{ rows pgx.Rows }

func (f *nullableCostQualityDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (f *nullableCostQualityDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return f.rows, nil
}
func (f *nullableCostQualityDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

type nullableCostQualityRow struct{ values []any }

func (r nullableCostQualityRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan destination count = %d, values = %d", len(dest), len(r.values))
	}
	for index, target := range dest {
		if err := assignNullableRowsValue(target, r.values[index]); err != nil {
			return fmt.Errorf("column %d: %w", index, err)
		}
	}
	return nil
}

type capturingMetricsDB struct {
	query     string
	queryArgs []any
	queryErr  error
}

func (f *capturingMetricsDB) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f *capturingMetricsDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	f.query = sql
	f.queryArgs = args
	return nil, f.queryErr
}

func (f *capturingMetricsDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return nil
}

func TestListRangeUsesExplicitBusinessDateArgument(t *testing.T) {
	queryErr := errors.New("stop after capturing query")
	db := &capturingMetricsDB{queryErr: queryErr}
	repo := newMetricsRepository(db)

	_, err := repo.ListRange(context.Background(), "user-1", "account-1", 7, "2026-08-01")
	if !errors.Is(err, queryErr) {
		t.Fatalf("ListRange() error = %v, want capture sentinel", err)
	}
	if strings.Contains(db.query, "CURRENT_DATE") {
		t.Fatalf("ListRange query still depends on database CURRENT_DATE: %s", db.query)
	}
	wantArgs := []any{"user-1", "account-1", "2026-08-01", 7}
	if !reflect.DeepEqual(db.queryArgs, wantArgs) {
		t.Fatalf("ListRange query args = %#v, want %#v", db.queryArgs, wantArgs)
	}
}

func TestListRangePreservesSnapshotWhenCostQualityModeIsNull(t *testing.T) {
	createdAt := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	profit := 42.0
	rows := &nullableCostQualityRows{values: [][]any{{
		"snapshot-1", "user-1", "account-1", createdAt,
		profit, nil, nil, nil, nil, createdAt, "final",
		nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil,
	}}}
	repo := newMetricsRepository(&nullableCostQualityDB{rows: rows})

	snapshots, err := repo.ListRange(context.Background(), "user-1", "account-1", 7, "2026-08-16")
	if err != nil {
		t.Fatalf("ListRange() error = %v, want NULL cost quality mode to be tolerated", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("ListRange() returned %d snapshots, want 1", len(snapshots))
	}
	if snapshots[0].TodayProfit == nil || *snapshots[0].TodayProfit != profit {
		t.Fatalf("TodayProfit = %v, want %v", snapshots[0].TodayProfit, profit)
	}
	if snapshots[0].CostQualityMode != "unknown" {
		t.Fatalf("CostQualityMode = %q, want unknown", snapshots[0].CostQualityMode)
	}
}

func TestLatestDashboardSnapshotPreservesSnapshotWhenCostQualityModeIsNull(t *testing.T) {
	createdAt := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	profit := 42.0
	row := nullableCostQualityRow{values: []any{
		"snapshot-1", "user-1", "account-1", createdAt,
		profit, nil, nil, nil, nil, createdAt, "final", "live_cache", nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil,
	}}
	db := &latestSnapshotDB{row: row}
	repo := newMetricsRepository(db)

	snapshot, err := repo.LatestDashboardSnapshot(context.Background(), "user-1", "account-1", "2026-08-15")
	if err != nil {
		t.Fatalf("LatestDashboardSnapshot() error = %v, want NULL cost quality mode to be tolerated", err)
	}
	if snapshot == nil || snapshot.TodayProfit == nil || *snapshot.TodayProfit != profit {
		t.Fatalf("snapshot profit = %v, want %v", snapshot, profit)
	}
	if snapshot.CostQualityMode != "unknown" {
		t.Fatalf("CostQualityMode = %q, want unknown", snapshot.CostQualityMode)
	}
}

func TestListSiteCostsPreservesRowsWhenLastAttemptFieldsAreNull(t *testing.T) {
	observedAt := time.Date(2026, 8, 16, 0, 5, 0, 0, time.UTC)
	rows := &nullableCostQualityRows{values: [][]any{{
		"cost-1", "user-1", "account-1", observedAt,
		"site-1", "site one", "sub2api",
		12.5, 1.0, 12.5, "ok", "dated_query", "", observedAt,
		nil, nil, nil, nil,
	}}}
	repo := newMetricsRepository(&nullableCostQualityDB{rows: rows})

	costs, err := repo.ListSiteCosts(context.Background(), "user-1", "account-1", "2026-08-15")
	if err != nil {
		t.Fatalf("ListSiteCosts() error = %v, want NULL last attempt fields to be tolerated", err)
	}
	if len(costs) != 1 {
		t.Fatalf("ListSiteCosts() returned %d rows, want 1", len(costs))
	}
	if costs[0].LastAttemptStatus != "" || costs[0].LastAttemptError != "" || costs[0].LastAttemptRunID != "" {
		t.Fatalf("last attempt fields = status %q error %q run %q, want empty strings", costs[0].LastAttemptStatus, costs[0].LastAttemptError, costs[0].LastAttemptRunID)
	}
}

func TestListLatestSiteCostsPreservesRowsWhenLastAttemptFieldsAreNull(t *testing.T) {
	observedAt := time.Date(2026, 8, 16, 0, 5, 0, 0, time.UTC)
	rows := &nullableCostQualityRows{values: [][]any{{
		"cost-1", "user-1", "account-1", observedAt,
		"site-1", "site one", "sub2api",
		12.5, 1.0, 12.5, "ok", "dated_query", "", observedAt,
		nil, nil, nil, nil,
	}}}
	repo := newMetricsRepository(&nullableCostQualityDB{rows: rows})

	costs, err := repo.ListLatestSiteCosts(context.Background(), "user-1", "account-1", "2026-08-15")
	if err != nil {
		t.Fatalf("ListLatestSiteCosts() error = %v, want NULL last attempt fields to be tolerated", err)
	}
	if len(costs) != 1 {
		t.Fatalf("ListLatestSiteCosts() returned %d rows, want 1", len(costs))
	}
	if costs[0].LastAttemptStatus != "" || costs[0].LastAttemptError != "" || costs[0].LastAttemptRunID != "" {
		t.Fatalf("last attempt fields = status %q error %q run %q, want empty strings", costs[0].LastAttemptStatus, costs[0].LastAttemptError, costs[0].LastAttemptRunID)
	}
}

func TestListSiteCostsPreservesRowsWhenErrorReasonIsNull(t *testing.T) {
	observedAt := time.Date(2026, 8, 16, 0, 5, 0, 0, time.UTC)
	rows := &nullableCostQualityRows{values: [][]any{{
		"cost-1", "user-1", "account-1", observedAt,
		"site-1", "site one", "sub2api",
		12.5, 1.0, 12.5, "ok", "dated_query", nil, observedAt,
		"ok", "", nil, "",
	}}}
	repo := newMetricsRepository(&nullableCostQualityDB{rows: rows})

	costs, err := repo.ListSiteCosts(context.Background(), "user-1", "account-1", "2026-08-15")
	if err != nil {
		t.Fatalf("ListSiteCosts() error = %v, want NULL error reason to be tolerated", err)
	}
	if len(costs) != 1 {
		t.Fatalf("ListSiteCosts() returned %d rows, want 1", len(costs))
	}
	if costs[0].ErrorReason != "" {
		t.Fatalf("ErrorReason = %q, want empty string", costs[0].ErrorReason)
	}
}

func TestListLatestSiteCostsPreservesRowsWhenErrorReasonIsNull(t *testing.T) {
	observedAt := time.Date(2026, 8, 16, 0, 5, 0, 0, time.UTC)
	rows := &nullableCostQualityRows{values: [][]any{{
		"cost-1", "user-1", "account-1", observedAt,
		"site-1", "site one", "sub2api",
		12.5, 1.0, 12.5, "ok", "dated_query", nil, observedAt,
		"ok", "", nil, "",
	}}}
	repo := newMetricsRepository(&nullableCostQualityDB{rows: rows})

	costs, err := repo.ListLatestSiteCosts(context.Background(), "user-1", "account-1", "2026-08-15")
	if err != nil {
		t.Fatalf("ListLatestSiteCosts() error = %v, want NULL error reason to be tolerated", err)
	}
	if len(costs) != 1 {
		t.Fatalf("ListLatestSiteCosts() returned %d rows, want 1", len(costs))
	}
	if costs[0].ErrorReason != "" {
		t.Fatalf("ErrorReason = %q, want empty string", costs[0].ErrorReason)
	}
}

func TestListSiteCostsPreservesNonNullNullableTextFields(t *testing.T) {
	observedAt := time.Date(2026, 8, 16, 0, 5, 0, 0, time.UTC)
	rows := &nullableCostQualityRows{values: [][]any{{
		"cost-1", "user-1", "account-1", observedAt,
		"site-1", "site one", "sub2api",
		12.5, 1.0, 12.5, "partial", "best_effort", "key sum fallback", observedAt,
		"failed", "timeout", observedAt, "run-1",
	}}}
	repo := newMetricsRepository(&nullableCostQualityDB{rows: rows})

	costs, err := repo.ListSiteCosts(context.Background(), "user-1", "account-1", "2026-08-15")
	if err != nil {
		t.Fatalf("ListSiteCosts() error = %v", err)
	}
	if len(costs) != 1 {
		t.Fatalf("ListSiteCosts() returned %d rows, want 1", len(costs))
	}
	cost := costs[0]
	if cost.ErrorReason != "key sum fallback" || cost.LastAttemptStatus != "failed" || cost.LastAttemptError != "timeout" || cost.LastAttemptRunID != "run-1" {
		t.Fatalf("nullable text fields were not preserved: error_reason=%q last_status=%q last_error=%q run_id=%q", cost.ErrorReason, cost.LastAttemptStatus, cost.LastAttemptError, cost.LastAttemptRunID)
	}
}

func TestListLatestSiteCostsPreservesNonNullNullableTextFields(t *testing.T) {
	observedAt := time.Date(2026, 8, 16, 0, 5, 0, 0, time.UTC)
	rows := &nullableCostQualityRows{values: [][]any{{
		"cost-1", "user-1", "account-1", observedAt,
		"site-1", "site one", "sub2api",
		12.5, 1.0, 12.5, "partial", "best_effort", "key sum fallback", observedAt,
		"failed", "timeout", observedAt, "run-1",
	}}}
	repo := newMetricsRepository(&nullableCostQualityDB{rows: rows})

	costs, err := repo.ListLatestSiteCosts(context.Background(), "user-1", "account-1", "2026-08-15")
	if err != nil {
		t.Fatalf("ListLatestSiteCosts() error = %v", err)
	}
	if len(costs) != 1 {
		t.Fatalf("ListLatestSiteCosts() returned %d rows, want 1", len(costs))
	}
	cost := costs[0]
	if cost.ErrorReason != "key sum fallback" || cost.LastAttemptStatus != "failed" || cost.LastAttemptError != "timeout" || cost.LastAttemptRunID != "run-1" {
		t.Fatalf("nullable text fields were not preserved: error_reason=%q last_status=%q last_error=%q run_id=%q", cost.ErrorReason, cost.LastAttemptStatus, cost.LastAttemptError, cost.LastAttemptRunID)
	}
}

type latestSnapshotDB struct{ row pgx.Row }

func (f *latestSnapshotDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (f *latestSnapshotDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query call")
}
func (f *latestSnapshotDB) QueryRow(context.Context, string, ...any) pgx.Row { return f.row }

func TestListDailyStatsPreservesSnapshotWhenCostQualityModeIsNull(t *testing.T) {
	createdAt := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	profit := 42.0
	rows := &nullableCostQualityRows{values: [][]any{{
		"snapshot-1", "user-1", "account-1", createdAt,
		profit, nil, nil, nil, nil, createdAt, "final", "live_cache", nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil,
	}}}
	repo := newMetricsRepository(&nullableCostQualityDB{rows: rows})

	snapshots, err := repo.ListDailyStats(context.Background(), "user-1", "account-1", "2026-08-15", "2026-08-15")
	if err != nil {
		t.Fatalf("ListDailyStats() error = %v, want NULL cost quality mode to be tolerated", err)
	}
	if len(snapshots) != 1 || snapshots[0].TodayProfit == nil || *snapshots[0].TodayProfit != profit {
		t.Fatalf("snapshots = %+v, want one snapshot preserving profit %v", snapshots, profit)
	}
	if snapshots[0].CostQualityMode != "unknown" {
		t.Fatalf("CostQualityMode = %q, want unknown", snapshots[0].CostQualityMode)
	}
}
