package my_sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAttentionAdditionalSourceShapesPostgres(t *testing.T) {
	pool, repo := openMySitesCleanupPostgres(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE upstream_sites(id text,user_id text,admin_account_id text,name text,metrics jsonb,last_synced_at bigint); INSERT INTO upstream_sites VALUES('site-a','user-1','workspace-1','Site A','{}',1000)`); err != nil {
		t.Fatal(err)
	}
	insertCleanupConnection(t, repo, RealConnection{ID: "connection", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: "site-a", UpstreamGroupName: "Group", Status: "active"})
	for _, sample := range []struct {
		name, metrics, connectionID, reason string
		unsynced                            bool
	}{
		{name: "same ID renamed", metrics: `{"groups":[{"id":"A","name":"Renamed"}]}`, connectionID: "A"},
		{name: "different nonblank ID", metrics: `{"groups":[{"id":"B","name":"Group"}]}`, connectionID: "A", reason: "group_not_found"},
		{name: "legacy source blank ID", metrics: `{"groups":[{"id":"","name":"Group"}]}`, connectionID: "A"},
		{name: "legacy connection blank ID", metrics: `{"groups":[{"id":"A","name":"Group"}]}`},
		{name: "ambiguous legacy name", metrics: `{"groups":[{"id":"A","name":"Group"},{"id":"B","name":"Group"}]}`, reason: "source_unconfirmed"},
		{name: "exact ID takes precedence", metrics: `{"groups":[{"id":"A","name":"Group"},{"id":"B","name":"Group"}]}`, connectionID: "A"},
		{name: "duplicate IDs", metrics: `{"groups":[{"id":"A","name":"Group"},{"id":"A","name":"Other"}]}`, connectionID: "A", reason: "source_unconfirmed"},
		{name: "auto stays present", metrics: `{"groups":[{"id":"A","name":"Group","multiplierMode":"auto","multiplier":null}]}`, connectionID: "A"},
		{name: "no saved sync", metrics: `{"groups":[{"id":"A","name":"Group"}]}`, connectionID: "A", unsynced: true, reason: "source_unconfirmed"},
		{name: "empty saved groups", metrics: `{"groups":[]}`, connectionID: "A", reason: "source_unconfirmed"},
		{name: "missing groups", metrics: `{}`, connectionID: "A", reason: "source_unconfirmed"},
		{name: "object groups", metrics: `{"groups":{}}`, connectionID: "A", reason: "source_unconfirmed"},
		{name: "scalar item", metrics: `{"groups":[false]}`, connectionID: "A", reason: "source_unconfirmed"},
		{name: "bad ID", metrics: `{"groups":[{"id":1,"name":"Group"}]}`, connectionID: "A", reason: "source_unconfirmed"},
		{name: "bad name", metrics: `{"groups":[{"id":"A","name":1}]}`, connectionID: "A", reason: "source_unconfirmed"},
		{name: "noncanonical name", metrics: `{"groups":[{"id":"A","name":" Group"}]}`, connectionID: "A", reason: "source_unconfirmed"},
		{name: "tabbed name", metrics: `{"groups":[{"id":"A","name":"\tGroup"}]}`, connectionID: "A", reason: "source_unconfirmed"},
		{name: "bad multiplier", metrics: `{"groups":[{"id":"A","name":"Group","multiplier":"oops"}]}`, connectionID: "A", reason: "source_unconfirmed"},
		{name: "negative multiplier", metrics: `{"groups":[{"id":"A","name":"Group","multiplier":-1}]}`, connectionID: "A", reason: "source_unconfirmed"},
		{name: "zero multiplier", metrics: `{"groups":[{"id":"A","name":"Group","multiplier":0}]}`, connectionID: "A"},
		{name: "bad mode", metrics: `{"groups":[{"id":"A","name":"Group","multiplierMode":false}]}`, connectionID: "A", reason: "source_unconfirmed"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			var synced any = int64(1000)
			if sample.unsynced {
				synced = nil
			}
			if _, err := pool.Exec(ctx, `UPDATE upstream_sites SET metrics=$1::jsonb,last_synced_at=$2`, sample.metrics, synced); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE real_connections SET upstream_group_id=$1 WHERE id='connection'`, sample.connectionID); err != nil {
				t.Fatal(err)
			}
			result, err := repo.ListConnectionsNeedingAttention(ctx, "user-1", "workspace-1", NeedsAttentionQuery{Page: 1})
			if err != nil {
				t.Fatal(err)
			}
			if sample.reason == "" {
				if result.Total != 0 || len(result.Items) != 0 {
					t.Fatalf("known group wrongly needs attention: %#v", result)
				}
			} else if result.Total != 1 || len(result.Items) != 1 || result.Items[0].Reason != sample.reason || result.Items[0].Detail == "" {
				t.Fatalf("unsafe shape classification: %#v", result)
			}
		})
	}
	if _, err := pool.Exec(ctx, `DELETE FROM upstream_sites`); err != nil {
		t.Fatal(err)
	}
	result, err := repo.ListConnectionsNeedingAttention(ctx, "user-1", "workspace-1", NeedsAttentionQuery{Page: 1})
	if err != nil || len(result.Items) != 1 || result.Items[0].Reason != "source_unconfirmed" || result.Items[0].LastSyncedAt != nil {
		t.Fatalf("removed site was not retained as unconfirmed: %#v err=%v", result, err)
	}
}

type attentionRuntimeDB struct {
	pool  *pgxpool.Pool
	calls int
}

func (r *attentionRuntimeDB) CleanupRealConnectionRuntime(ctx context.Context, userID, workspaceID, connectionID string) error {
	r.calls++
	_, err := r.pool.Exec(ctx, `DELETE FROM connection_health_states WHERE connection_id=$1 AND user_id=$2 AND admin_account_id=$3`, connectionID, userID, workspaceID)
	return err
}

func TestAttentionAdditionalRuntimePartialAndPoolLimitPostgres(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(fmt.Sprintf("limited pool %t", limited), func(t *testing.T) {
			pool, repo := openMySitesCleanupPostgres(t)
			ctx := context.Background()
			prepareCleanupState(t, repo, []UpstreamGroupRef{{SiteID: "site-a", GroupName: "gone"}})
			insertCleanupConnection(t, repo, RealConnection{ID: "checked", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: "site-a", UpstreamGroupName: "gone", OwnGroupNames: []string{"own-a"}, Status: "missing", PricingMappingEnabled: true})
			if _, err := pool.Exec(ctx, `CREATE TABLE connection_health_states(connection_id text,user_id text,admin_account_id text); INSERT INTO connection_health_states VALUES('checked','user-1','workspace-1'); CREATE TABLE connection_health_events(connection_id text); INSERT INTO connection_health_events VALUES('checked'); CREATE FUNCTION reject_runtime_partial_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'reject local connection delete'; END; $$; CREATE TRIGGER reject_runtime_partial_delete BEFORE DELETE ON real_connections FOR EACH ROW EXECUTE FUNCTION reject_runtime_partial_delete()`); err != nil {
				t.Fatal(err)
			}
			executionPool := pool
			if limited {
				config := pool.Config()
				config.MaxConns = 1
				var err error
				executionPool, err = pgxpool.NewWithConfig(ctx, config)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(executionPool.Close)
			}
			actual, err := repo.GetRealConnection(ctx, "checked", "user-1", "workspace-1")
			if err != nil {
				t.Fatal(err)
			}
			cleaner := &attentionRuntimeDB{pool: executionPool}
			service := NewService(NewRepository(executionPool), nil, nil)
			service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
			service.SetConnectionRuntimeCleaner(cleaner)
			started := time.Now()
			w := attentionCriticalRequest(service, "POST", "/api/my-sites/real-disconnect", `{"connectionId":"checked","mode":"unlink","removePricingMapping":true,"expectedConnection":`+attentionCriticalExpected(*actual)+`}`)
			if w.Code != 500 || cleaner.calls != 1 || time.Since(started) > 4*time.Second {
				t.Fatalf("local cleanup failed to exit within deadline: HTTP %d %s", w.Code, w.Body.String())
			}
			assertCleanupConnectionCount(t, pool, "checked", 1)
			var states, events int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM connection_health_states),(SELECT count(*) FROM connection_health_events)`).Scan(&states, &events); err != nil {
				t.Fatal(err)
			}
			wantStates, text := 0, "旧运行记录已清理"
			if limited {
				wantStates, text = 1, "是否已清理尚未确认"
			}
			if states != wantStates || events != 1 || !strings.Contains(w.Body.String(), text) {
				t.Fatalf("runtime effects/response drift: states=%d events=%d body=%s", states, events, w.Body.String())
			}
			state, err := repo.Get(ctx, "user-1", "workspace-1")
			if err != nil || len(state.Mappings[0].UpstreamTargets) != 1 {
				t.Fatal("post-runtime failure escaped mapping transaction")
			}
		})
	}
}

func TestAttentionAdditionalPaginationOwnershipAndErrorsPostgres(t *testing.T) {
	pool, repo := openMySitesCleanupPostgres(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE upstream_sites(id text,user_id text,admin_account_id text,name text,metrics jsonb,last_synced_at bigint); INSERT INTO upstream_sites VALUES('site-a','user-1','workspace-1','Site A','{"groups":[{"id":"live","name":"live"}]}',1000),('site-foreign','user-1','other-workspace','Foreign','{"groups":[]}',1000)`); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 35; index++ {
		insertCleanupConnection(t, repo, RealConnection{ID: fmt.Sprintf("connection-%02d", index), UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: "site-a", UpstreamGroupName: "gone", UpstreamKey: "fixture-key-must-not-leak", OperationID: fmt.Sprintf("fixture-operation-%d", index), Status: "active"})
	}
	for _, conn := range []RealConnection{{ID: "other-workspace", UserID: "user-1", WorkspaceAdminAccountID: "other-workspace"}, {ID: "other-user", UserID: "other-user", WorkspaceAdminAccountID: "workspace-1"}} {
		conn.UpstreamSiteID = "site-a"
		conn.UpstreamGroupName = "gone"
		conn.Status = "active"
		insertCleanupConnection(t, repo, conn)
	}
	service := NewService(repo, nil, nil)
	service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
	for _, page := range []int{1, 2, 3, 2147483647} {
		w := attentionCriticalRequest(service, "GET", fmt.Sprintf("/api/my-sites/real-connections/needs-attention?workspaceAdminAccountId=workspace-1&page=%d", page), "")
		if w.Code != 200 {
			t.Fatalf("page %d: HTTP %d %s", page, w.Code, w.Body.String())
		}
		var result NeedsAttentionResponse
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		want := 0
		if page == 1 {
			want = 30
		} else if page == 2 {
			want = 5
		}
		if result.Total != 35 || result.Page != page || result.PageSize != 30 || result.TotalPages != 2 || len(result.Items) != want || result.Items == nil {
			t.Fatalf("page metadata drift: %#v", result)
		}
		for index, item := range result.Items {
			if item.Connection.ID != fmt.Sprintf("connection-%02d", (page-1)*30+index) || item.LastSyncedAt == nil || *item.LastSyncedAt != 1000 {
				t.Fatal("ordering or saved sync date drift")
			}
		}
		if strings.Contains(w.Body.String(), "fixture-key-must-not-leak") || strings.Contains(w.Body.String(), "fixture-operation-") {
			t.Fatal("sensitive connection data leaked")
		}
	}
	for _, request := range []struct {
		query string
		code  int
	}{
		{"", 400}, {"workspaceAdminAccountId=other-workspace", 409}, {"workspaceAdminAccountId=workspace-1&siteId=site-foreign", 404},
		{"workspaceAdminAccountId=workspace-1&siteId=absent", 404}, {"workspaceAdminAccountId=workspace-1&page=0", 400},
		{"workspaceAdminAccountId=workspace-1&page=2147483648", 400}, {"workspaceAdminAccountId=workspace-1&page=wrong", 400},
	} {
		w := attentionCriticalRequest(service, "GET", "/api/my-sites/real-connections/needs-attention?"+request.query, "")
		if w.Code != request.code || !strings.Contains(w.Body.String(), "message") || !strings.Contains(w.Body.String(), "next") {
			t.Fatalf("ownership/error request %s: HTTP %d %s", request.query, w.Code, w.Body.String())
		}
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/my-sites/real-connections/needs-attention?workspaceAdminAccountId=workspace-1", nil))
	if w.Code != 401 {
		t.Fatal("unauthenticated attention read accepted")
	}
	if _, err := pool.Exec(ctx, `DROP TABLE upstream_sites`); err != nil {
		t.Fatal(err)
	}
	w = attentionCriticalRequest(service, "GET", "/api/my-sites/real-connections/needs-attention?workspaceAdminAccountId=workspace-1", "")
	if w.Code != 500 || !strings.Contains(w.Body.String(), "数据表") || strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal("read failure looked like an empty successful list")
	}
}

func TestAttentionAdditionalCheckedUnlinkFailureAndIdentityPostgres(t *testing.T) {
	for _, mode := range []string{"runtime failure", "delete failure after runtime", "deduplicated arrays", "shared mapping", "full rejected"} {
		t.Run(mode, func(t *testing.T) {
			pool, repo := openMySitesCleanupPostgres(t)
			ctx := context.Background()
			prepareCleanupState(t, repo, []UpstreamGroupRef{{SiteID: "site-a", GroupName: "gone"}})
			insertCleanupConnection(t, repo, RealConnection{ID: "checked", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: "site-a", UpstreamGroupName: "gone", OwnGroupNames: []string{"own-a", "own-b"}, Status: "missing", PricingMappingEnabled: true})
			if mode == "shared mapping" {
				insertCleanupConnection(t, repo, RealConnection{ID: "shared", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: "site-a", UpstreamGroupName: "gone", OwnGroupNames: []string{"own-a"}, Status: "active", PricingMappingEnabled: true})
			}
			if mode == "delete failure after runtime" {
				if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_checked_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture-secret-should-not-appear'; END; $$; CREATE TRIGGER reject_checked_delete BEFORE DELETE ON real_connections FOR EACH ROW EXECUTE FUNCTION reject_checked_delete()`); err != nil {
					t.Fatal(err)
				}
			}
			actual, err := repo.GetRealConnection(ctx, "checked", "user-1", "workspace-1")
			if err != nil {
				t.Fatal(err)
			}
			expected := expectedConnectionIdentity(*actual)
			if mode == "deduplicated arrays" {
				expected.OwnGroupNames = []string{"own-b", "own-a", "own-a"}
				expected.OwnGroupIDs = append(expected.OwnGroupIDs, expected.OwnGroupIDs...)
			}
			cleaner := &cleanupRuntimeRecorder{}
			if mode == "runtime failure" {
				cleaner.err = errors.New("fixture-secret-should-not-appear")
			}
			service := NewService(repo, nil, nil)
			service.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
			service.SetConnectionRuntimeCleaner(cleaner)
			removePricing := true
			request := RealDisconnectRequest{ConnectionID: "checked", Mode: "unlink", ExpectedConnection: &expected, RemovePricingMapping: &removePricing}
			if mode == "full rejected" {
				request.Mode = "full"
			}
			raw, _ := json.Marshal(request)
			w := attentionCriticalRequest(service, "POST", "/api/my-sites/real-disconnect", string(raw))
			wantCode, remaining := 200, 0
			if mode == "runtime failure" || mode == "delete failure after runtime" {
				wantCode, remaining = 500, 1
			} else if mode == "full rejected" {
				wantCode, remaining = 400, 1
			}
			if w.Code != wantCode {
				t.Fatalf("checked failure classification: HTTP %d %s", w.Code, w.Body.String())
			}
			assertCleanupConnectionCount(t, pool, "checked", remaining)
			state, err := repo.Get(ctx, "user-1", "workspace-1")
			if err != nil {
				t.Fatal(err)
			}
			keepMapping := mode != "deduplicated arrays"
			if (len(state.Mappings[0].UpstreamTargets) == 1) != keepMapping {
				t.Fatal("failure/shared connection changed mapping preservation")
			}
			if mode == "full rejected" && cleaner.calls != 0 {
				t.Fatal("unsupported full request ran runtime cleanup")
			}
			if mode == "delete failure after runtime" && (!strings.Contains(w.Body.String(), "旧运行记录已清理") || !strings.Contains(w.Body.String(), "原对接和映射仍保留") || cleaner.calls != 1) {
				t.Fatal("partial local completion was reported as full rollback")
			}
			if strings.Contains(w.Body.String(), "fixture-secret-should-not-appear") {
				t.Fatal("unsafe storage detail leaked")
			}
		})
	}
}

func TestAttentionAdditionalCheckedFailureMessages(t *testing.T) {
	for _, sample := range []struct {
		storage checkedDisconnectStorageError
		code    int
		text    string
	}{
		{checkedDisconnectStorageError{Step: "提交本地解除", Cause: errors.New("fixture-private"), Uncertain: true}, 503, "尚未确认"},
		{checkedDisconnectStorageError{Step: "提交本地解除", Cause: pgx.ErrTxCommitRollback, RuntimeCleared: true}, 500, "旧运行记录已清理"},
		{checkedDisconnectStorageError{Step: "清理旧运行记录", Cause: context.DeadlineExceeded, CleanupUncertain: true}, 500, "是否已清理尚未确认"},
	} {
		err := checkedDisconnectFailure(&sample.storage)
		var known *scopedRequestError
		if !errors.As(err, &known) || known.Status != sample.code || !strings.Contains(known.Message+known.Detail, sample.text) || strings.Contains(known.Detail, "fixture-private") {
			t.Fatalf("incorrect checked error: %v", err)
		}
	}
	a := RealConnection{UserID: "u", WorkspaceAdminAccountID: "w", OwnGroupIDs: []string{"2", "1", "1"}, OwnGroupNames: []string{"b", "a"}}
	b := a
	b.OwnGroupIDs, b.OwnGroupNames = []string{"1", "2"}, []string{"a", "b", "a"}
	if !sameCheckedConnection(a, b) || !reflect.DeepEqual(expectedConnectionIdentity(a).OwnGroupIDs, []string{"1", "2"}) {
		t.Fatal("public identity depended on group array order or duplicates")
	}
	deadline, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	cancel()
	if safeLocalStorageCause(deadline.Err()) == "" {
		t.Fatal("canceled local operation lost cause")
	}
}
