package httpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/config"
	"transithub/backend/internal/database/migrations"
	"transithub/backend/internal/modules/admin_accounts"
	"transithub/backend/internal/modules/dashboard"
	"transithub/backend/internal/modules/reports"
	"transithub/backend/internal/modules/sales"
	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/modules/user_activity"
	"transithub/backend/internal/modules/vip"
)

var absorbReadRoutes = []string{
	"/api/user-activity/summary", "/api/user-activity/sync", "/api/user-activity/users", "/api/user-activity/users/isolated-customer",
	"/api/vip/summary", "/api/vip/search", "/api/sales/summary", "/api/sales/entries",
	"/api/reports/daily/preview", "/api/dashboard/operating-insights",
}

type absorbWriteRoute struct {
	method, path string
	fields       map[string]any
}

func absorbWriteRoutes() []absorbWriteRoute {
	return []absorbWriteRoute{
		{http.MethodPost, "/api/user-activity/sync", nil},
		{http.MethodPost, "/api/vip/watch", map[string]any{"siteUserId": "isolated-customer", "note": "隔离测试"}},
		{http.MethodPost, "/api/vip/unwatch", map[string]any{"siteUserId": "isolated-customer"}},
		{http.MethodPost, "/api/vip/dismissed/restore", nil},
		{http.MethodPut, "/api/vip/settings", map[string]any{"dailyTop": 20, "lifetimeTop": 20, "alertDays": 2, "alertBalance": 20, "alertsEnabled": false, "botIds": []string{}, "reminderText": "", "emptyReminderText": ""}},
		{http.MethodPost, "/api/sales/exclusions", map[string]any{"entries": []any{map[string]any{"siteUserId": "isolated-customer", "day": "2026-11-19", "channel": "payment"}}}},
		{http.MethodPost, "/api/sales/exclusions/delete", map[string]any{"entries": []any{map[string]any{"siteUserId": "isolated-customer", "day": "2026-11-19", "channel": "payment"}}}},
		{http.MethodPut, "/api/sales/settings", map[string]any{"excludedChannels": []string{}}},
		{http.MethodPost, "/api/reports/daily/test", nil},
		{http.MethodPut, "/api/settings/strategy", map[string]any{"dailyReportEnabled": false}},
	}
}

func absorbRequestBody(route absorbWriteRoute, workspace, created string) map[string]any {
	result := map[string]any{"workspaceAdminAccountId": workspace, "workspaceCreatedAt": created}
	for key, value := range route.fields {
		result[key] = value
	}
	return result
}

func absorbScopePath(path, workspace, created string) string {
	return path + "?" + url.Values{"workspaceAdminAccountId": {workspace}, "workspaceCreatedAt": {created}}.Encode()
}

func absorbCreated(t *testing.T, f *notificationAPIFixture, id string) string {
	t.Helper()
	var created time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT created_at FROM admin_accounts WHERE id=$1`, id).Scan(&created); err != nil {
		t.Fatal("cannot read isolated workspace creation timestamp")
	}
	return created.Format(time.RFC3339Nano)
}

func absorbError(t *testing.T, w *httptest.ResponseRecorder, status int, fragment string) {
	t.Helper()
	body := notificationResponse(t, w, status)
	for _, field := range []string{"message", "step", "impact", "action"} {
		if value, ok := body[field].(string); !ok || strings.TrimSpace(value) == "" {
			t.Fatalf("new API error lacks useful %s: %s", field, w.Body.String())
		}
	}
	if _, ok := body["retryable"].(bool); !ok {
		t.Fatal("new API error must state whether a retry is possible")
	}
	if fragment != "" && !strings.Contains(w.Body.String(), fragment) {
		t.Fatalf("error lacks concrete reason %q: %s", fragment, w.Body.String())
	}
}

func absorbAPIFixture(t *testing.T) *notificationAPIFixture {
	t.Helper()
	var schema string
	// Register before the existing fixture, so this runs after its pool/schema cleanup.
	t.Cleanup(func() {
		if schema == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		base, err := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			t.Error("cannot verify isolated API schema cleanup")
			return
		}
		defer base.Close()
		var exists bool
		if err := base.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists); err != nil || exists {
			t.Error("isolated API schema cleanup was incomplete")
		}
	})
	f := newNotificationAPIFixture(t)
	if err := f.pool.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal("cannot identify exact isolated API schema")
	}
	return f
}

func absorbForbidOutboundHTTP(t *testing.T) *atomic.Int32 {
	t.Helper()
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	calls := &atomic.Int32{}
	transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("isolated API regression forbids outbound HTTP")
	}
	http.DefaultTransport = transport
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		http.DefaultTransport = original
	})
	return calls
}

func TestAbsorbAPIAllNewRoutesUseHubAuthentication(t *testing.T) {
	server := New(config.Config{APIOnly: true, TicketUploadDir: t.TempDir()}, nil, nil)
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	check := func(method, path string) {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, request)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("new route %s %s bypassed Hub authentication: %d", method, path, w.Code)
		}
	}
	for _, path := range append(append([]string{}, absorbReadRoutes...), "/api/system/credentials/status") {
		check(http.MethodGet, path)
	}
	for _, route := range absorbWriteRoutes() {
		check(route.method, route.path)
	}
}

func TestAbsorbAPIOnlyPreservesOldWritesAndDoesNotInitializeAnalysisPostgres(t *testing.T) {
	calls := absorbForbidOutboundHTTP(t)
	f := absorbAPIFixture(t)
	created := absorbCreated(t, f, "workspace-a")
	for _, path := range absorbReadRoutes[:len(absorbReadRoutes)-1] {
		t.Run("uninitialized "+path, func(t *testing.T) {
			absorbError(t, f.request(t, http.MethodGet, absorbScopePath(path, "workspace-a", created), nil, true), http.StatusServiceUnavailable, "尚未初始化")
		})
	}
	for _, route := range absorbWriteRoutes() {
		t.Run("APIOnly "+route.path, func(t *testing.T) {
			absorbError(t, f.request(t, route.method, route.path, absorbRequestBody(route, "workspace-a", created), true), http.StatusConflict, "当前实例不提供这项操作")
		})
	}
	// The existing refresh strategy PUT retains its original APIOnly contract and needs no new scope fields.
	notificationResponse(t, f.request(t, http.MethodPut, "/api/settings/strategy", map[string]any{"enableRefreshInterval": true, "refreshInterval": 120}, true), http.StatusOK)
	var enabled bool
	var interval int
	if err := f.pool.QueryRow(t.Context(), `SELECT (settings->>'enableRefreshInterval')::boolean,(settings->>'refreshInterval')::integer FROM strategy_settings WHERE user_id='user' AND admin_account_id='workspace-a'`).Scan(&enabled, &interval); err != nil || !enabled || interval != 120 {
		t.Fatal("old APIOnly strategy write was rejected or changed its saved behavior")
	}
	for _, table := range []string{"user_activity_profiles", "user_activity_daily", "user_activity_recharge_daily", "user_activity_site_daily", "user_activity_sync_states", "vip_watch", "vip_settings", "vip_alert_states", "sales_excluded_entries", "sales_settings"} {
		var exists bool
		if err := f.pool.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil || exists {
			t.Fatalf("APIOnly request initialized new table %s", table)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("APIOnly new operations attempted outbound HTTP")
	}
}

func absorbInitializeAPI(t *testing.T, f *notificationAPIFixture) {
	t.Helper()
	for _, ensure := range []func(context.Context) error{
		func(ctx context.Context) error { return migrations.EnsureUserActivitySchema(ctx, f.pool) },
		func(ctx context.Context) error { return migrations.EnsureVIPSchema(ctx, f.pool) },
		func(ctx context.Context) error { return migrations.EnsureSalesSchema(ctx, f.pool) },
	} {
		if err := ensure(t.Context()); err != nil {
			t.Fatal("cannot initialize isolated analysis schema")
		}
	}
	// Preview checks this original table before resolving scope. Mismatched requests never read its columns.
	if _, err := f.pool.Exec(t.Context(), `CREATE TABLE dashboard_daily_stats(user_id text,admin_account_id text)`); err != nil {
		t.Fatal("cannot initialize original report dependency")
	}
}

func TestAbsorbAPIReadsRequireCurrentWorkspaceGenerationPostgres(t *testing.T) {
	f := absorbAPIFixture(t)
	absorbInitializeAPI(t, f)
	created := absorbCreated(t, f, "workspace-a")
	for _, path := range absorbReadRoutes {
		t.Run(path, func(t *testing.T) {
			for _, query := range []string{"", "?workspaceAdminAccountId=workspace-a", "?workspaceCreatedAt=" + url.QueryEscape(created)} {
				absorbError(t, f.request(t, http.MethodGet, path+query, nil, true), http.StatusBadRequest, "工作区")
			}
			for _, workspace := range []string{"workspace-b", "foreign-workspace"} {
				absorbError(t, f.request(t, http.MethodGet, absorbScopePath(path, workspace, absorbCreated(t, f, workspace)), nil, true), http.StatusConflict, "工作区已变化")
			}
			absorbError(t, f.request(t, http.MethodGet, absorbScopePath(path, "workspace-a", "2000-01-01T00:00:00Z"), nil, true), http.StatusConflict, "工作区已变化")
			absorbError(t, f.request(t, http.MethodGet, absorbScopePath(path, "workspace-a", created)+"&userId=other-user", nil, true), http.StatusBadRequest, "参数")
		})
	}
}

func TestAbsorbAPICredentialsStatusNeedsOnlyOrdinaryHubLoginPostgres(t *testing.T) {
	f := absorbAPIFixture(t)
	// No role, super-admin flag or current workspace is available in this ordinary Hub session.
	if _, err := f.pool.Exec(t.Context(), `UPDATE users SET current_admin_account_id='' WHERE id='user'`); err != nil {
		t.Fatal(err)
	}
	body := notificationResponse(t, f.request(t, http.MethodGet, "/api/system/credentials/status", nil, true), http.StatusOK)
	if len(body) != 3 || body["configured"] != false || body["mode"] != "legacy" || body["message"] == "" {
		t.Fatalf("credential status must expose only safe configuration metadata: %v", body)
	}
}

func TestAbsorbAPIReadsKeepOtherUsersAndWorkspacesPrivatePostgres(t *testing.T) {
	f := absorbAPIFixture(t)
	absorbInitializeAPI(t, f)
	created := absorbCreated(t, f, "workspace-a")
	for _, scope := range [][3]string{{"user", "workspace-a", "visible-local"}, {"user", "workspace-b", "hidden-other-workspace"}, {"other-user", "workspace-a", "hidden-other-user"}} {
		if _, err := f.pool.Exec(t.Context(), `WITH seeded AS (INSERT INTO user_activity_profiles VALUES($1,$2,$3,$3,$3,'user','active',10,NULL,NULL,now(),now()) RETURNING user_id,admin_account_id,site_user_id) INSERT INTO vip_watch(user_id,admin_account_id,site_user_id,kind) SELECT user_id,admin_account_id,site_user_id,'manual' FROM seeded`, scope[0], scope[1], scope[2]); err != nil {
			t.Fatal("cannot seed isolated analysis profiles")
		}
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO user_activity_recharge_daily VALUES($1,$2,$3,current_date-1,'payment',10,1)`, scope[0], scope[1], scope[2]); err != nil {
			t.Fatal("cannot seed isolated sales records")
		}
	}
	for _, path := range []string{"/api/user-activity/users", "/api/vip/summary", "/api/vip/search", "/api/sales/entries"} {
		w := f.request(t, http.MethodGet, absorbScopePath(path, "workspace-a", created), nil, true)
		notificationResponse(t, w, http.StatusOK)
		if !strings.Contains(w.Body.String(), "visible-local") || strings.Contains(w.Body.String(), "hidden-other-") {
			t.Fatalf("%s exposed another workspace/user or omitted the selected local customer: %s", path, w.Body.String())
		}
	}
	for _, hidden := range []string{"hidden-other-workspace", "hidden-other-user"} {
		w := f.request(t, http.MethodGet, absorbScopePath("/api/user-activity/users/"+hidden, "workspace-a", created), nil, true)
		absorbError(t, w, http.StatusNotFound, "当前工作区没有")
		if strings.Contains(w.Body.String(), hidden) {
			t.Fatal("unknown local customer response disclosed a foreign customer")
		}
	}
}

func TestAbsorbAPINewWritesRejectStaleScopeBeforeSavingOrSendingPostgres(t *testing.T) {
	f := absorbAPIFixture(t)
	absorbInitializeAPI(t, f)
	transport := &absorbRejectHTTP{}
	notifier := settings.NewService(&http.Client{Transport: transport}, settings.NewRepository(f.pool))
	notifier.SetAdminAccountResolver(admin_accounts.NewService(admin_accounts.NewRepository(f.pool)))
	salesService := sales.NewService(sales.NewRepository(f.pool), false)
	activityService := user_activity.NewService(user_activity.NewRepository(f.pool), nil, nil, false)
	vipService := vip.NewService(vip.NewRepository(f.pool), notifier, false)
	reportService := reports.NewService(reports.NewRepository(f.pool), dashboard.NewMetricsRepository(f.pool), salesService, notifier, nil, false)
	// Reassemble just the new handlers with writes enabled; never start schedulers or listeners.
	mux := http.NewServeMux()
	user_activity.RegisterRoutes(mux, activityService)
	vip.RegisterRoutes(mux, vipService)
	sales.RegisterRoutes(mux, salesService)
	reports.RegisterRoutes(mux, reportService)
	settings.RegisterRoutes(mux, notifier)
	f.server.mux = mux
	t.Cleanup(func() {
		_ = activityService.Shutdown(context.Background())
		_ = vipService.Shutdown(context.Background())
		_ = reportService.Shutdown(context.Background())
	})
	created := absorbCreated(t, f, "workspace-a")
	for _, route := range absorbWriteRoutes() {
		t.Run(route.path, func(t *testing.T) {
			for _, missing := range []string{"workspaceAdminAccountId", "workspaceCreatedAt"} {
				body := absorbRequestBody(route, "workspace-a", created)
				delete(body, missing)
				absorbError(t, f.request(t, route.method, route.path, body, true), http.StatusBadRequest, "工作区")
			}
			for _, scope := range [][2]string{{"workspace-a", "2000-01-01T00:00:00Z"}, {"workspace-b", absorbCreated(t, f, "workspace-b")}, {"foreign-workspace", absorbCreated(t, f, "foreign-workspace")}} {
				absorbError(t, f.request(t, route.method, route.path, absorbRequestBody(route, scope[0], scope[1]), true), http.StatusConflict, "工作区已变化")
			}
			body := absorbRequestBody(route, "workspace-a", created)
			body["userId"] = "other-user"
			absorbError(t, f.request(t, route.method, route.path, body, true), http.StatusBadRequest, "字段")
		})
	}
	for _, table := range []string{"user_activity_sync_states", "vip_watch", "vip_settings", "vip_alert_states", "sales_excluded_entries", "sales_settings", "strategy_settings", "notification_channel_settings"} {
		var count int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rejected scope wrote %s rows", table)
		}
	}
	if transport.calls != 0 {
		t.Fatal("rejected workspace scope attempted outbound HTTP")
	}
}

type absorbRejectHTTP struct{ calls int }

func (r *absorbRejectHTTP) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	return nil, errors.New("isolated API regression forbids outbound HTTP")
}
