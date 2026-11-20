package user_activity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

type absorbUsagePlatform struct {
	Platform
	points []upstream.Sub2APIUserUsageTrendPoint
	stats  upstream.Sub2APIActivityUsageStats
	pacer  *pacedReader
	calls  int
}

func (p *absorbUsagePlatform) FetchSub2APIUserUsageTrendContext(ctx context.Context, _ upstream.Session, from, to string, limit int) ([]upstream.Sub2APIUserUsageTrendPoint, error) {
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded upstream read")
	}
	if from != to || limit != 1000 {
		panic("unexpected daily query budget")
	}
	p.calls++
	p.pacer.last = time.Time{}
	return p.points, nil
}
func (p *absorbUsagePlatform) FetchSub2APIActivityUsageStatsContext(context.Context, upstream.Session, string) (upstream.Sub2APIActivityUsageStats, error) {
	p.calls++
	p.pacer.last = time.Time{}
	return p.stats, nil
}

func TestAbsorbUsageRequiresClosedDayProof(t *testing.T) {
	for _, tc := range []struct {
		name    string
		points  []upstream.Sub2APIUserUsageTrendPoint
		stats   upstream.Sub2APIActivityUsageStats
		today   bool
		wantErr bool
	}{
		{"closed zero unknown", nil, upstream.Sub2APIActivityUsageStats{}, false, true},
		{"today zero temporary", nil, upstream.Sub2APIActivityUsageStats{}, true, false},
		{"free requests are real use", []upstream.Sub2APIUserUsageTrendPoint{{UserID: "1", Requests: 3}}, upstream.Sub2APIActivityUsageStats{Requests: 3}, false, false},
		{"requests mismatch", []upstream.Sub2APIUserUsageTrendPoint{{UserID: "1", Requests: 3, ActualCost: 2}}, upstream.Sub2APIActivityUsageStats{Requests: 4, ActualCost: 2}, false, true},
		{"money mismatch", []upstream.Sub2APIUserUsageTrendPoint{{UserID: "1", Requests: 3, ActualCost: 2}}, upstream.Sub2APIActivityUsageStats{Requests: 3, ActualCost: 2.01}, false, true},
		{"today is not a closed reconciliation", []upstream.Sub2APIUserUsageTrendPoint{{UserID: "1", Requests: 3, ActualCost: 2}}, upstream.Sub2APIActivityUsageStats{Requests: 4, ActualCost: 3}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pacer := &pacedReader{}
			p := &absorbUsagePlatform{points: tc.points, stats: tc.stats, pacer: pacer}
			s := NewService(nil, nil, p, false)
			defer s.cancel()
			today := "2026-11-20"
			day := "2026-11-19"
			if tc.today {
				day = today
			}
			_, err := s.usageDay(t.Context(), pacer, upstream.Session{}, day, today, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("closed-day proof result incorrect: %v", err)
			}
			if p.calls != 2 {
				t.Fatal("expected exactly trend and aggregate reads")
			}
		})
	}
	p := &pacedReader{last: time.Now()}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()
	if !errors.Is(p.wait(ctx), context.DeadlineExceeded) {
		t.Fatal("request pacing did not honor cancellation")
	}
}

func TestAbsorbCoverageKeepsNewestSegmentWithoutBridging(t *testing.T) {
	from, to := mergeCoverage("2026-01-01", "2026-01-10", "2026-11-18", "2026-11-19")
	if from != "2026-11-18" || to != "2026-11-19" {
		t.Fatal("stale old range blocked recent proof")
	}
	from, to = mergeCoverage(from, to, "2026-11-17", "2026-11-17")
	if from != "2026-11-17" || to != "2026-11-19" {
		t.Fatal("adjacent verified day failed to extend coverage")
	}
	if covered(from, to, "2026-11-10", "2026-11-19") {
		t.Fatal("coverage bridged an unverified gap")
	}
	st := SyncState{UsageFrom: "2025-01-01", UsageTo: "2026-11-19", RechargeFrom: "2024-01-01", RechargeTo: "2024-12-31"}
	clipCoverage(&st, "2025-10-18")
	if st.UsageFrom != "2025-10-18" || st.RechargeFrom != "" || st.RechargeTo != "" {
		t.Fatal("retention left proof for deleted data")
	}
}

func TestAbsorbInventoryAndTemporaryZeroStorage(t *testing.T) {
	pool, scope := absorbStore(t)
	ctx := t.Context()
	started := time.Now().Truncate(time.Microsecond)
	today := businesstime.DateAt(started)
	st := SyncState{Status: "running", StartedAt: &started, UserCount: 9, Warnings: []string{}}
	if err := publish(ctx, pool, scope, func(tx pgx.Tx) error { return writeSync(ctx, tx, scope, st) }); err != nil {
		t.Fatal(err)
	}
	if err := publishUsage(ctx, pool, scope, shift(today, -1), []DailyUsage{{SiteUserID: "deleted-customer", Requests: 1, Cost: 4}}, st); err != nil {
		t.Fatal(err)
	}
	if err := publishInventory(ctx, pool, scope, nil, started); err != nil {
		t.Fatal(err)
	}
	var profiles, history int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM user_activity_profiles),(SELECT count(*) FROM user_activity_daily)`).Scan(&profiles, &history); err != nil || profiles != 0 || history != 1 {
		t.Fatal("complete zero inventory erased history or retained profiles")
	}
	s := NewService(NewRepository(pool), nil, nil, true)
	defer s.cancel()
	summary, err := s.Summary(ctx, scope, 2)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Sync.UserCount != 0 {
		t.Fatalf("inventory balance and user count came from different publications: count=%d", summary.Sync.UserCount)
	}
	st.Status = "failed"
	st.TodayUsageObservedAt = &started
	if err := publishUsage(ctx, pool, scope, today, nil, st); err != nil {
		t.Fatal(err)
	}
	summary, err = s.Summary(ctx, scope, 2)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Today.Cost == nil || *summary.Today.Cost != 0 || summary.Today.Requests == nil || *summary.Today.Requests != 0 {
		t.Fatal("confirmed temporary zero lost when another sync step failed")
	}
	yesterday := started.AddDate(0, 0, -1)
	st.TodayUsageObservedAt = &yesterday
	if err := publish(ctx, pool, scope, func(tx pgx.Tx) error { return writeSync(ctx, tx, scope, st) }); err != nil {
		t.Fatal(err)
	}
	summary, err = s.Summary(ctx, scope, 2)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Today.Cost != nil || summary.Today.Requests != nil {
		t.Fatal("yesterday observation falsely proved today's zero")
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_accounts SET created_at=created_at+interval '1 second' WHERE id=$1`, scope.AdminAccountID); err != nil {
		t.Fatal(err)
	}
	if err := publishInventory(ctx, pool, scope, nil, started); !errors.Is(err, workspacescope.ErrChanged) {
		t.Fatal("old task could publish to a recreated workspace")
	}
}

type absorbQueryPause struct {
	enabled          atomic.Bool
	entered, release chan struct{}
}

func (p *absorbQueryPause) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT status,") && p.enabled.CompareAndSwap(true, false) {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (*absorbQueryPause) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestAbsorbGETDoesNotRelabelRecreatedWorkspaceData(t *testing.T) {
	pause := &absorbQueryPause{entered: make(chan struct{}), release: make(chan struct{})}
	pool, scope := absorbStore(t, func(c *pgxpool.Config) { c.ConnConfig.Tracer = pause })
	defer func() {
		select {
		case <-pause.release:
		default:
			close(pause.release)
		}
	}()
	s := NewService(NewRepository(pool), nil, nil, true)
	defer s.cancel()
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)
	query := url.Values{"workspaceAdminAccountId": {scope.AdminAccountID}, "workspaceCreatedAt": {scope.CreatedAt}}
	req := httptest.NewRequest("GET", "/api/user-activity/summary?"+query.Encode(), nil).WithContext(authctx.WithUserID(t.Context(), scope.UserID))
	response := httptest.NewRecorder()
	done := make(chan struct{})
	pause.enabled.Store(true)
	go func() { defer close(done); mux.ServeHTTP(response, req) }()
	select {
	case <-pause.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("read did not reach expected storage boundary")
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `UPDATE admin_accounts SET created_at=created_at+interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO user_activity_sync_states(user_id,admin_account_id,status,user_count) VALUES($1,$2,'idle',123)`, scope.UserID, scope.AdminAccountID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	close(pause.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("read did not terminate")
	}
	if response.Code == 409 {
		return
	}
	if response.Code != 200 {
		t.Fatalf("unexpected read status %d", response.Code)
	}
	var body Summary
	if json.Unmarshal(response.Body.Bytes(), &body) != nil {
		t.Fatal("invalid summary response")
	}
	if body.Sync.UserCount == 123 {
		t.Fatal("new generation data returned under the old workspace scope")
	}
}
