package my_sites

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"transithub/backend/internal/shared/authctx"
)

func attentionCriticalRequest(s *Service, method, path, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	RegisterRoutes(mux, s)
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r = r.WithContext(authctx.WithUserID(r.Context(), "user-1"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func attentionCriticalExpected(c RealConnection) string {
	value := map[string]any{"workspaceAdminAccountId": c.WorkspaceAdminAccountID, "id": c.ID, "upstreamSiteId": c.UpstreamSiteID, "upstreamGroupId": c.UpstreamGroupID, "upstreamGroupName": c.UpstreamGroupName, "upstreamKeyId": c.UpstreamKeyID, "adminAccountId": c.AdminAccountID, "adminPlatform": c.AdminPlatform, "upstreamPlatform": c.UpstreamPlatform, "provisioningMode": c.ProvisioningMode, "status": c.Status, "ownGroupIds": c.OwnGroupIDs, "ownGroupNames": c.OwnGroupNames, "pricingMappingEnabled": c.PricingMappingEnabled}
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestAttentionCriticalReadWithoutSnapshotsAndNoSideEffectsPostgres(t *testing.T) {
	pool, repo := openMySitesCleanupPostgres(t)
	_, err := pool.Exec(context.Background(), `CREATE TABLE upstream_sites(id text,user_id text,admin_account_id text,name text,metrics jsonb,last_synced_at bigint); INSERT INTO upstream_sites VALUES('site-a','user-1','workspace-1','Site A','{"groups":[{"id":"auto","name":"auto","multiplierMode":"auto"}]}',1000)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []RealConnection{
		{ID: "active", UpstreamGroupID: "gone", UpstreamGroupName: "gone", Status: "active"},
		{ID: "missing", UpstreamGroupID: "gone", UpstreamGroupName: "gone", Status: "missing"},
		{ID: "legacy", UpstreamGroupID: "", UpstreamGroupName: "gone", Status: ""},
		{ID: "auto", UpstreamGroupID: "auto", UpstreamGroupName: "auto", Status: "active"},
	} {
		c.UserID = "user-1"
		c.WorkspaceAdminAccountID = "workspace-1"
		c.UpstreamSiteID = "site-a"
		c.UpstreamKey = "fixture-must-not-be-public"
		c.OperationID = "fixture-must-not-be-public-" + c.ID
		insertCleanupConnection(t, repo, c)
	}
	s := NewService(repo, nil, nil)
	s.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
	w := attentionCriticalRequest(s, "GET", "/api/my-sites/real-connections/needs-attention?workspaceAdminAccountId=workspace-1", "")
	if w.Code != 200 {
		t.Fatalf("maintenance must work with no rate table: HTTP %d %s", w.Code, w.Body.String())
	}
	var v struct {
		Total int `json:"total"`
		Items []struct {
			Connection RealConnection `json:"connection"`
			Reason     string         `json:"reason"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Total != 3 || len(v.Items) != 3 {
		t.Fatalf("lost duplicates/statuses or included auto: total=%d rows=%d", v.Total, len(v.Items))
	}
	for _, item := range v.Items {
		if item.Connection.ID == "auto" || item.Connection.UpstreamKey != "" || item.Connection.OperationID != "" || item.Reason != "group_not_found" {
			t.Fatalf("unsafe or wrong maintenance record: id=%s reason=%s", item.Connection.ID, item.Reason)
		}
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM real_connections`).Scan(&n); err != nil || n != 4 {
		t.Fatal("read changed connections")
	}
}

func TestAttentionCriticalChangedIdentityRejectsBeforeRuntimeOrMappingPostgres(t *testing.T) {
	for _, change := range []string{"own_group_ids='[\"20\"]'", "status='active'", "upstream_key_id='replacement'", "upstream_group_id='replacement'"} {
		t.Run(change, func(t *testing.T) {
			pool, repo := openMySitesCleanupPostgres(t)
			prepareCleanupState(t, repo, []UpstreamGroupRef{{SiteID: "site-a", GroupName: "gone"}})
			c := RealConnection{ID: "checked", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: "site-a", UpstreamGroupID: "gone", UpstreamGroupName: "gone", UpstreamKeyID: "key-a", AdminAccountID: "account-a", AdminPlatform: "sub2api", UpstreamPlatform: "sub2api", OwnGroupIDs: []string{"10"}, OwnGroupNames: []string{"own-a"}, Status: "missing", PricingMappingEnabled: true}
			insertCleanupConnection(t, repo, c)
			actual, err := repo.GetRealConnection(context.Background(), c.ID, c.UserID, c.WorkspaceAdminAccountID)
			if err != nil {
				t.Fatal(err)
			}
			expected := attentionCriticalExpected(*actual)
			if _, err := pool.Exec(context.Background(), `UPDATE real_connections SET `+change+` WHERE id='checked'`); err != nil {
				t.Fatal(err)
			}
			cleaner := &cleanupRuntimeRecorder{}
			s := NewService(repo, nil, nil)
			s.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
			s.SetConnectionRuntimeCleaner(cleaner)
			w := attentionCriticalRequest(s, "POST", "/api/my-sites/real-disconnect", `{"connectionId":"checked","mode":"unlink","removePricingMapping":true,"expectedConnection":`+expected+`}`)
			if w.Code != 409 {
				t.Fatalf("changed public identity must conflict: HTTP %d %s", w.Code, w.Body.String())
			}
			assertCleanupConnectionCount(t, pool, "checked", 1)
			if cleaner.calls != 0 {
				t.Fatal("changed identity ran cleanup")
			}
			state, err := repo.Get(context.Background(), "user-1", "workspace-1")
			if err != nil || len(state.Mappings[0].UpstreamTargets) != 1 {
				t.Fatal("changed identity altered mapping")
			}
		})
	}
}

func TestAttentionCriticalLocalDefaultPreservesMappingPostgres(t *testing.T) {
	pool, repo := openMySitesCleanupPostgres(t)
	prepareCleanupState(t, repo, []UpstreamGroupRef{{SiteID: "site-a", GroupName: "gone"}})
	c := RealConnection{ID: "checked", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: "site-a", UpstreamGroupName: "gone", OwnGroupNames: []string{"own-a"}, Status: "active", PricingMappingEnabled: true}
	insertCleanupConnection(t, repo, c)
	actual, err := repo.GetRealConnection(context.Background(), c.ID, c.UserID, c.WorkspaceAdminAccountID)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(repo, nil, nil)
	s.SetAdminAccountResolver(testAdminResolver{currentID: "workspace-1"})
	w := attentionCriticalRequest(s, "POST", "/api/my-sites/real-disconnect", `{"connectionId":"checked","mode":"unlink","expectedConnection":`+attentionCriticalExpected(*actual)+`}`)
	if w.Code != 200 {
		t.Fatalf("local unlink failed HTTP %d %s", w.Code, w.Body.String())
	}
	assertCleanupConnectionCount(t, pool, "checked", 0)
	state, err := repo.Get(context.Background(), "user-1", "workspace-1")
	if err != nil || len(state.Mappings[0].UpstreamTargets) != 1 {
		t.Fatal("maintenance default deleted pricing mapping")
	}
}
