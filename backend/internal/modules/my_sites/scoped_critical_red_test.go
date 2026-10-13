package my_sites

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

// This exercises the authenticated route, real row versions and the shared
// HTTP client. The upstream simulator is started explicitly and always closed.
func TestScopedConnectionCriticalHTTPPostgres(t *testing.T) {
	for _, mode := range []string{"automatic", "manual", "manual-contradictory"} {
		t.Run(mode, func(t *testing.T) {
			pool, repo := openMySitesCleanupPostgres(t)
			ctx := context.Background()
			_, err := pool.Exec(ctx, `CREATE TABLE upstream_sites(id text,user_id text,admin_account_id text,name text,base_url text); INSERT INTO upstream_sites VALUES('site-a','user-1','workspace-1','Site A','https://a.invalid'),('site-b','user-1','workspace-1','Site B','https://b.invalid'); CREATE TABLE group_rate_snapshots(id text,user_id text,admin_account_id text,site_id text,group_id text,group_name text,created_at timestamptz)`)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range []struct{ id, site, account, status string }{{"a", "site-a", "101", "active"}, {"duplicate", "site-a", "101", "active"}, {"missing", "site-a", "102", "missing"}, {"outside", "site-b", "103", "active"}} {
				err := repo.SaveRealConnection(ctx, RealConnection{ID: v.id, UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: v.site, UpstreamGroupID: "1", UpstreamGroupName: "Group", UpstreamKeyID: "key-" + v.id, AdminAccountID: v.account, Status: v.status, AdminPlatform: "sub2api", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
				if err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			counts := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				counts[r.URL.Path]++
				mu.Unlock()
				switch r.URL.Path {
				case "/api/v1/auth/me":
					writeConnectionTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"role": "admin"}})
				case "/api/v1/admin/accounts/101", "/api/v1/admin/accounts/102":
					http.Error(w, "Account not found", 404)
				case "/api/v1/admin/accounts":
					if mode == "manual-contradictory" {
						writeConnectionTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"items": []any{}, "total": 0, "has_more": true}})
						return
					}
					writeConnectionTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"id": 102}}, "total": 1}})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.Error(w, "unexpected", 500)
				}
			}))
			defer server.Close()
			session := platformTestSession(upstream.PlatformSub2API, server.URL)
			expired := time.Now().Add(-time.Hour).UnixMilli()
			session.ExpiresAt = &expired
			session.RefreshToken = "scoped-test-refresh-placeholder"
			if err := repo.Save(ctx, State{UserID: "user-1", AdminAccountID: "workspace-1", Session: session}); err != nil {
				t.Fatal(err)
			}
			var sessionVersion string
			if err := pool.QueryRow(ctx, `SELECT xmin::text FROM my_site_states WHERE user_id='user-1' AND admin_account_id='workspace-1'`).Scan(&sessionVersion); err != nil {
				t.Fatal(err)
			}
			s := NewService(repo, upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), nil)
			s.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
			mux := http.NewServeMux()
			RegisterRoutes(mux, s)
			checkMode := mode
			if mode == "manual-contradictory" {
				checkMode = "manual"
			}
			body := fmt.Sprintf(`{"workspaceAdminAccountId":"workspace-1","siteId":"site-a","mode":%q}`, checkMode)
			req := httptest.NewRequest(http.MethodPost, "/api/my-sites/real-connections/check-scoped", strings.NewReader(body))
			req = req.WithContext(authctx.WithUserID(req.Context(), "user-1"))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("scoped authenticated check status=%d expected200", w.Code)
			}
			var v map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			if v["total"] != float64(3) {
				t.Fatalf("frozen total=%v want3", v["total"])
			}
			mu.Lock()
			defer mu.Unlock()
			if counts["/api/v1/auth/me"] != 1 || counts["/api/v1/admin/accounts/101"] != 1 || counts["/api/v1/admin/accounts/102"] != 1 {
				t.Fatalf("account or verification repeated: %#v", counts)
			}
			if mode == "automatic" && counts["/api/v1/admin/accounts"] != 0 {
				t.Fatal("automatic check read complete account inventory")
			}
			if checkMode == "manual" && counts["/api/v1/admin/accounts"] != 1 {
				t.Fatal("manual multiple404 did not read inventory once")
			}
			var finalSessionVersion string
			if err := pool.QueryRow(ctx, `SELECT xmin::text FROM my_site_states WHERE user_id='user-1' AND admin_account_id='workspace-1'`).Scan(&finalSessionVersion); err != nil {
				t.Fatal(err)
			}
			if finalSessionVersion != sessionVersion {
				t.Fatal("scoped check saved the stored session/mappings")
			}
			statuses := map[string]string{}
			rows, err := pool.Query(ctx, `SELECT id,status FROM real_connections`)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var id, status string
				if err := rows.Scan(&id, &status); err != nil {
					t.Fatal(err)
				}
				statuses[id] = status
			}
			rows.Close()
			if statuses["outside"] != "active" {
				t.Fatal("outside scope was changed")
			}
			if mode == "automatic" && (statuses["a"] != "active" || statuses["duplicate"] != "active" || statuses["missing"] != "missing") {
				t.Fatal("automatic404 changed saved statuses")
			}
			if mode == "manual" && (statuses["a"] != "missing" || statuses["duplicate"] != "missing" || statuses["missing"] != "active") {
				t.Fatal("strict complete inventory existence/absence was not applied")
			}
			if mode == "manual-contradictory" {
				if statuses["a"] != "active" || statuses["duplicate"] != "active" || statuses["missing"] != "missing" {
					t.Fatal("Review1 contradictory pagination changed saved statuses")
				}
				confirmed, _ := v["confirmedConnectionIds"].([]any)
				unknown, _ := v["unconfirmedConnectionIds"].([]any)
				if len(confirmed) != 0 || len(unknown) != 3 {
					t.Fatal("Review1 contradictory pagination authorized an unconfirmed account")
				}
			}
		})
	}
}

func TestScopedConnectionWindowPostgres(t *testing.T) {
	pool, repo := openMySitesCleanupPostgres(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE upstream_sites(id text,user_id text,admin_account_id text,name text,base_url text); INSERT INTO upstream_sites VALUES('site-a','user-1','workspace-1','Site A','https://a.invalid'); CREATE TABLE group_rate_snapshots(id text,user_id text,admin_account_id text,site_id text,group_id text,group_name text,created_at timestamptz)`); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"same-value", "rebound", "deleted", "untouched"} {
		if err := repo.SaveRealConnection(ctx, RealConnection{ID: id, UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: "site-a", UpstreamGroupID: "1", UpstreamGroupName: "Group", AdminAccountID: fmt.Sprint(101 + i), AdminPlatform: "sub2api", Status: "missing", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SaveRealConnection(ctx, RealConnection{ID: "other-workspace", UserID: "user-1", WorkspaceAdminAccountID: "workspace-2", UpstreamSiteID: "site-a", UpstreamGroupID: "1", UpstreamGroupName: "Group", AdminAccountID: "104", AdminPlatform: "sub2api", Status: "missing", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/me" {
			writeConnectionTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"role": "admin"}})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/accounts/")
		var err error
		switch id {
		case "101":
			_, err = pool.Exec(ctx, `UPDATE real_connections SET status=status WHERE id='same-value'; INSERT INTO real_connections SELECT 'new-record',user_id,workspace_admin_account_id,upstream_site_id,upstream_group_id,upstream_group_name,upstream_key_id,upstream_key,admin_account_id,admin_account_name,own_group_ids,group_type,created_at,own_group_names,provisioning_mode,status,upstream_platform,admin_platform,pricing_mapping_enabled,operation_id FROM real_connections WHERE id='same-value'`)
		case "102":
			_, err = pool.Exec(ctx, `UPDATE real_connections SET admin_account_id='999' WHERE id='rebound'`)
		case "103":
			_, err = pool.Exec(ctx, `DELETE FROM real_connections WHERE id='deleted'`)
		}
		if err != nil {
			t.Error(err)
			http.Error(w, "fixture failed", 500)
			return
		}
		writeConnectionTestJSON(w, map[string]any{"code": 0, "data": map[string]any{"id": id, "status": "inactive", "error_message": "old error", "group_ids": []any{}}})
	}))
	defer server.Close()
	if err := repo.Save(ctx, State{UserID: "user-1", AdminAccountID: "workspace-1", Session: platformTestSession(upstream.PlatformSub2API, server.URL)}); err != nil {
		t.Fatal(err)
	}
	s := NewService(repo, upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), nil)
	s.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)
	req := httptest.NewRequest(http.MethodPost, "/api/my-sites/real-connections/check-scoped", strings.NewReader(`{"workspaceAdminAccountId":"workspace-1","siteId":"site-a","mode":"automatic"}`))
	req = req.WithContext(authctx.WithUserID(req.Context(), "user-1"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v["total"] != float64(4) || len(v["changedConnectionIds"].([]any)) != 3 || len(v["confirmedConnectionIds"].([]any)) != 1 {
		t.Fatalf("frozen result total=%v changed=%v confirmed=%v", v["total"], v["changedConnectionIds"], v["confirmedConnectionIds"])
	}
	for _, id := range []string{"same-value", "rebound", "new-record", "other-workspace"} {
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM real_connections WHERE id=$1`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "missing" {
			t.Fatalf("%s overwritten", id)
		}
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM real_connections WHERE id='untouched'`).Scan(&status); err != nil || status != "active" {
		t.Fatal("inactive/error/off-group existing account was not confirmed active")
	}
}
