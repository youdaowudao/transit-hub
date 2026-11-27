package settings

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"transithub/backend/internal/shared/authctx"
)

func TestMainAccountCheckConfigDefaultsAndValidation(t *testing.T) {
	cfg, e := decodeMainAccountCheckConfig(nil)
	if e != "" || !cfg.Enabled || !reflect.DeepEqual(cfg.Times, []string{"09:00", "13:00", "18:00", "22:00"}) {
		t.Fatal("wrong default")
	}
	for _, raw := range []string{`null`, `{}`, `{"enabled":true,"times":["09:00","09:00","18:00","22:00"]}`, `{"enabled":true,"times":["9:00","13:00","18:00","24:00"]}`} {
		got, err := decodeMainAccountCheckConfig([]byte(raw))
		if err == "" || got.Enabled {
			t.Errorf("malformed config enabled: %s", raw)
		}
	}
	got, e := decodeMainAccountCheckConfig([]byte(`{"enabled":false,"times":["08:00","12:00","17:00","21:00"]}`))
	if e != "" || got.Enabled || got.Times[0] != "08:00" {
		t.Fatal("saved disabled not retained")
	}
}
func TestMainAccountCheckSettingsPostgres(t *testing.T) {
	pool, scope, _ := absorbStrategyStore(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `ALTER TABLE admin_accounts ADD COLUMN platform text NOT NULL DEFAULT 'sub2api';CREATE TABLE real_connections(user_id text,workspace_admin_account_id text,admin_platform text,status text,admin_account_id text);INSERT INTO real_connections VALUES('fixture-user','fixture-workspace','sub2api','missing','42');`); err != nil {
		t.Fatal(err)
	}
	svc := absorbStrategyService(pool)
	items, err := svc.ListMainAccountCheckWorkspaces(ctx)
	if err != nil || len(items) != 1 || !items[0].Config.Enabled {
		t.Fatalf("default workspace not scheduled: %+v %v", items, err)
	}
	absorbSaveRoot(t, pool, `{"refreshInterval":120,"dailyReportEnabled":false,"unknown":{"keep":123}}`)
	cfg := MainAccountCheckConfig{Enabled: false, Times: []string{"08:00", "12:00", "17:00", "21:00"}}
	if err = svc.SaveMainAccountCheckConfig(ctx, scope, cfg); err != nil {
		t.Fatal(err)
	}
	result, err := svc.ReadMainAccountCheckConfig(ctx, scope, true)
	if err != nil || !reflect.DeepEqual(cfg, result.Config) {
		t.Fatalf("readback %+v %v", result, err)
	}
	root := absorbReadRoot(t, pool)
	if string(root["refreshInterval"]) != "120" || string(root["dailyReportEnabled"]) != "false" || len(root["unknown"]) == 0 {
		t.Fatal("old settings overwritten")
	}
	// Legacy strategy save preserves independent new config and unknown keys.
	before := string(root["mainAccountCheck"])
	if _, err := svc.repository.SaveStrategyPreservingReport(ctx, scope.UserID, scope.AdminAccountID, StrategySettings{RefreshInterval: 180}); err != nil {
		t.Fatal(err)
	}
	root = absorbReadRoot(t, pool)
	if string(root["mainAccountCheck"]) != before || len(root["unknown"]) == 0 {
		t.Fatal("legacy save overwrote main check settings")
	}
	mux := http.NewServeMux()
	RegisterMainAccountCheckRoutes(mux, svc, nil)
	body, _ := json.Marshal(map[string]any{"workspaceAdminAccountId": scope.AdminAccountID, "workspaceCreatedAt": scope.CreatedAt, "enabled": true, "times": []string{"09:00", "09:00", "18:00", "22:00"}})
	req := httptest.NewRequest(http.MethodPut, "/api/settings/main-account-check", strings.NewReader(string(body)))
	req = req.WithContext(authctx.WithUserID(req.Context(), scope.UserID))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 400 || string(absorbReadRoot(t, pool)["mainAccountCheck"]) != before {
		t.Fatal("invalid times saved")
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET current_admin_account_id='other' WHERE id='fixture-user'`); err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveMainAccountCheckConfig(ctx, scope, MainAccountCheckConfig{Enabled: true, Times: cfg.Times}); err == nil {
		t.Fatal("stale selected workspace saved")
	}
}
