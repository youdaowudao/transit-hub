package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/config"
	"transithub/backend/internal/modules/admin_accounts"
	"transithub/backend/internal/modules/notifications"
	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/modules/upstream"
)

// These tests use the existing authenticated Server entry point, no listener,
// workers, Redis or external upstream. They compile on the stage A baseline.
type notificationAPIFixture struct {
	pool   *pgxpool.Pool
	server *Server
}

const notificationFixtureToken = "isolated-notification-http-fixture"

func newNotificationAPIFixture(t *testing.T) *notificationAPIFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires explicit TEST_DATABASE_URL for isolated PostgreSQL fixture")
	}
	base, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal("cannot open explicit test database")
	}
	schema := fmt.Sprintf("notification_api_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := base.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		base.Close()
		t.Fatal("cannot create isolated notification API schema")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("cannot parse explicit test database")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal("cannot bind isolated notification API schema")
	}
	f := &notificationAPIFixture{pool: pool}
	t.Cleanup(func() {
		if f.server != nil {
			_ = f.server.Shutdown(context.Background())
		}
		pool.Close()
		if _, err := base.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("cannot clean isolated notification API schema")
		}
		base.Close()
	})
	_, err = pool.Exec(t.Context(), `CREATE TABLE users(id text PRIMARY KEY,current_admin_account_id text NOT NULL DEFAULT '');
CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL,platform text NOT NULL DEFAULT 'sub2api',base_url text NOT NULL DEFAULT 'https://fixture.invalid',identity text NOT NULL DEFAULT '',display_name text NOT NULL DEFAULT '隔离工作区',auth_method text NOT NULL DEFAULT 'password',last_used_at timestamptz,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE auth_sessions(id text PRIMARY KEY,"tokenHash" text NOT NULL,"userId" text NOT NULL,"expiresAt" timestamptz NOT NULL);
INSERT INTO users(id,current_admin_account_id) VALUES('user','workspace-a'),('other-user','foreign-workspace');
INSERT INTO admin_accounts(id,user_id) VALUES('workspace-a','user'),('workspace-b','user'),('foreign-workspace','other-user')`)
	if err != nil {
		t.Fatal("cannot prepare isolated auth/workspace fixtures")
	}
	hash := sha256.Sum256([]byte(notificationFixtureToken))
	if _, err := pool.Exec(t.Context(), `INSERT INTO auth_sessions VALUES('fixture',$1,'user',now()+interval '1 hour')`, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal("cannot prepare isolated auth session")
	}
	if err := upstream.NewRepository(pool).EnsureSchema(t.Context()); err != nil {
		t.Fatal("cannot prepare original upstream schema")
	}
	if err := notifications.NewRepository(pool).EnsureSchema(t.Context()); err != nil {
		t.Fatal("cannot prepare notification schema")
	}
	if err := settings.NewRepository(pool).EnsureSchema(t.Context()); err != nil {
		t.Fatal("cannot prepare strategy schema")
	}
	f.server = New(config.Config{APIOnly: true, TicketUploadDir: t.TempDir()}, pool, nil)
	return f
}

func (f *notificationAPIFixture) request(t *testing.T, method, path string, body any, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if authenticated {
		req.Header.Set("Authorization", "Bearer "+notificationFixtureToken)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, req)
	return w
}

func notificationResponse(t *testing.T, w *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	if w.Code != status {
		t.Fatalf("notification API HTTP status=%d, want %d; body=%s", w.Code, status, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal("notification API must return JSON")
	}
	return body
}

func (f *notificationAPIFixture) publish(t *testing.T, workspace string, level notifications.Level, key string, created time.Time) string {
	t.Helper()
	user := "user"
	if workspace == "foreign-workspace" {
		user = "other-user"
	}
	id, inserted, err := notifications.NewService(notifications.NewRepository(f.pool)).Publish(t.Context(), notifications.NoticeInput{
		UserID: user, AdminAccountID: workspace, Kind: "fixture.fact", EventKey: key, SubjectType: "fixture", SubjectID: key, Level: level,
		DiscoveredAt: created, Content: notifications.Content{Title: "隔离通知 " + key, Summary: "这条通知用于验证本地状态，不会修改主站", Details: []notifications.Detail{}},
	})
	if err != nil || !inserted {
		t.Fatal("cannot seed isolated notification fact")
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE site_notifications SET created_at=$2 WHERE id=$1`, id, created); err != nil {
		t.Fatal("cannot set deterministic fixture order")
	}
	return id
}

func notificationScopePath(path string) string { return path + "?workspaceAdminAccountId=workspace-a" }

func TestNotificationAPIAuthenticationAndExplicitWorkspacePostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	for _, path := range []string{"/api/notifications/summary", "/api/notifications/", "/api/notifications/preferences", "/api/notifications/fixture"} {
		w := f.request(t, http.MethodGet, notificationScopePath(path), nil, false)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("notification route %s must use existing authentication; got %d", path, w.Code)
		}
	}
	for _, path := range []string{"/api/notifications/summary", "/api/notifications/", "/api/notifications/preferences", "/api/notifications/fixture"} {
		body := notificationResponse(t, f.request(t, http.MethodGet, path, nil, true), http.StatusBadRequest)
		assertNotificationError(t, body, "")
		body = notificationResponse(t, f.request(t, http.MethodGet, path+"?workspaceAdminAccountId=workspace-b", nil, true), http.StatusConflict)
		assertNotificationError(t, body, "workspace-b")
	}
	id := f.publish(t, "foreign-workspace", notifications.LevelUrgent, "foreign", time.Now())
	body := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/"+id), nil, true), http.StatusNotFound)
	assertNotificationError(t, body, "workspace-a")
	if strings.Contains(fmt.Sprint(body), "foreign-workspace") || strings.Contains(fmt.Sprint(body), "隔离通知 foreign") {
		t.Fatal("foreign notice facts must not be disclosed")
	}
}

func assertNotificationError(t *testing.T, body map[string]any, workspace string) {
	t.Helper()
	for _, field := range []string{"message", "detail", "next"} {
		if text, ok := body[field].(string); !ok || strings.TrimSpace(text) == "" {
			t.Fatalf("notification error lacks plain detailed %s", field)
		}
	}
	if _, ok := body["retryable"].(bool); !ok {
		t.Fatal("notification error lacks retry decision")
	}
	if body["workspaceAdminAccountId"] != workspace {
		t.Fatal("notification error does not identify requested workspace")
	}
}

func TestNotificationAPISummaryKeepsReadUrgentAndBoundedOldestCandidatesPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := make([]string, 24)
	for i := range ids {
		ids[i] = f.publish(t, "workspace-a", notifications.LevelUrgent, fmt.Sprintf("urgent-%02d", i), start.Add(time.Duration(i)*time.Second))
	}
	for _, level := range []notifications.Level{notifications.LevelNormal, notifications.LevelAttention, notifications.LevelImportant} {
		f.publish(t, "workspace-a", level, fmt.Sprintf("level-%d", level), start)
	}
	f.publish(t, "workspace-b", notifications.LevelUrgent, "other-workspace", start)
	f.publish(t, "foreign-workspace", notifications.LevelUrgent, "other-user", start)
	if _, err := f.pool.Exec(t.Context(), `UPDATE site_notifications SET read_at=now() WHERE id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	body := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true), http.StatusOK)
	if body["workspaceAdminAccountId"] != "workspace-a" || body["unreadCount"] != float64(26) || body["pendingCriticalCount"] != float64(24) || body["intervalSeconds"] != float64(300) {
		t.Fatal("summary must preserve accurate scoped counts and default local interval")
	}
	active := body["active"].(map[string]any)
	critical := active["critical"].(map[string]any)
	items := critical["items"].([]any)
	if len(items) != 20 || critical["totalCount"] != float64(24) || items[0].(map[string]any)["id"] != ids[0] || items[19].(map[string]any)["id"] != ids[19] {
		t.Fatal("oldest unconfirmed urgent notices, including read ones, must remain in bounded summary")
	}
	cursor, ok := critical["nextCursor"].(string)
	if !ok || cursor == "" {
		t.Fatal("bounded summary needs continuation cursor")
	}
	body = notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary")+"&criticalCursor="+url.QueryEscape(cursor), nil, true), http.StatusOK)
	critical = body["active"].(map[string]any)["critical"].(map[string]any)
	items = critical["items"].([]any)
	if len(items) != 4 || items[0].(map[string]any)["id"] != ids[20] || critical["totalCount"] != float64(24) {
		t.Fatal("summary page must continue without dropping old pending count")
	}
	for _, query := range []string{"&attentionCursor=" + url.QueryEscape(cursor), "&criticalCursor=" + strings.Repeat("x", 513)} {
		notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary")+query, nil, true), http.StatusBadRequest)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE users SET current_admin_account_id='workspace-b' WHERE id='user'`); err != nil {
		t.Fatal(err)
	}
	notificationResponse(t, f.request(t, http.MethodGet, "/api/notifications/summary?workspaceAdminAccountId=workspace-b&criticalCursor="+url.QueryEscape(cursor), nil, true), http.StatusBadRequest)
}

func TestNotificationAPIProgressAtomicLevelsAndMonotonicStatePostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	ids := map[notifications.Level]string{}
	for level := notifications.LevelNormal; level <= notifications.LevelUrgent; level++ {
		ids[level] = f.publish(t, "workspace-a", level, fmt.Sprintf("progress-%d", level), time.Now())
	}
	foreign := f.publish(t, "foreign-workspace", notifications.LevelUrgent, "foreign-progress", time.Now())
	otherWorkspace := f.publish(t, "workspace-b", notifications.LevelUrgent, "other-workspace-progress", time.Now())
	write := func(action string, list []string, status int) {
		t.Helper()
		notificationResponse(t, f.request(t, http.MethodPost, "/api/notifications/progress", map[string]any{"workspaceAdminAccountId": "workspace-a", "ids": list, "action": action}, true), status)
	}
	for _, attempt := range []struct {
		action string
		ids    []string
		status int
	}{
		{"ack", []string{ids[4], ids[1]}, http.StatusBadRequest}, {"read", []string{ids[4], "missing"}, http.StatusNotFound}, {"present", []string{ids[2], ids[3]}, http.StatusBadRequest}, {"dismiss", []string{ids[3], ids[4]}, http.StatusBadRequest}, {"read", []string{ids[4], ids[4]}, http.StatusBadRequest},
		{"ack", []string{ids[4], foreign}, http.StatusNotFound}, {"ack", []string{ids[4], otherWorkspace}, http.StatusNotFound},
	} {
		write(attempt.action, attempt.ids, attempt.status)
	}
	var changed int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM site_notifications WHERE read_at IS NOT NULL OR reminder_completed_at IS NOT NULL`).Scan(&changed); err != nil || changed != 0 {
		t.Fatal("invalid grouped progress must leave every original value unchanged")
	}
	write("read", []string{ids[1], ids[2], ids[3], ids[4]}, http.StatusOK)
	var read, completed *time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT read_at,reminder_completed_at FROM site_notifications WHERE id=$1`, ids[2]).Scan(&read, &completed); err != nil || read == nil || completed != nil {
		t.Fatal("reading attention must not stand in for actual presentation")
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT read_at,reminder_completed_at FROM site_notifications WHERE id=$1`, ids[3]).Scan(&read, &completed); err != nil || read == nil || completed == nil {
		t.Fatal("reading important must complete its reminder")
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT read_at,reminder_completed_at FROM site_notifications WHERE id=$1`, ids[4]).Scan(&read, &completed); err != nil || read == nil || completed != nil {
		t.Fatal("reading urgent must not confirm its reminder")
	}
	firstRead := *read
	write("ack", []string{ids[4]}, http.StatusOK)
	write("present", []string{ids[2]}, http.StatusOK)
	write("dismiss", []string{ids[3]}, http.StatusOK)
	if err := f.pool.QueryRow(t.Context(), `SELECT read_at,reminder_completed_at FROM site_notifications WHERE id=$1`, ids[4]).Scan(&read, &completed); err != nil || !read.Equal(firstRead) || completed == nil {
		t.Fatal("urgent confirmation must preserve first read time and complete reminder")
	}
	firstCompleted := *completed
	write("ack", []string{ids[4]}, http.StatusOK)
	if err := f.pool.QueryRow(t.Context(), `SELECT read_at,reminder_completed_at FROM site_notifications WHERE id=$1`, ids[4]).Scan(&read, &completed); err != nil || !read.Equal(firstRead) || !completed.Equal(firstCompleted) {
		t.Fatal("repeated progress must be monotonic and idempotent")
	}
}

func TestNotificationAPIPreferencesBoundsIsolationAndAuxiliaryFailurePostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	id := f.publish(t, "workspace-a", notifications.LevelUrgent, "retain-primary", time.Now())
	save := func(minutes any, status int) map[string]any {
		t.Helper()
		return notificationResponse(t, f.request(t, http.MethodPut, "/api/notifications/preferences", map[string]any{"workspaceAdminAccountId": "workspace-a", "intervalMinutes": minutes}, true), status)
	}
	for _, value := range []any{0, 61, 1.5, "5", nil} {
		assertNotificationError(t, save(value, http.StatusBadRequest), "workspace-a")
	}
	if body := save(7, http.StatusOK); body["intervalSeconds"] != float64(420) || body["workspaceAdminAccountId"] != "workspace-a" {
		t.Fatal("valid preference must return confirmed actual interval")
	}
	for _, value := range []any{0, 61, 1.5} {
		save(value, http.StatusBadRequest)
	}
	body := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/preferences"), nil, true), http.StatusOK)
	if body["intervalSeconds"] != float64(420) {
		t.Fatal("rejected preference must preserve original confirmed value")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE users SET current_admin_account_id='workspace-b' WHERE id='user'`); err != nil {
		t.Fatal(err)
	}
	body = notificationResponse(t, f.request(t, http.MethodGet, "/api/notifications/preferences?workspaceAdminAccountId=workspace-b", nil, true), http.StatusOK)
	if body["intervalSeconds"] != float64(300) {
		t.Fatal("new workspace must have independent default preference")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE users SET current_admin_account_id='workspace-a' WHERE id='user'; ALTER TABLE site_notification_preferences RENAME TO unavailable_preferences; ALTER TABLE strategy_settings RENAME TO unavailable_strategies`); err != nil {
		t.Fatal(err)
	}
	body = notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true), http.StatusOK)
	if body["intervalSeconds"] != nil || body["preferenceWarning"] == nil || body["pendingCriticalCount"] != float64(1) {
		t.Fatal("auxiliary failure must preserve primary messages and return unknown interval with reason")
	}
	items := body["active"].(map[string]any)["critical"].(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != id {
		t.Fatal("auxiliary failure must not erase acquired notification facts")
	}
	state := body["samplingState"].(map[string]any)
	if state["autoSyncEnabled"] != nil || state["warning"] == nil {
		t.Fatal("failed setting read must be unknown with reason, never falsely disabled")
	}
	assertNotificationError(t, notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/preferences"), nil, true), http.StatusServiceUnavailable), "workspace-a")
}

func TestNotificationAPIHistoryFiltersDetailReadOnlyAndPrimaryFailurePostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := []string{f.publish(t, "workspace-a", notifications.LevelNormal, "old", start), f.publish(t, "workspace-a", notifications.LevelUrgent, "middle", start.Add(time.Second)), f.publish(t, "workspace-a", notifications.LevelUrgent, "new", start.Add(2*time.Second))}
	if _, err := f.pool.Exec(t.Context(), `UPDATE site_notifications SET read_at=now() WHERE id=$1`, ids[1]); err != nil {
		t.Fatal("cannot prepare history progress")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE site_notifications SET reminder_completed_at=now() WHERE id=$1`, ids[2]); err != nil {
		t.Fatal("cannot prepare history progress")
	}
	path := notificationScopePath("/api/notifications/")
	body := notificationResponse(t, f.request(t, http.MethodGet, path+"&limit=2", nil, true), http.StatusOK)
	items := body["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["id"] != ids[2] || items[1].(map[string]any)["id"] != ids[1] {
		t.Fatal("history must be bounded and newest-first")
	}
	cursor := body["nextCursor"].(string)
	body = notificationResponse(t, f.request(t, http.MethodGet, path+"&limit=2&cursor="+url.QueryEscape(cursor), nil, true), http.StatusOK)
	items = body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != ids[0] {
		t.Fatal("history cursor must continue exactly once")
	}
	body = notificationResponse(t, f.request(t, http.MethodGet, path+"&filter=pending", nil, true), http.StatusOK)
	items = body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != ids[1] {
		t.Fatal("pending history must retain only unconfirmed urgent including read notices")
	}
	body = notificationResponse(t, f.request(t, http.MethodGet, path+"&filter=unread", nil, true), http.StatusOK)
	items = body["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["id"] != ids[2] || items[1].(map[string]any)["id"] != ids[0] {
		t.Fatal("unread history must follow reading state independently of reminder completion")
	}
	for _, query := range []string{"&filter=wrong", "&limit=51", "&filter=unread&cursor=" + url.QueryEscape(cursor)} {
		notificationResponse(t, f.request(t, http.MethodGet, path+query, nil, true), http.StatusBadRequest)
	}
	notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/"+ids[0]), nil, true), http.StatusOK)
	var read *time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT read_at FROM site_notifications WHERE id=$1`, ids[0]).Scan(&read); err != nil || read != nil {
		t.Fatal("opening notification detail must not silently mark it read")
	}
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE site_notifications RENAME TO unavailable_notifications`); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/api/notifications/summary", "/api/notifications/", "/api/notifications/" + ids[0]} {
		body = notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath(endpoint), nil, true), http.StatusServiceUnavailable)
		assertNotificationError(t, body, "workspace-a")
	}
}

func (f *notificationAPIFixture) waitForSQLLock(t *testing.T, fragment string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		err := f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1 AND pid<>pg_backend_pid())`, "%"+fragment+"%").Scan(&waiting)
		if err != nil {
			t.Fatal("cannot observe isolated PostgreSQL lock wait")
		}
		if waiting {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("notification write did not reach expected isolated PostgreSQL lock")
}

func TestNotificationAPIProgressFinishesBeforeExistingWorkspaceSwitchPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	id := f.publish(t, "workspace-a", notifications.LevelUrgent, "switch-race", time.Now())
	blocker, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), `SELECT id FROM site_notifications WHERE id=$1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		requestDone <- f.request(t, http.MethodPost, "/api/notifications/progress", map[string]any{"workspaceAdminAccountId": "workspace-a", "ids": []string{id}, "action": "ack"}, true)
	}()
	f.waitForSQLLock(t, "FROM site_notifications")
	switchDone := make(chan error, 1)
	go func() {
		_, err := admin_accounts.NewRepository(f.pool).Switch(context.Background(), "user", "workspace-b")
		switchDone <- err
	}()
	f.waitForSQLLock(t, "UPDATE users SET current_admin_account_id")
	select {
	case <-switchDone:
		t.Fatal("existing workspace switch overtook an in-flight notification write")
	default:
	}
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-requestDone:
		notificationResponse(t, w, http.StatusOK)
	case <-time.After(5 * time.Second):
		t.Fatal("notification write did not complete after fixture lock released")
	}
	select {
	case err := <-switchDone:
		if err != nil {
			t.Fatal("existing workspace switch failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workspace switch did not resume after notification transaction")
	}
	var read, completed *time.Time
	if err := f.pool.QueryRow(t.Context(), `SELECT read_at,reminder_completed_at FROM site_notifications WHERE id=$1`, id).Scan(&read, &completed); err != nil || read == nil || completed == nil {
		t.Fatal("correct old workspace write should finish before switch")
	}
	for _, entry := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, notificationScopePath("/api/notifications/summary"), nil},
		{http.MethodPost, "/api/notifications/progress", map[string]any{"workspaceAdminAccountId": "workspace-a", "ids": []string{id}, "action": "ack"}},
		{http.MethodPut, "/api/notifications/preferences", map[string]any{"workspaceAdminAccountId": "workspace-a", "intervalMinutes": 9}},
	} {
		assertNotificationError(t, notificationResponse(t, f.request(t, entry.method, entry.path, entry.body, true), http.StatusConflict), "workspace-a")
	}
}

func TestNotificationAPICanceledInFlightWriteDoesNotDetachPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	id := f.publish(t, "workspace-a", notifications.LevelUrgent, "cancel-write", time.Now())
	blocker, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), `SELECT id FROM site_notifications WHERE id=$1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"workspaceAdminAccountId": "workspace-a", "ids": []string{id}, "action": "ack"})
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/progress", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+notificationFixtureToken)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { f.server.Handler().ServeHTTP(w, req); close(done) }()
	f.waitForSQLLock(t, "FROM site_notifications")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled notification write kept running")
	}
	if w.Code == http.StatusOK {
		t.Fatal("canceled write must not report saved success")
	}
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var changed int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM site_notifications WHERE read_at IS NOT NULL OR reminder_completed_at IS NOT NULL`).Scan(&changed); err != nil || changed != 0 {
		t.Fatal("canceled request must not detach a late progress update")
	}
}

func TestNotificationAPICommitFailureRereadAndSameIDRetryPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	id := f.publish(t, "workspace-a", notifications.LevelUrgent, "commit-result", time.Now())
	if _, err := f.pool.Exec(t.Context(), `CREATE FUNCTION notification_commit_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'password=fixture-secret private-row' USING ERRCODE='23514'; END $$;
CREATE CONSTRAINT TRIGGER notification_commit_fault AFTER UPDATE ON site_notifications DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION notification_commit_fault()`); err != nil {
		t.Fatal("cannot prepare deferred commit fault fixture")
	}
	request := map[string]any{"workspaceAdminAccountId": "workspace-a", "ids": []string{id}, "action": "ack"}
	body := notificationResponse(t, f.request(t, http.MethodPost, "/api/notifications/progress", request, true), http.StatusServiceUnavailable)
	assertNotificationError(t, body, "workspace-a")
	text := fmt.Sprint(body)
	if !strings.Contains(text, "无法确认") && !strings.Contains(text, "不确定") {
		t.Fatal("failed commit must report uncertainty and guide reread")
	}
	if strings.Contains(text, "fixture-secret") || strings.Contains(text, "private-row") {
		t.Fatal("raw database message must not reach notification API")
	}
	if !strings.Contains(text, "SQLSTATE 23514") || !strings.Contains(text, "存储约束") {
		t.Fatal("known safe commit failure cause must be retained beside uncertainty")
	}
	body = notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/"+id), nil, true), http.StatusOK)
	item := body["item"].(map[string]any)
	if item["readAt"] != nil || item["reminderCompletedAt"] != nil {
		t.Fatal("rereading same ID must expose actual retained progress after failed commit")
	}
	if _, err := f.pool.Exec(t.Context(), `DROP TRIGGER notification_commit_fault ON site_notifications; DROP FUNCTION notification_commit_fault()`); err != nil {
		t.Fatal("cannot remove isolated commit fault fixture")
	}
	notificationResponse(t, f.request(t, http.MethodPost, "/api/notifications/progress", request, true), http.StatusOK)
	notificationResponse(t, f.request(t, http.MethodPost, "/api/notifications/progress", request, true), http.StatusOK)
	body = notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/"+id), nil, true), http.StatusOK)
	item = body["item"].(map[string]any)
	if item["readAt"] == nil || item["reminderCompletedAt"] == nil {
		t.Fatal("same-ID retry must save confirmed progress exactly once")
	}
}

func TestNotificationAPISamplingIsLocalBoundedAndHonestPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO strategy_settings(user_id,admin_account_id,settings) VALUES('user','workspace-a','{"enableRefreshInterval":true}')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 15; i++ {
		metrics := map[string]any{"balance": 987654321, "privateMetric": "fixture-private-metric", "groups": []any{map[string]any{"credential": "fixture-full-metrics-secret"}}}
		if i != 3 {
			reason := "上游倍率读取没有完成，请打开网站同步结果检查"
			confirmed := false
			if i == 0 {
				confirmed = true
				reason = ""
			}
			if i == 1 {
				confirmed = true
				reason = "部分分组没有返回倍率，请检查同步结果"
			}
			if i == 2 {
				reason = "上游拒绝读取：https://fixture-user:prefix'fixture-reason-tail@example.test/rates?auth=prefix'fixture-query-tail#prefix'fixture-fragment-tail；password=fixture-private-credential，请检查同步结果"
			}
			metrics["rateNoticeObservation"] = map[string]any{"confirmed": confirmed, "observedAt": "2030-01-01T00:00:00Z", "reason": reason}
		}
		raw, _ := json.Marshal(metrics)
		name := fmt.Sprintf("隔离站点-%02d", i)
		if i == 4 {
			name = "https://fixture-name-user:prefix'fixture-name-tail@example.test/rates"
		}
		if i == 5 {
			name = "https://example.test/query?access=prefix'fixture-name-query#prefix'fixture-name-fragment"
		}
		if i == 6 {
			name = "https://fixture-name-user:prefix\"fixture-name-double-tail@example.test/rates"
		}
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO upstream_sites(id,user_id,admin_account_id,name,base_url,platform,requested_platform,account,status,metrics,session,created_at,updated_at) VALUES($1,'user','workspace-a',$2,'https://fixture.invalid','sub2api','sub2api','fixture','success',$3::jsonb,'{"token":"fixture-session-secret"}',now(),now())`, fmt.Sprintf("sampling-%02d", i), name, string(raw)); err != nil {
			t.Fatal("cannot seed isolated local sampling observation")
		}
	}
	w := f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true)
	body := notificationResponse(t, w, http.StatusOK)
	state := body["samplingState"].(map[string]any)
	if state["autoSyncEnabled"] != true || state["unconfirmedSiteCount"] != float64(14) {
		t.Fatal("sampling must count missing, failed and partial observations while retaining actual sync setting")
	}
	sites := state["sites"].([]any)
	if len(sites) != 10 {
		t.Fatal("sampling preview must be bounded to ten sites")
	}
	partialSeen, maskedNameSeen := false, false
	for _, site := range sites {
		item := site.(map[string]any)
		if item["siteName"] == "隔离站点-00" {
			t.Fatal("trusted complete observation must not be shown as unconfirmed")
		}
		if item["siteName"] == "隔离站点-01" {
			partialSeen = true
			if !strings.Contains(item["reason"].(string), "部分分组") {
				t.Fatal("partial-directory observation must preserve known reason")
			}
		}
		if item["siteName"] == "隔离站点-02" && !strings.Contains(item["reason"].(string), "上游拒绝读取") {
			t.Fatal("masking sensitive suffix must preserve known direct sampling failure reason")
		}
		if item["siteName"] == "https://example.test/rates" {
			maskedNameSeen = true
		}
		for key := range item {
			if key != "siteName" && key != "observedAt" && key != "reason" {
				t.Fatal("sampling must return only necessary safe display fields")
			}
		}
	}
	if !partialSeen {
		t.Fatal("partial observations are unconfirmed even when returned groups can be compared")
	}
	if !maskedNameSeen {
		t.Fatal("sampling website name must preserve safe URL host/path while masking userinfo, query and fragment")
	}
	for _, secret := range []string{"fixture-private-metric", "fixture-full-metrics-secret", "fixture-private-credential", "fixture-session-secret", "fixture-user:", "fixture-name-user", "fixture-name-query", "fixture-name-fragment", "fixture-name-tail", "fixture-reason-tail", "fixture-query-tail", "fixture-fragment-tail", "fixture-name-double-tail", "987654321"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("sampling exposed session, full metrics or credentials")
		}
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE strategy_settings SET settings='[]'::jsonb WHERE user_id='user' AND admin_account_id='workspace-a'`); err != nil {
		t.Fatal(err)
	}
	body = notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true), http.StatusOK)
	state = body["samplingState"].(map[string]any)
	if state["autoSyncEnabled"] != nil || state["warning"] == nil || state["unconfirmedSiteCount"] != float64(14) {
		t.Fatal("malformed stored settings must be unknown while independently readable site state remains available")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE strategy_settings SET settings='{"enableRefreshInterval":true}'::jsonb WHERE user_id='user' AND admin_account_id='workspace-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE upstream_sites RENAME TO unavailable_upstream_sites`); err != nil {
		t.Fatal(err)
	}
	body = notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true), http.StatusOK)
	state = body["samplingState"].(map[string]any)
	if state["autoSyncEnabled"] != true || state["unconfirmedSiteCount"] != nil || state["warning"] == nil {
		t.Fatal("site sampling failure must be unknown while preserving separately confirmed enabled setting")
	}
}

func TestNotificationAPIDeletedCheckDestinationKeepsHistoricalFactPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	id, _, err := notifications.NewService(notifications.NewRepository(f.pool)).Publish(t.Context(), notifications.NoticeInput{
		UserID: "user", AdminAccountID: "workspace-a", Kind: "fixture.deleted", EventKey: "deleted-destination", SubjectType: "site", SubjectID: "deleted-site", Level: notifications.LevelImportant, DiscoveredAt: time.Now(),
		Content: notifications.Content{Title: "已删除网站当时名称", Summary: "当时的倍率变化仍可查看", Details: []notifications.Detail{}, Action: &notifications.Action{Kind: "group_rates", SiteID: "deleted-site", FocusGroupID: "7"}},
	})
	if err != nil {
		t.Fatal("cannot seed deleted destination historical fact")
	}
	body := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/"+id), nil, true), http.StatusOK)
	item := body["item"].(map[string]any)
	if item["content"].(map[string]any)["title"] != "已删除网站当时名称" {
		t.Fatal("missing destination must not discard historical content")
	}
	action := body["checkAction"].(map[string]any)
	if action["available"] != false || action["message"] != "相关网站已删除，这条提醒保留当时信息" {
		t.Fatal("missing destination must have explicit retained-fact guidance")
	}
}

func TestNotificationAPIPreferenceWriteSerializesWithSwitchAndFailedCommitKeepsValuePostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO site_notification_preferences(user_id,admin_account_id,poll_interval_seconds) VALUES('user','workspace-a',420)`); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), `SELECT user_id FROM site_notification_preferences WHERE user_id='user' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		requestDone <- f.request(t, http.MethodPut, "/api/notifications/preferences", map[string]any{"workspaceAdminAccountId": "workspace-a", "intervalMinutes": 8}, true)
	}()
	f.waitForSQLLock(t, "INSERT INTO site_notification_preferences")
	switchDone := make(chan error, 1)
	go func() {
		_, err := admin_accounts.NewRepository(f.pool).Switch(context.Background(), "user", "workspace-b")
		switchDone <- err
	}()
	f.waitForSQLLock(t, "UPDATE users SET current_admin_account_id")
	select {
	case <-switchDone:
		t.Fatal("workspace switch overtook preference write")
	default:
	}
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-requestDone:
		if body := notificationResponse(t, w, http.StatusOK); body["intervalSeconds"] != float64(480) {
			t.Fatal("confirmed in-flight preference did not save actual value")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("preference write did not finish")
	}
	select {
	case err := <-switchDone:
		if err != nil {
			t.Fatal("existing switch failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("switch did not resume after preference transaction")
	}
	if _, err := admin_accounts.NewRepository(f.pool).Switch(t.Context(), "user", "workspace-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `CREATE FUNCTION notification_preference_commit_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'token=fixture-private-value' USING ERRCODE='23514'; END $$;
CREATE CONSTRAINT TRIGGER notification_preference_commit_fault AFTER UPDATE ON site_notification_preferences DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION notification_preference_commit_fault()`); err != nil {
		t.Fatal("cannot prepare isolated preference commit fault")
	}
	w := f.request(t, http.MethodPut, "/api/notifications/preferences", map[string]any{"workspaceAdminAccountId": "workspace-a", "intervalMinutes": 9}, true)
	body := notificationResponse(t, w, http.StatusServiceUnavailable)
	assertNotificationError(t, body, "workspace-a")
	if !strings.Contains(fmt.Sprint(body), "无法确认") || strings.Contains(w.Body.String(), "fixture-private-value") {
		t.Fatal("preference commit failure must be safely uncertain")
	}
	body = notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/preferences"), nil, true), http.StatusOK)
	if body["intervalSeconds"] != float64(480) {
		t.Fatal("failed preference commit must preserve previous actual value for reread")
	}
	if _, err := f.pool.Exec(t.Context(), `DROP TRIGGER notification_preference_commit_fault ON site_notification_preferences; DROP FUNCTION notification_preference_commit_fault()`); err != nil {
		t.Fatal("cannot remove isolated preference fault")
	}
	body = notificationResponse(t, f.request(t, http.MethodPut, "/api/notifications/preferences", map[string]any{"workspaceAdminAccountId": "workspace-a", "intervalMinutes": 9}, true), http.StatusOK)
	if body["intervalSeconds"] != float64(540) {
		t.Fatal("preference reread and retry must save confirmed requested value")
	}
}

func TestNotificationAPICoreProducerRejectsHTTPUserinfoWithoutChangingIdentifiersPostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	for variant, unsafeURL := range []string{"https://fixture-content-user:fixture-content-password@example.test/rates", "https://fixture-content-user:prefix'fixture-content-password@example.test/rates", "https://safe.example/rates?next=https://fixture-content-user:fixture-content-password@example.test/child", "https://fixture-content-user:prefix\"fixture-content-password@example.test/rates"} {
		for _, field := range []string{"title", "summary", "detailLabel", "detailValue", "focusGroup"} {
			t.Run(fmt.Sprintf("%d-%s", variant, field), func(t *testing.T) {
				input := notifications.NoticeInput{UserID: "user", AdminAccountID: "workspace-a", Kind: "fixture.safe", EventKey: fmt.Sprintf("%d-%s", variant, field), SubjectType: "site", SubjectID: "safe-site", Level: notifications.LevelNormal, DiscoveredAt: time.Now(), Content: notifications.Content{Title: "正常通知", Summary: "正常说明", Details: []notifications.Detail{{Label: "网站", Value: "原名称"}}}}
				switch field {
				case "title":
					input.Content.Title = unsafeURL
				case "summary":
					input.Content.Summary = unsafeURL
				case "detailLabel":
					input.Content.Details[0].Label = unsafeURL
				case "detailValue":
					input.Content.Details[0].Value = unsafeURL
				case "focusGroup":
					input.Content.Action = &notifications.Action{Kind: "group_rates", SiteID: "safe-site", FocusGroup: unsafeURL}
				}
				id, inserted, err := notifications.NewService(notifications.NewRepository(f.pool)).Publish(t.Context(), input)
				if err == nil || inserted || id != "" {
					t.Fatal("core producer accepted HTTP URL login credentials into stored content or original navigation identifier")
				}
				if strings.Contains(err.Error(), "fixture-content-password") || strings.Contains(err.Error(), "fixture-content-user") {
					t.Fatal("validation error exposed rejected URL credentials")
				}
			})
		}
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM site_notifications`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected credential-shaped content must save no facts or altered navigation identifiers")
	}
	input := notifications.NoticeInput{UserID: "user", AdminAccountID: "workspace-a", Kind: "fixture.safe", EventKey: "safe-url", SubjectType: "site", SubjectID: strings.Repeat("x", 512), Level: notifications.LevelNormal, DiscoveredAt: time.Now(), Content: notifications.Content{Title: "https://example.test/path@public'part?mail=reader@example.test#plain@fragment", Summary: "https://example.test；联系 reader@example.test，普通安全地址仍可保存", Details: []notifications.Detail{}}}
	id, inserted, err := notifications.NewService(notifications.NewRepository(f.pool)).Publish(t.Context(), input)
	if err != nil || !inserted {
		t.Fatal("safe HTTP URL and complete existing-length identifier must remain accepted")
	}
	var title, subject string
	if err := f.pool.QueryRow(t.Context(), `SELECT content->>'title',subject_id FROM site_notifications WHERE id=$1`, id).Scan(&title, &subject); err != nil || title != input.Content.Title || subject != input.SubjectID {
		t.Fatal("safe URL or complete business identifier was unexpectedly altered")
	}
}

func TestNotificationAPIStoredInvalidContentAndPreferenceKeepSafeKnownCausePostgres(t *testing.T) {
	f := newNotificationAPIFixture(t)
	id := f.publish(t, "workspace-a", notifications.LevelUrgent, "unsafe-stored", time.Now())
	for _, fixture := range []struct{ content, reason string }{
		{`{"title":"隔离通知","summary":"原事实","details":"broken"}`, "格式"},
		{`{"title":"token=fixture-unsafe-content","summary":"原事实","details":[]}`, "安全"},
		{`{"title":"https://fixture-content-user:fixture-content-password@example.test/rates","summary":"原事实","details":[]}`, "安全"},
		{`{"title":"https://fixture-content-user:prefix'fixture-content-password@example.test/rates","summary":"原事实","details":[]}`, "安全"},
		{`{"title":"https://safe.example/rates?next=https://fixture-content-user:fixture-content-password@example.test/rates","summary":"原事实","details":[]}`, "安全"},
	} {
		if _, err := f.pool.Exec(t.Context(), `UPDATE site_notifications SET content=$2::jsonb WHERE id=$1`, id, fixture.content); err != nil {
			t.Fatal("cannot prepare isolated invalid stored content")
		}
		w := f.request(t, http.MethodGet, notificationScopePath("/api/notifications/"+id), nil, true)
		body := notificationResponse(t, w, http.StatusServiceUnavailable)
		assertNotificationError(t, body, "workspace-a")
		if !strings.Contains(fmt.Sprint(body), fixture.reason) || strings.Contains(w.Body.String(), "fixture-unsafe-content") || strings.Contains(w.Body.String(), "fixture-content-password") {
			t.Fatal("stored-content rejection must preserve known safe reason and never disclose unsafe content")
		}
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE site_notifications SET content='{"title":"隔离通知","summary":"原事实","details":[]}' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE site_notification_preferences DROP CONSTRAINT site_notification_preferences_poll_interval_seconds_check;
INSERT INTO site_notification_preferences(user_id,admin_account_id,poll_interval_seconds) VALUES('user','workspace-a',61)`); err != nil {
		t.Fatal("cannot prepare isolated malformed stored preference")
	}
	body := notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/summary"), nil, true), http.StatusOK)
	if body["intervalSeconds"] != nil || body["pendingCriticalCount"] != float64(1) || !strings.Contains(fmt.Sprint(body["preferenceWarning"]), "整数分钟") {
		t.Fatal("malformed stored preference must retain primary notice and report its known rule violation")
	}
	body = notificationResponse(t, f.request(t, http.MethodGet, notificationScopePath("/api/notifications/preferences"), nil, true), http.StatusServiceUnavailable)
	assertNotificationError(t, body, "workspace-a")
	if !strings.Contains(fmt.Sprint(body), "整数分钟") {
		t.Fatal("independent preference failure must preserve its known reason")
	}
}
