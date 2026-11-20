package reports

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"transithub/backend/internal/modules/dashboard"
	"transithub/backend/internal/modules/sales"
	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/shared/businesstime"
	"transithub/backend/internal/shared/workspacescope"
)

type absorbReportTransport func(*http.Request) (*http.Response, error)

func (f absorbReportTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type absorbReportMetrics struct {
	err   error
	calls int
}

func (m *absorbReportMetrics) ListDailyStats(context.Context, string, string, string, string) ([]dashboard.DailySnapshot, error) {
	m.calls++
	return nil, m.err
}

type absorbReportSales struct{}

func (absorbReportSales) Summary(context.Context, sales.Scope, sales.DayRange) (sales.Summary, error) {
	return sales.Summary{}, nil
}
func absorbRedis(t *testing.T) *redis.Client {
	t.Helper()
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("explicit isolated Redis verification requires TEST_REDIS_URL")
	}
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal("invalid explicit Redis configuration")
	}
	client := redis.NewClient(options)
	if client.Ping(t.Context()).Err() != nil {
		client.Close()
		t.Fatal("explicit Redis unavailable")
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestAbsorbReportQualityAndDates(t *testing.T) {
	now := time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC)
	day := "2026-11-19"
	date, _, _ := businesstime.Bounds(day)
	p := func(v float64) *float64 { return &v }
	row := dashboard.DailySnapshot{Date: date, SettlementStatus: "final", CostQualityMode: "retained", TodayProfit: p(100), TodayPurchase: p(30), OperatingCost: p(5), AdjustedNetProfit: p(65), ObservedAt: &now}
	section := businessSection(day, []dashboard.DailySnapshot{row})
	if !section.Coverage.Complete || !strings.Contains(section.Text, "65.00") || !strings.Contains(section.Text, "沿用已核实成本") {
		t.Fatal("report recomputed or hid the existing retained final amounts")
	}
	for _, bad := range []string{"partial", "provisional", "missing", "nonfinite", "duplicate"} {
		t.Run(bad, func(t *testing.T) {
			copy := row
			rows := []dashboard.DailySnapshot{copy}
			switch bad {
			case "partial":
				rows[0].CostQualityMode = "partial"
			case "provisional":
				rows[0].SettlementStatus = "provisional"
			case "missing":
				rows[0].TodayProfit = nil
			case "nonfinite":
				rows[0].AdjustedNetProfit = p(math.Inf(1))
			case "duplicate":
				rows = append(rows, copy)
			}
			if businessSection(day, rows).Coverage.Complete {
				t.Fatal("incomplete original settlement was reported as final")
			}
		})
	}
	if businessSection(day, nil).Coverage.Complete || !strings.Contains(usageSection(day, localActivity{}).Text, "不能按0处理") {
		t.Fatal("unknown source became a valid zero")
	}
	empty := usageSection(day, localActivity{UsageFrom: day, UsageTo: day})
	if !empty.Coverage.Complete || !strings.Contains(empty.Text, "请求0次") {
		t.Fatal("confirmed covered empty usage was lost")
	}
	customer := customerSection(day, "2026-11-20", localActivity{ObservedAt: &now, ProfileCount: 0})
	if !strings.Contains(customer.Text, "不是昨日余额") || customer.Date != businesstime.DateAt(now) {
		t.Fatal("latest balance was mislabeled as yesterday")
	}
}

func TestAbsorbReportRedisCASAndGeneration(t *testing.T) {
	client := absorbRedis(t)
	scope := Scope{UserID: "absorb-reports-" + time.Now().Format("150405.000000000"), AdminAccountID: "fixture", CreatedAt: time.Now().Format(time.RFC3339Nano)}
	day := businesstime.Today()
	key := reportKey(scope, day)
	ctx := t.Context()
	t.Cleanup(func() { client.Del(context.Background(), key) })
	store := &dailyStore{client}
	marker := ScheduledResult{AttemptID: "attempt-one", At: time.Now(), Status: "sending", Name: "isolated recipient"}
	raw, _ := json.Marshal(marker)
	var registered atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.register(ctx, key, string(raw))
			if err != nil {
				t.Error(err)
			}
			if ok {
				registered.Add(1)
			}
		}()
	}
	wg.Wait()
	if registered.Load() != 1 {
		t.Fatal("automatic send admitted more than one attempt")
	}
	latest, err := store.latest(ctx, scope, day)
	if err != nil || latest == nil || latest.Status != "unknown" {
		t.Fatal("abandoned sending marker was treated as confirmed")
	}
	client.PExpire(ctx, key, 5*time.Minute)
	before := client.PTTL(ctx, key).Val()
	marker.Status = "sent"
	if err := store.replace(ctx, key, string(raw), marker); err != nil {
		t.Fatal(err)
	}
	after := client.PTTL(ctx, key).Val()
	if after <= 0 || after > before {
		t.Fatal("result replacement extended or removed original TTL")
	}
	next := marker
	next.AttemptID = "new-attempt"
	next.Status = "failed"
	if err := store.replace(ctx, key, string(raw), next); err != nil {
		t.Fatal(err)
	}
	latest, err = store.latest(ctx, scope, day)
	if err != nil || latest.Status != "sent" {
		t.Fatal("late result overwrote a newer marker")
	}
	newer := scope
	newer.CreatedAt += "-new"
	if reportKey(newer, day) == key {
		t.Fatal("recreated workspace reuses old dedupe key")
	}
	if value, err := store.latest(ctx, newer, day); err != nil || value != nil {
		t.Fatal("new generation inherited an old send result")
	}
	client.Del(ctx, key)
	if err := store.replace(ctx, key, string(raw), marker); err != nil {
		t.Fatal(err)
	}
	if client.Exists(ctx, key).Val() != 0 {
		t.Fatal("late result resurrected a deleted key")
	}
	client.Set(ctx, key, `{"status":"sent"}`, time.Minute)
	if _, err := store.latest(ctx, scope, day); !errors.Is(err, ErrReportMarker) {
		t.Fatal("malformed marker was accepted")
	}
}

func TestAbsorbReportSavedTargetManualAndScheduled(t *testing.T) {
	pool, scope := absorbStore(t)
	client := absorbRedis(t)
	ctx := t.Context()
	now := time.Now()
	day := businesstime.DateAt(now)
	key := reportKey(scope, day)
	t.Cleanup(func() { client.Del(context.Background(), key) })
	if err := sales.NewRepository(pool).EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE dashboard_daily_stats(id text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO notification_channel_settings VALUES($1,$2,$3::jsonb,now());`, scope.UserID, scope.AdminAccountID, `{"telegram":[{"id":"saved","name":"isolated recipient","enabled":true,"botToken":"fixture-token","chatId":"fixture-chat"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO strategy_settings VALUES($1,$2,'{"dailyReportEnabled":false,"dailyReportTime":"00:00","dailyReportBotIds":["saved"]}',now())`, scope.UserID, scope.AdminAccountID); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var scheduled atomic.Bool
	var admitted atomic.Bool
	notifier := settings.NewService(&http.Client{Transport: absorbReportTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if scheduled.Load() {
			admitted.Store(client.Exists(ctx, key).Val() == 1)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "fixture-chat") {
			t.Error("send did not use the saved target")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)), Request: r}, nil
	})}, settings.NewRepository(pool))
	metrics := &absorbReportMetrics{}
	s := NewService(NewRepository(pool), metrics, absorbReportSales{}, notifier, client, false)
	s.now = func() time.Time { return now }
	defer s.Shutdown(context.Background())
	manual, err := s.Test(ctx, scope)
	if err != nil || manual.Sent != 1 || calls.Load() != 1 {
		t.Fatal("saved target test failed while automatic switch was off")
	}
	if client.Exists(ctx, key).Val() != 0 {
		t.Fatal("manual test consumed automatic daily dedupe")
	}
	if err := s.runScheduled(ctx, scope); err != nil || calls.Load() != 1 {
		t.Fatal("disabled automatic schedule sent")
	}
	if _, err := pool.Exec(ctx, `UPDATE strategy_settings SET settings=jsonb_set(settings,'{dailyReportEnabled}','true')`); err != nil {
		t.Fatal(err)
	}
	scheduled.Store(true)
	// Two schedulers can build concurrently; admission must still happen once.
	// Avoid racing the metrics instrumentation, which is unrelated to sender concurrency.
	document := Document{BusinessDate: shift(day, -1), Text: "isolated report"}
	var wg sync.WaitGroup
	var admittedCount atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prepared, err := s.prepare(ctx, scope, document, false, day)
			if err != nil {
				t.Error(err)
			}
			if prepared != nil {
				admittedCount.Add(1)
				delivery := notifier.SendFrozenTelegramWithResult(ctx, prepared.Target, document.Text, settings.NotificationTemplateFormatText)
				marker := prepared.Marker
				marker.Status = delivery.Status
				marker.Attempted = delivery.Attempted
				if err := s.store.replace(ctx, prepared.Key, prepared.Original, marker); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if admittedCount.Load() != 1 || calls.Load() != 2 || !admitted.Load() {
		t.Fatal("scheduled send was duplicated or occurred before Redis admission")
	}
	if err := s.runScheduled(ctx, scope); err != nil || calls.Load() != 2 {
		t.Fatal("same sending date was sent again")
	}
	if _, err := pool.Exec(ctx, `UPDATE admin_accounts SET created_at=created_at+interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.prepare(ctx, scope, document, true, ""); !errors.Is(err, workspacescope.ErrChanged) {
		t.Fatal("old request sent with recreated workspace configuration")
	}
	if calls.Load() != 2 {
		t.Fatal("stale workspace contacted Telegram")
	}
	api := NewService(s.repo, metrics, absorbReportSales{}, notifier, client, true)
	defer api.Shutdown(context.Background())
	if _, err := api.Test(ctx, scope); !errors.Is(err, settings.ErrDailyReportWritesDisabled) {
		t.Fatal("API-only instance allowed sending")
	}
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Test(ctx, scope); !errors.Is(err, ErrStopped) {
		t.Fatal("stopped service accepted a send")
	}
}

type absorbCancelCommit struct{ armed atomic.Bool }

func (f *absorbCancelCommit) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.EqualFold(strings.TrimSpace(data.SQL), "commit") && f.armed.Swap(false) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		return cancelled
	}
	return ctx
}
func (*absorbCancelCommit) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestAbsorbReportCommitFailureNeverContactsTelegram(t *testing.T) {
	fault := &absorbCancelCommit{}
	pool, scope := absorbStore(t, func(cfg *pgxpool.Config) { cfg.ConnConfig.Tracer = fault })
	client := absorbRedis(t)
	ctx := t.Context()
	now := time.Now()
	key := reportKey(scope, businesstime.DateAt(now))
	t.Cleanup(func() { client.Del(context.Background(), key) })
	if err := sales.NewRepository(pool).EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE dashboard_daily_stats(id text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO notification_channel_settings VALUES($1,$2,$3::jsonb,now())`, scope.UserID, scope.AdminAccountID, `{"telegram":[{"id":"saved","name":"isolated recipient","enabled":true,"botToken":"fixture-token","chatId":"fixture-chat"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO strategy_settings VALUES($1,$2,'{"dailyReportEnabled":true,"dailyReportTime":"00:00","dailyReportBotIds":["saved"]}',now())`, scope.UserID, scope.AdminAccountID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	notifier := settings.NewService(&http.Client{Transport: absorbReportTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected test request")
	})}, settings.NewRepository(pool))
	s := NewService(NewRepository(pool), &absorbReportMetrics{}, absorbReportSales{}, notifier, client, false)
	s.now = func() time.Time { return now }
	defer s.Shutdown(context.Background())
	fault.armed.Store(true)
	if err := s.runScheduled(ctx, scope); err == nil {
		t.Fatal("unconfirmed admission transaction was reported successful")
	}
	if calls != 0 {
		t.Fatal("Telegram was contacted after an unconfirmed admission transaction")
	}
	marker, err := s.store.latest(ctx, scope, businesstime.DateAt(now))
	if err != nil || marker == nil || marker.Status != "failed" || marker.Attempted {
		t.Fatal("failed admission lost the conservative daily marker")
	}
	if err := s.runScheduled(ctx, scope); err != nil || calls != 0 {
		t.Fatal("failed transaction registration was automatically retried")
	}
}

type absorbReportActivityTrace struct{ calls atomic.Int32 }

func (v *absorbReportActivityTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(strings.ToLower(data.SQL), "from user_activity_") {
		v.calls.Add(1)
	}
	return ctx
}
func (*absorbReportActivityTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type absorbReportCountedSales struct{ calls int }

func (v *absorbReportCountedSales) Summary(context.Context, sales.Scope, sales.DayRange) (sales.Summary, error) {
	v.calls++
	return sales.Summary{}, nil
}

func TestAbsorbReportRecordedDaySkipsAllAggregation(t *testing.T) {
	trace := &absorbReportActivityTrace{}
	pool, scope := absorbStore(t, func(cfg *pgxpool.Config) { cfg.ConnConfig.Tracer = trace })
	client := absorbRedis(t)
	ctx := t.Context()
	now := time.Now()
	day := businesstime.DateAt(now)
	key := reportKey(scope, day)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Del(cleanup, key).Err(); err != nil {
			t.Error("cannot clean exact report test marker")
		}
		if count, err := client.Exists(cleanup, key).Result(); err != nil || count != 0 {
			t.Error("exact report test marker remains after cleanup")
		}
	})
	if err := sales.NewRepository(pool).EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE dashboard_daily_stats(id text);`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO notification_channel_settings VALUES($1,$2,'{"telegram":[{"id":"saved","enabled":true,"botToken":"fixture-token","chatId":"fixture-chat"}]}',now());`, scope.UserID, scope.AdminAccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO strategy_settings VALUES($1,$2,'{"dailyReportEnabled":true,"dailyReportTime":"00:00","dailyReportBotIds":["saved"]}',now())`, scope.UserID, scope.AdminAccountID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	notifier := settings.NewService(&http.Client{Transport: absorbReportTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`)), Request: r}, nil
	})}, settings.NewRepository(pool))
	metrics := &absorbReportMetrics{}
	sale := &absorbReportCountedSales{}
	service := NewService(NewRepository(pool), metrics, sale, notifier, client, false)
	service.now = func() time.Time { return now }
	defer service.Shutdown(context.Background())
	if err := service.runScheduled(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || metrics.calls != 1 || sale.calls != 1 || trace.calls.Load() == 0 {
		t.Fatal("first schedule did not aggregate and send exactly once")
	}
	beforeActivity := trace.calls.Load()
	beforeTTL := client.PTTL(ctx, key).Val()
	if err := service.runScheduled(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || metrics.calls != 1 || sale.calls != 1 || trace.calls.Load() != beforeActivity {
		t.Fatal("recorded day rebuilt metrics, sales or activity, or sent again")
	}
	if after := client.PTTL(ctx, key).Val(); after <= 0 || after > beforeTTL {
		t.Fatal("recorded-day check extended or removed marker TTL")
	}
	// Every existing marker, even malformed or incomplete, blocks automatic rebuilds.
	for _, raw := range []string{`{"status":"unknown"}`, `{"status":"failed"}`, `{"status":"sending"}`, `not-json`} {
		if err := client.Set(ctx, key, raw, time.Hour).Err(); err != nil {
			t.Fatal(err)
		}
		if err := service.runScheduled(ctx, scope); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || metrics.calls != 1 || sale.calls != 1 || trace.calls.Load() != beforeActivity {
			t.Fatal("existing marker allowed automatic aggregation or resend")
		}
	}
	preview, err := service.Preview(ctx, scope)
	if err != nil || preview.ScheduledWarning == "" || metrics.calls != 2 || sale.calls != 2 || trace.calls.Load() <= beforeActivity {
		t.Fatal("preview did not build or diagnose malformed automatic marker")
	}
	manual, err := service.Test(ctx, scope)
	if err != nil || manual.Sent != 1 || calls != 2 || metrics.calls != 3 || sale.calls != 3 {
		t.Fatal("automatic marker blocked an explicit manual test")
	}
	beforeActivity = trace.calls.Load()
	service.store.client = nil
	if err := service.runScheduled(ctx, scope); !errors.Is(err, ErrReportStore) {
		t.Fatal("unavailable Redis did not stop automatic sending")
	}
	if calls != 2 || metrics.calls != 3 || sale.calls != 3 || trace.calls.Load() != beforeActivity {
		t.Fatal("unavailable Redis still aggregated or sent an automatic report")
	}
}
