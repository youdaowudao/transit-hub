package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"transithub/backend/internal/shared/authctx"
	"transithub/backend/internal/shared/workspacescope"
)

func absorbStrategyStore(t *testing.T) (*pgxpool.Pool, workspacescope.Scope, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("explicit PostgreSQL verification requires TEST_DATABASE_URL")
	}
	ctx := t.Context()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("cannot open explicit fixture database")
	}
	schema := fmt.Sprintf("absorb_settings_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		base.Close()
		t.Fatal("cannot create isolated settings schema")
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := base.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error("cannot clean exact settings schema")
		}
		base.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("cannot parse explicit fixture database")
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("cannot open isolated settings schema")
	}
	_, err = pool.Exec(ctx, `CREATE TABLE users(id text PRIMARY KEY,current_admin_account_id text NOT NULL);
 CREATE TABLE admin_accounts(id text PRIMARY KEY,user_id text NOT NULL,created_at timestamptz NOT NULL);
 CREATE TABLE notification_channel_settings(user_id text,admin_account_id text,settings jsonb NOT NULL,updated_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(user_id,admin_account_id));
 CREATE TABLE strategy_settings(user_id text,admin_account_id text,settings jsonb NOT NULL,updated_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(user_id,admin_account_id));
 INSERT INTO users VALUES('fixture-user','fixture-workspace');
 INSERT INTO admin_accounts VALUES('fixture-workspace','fixture-user',clock_timestamp());
 INSERT INTO notification_channel_settings(user_id,admin_account_id,settings) VALUES('fixture-user','fixture-workspace','{"telegram":[{"id":"bot-1","name":"Fixture bot","enabled":true,"botToken":"fixture-token","chatId":"fixture-chat"},{"id":"disabled-bot","name":"Disabled fixture","enabled":false,"botToken":"fixture-token","chatId":"fixture-chat"}]}'::jsonb);`)
	if err != nil {
		t.Fatal("cannot initialize exact isolated settings fixture")
	}
	var created time.Time
	if err := pool.QueryRow(ctx, `SELECT created_at FROM admin_accounts WHERE id='fixture-workspace'`).Scan(&created); err != nil {
		t.Fatal("cannot read fixture workspace creation time")
	}
	return pool, workspacescope.Scope{UserID: "fixture-user", AdminAccountID: "fixture-workspace", CreatedAt: created.Format(time.RFC3339Nano)}, schema
}
func absorbStrategyService(pool *pgxpool.Pool) *Service {
	service := NewService(nil, NewRepository(pool))
	service.SetAdminAccountResolver(fixedSettingsAccountResolver{id: "fixture-workspace"})
	return service
}
func absorbSaveRoot(t *testing.T, pool *pgxpool.Pool, raw string) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `INSERT INTO strategy_settings(user_id,admin_account_id,settings) VALUES('fixture-user','fixture-workspace',$1::jsonb) ON CONFLICT(user_id,admin_account_id) DO UPDATE SET settings=excluded.settings`, raw)
	if err != nil {
		t.Fatal("cannot save isolated strategy fixture")
	}
}
func absorbReadRoot(t *testing.T, pool *pgxpool.Pool) map[string]json.RawMessage {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(t.Context(), `SELECT settings FROM strategy_settings WHERE user_id='fixture-user' AND admin_account_id='fixture-workspace'`).Scan(&raw); err != nil {
		t.Fatal("cannot read isolated strategy result")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	return root
}
func absorbStrategyRequest(service *Service, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/settings/strategy", strings.NewReader(body))
	req = req.WithContext(authctx.WithUserID(req.Context(), "fixture-user"))
	rec := httptest.NewRecorder()
	(&Handler{service: service}).saveStrategy(rec, req)
	return rec
}
func absorbPatchBody(t *testing.T, scope workspacescope.Scope, patch map[string]any) string {
	t.Helper()
	fields := map[string]any{"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt, "refreshInterval": 120}
	for key, value := range patch {
		fields[key] = value
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAbsorbStrategyLegacyPreservesBadReportAndUnknownRoot(t *testing.T) {
	pool, _, _ := absorbStrategyStore(t)
	service := absorbStrategyService(pool)
	service.SetDailyReportWritesDisabled(true)
	original := `{"dailyReportEnabled":{"bad":"keep exact value"},"dailyReportTime":"bad-time","dailyReportBotIds":["missing","disabled-bot"],"futureRoot":{"nested":[1,"preserve",true]}}`
	absorbSaveRoot(t, pool, original)
	before := absorbReadRoot(t, pool)
	callbacks := 0
	service.OnStrategyChanged = func(userID, workspaceID string, saved StrategySettings) {
		callbacks++
		if userID != "fixture-user" || workspaceID != "fixture-workspace" || saved.RefreshInterval != 120 {
			t.Error("legacy callback changed its workspace or normalized result")
		}
	}
	template := strings.Repeat("ordinary legacy template dailyReportEnabled dailyReportTime dailyReportBotIds ", 1200)
	body, _ := json.Marshal(map[string]any{"enableRefreshInterval": true, "refreshInterval": 120, "balanceTemplate": template})
	if len(body) <= strategyReportBodyLimit {
		t.Fatal("large legacy fixture is too small")
	}
	response := absorbStrategyRequest(service, string(body))
	if response.Code != 200 {
		t.Fatalf("large pure legacy request status=%d body=%s", response.Code, response.Body.String())
	}
	after := absorbReadRoot(t, pool)
	for _, key := range []string{"dailyReportEnabled", "dailyReportTime", "dailyReportBotIds", "futureRoot"} {
		if !reflect.DeepEqual(before[key], after[key]) {
			t.Fatalf("legacy save changed original %s", key)
		}
	}
	var actualTemplate string
	_ = json.Unmarshal(after["balanceTemplate"], &actualTemplate)
	if actualTemplate != template || callbacks != 1 {
		t.Fatal("legacy template or original callback was not preserved")
	}
}

func TestAbsorbStrategyNewRequestRejectsInvalidWithoutSaving(t *testing.T) {
	pool, scope, _ := absorbStrategyStore(t)
	service := absorbStrategyService(pool)
	absorbSaveRoot(t, pool, `{"dailyReportEnabled":false,"dailyReportTime":"09:15","dailyReportBotIds":["bot-1"],"futureRoot":{"keep":true},"refreshInterval":60}`)
	original := absorbReadRoot(t, pool)
	callbacks := 0
	service.OnStrategyChanged = func(string, string, StrategySettings) { callbacks++ }
	cases := []struct {
		name   string
		patch  map[string]any
		status int
	}{
		{"null-switch", map[string]any{"dailyReportEnabled": nil}, 400},
		{"string-switch", map[string]any{"dailyReportEnabled": "false"}, 400},
		{"numeric-switch", map[string]any{"dailyReportEnabled": 0}, 400},
		{"null-time", map[string]any{"dailyReportTime": nil}, 400},
		{"bad-time", map[string]any{"dailyReportTime": "24:00"}, 400},
		{"short-time", map[string]any{"dailyReportTime": "9:15"}, 400},
		{"object-time", map[string]any{"dailyReportTime": map[string]string{"bad": "value"}}, 400},
		{"null-bots", map[string]any{"dailyReportBotIds": nil}, 400},
		{"null-bot-element", map[string]any{"dailyReportBotIds": []any{nil}}, 400},
		{"wrong-bot-type", map[string]any{"dailyReportBotIds": []any{1}}, 400},
		{"blank-bot", map[string]any{"dailyReportBotIds": []string{""}}, 400},
		{"multiple-bots", map[string]any{"dailyReportBotIds": []string{"bot-1", "disabled-bot"}}, 400},
		{"enabled-no-target", map[string]any{"dailyReportEnabled": true, "dailyReportBotIds": []string{}}, 409},
		{"enabled-deleted-target", map[string]any{"dailyReportEnabled": true, "dailyReportBotIds": []string{"deleted"}}, 409},
		{"enabled-disabled-target", map[string]any{"dailyReportEnabled": true, "dailyReportBotIds": []string{"disabled-bot"}}, 409},
		{"new-unknown-field", map[string]any{"dailyReportEnabled": false, "notSupported": true}, 400},
		{"cannot-select-user", map[string]any{"dailyReportEnabled": false, "userId": "other-user"}, 400},
		{"new-null-legacy-field", map[string]any{"dailyReportEnabled": false, "balanceTemplate": nil}, 400},
		{"bad-scope-time", map[string]any{"dailyReportEnabled": false, "workspaceCreatedAt": "not-a-date"}, 400},
		{"stale-scope", map[string]any{"dailyReportEnabled": false, "workspaceCreatedAt": "2001-01-01T00:00:00Z"}, 409},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := absorbStrategyRequest(service, absorbPatchBody(t, scope, tc.patch))
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
			if !reflect.DeepEqual(original, absorbReadRoot(t, pool)) {
				t.Fatal("invalid request changed stored JSON")
			}
			if callbacks != 0 {
				t.Fatal("invalid request invoked strategy callback")
			}
		})
	}
	for _, body := range []string{absorbPatchBody(t, scope, map[string]any{"dailyReportEnabled": false}) + ` {}`, `{"dailyReportEnabled":false,"balanceTemplate":` + fmt.Sprintf("%q", strings.Repeat("x", strategyReportBodyLimit)) + `}`, `{"balanceTemplate":` + fmt.Sprintf("%q", strings.Repeat("x", strategyReportBodyLimit)) + `,"dailyReportEnabled":false}`} {
		response := absorbStrategyRequest(service, body)
		if response.Code != 400 {
			t.Fatalf("trailing/oversize request accepted: %d", response.Code)
		}
		if !reflect.DeepEqual(original, absorbReadRoot(t, pool)) || callbacks != 0 {
			t.Fatal("trailing/oversize request saved or invoked callback")
		}
	}
}

func TestAbsorbStrategyExplicitFalseEmptyAndPartialPatch(t *testing.T) {
	pool, scope, _ := absorbStrategyStore(t)
	service := absorbStrategyService(pool)
	absorbSaveRoot(t, pool, `{"dailyReportEnabled":true,"dailyReportTime":"09:15","dailyReportBotIds":["bot-1"],"futureRoot":{"keep":true}}`)
	callbacks := 0
	service.OnStrategyChanged = func(userID, workspaceID string, value StrategySettings) {
		callbacks++
		if userID != scope.UserID || workspaceID != scope.AdminAccountID {
			t.Error("new callback changed scope")
		}
	}
	response := absorbStrategyRequest(service, absorbPatchBody(t, scope, map[string]any{"dailyReportEnabled": false, "dailyReportBotIds": []string{}}))
	if response.Code != 200 {
		t.Fatalf("explicit close+clear=%d %s", response.Code, response.Body.String())
	}
	root := absorbReadRoot(t, pool)
	if string(root["dailyReportEnabled"]) != "false" || string(root["dailyReportBotIds"]) != "[]" || string(root["dailyReportTime"]) != `"09:15"` || string(root["futureRoot"]) != `{"keep": true}` {
		t.Fatal("close+clear did not retain omitted time or future JSON")
	}
	response = absorbStrategyRequest(service, absorbPatchBody(t, scope, map[string]any{"dailyReportTime": "10:45"}))
	if response.Code != 200 {
		t.Fatalf("partial time repair=%d", response.Code)
	}
	root = absorbReadRoot(t, pool)
	if string(root["dailyReportEnabled"]) != "false" || string(root["dailyReportBotIds"]) != "[]" || string(root["dailyReportTime"]) != `"10:45"` || callbacks != 1 {
		t.Fatal("partial patch reset omitted report fields or notified unchanged refresh")
	}
	var dto map[string]any
	if json.Unmarshal(response.Body.Bytes(), &dto) != nil || dto["workspaceAdminAccountId"] != scope.AdminAccountID || dto["workspaceCreatedAt"] != scope.CreatedAt {
		t.Fatal("saved response did not return the original exact scope")
	}
	service.SetDailyReportWritesDisabled(true)
	before := absorbReadRoot(t, pool)
	response = absorbStrategyRequest(service, absorbPatchBody(t, scope, map[string]any{"dailyReportEnabled": true}))
	if response.Code != 409 || !reflect.DeepEqual(before, absorbReadRoot(t, pool)) || callbacks != 1 {
		t.Fatal("APIOnly admitted a new report write")
	}
}

func TestAbsorbStrategyNewPathKeepsOldProjectionAndNeverSends(t *testing.T) {
	pool, scope, _ := absorbStrategyStore(t)
	var requests atomic.Int32
	service := NewService(&http.Client{Transport: globalizationTransport(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, fmt.Errorf("no sending authorized")
	})}, NewRepository(pool))
	absorbSaveRoot(t, pool, `{"enableRefreshInterval":false,"refreshInterval":120}`)
	callback := StrategySettings{}
	service.OnStrategyChanged = func(_, _ string, value StrategySettings) { callback = value }
	legacy := DefaultStrategySettings()
	legacy.EnableBalanceWarning = true
	legacy.BalanceNotifyBotIDs = []string{"removed-bot", "disabled-bot"}
	legacy.RefreshInterval = 1
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var legacyPatch map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &legacyPatch); err != nil {
		t.Fatal(err)
	}
	saved, err := service.SaveStrategyWithDailyReport(t.Context(), scope, legacyPatch, DailyReportPatch{Enabled: json.RawMessage("true"), BotIDs: json.RawMessage(`["bot-1"]`)})
	if err != nil {
		t.Fatal(err)
	}
	if !saved.EnableBalanceWarning || !saved.BalanceNotifyRecipientsInvalid || !reflect.DeepEqual(saved.BalanceNotifyBotIDs, []string{"disabled-bot"}) || saved.RefreshInterval != minRefreshIntervalSeconds {
		t.Fatal("new path changed old disabled recipient projection or minimum refresh normalization")
	}
	if !reflect.DeepEqual(callback, saved) || requests.Load() != 0 {
		t.Fatal("new save lost the original callback or made an unauthorized HTTP request")
	}
}

func TestAbsorbStrategyFirstSaveSerializesBeforeLegacyMerge(t *testing.T) {
	pool, scope, application := absorbStrategyStore(t)
	service := absorbStrategyService(pool)
	var callbacks atomic.Int32
	service.OnStrategyChanged = func(string, string, StrategySettings) { callbacks.Add(1) }
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	original, err := readStrategyForUpdate(ctx, tx, scope.UserID, scope.AdminAccountID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.SaveStrategy(ctx, scope.UserID, StrategySettings{EnableRefreshInterval: true, RefreshInterval: 120})
		done <- err
	}()
	released := false
	defer func() {
		cancel()
		if !released {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("concurrent legacy save did not stop")
			}
		}
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	waiting := false
	for !waiting {
		select {
		case err := <-done:
			released = true
			t.Fatalf("legacy first save completed before report transaction released its row: %v", err)
		case <-ctx.Done():
			t.Fatal("concurrent first-save lock was not observed")
		case <-ticker.C:
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock'`, application).Scan(&count); err != nil {
				t.Fatal("cannot observe isolated first-save lock")
			}
			waiting = count > 0
		}
	}
	raw, _, err := mergeStrategyJSON(original, DefaultStrategySettings(), DailyReportPatch{Enabled: json.RawMessage("true"), Time: json.RawMessage(`"07:30"`), BotIDs: json.RawMessage(`["bot-1"]`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStrategyJSON(ctx, tx, scope.UserID, scope.AdminAccountID, raw); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		released = true
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("legacy save remained blocked after report commit")
	}
	root := absorbReadRoot(t, pool)
	if string(root["dailyReportEnabled"]) != "true" || string(root["dailyReportTime"]) != `"07:30"` || string(root["dailyReportBotIds"]) != `["bot-1"]` || string(root["refreshInterval"]) != "120" || callbacks.Load() != 1 {
		t.Fatal("concurrent initial legacy save lost report settings or original callback")
	}
}

// A report-only save must not treat omitted old fields as a new all-zero form.
func TestAbsorbStrategyDailyOnlyPreservesLegacyRaw(t *testing.T) {
	pool, scope, _ := absorbStrategyStore(t)
	service := absorbStrategyService(pool)
	absorbSaveRoot(t, pool, `{"enableRefreshInterval":true,"refreshInterval":360,"enableBalanceWarning":true,"defaultBalanceThreshold":21,"balanceNotifyBotIds":["bot-1"],"balanceTemplate":"keep balance","balanceTemplateFormat":"markdown","enableMultiplierAlert":true,"multiplierNotifyBotIds":["disabled-bot"],"multiplierTemplate":"keep multiplier","multiplierTemplateFormat":"html","enableAutoChangeMultiplier":true,"dailyReportEnabled":true,"dailyReportTime":"09:15","dailyReportBotIds":["bot-1"],"futureRoot":{"keep":[true,1]}}`)
	before := absorbReadRoot(t, pool)
	callbacks := 0
	service.OnStrategyChanged = func(_, _ string, _ StrategySettings) { callbacks++ }
	body, _ := json.Marshal(map[string]any{"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt, "dailyReportTime": "10:45"})
	response := absorbStrategyRequest(service, string(body))
	if response.Code != 200 {
		t.Fatalf("report-only status=%d body=%s", response.Code, response.Body.String())
	}
	after := absorbReadRoot(t, pool)
	for key, raw := range before {
		if key != "dailyReportTime" && !reflect.DeepEqual(raw, after[key]) {
			t.Errorf("report-only save changed omitted %s: %s -> %s", key, raw, after[key])
		}
	}
	if string(after["dailyReportTime"]) != `"10:45"` || callbacks != 0 {
		t.Fatal("report time was not saved or unrelated refresh was rescheduled")
	}
}

func TestAbsorbStrategyDailyOnlyMergesLatestLockedLegacy(t *testing.T) {
	pool, scope, application := absorbStrategyStore(t)
	service := absorbStrategyService(pool)
	var callbacks atomic.Int32
	service.OnStrategyChanged = func(string, string, StrategySettings) { callbacks.Add(1) }
	absorbSaveRoot(t, pool, `{"enableRefreshInterval":true,"refreshInterval":120,"enableBalanceWarning":true,"dailyReportEnabled":false,"dailyReportTime":"09:15","dailyReportBotIds":[],"futureRoot":{"keep":true}}`)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := readStrategyForUpdate(ctx, tx, scope.UserID, scope.AdminAccountID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt, "dailyReportTime": "10:45"})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- absorbStrategyRequest(service, string(body)) }()
	released := false
	defer func() {
		cancel()
		if !released {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("waiting report save did not stop")
			}
		}
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	waiting := false
	for !waiting {
		select {
		case response := <-done:
			released = true
			t.Fatalf("report save did not wait for old strategy save: %d", response.Code)
		case <-ctx.Done():
			t.Fatal("report save row lock was not observed")
		case <-ticker.C:
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock'`, application).Scan(&count); err != nil {
				t.Fatal("cannot observe isolated report save lock")
			}
			waiting = count > 0
		}
	}
	// The old client commits its newer refresh and balance choices while the report client waits.
	if _, err := tx.Exec(ctx, `UPDATE strategy_settings SET settings=settings || '{"refreshInterval":480,"enableBalanceWarning":false,"balanceTemplate":"newer old-client value"}'::jsonb`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-done:
		released = true
		if response.Code != 200 {
			t.Fatalf("report save failed after old save: %d %s", response.Code, response.Body.String())
		}
	case <-ctx.Done():
		t.Fatal("report save remained blocked")
	}
	root := absorbReadRoot(t, pool)
	if string(root["refreshInterval"]) != "480" || string(root["enableRefreshInterval"]) != "true" || string(root["enableBalanceWarning"]) != "false" || string(root["balanceTemplate"]) != `"newer old-client value"` || string(root["dailyReportTime"]) != `"10:45"` {
		t.Fatal("report-only save overwrote the concurrent legacy choices")
	}
	if callbacks.Load() != 0 {
		t.Fatal("report-only save rescheduled the newer locked refresh configuration")
	}
}

func TestAbsorbStrategySparseLegacyPatchKeepsOldLegalization(t *testing.T) {
	pool, scope, _ := absorbStrategyStore(t)
	service := absorbStrategyService(pool)
	original := `{"enableRefreshInterval":true,"refreshInterval":15,"enableBalanceWarning":true,"balanceNotifyBotIds":["bot-1"],"balanceTemplate":"keep balance","balanceTemplateFormat":"retained-legacy-format","enableMultiplierAlert":true,"multiplierNotifyBotIds":["disabled-bot"],"multiplierTemplate":"keep multiplier","multiplierTemplateFormat":"html","dailyReportEnabled":false,"dailyReportTime":"09:15","dailyReportBotIds":[],"futureRoot":{"keep":true}}`
	for _, tc := range []struct {
		name    string
		fields  map[string]any
		changed []string
		check   func(*testing.T, map[string]json.RawMessage)
	}{
		{"minimum-refresh", map[string]any{"refreshInterval": 1}, []string{"refreshInterval"}, func(t *testing.T, root map[string]json.RawMessage) {
			if string(root["refreshInterval"]) != "60" {
				t.Fatal("submitted refresh interval lost original minimum")
			}
		}},
		{"explicit-false", map[string]any{"enableMultiplierAlert": false}, []string{"enableMultiplierAlert"}, func(t *testing.T, root map[string]json.RawMessage) {
			if string(root["enableMultiplierAlert"]) != "false" {
				t.Fatal("explicit old false was ignored")
			}
		}},
		{"recipient-projection", map[string]any{"balanceNotifyBotIds": []string{"removed-bot"}}, []string{"balanceNotifyBotIds", "enableBalanceWarning", "balanceNotifyRecipientsInvalid"}, func(t *testing.T, root map[string]json.RawMessage) {
			if string(root["balanceNotifyBotIds"]) != "[]" || string(root["enableBalanceWarning"]) != "false" || string(root["balanceNotifyRecipientsInvalid"]) != "true" {
				t.Fatal("submitted recipient lost original projection/disable behavior")
			}
		}},
		{"empty-recipients", map[string]any{"multiplierNotifyBotIds": []string{}}, []string{"multiplierNotifyBotIds", "multiplierNotifyRecipientsInvalid"}, func(t *testing.T, root map[string]json.RawMessage) {
			if string(root["multiplierNotifyBotIds"]) != "[]" || string(root["enableMultiplierAlert"]) != "true" {
				t.Fatal("explicit old empty recipients changed original empty-list behavior")
			}
		}},
		{"template-format", map[string]any{"balanceTemplateFormat": "unsupported"}, []string{"balanceTemplateFormat"}, func(t *testing.T, root map[string]json.RawMessage) {
			if string(root["balanceTemplateFormat"]) != `"text"` {
				t.Fatal("submitted old format was not normalized")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			absorbSaveRoot(t, pool, original)
			before := absorbReadRoot(t, pool)
			fields := map[string]any{"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt, "dailyReportTime": "10:45"}
			for key, value := range tc.fields {
				fields[key] = value
			}
			raw, _ := json.Marshal(fields)
			response := absorbStrategyRequest(service, string(raw))
			if response.Code != 200 {
				t.Fatalf("sparse save status=%d body=%s", response.Code, response.Body.String())
			}
			after := absorbReadRoot(t, pool)
			allowed := map[string]bool{"dailyReportTime": true}
			for _, key := range tc.changed {
				allowed[key] = true
			}
			for key, raw := range before {
				if !allowed[key] && !reflect.DeepEqual(raw, after[key]) {
					t.Errorf("sparse old patch changed omitted %s", key)
				}
			}
			tc.check(t, after)
		})
	}
}

func TestAbsorbStrategyReportPatchOnlyNotifiesChangedRefresh(t *testing.T) {
	pool, scope, _ := absorbStrategyStore(t)
	service := absorbStrategyService(pool)
	for _, tc := range []struct {
		name        string
		patch       map[string]any
		oldInterval int
		calls       int
		enabled     bool
		interval    int
	}{
		{"report-time-only", map[string]any{"dailyReportTime": "10:45"}, 120, 0, true, 120},
		{"report-switch-only", map[string]any{"dailyReportEnabled": true}, 120, 0, true, 120},
		{"report-target-only", map[string]any{"dailyReportBotIds": []string{}}, 120, 0, true, 120},
		{"legacy-alert-only", map[string]any{"dailyReportTime": "10:45", "enableBalanceWarning": true}, 120, 0, true, 120},
		{"same-refresh", map[string]any{"dailyReportTime": "10:45", "refreshInterval": 120}, 120, 0, true, 120},
		{"same-effective-refresh", map[string]any{"dailyReportTime": "10:45", "refreshInterval": 1}, 15, 0, true, 60},
		{"changed-refresh-interval", map[string]any{"dailyReportTime": "10:45", "refreshInterval": 240}, 120, 1, true, 240},
		{"changed-refresh-switch", map[string]any{"dailyReportTime": "10:45", "enableRefreshInterval": false}, 120, 1, false, 120},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := fmt.Sprintf(`{"enableRefreshInterval":true,"refreshInterval":%d,"enableBalanceWarning":false,"dailyReportEnabled":false,"dailyReportTime":"09:15","dailyReportBotIds":["bot-1"]}`, tc.oldInterval)
			absorbSaveRoot(t, pool, original)
			calls := 0
			var callback StrategySettings
			service.OnStrategyChanged = func(userID, workspaceID string, value StrategySettings) {
				calls++
				callback = value
				if userID != scope.UserID || workspaceID != scope.AdminAccountID {
					t.Error("refresh callback changed workspace")
				}
			}
			fields := map[string]any{"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt}
			for key, value := range tc.patch {
				fields[key] = value
			}
			body, _ := json.Marshal(fields)
			response := absorbStrategyRequest(service, string(body))
			if response.Code != 200 {
				t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
			}
			if calls != tc.calls {
				t.Fatalf("refresh callback calls=%d want=%d", calls, tc.calls)
			}
			if calls > 0 && (callback.EnableRefreshInterval != tc.enabled || callback.RefreshInterval != tc.interval) {
				t.Fatalf("refresh callback received unrelated or stale refresh settings: %#v", callback)
			}
		})
	}
}

func TestAbsorbStrategyReportResponseMatchesLegacyReadWithoutRewriting(t *testing.T) {
	pool, scope, _ := absorbStrategyStore(t)
	service := absorbStrategyService(pool)
	for _, tc := range []struct {
		name                              string
		balanceIDs, multiplierIDs         string
		balanceInvalid, multiplierInvalid bool
	}{
		{"deleted-balance-recipient", `["removed-bot"]`, `["disabled-bot"]`, false, false},
		{"deleted-multiplier-recipient", `["bot-1"]`, `["removed-bot"]`, false, false},
		{"retained-invalid-empty-lists", `[]`, `[]`, true, true},
		{"valid-lists-recompute-invalid-flags", `["bot-1"]`, `["disabled-bot"]`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := fmt.Sprintf(`{"enableRefreshInterval":true,"refreshInterval":120,"enableBalanceWarning":true,"balanceNotifyBotIds":%s,"balanceNotifyRecipientsInvalid":%t,"enableMultiplierAlert":true,"multiplierNotifyBotIds":%s,"multiplierNotifyRecipientsInvalid":%t,"dailyReportEnabled":false,"dailyReportTime":"09:15","dailyReportBotIds":[],"futureRoot":{"keep":true}}`, tc.balanceIDs, tc.balanceInvalid, tc.multiplierIDs, tc.multiplierInvalid)
			absorbSaveRoot(t, pool, original)
			before := absorbReadRoot(t, pool)
			request := httptest.NewRequest(http.MethodGet, "/api/settings/strategy", nil)
			request = request.WithContext(authctx.WithUserID(request.Context(), scope.UserID))
			recorder := httptest.NewRecorder()
			(&Handler{service: service}).getStrategy(recorder, request)
			if recorder.Code != 200 {
				t.Fatalf("legacy GET status=%d", recorder.Code)
			}
			var read StrategySettings
			if err := json.Unmarshal(recorder.Body.Bytes(), &read); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]any{"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt, "dailyReportTime": "10:45"})
			response := absorbStrategyRequest(service, string(body))
			if response.Code != 200 {
				t.Fatalf("report save status=%d body=%s", response.Code, response.Body.String())
			}
			var saved StrategySettings
			if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
				t.Fatal(err)
			}
			read.DailyReportTime = json.RawMessage(`"10:45"`)
			if !reflect.DeepEqual(saved, read) {
				t.Errorf("report PUT old reminder display differs from GET: saved=%#v read=%#v", saved, read)
			}
			after := absorbReadRoot(t, pool)
			for key, raw := range before {
				if key != "dailyReportTime" && !reflect.DeepEqual(raw, after[key]) {
					t.Errorf("response projection rewrote omitted raw field %s", key)
				}
			}
		})
	}
}

func TestAbsorbStrategyReportResponseKeepsExplicitRecipientSaveWarning(t *testing.T) {
	pool, scope, _ := absorbStrategyStore(t)
	service := absorbStrategyService(pool)
	for _, kind := range []struct{ name, ids, invalid, enabled string }{
		{"balance", "balanceNotifyBotIds", "balanceNotifyRecipientsInvalid", "enableBalanceWarning"},
		{"multiplier", "multiplierNotifyBotIds", "multiplierNotifyRecipientsInvalid", "enableMultiplierAlert"},
	} {
		for _, removedOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-all-removed-%t", kind.name, removedOnly), func(t *testing.T) {
				absorbSaveRoot(t, pool, `{"enableRefreshInterval":true,"refreshInterval":120,"enableBalanceWarning":true,"balanceNotifyBotIds":["bot-1"],"enableMultiplierAlert":true,"multiplierNotifyBotIds":["bot-1"],"dailyReportEnabled":false,"dailyReportTime":"09:15","dailyReportBotIds":[]}`)
				recipients := []string{"removed-bot", "disabled-bot"}
				expectedIDs := `["disabled-bot"]`
				expectedEnabled := "true"
				if removedOnly {
					recipients = []string{"removed-bot"}
					expectedIDs = `[]`
					expectedEnabled = "false"
				}
				body, _ := json.Marshal(map[string]any{"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt, "dailyReportTime": "10:45", kind.ids: recipients})
				response := absorbStrategyRequest(service, string(body))
				if response.Code != 200 {
					t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
				}
				var saved map[string]json.RawMessage
				if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
					t.Fatal(err)
				}
				stored := absorbReadRoot(t, pool)
				for _, result := range []map[string]json.RawMessage{saved, stored} {
					if string(result[kind.ids]) != expectedIDs || string(result[kind.invalid]) != "true" || string(result[kind.enabled]) != expectedEnabled {
						t.Fatal("explicit recipient edit lost the original immediate invalid/disable warning")
					}
				}
			})
		}
	}
}

func TestAbsorbStrategyReportPatchEnablesRefreshWithOriginalMinimum(t *testing.T) {
	pool, scope, _ := absorbStrategyStore(t)
	service := absorbStrategyService(pool)
	absorbSaveRoot(t, pool, `{"enableRefreshInterval":false,"refreshInterval":15,"dailyReportEnabled":false,"dailyReportTime":"09:15","dailyReportBotIds":[]}`)
	callbacks := 0
	var callback StrategySettings
	service.OnStrategyChanged = func(_, _ string, value StrategySettings) { callbacks++; callback = value }
	body, _ := json.Marshal(map[string]any{"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt, "dailyReportTime": "10:45", "enableRefreshInterval": true})
	response := absorbStrategyRequest(service, string(body))
	if response.Code != 200 || callbacks != 1 || !callback.EnableRefreshInterval || callback.RefreshInterval != minRefreshIntervalSeconds {
		t.Fatal("enabling refresh did not notify the original effective minimum interval")
	}
	if string(absorbReadRoot(t, pool)["refreshInterval"]) != "15" {
		t.Fatal("enabling refresh rewrote an omitted raw interval")
	}
}

func TestAbsorbStrategyReportPutBeforeGetUsesLegacyChannelIDs(t *testing.T) {
	pool, scope, _ := absorbStrategyStore(t)
	service := absorbStrategyService(pool)
	for _, tc := range []struct {
		name, channels, ids string
		enabled, invalid    bool
	}{
		{"trimmed-enabled", `[{"id":" bot-a ","enabled":true}]`, `["bot-a"]`, true, false},
		{"trimmed-disabled", `[{"id":" bot-a ","enabled":false}]`, `["bot-a"]`, true, false},
		{"trimmed-duplicates", `[{"id":" bot-a ","enabled":false},{"id":"bot-a","enabled":true}]`, `["bot-a","bot-a"]`, true, false},
		{"missing-channel-ids", `[{"id":"","enabled":true},{"id":"  ","enabled":false}]`, `["bot-a"]`, false, true},
		{"mixed-missing-recipient", `[{"id":" bot-a ","enabled":true}]`, `["bot-a","missing","bot-a"]`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(t.Context(), `UPDATE notification_channel_settings SET settings=$1::jsonb`, `{"telegram":`+tc.channels+`}`); err != nil {
				t.Fatal(err)
			}
			absorbSaveRoot(t, pool, fmt.Sprintf(`{"enableBalanceWarning":true,"balanceNotifyBotIds":%s,"enableMultiplierAlert":true,"multiplierNotifyBotIds":%s,"dailyReportEnabled":false,"dailyReportTime":"09:15","dailyReportBotIds":[],"futureRoot":{"keep":true}}`, tc.ids, tc.ids))
			before := absorbReadRoot(t, pool)
			var channelBefore, channelAfter []byte
			if err := pool.QueryRow(t.Context(), `SELECT settings FROM notification_channel_settings`).Scan(&channelBefore); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]any{"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt, "dailyReportTime": "10:45"})
			response := absorbStrategyRequest(service, string(body))
			if response.Code != 200 {
				t.Fatalf("PUT before GET status=%d body=%s", response.Code, response.Body.String())
			}
			var saved StrategySettings
			if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
				t.Fatal(err)
			}
			after := absorbReadRoot(t, pool)
			for key, raw := range before {
				if key != "dailyReportTime" && !reflect.DeepEqual(raw, after[key]) {
					t.Errorf("read projection rewrote unsubmitted strategy %s", key)
				}
			}
			if err := pool.QueryRow(t.Context(), `SELECT settings FROM notification_channel_settings`).Scan(&channelAfter); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(channelBefore, channelAfter) {
				t.Fatal("report PUT rewrote channel IDs")
			}
			request := httptest.NewRequest(http.MethodGet, "/api/settings/strategy", nil)
			request = request.WithContext(authctx.WithUserID(request.Context(), scope.UserID))
			recorder := httptest.NewRecorder()
			(&Handler{service: service}).getStrategy(recorder, request)
			if recorder.Code != 200 {
				t.Fatalf("GET after PUT status=%d", recorder.Code)
			}
			var read StrategySettings
			if err := json.Unmarshal(recorder.Body.Bytes(), &read); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(saved, read) {
				t.Error("PUT before GET disagrees with the existing legacy read projection")
			}
			if saved.EnableBalanceWarning != tc.enabled || saved.EnableMultiplierAlert != tc.enabled || saved.BalanceNotifyRecipientsInvalid != tc.invalid || saved.MultiplierNotifyRecipientsInvalid != tc.invalid {
				t.Fatal("legacy trim/disabled/duplicate/missing channel semantics changed")
			}
		})
	}
}

func TestAbsorbStrategyLegacyTrimDoesNotRelaxDailyTargetSelection(t *testing.T) {
	pool, scope, _ := absorbStrategyStore(t)
	service := absorbStrategyService(pool)
	if _, err := pool.Exec(t.Context(), `UPDATE notification_channel_settings SET settings='{"telegram":[{"id":" bot-a ","enabled":true,"botToken":"fixture-token","chatId":"fixture-chat"}]}'`); err != nil {
		t.Fatal(err)
	}
	original := `{"enableBalanceWarning":true,"balanceNotifyBotIds":["bot-a"],"enableMultiplierAlert":true,"multiplierNotifyBotIds":["bot-a"],"dailyReportEnabled":false,"dailyReportTime":"09:15","dailyReportBotIds":[]}`
	absorbSaveRoot(t, pool, original)
	before := absorbReadRoot(t, pool)
	body, _ := json.Marshal(map[string]any{"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt, "dailyReportEnabled": true, "dailyReportBotIds": []string{"bot-a"}})
	response := absorbStrategyRequest(service, string(body))
	if response.Code != 409 || !reflect.DeepEqual(before, absorbReadRoot(t, pool)) {
		t.Fatal("legacy display trimming relaxed strict daily target selection")
	}
	for _, kind := range []struct{ ids, invalid, enabled string }{{"balanceNotifyBotIds", "balanceNotifyRecipientsInvalid", "enableBalanceWarning"}, {"multiplierNotifyBotIds", "multiplierNotifyRecipientsInvalid", "enableMultiplierAlert"}} {
		absorbSaveRoot(t, pool, original)
		body, _ := json.Marshal(map[string]any{"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt, "dailyReportTime": "10:45", kind.ids: []string{"bot-a", "missing"}})
		response := absorbStrategyRequest(service, string(body))
		if response.Code != 200 {
			t.Fatalf("explicit legacy recipient PUT status=%d", response.Code)
		}
		var saved map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		if string(saved[kind.ids]) != `["bot-a"]` || string(saved[kind.invalid]) != "true" || string(saved[kind.enabled]) != "true" {
			t.Fatal("explicit legacy recipient projection lost trimmed IDs or immediate invalid warning")
		}
	}
}
