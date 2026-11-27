package my_sites

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/workspacescope"
)

type inventoryTestRepo struct {
	scopedAdditionalRepo
	scope ScopedConnectionScope
	valid bool
}

func (r *inventoryTestRepo) ReadScopedConnectionSnapshots(ctx context.Context, u, w string, scope ScopedConnectionScope) ([]ScopedConnectionSnapshot, error) {
	r.scope = scope
	return r.scopedAdditionalRepo.ReadScopedConnectionSnapshots(ctx, u, w, scope)
}
func (r *inventoryTestRepo) ValidateInventoryWorkspace(context.Context, workspacescope.Scope, bool) error {
	if !r.valid {
		return workspacescope.ErrChanged
	}
	return nil
}
func (r *inventoryTestRepo) ApplyInventoryConnectionStatuses(ctx context.Context, _ workspacescope.Scope, _ bool, snapshots []ScopedConnectionSnapshot, statuses map[string]string) (ScopedStatusApplyResult, error) {
	return r.ApplyScopedConnectionStatuses(ctx, snapshots, statuses)
}

func TestInventoryCheckFullListOnce(t *testing.T) {
	rows := []ScopedConnectionSnapshot{scopedTestRow("deleted", "101"), scopedTestRow("restored", "102"), scopedTestRow("duplicate", "102")}
	rows[1].Status = "missing"
	repo := &inventoryTestRepo{scopedAdditionalRepo: scopedAdditionalRepo{snapshots: rows}, valid: true}
	calls := 0
	svc, _ := scopedTestService(&repo.scopedAdditionalRepo, upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "http://inventory.invalid", AccessToken: "fixture"}, func(r *http.Request) (*http.Response, error) {
		calls++
		switch r.URL.Path {
		case "/api/v1/auth/me":
			return scopedResponse(`{"code":0,"data":{"role":"admin"}}`, 200), nil
		case "/api/v1/admin/accounts":
			return scopedResponse(`{"code":0,"data":{"items":[{"id":102}],"total":1}}`, 200), nil
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			return scopedResponse(`{}`, 500), nil
		}
	})
	svc.connRepository = repo
	var req ScopedCheckRequest
	if err := json.Unmarshal([]byte(`{"workspaceAdminAccountId":"w","workspaceCreatedAt":"2026-11-27T01:00:00Z","mode":"inventory","workspaceWide":true}`), &req); err != nil {
		t.Fatal(err)
	}
	result, err := svc.CheckScopedRealConnections(t.Context(), "u", req)
	if err != nil || calls != 2 || repo.writes != 1 || len(result.ConfirmedStatuses) != 3 || result.ConfirmedStatuses["deleted"] != "missing" || result.ConfirmedStatuses["restored"] != "active" || result.ConfirmedStatuses["duplicate"] != "active" {
		t.Fatalf("inventory must confirm one full list and save accurate statuses: calls=%d writes=%d result=%+v err=%v", calls, repo.writes, result, err)
	}
}

func TestNewAccountPlatformPrefixes(t *testing.T) {
	for platform, want := range map[string]string{"openai": "A-GPT", "anthropic": "B-Claude", "gemini": "C-Gemini", "antigravity": "D-Antigravity", "grok": "X-Grok", "kimi": "X-Kimi", "zhipu": "X-GLM", "deepseek": "X-DeepSeek", "minimax": "X-MiniMax", "unknown": "X"} {
		if got := groupTypePrefix(platform); got != want {
			t.Errorf("platform=%s got=%s want=%s", platform, got, want)
		}
	}
}

func TestInventoryCheckRejectedAndEmptyList(t *testing.T) {
	for _, caseName := range []string{"empty", "incomplete", "invalid workspace", "no candidates", "other platform"} {
		t.Run(caseName, func(t *testing.T) {
			repo := &inventoryTestRepo{scopedAdditionalRepo: scopedAdditionalRepo{snapshots: []ScopedConnectionSnapshot{scopedTestRow("a", "101")}}, valid: caseName != "invalid workspace"}
			if caseName == "no candidates" {
				repo.snapshots = nil
			}
			if caseName == "other platform" {
				repo.snapshots[0].AdminPlatform = "newapi"
			}
			calls := 0
			svc, _ := scopedTestService(&repo.scopedAdditionalRepo, upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "http://inventory.invalid", AccessToken: "fixture"}, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path == "/api/v1/auth/me" {
					return scopedResponse(`{"code":0,"data":{"role":"admin"}}`, 200), nil
				}
				if r.URL.Path != "/api/v1/admin/accounts" {
					t.Errorf("unexpected request %s", r.URL.Path)
				}
				if caseName == "incomplete" {
					return scopedResponse(`{"code":0,"data":{"items":[],"total":10}}`, 200), nil
				}
				return scopedResponse(`{"code":0,"data":{"items":[],"total":0}}`, 200), nil
			})
			svc.connRepository = repo
			req := ScopedCheckRequest{WorkspaceAdminAccountID: "w", WorkspaceCreatedAt: "2026-11-27T01:00:00Z", Mode: "inventory", SiteID: "site"}
			result, err := svc.CheckScopedRealConnections(t.Context(), "u", req)
			switch caseName {
			case "empty":
				if err != nil || repo.writes != 1 || result.ConfirmedStatuses["a"] != "missing" || calls != 2 || repo.scope.WorkspaceWide || repo.scope.SiteID != "site" {
					t.Fatalf("complete empty inventory: %+v %v", result, err)
				}
			case "incomplete":
				if err != nil || repo.writes != 0 || len(result.UnconfirmedConnectionIDs) != 1 || calls != 2 {
					t.Fatalf("incomplete inventory was applied: %+v %v", result, err)
				}
			case "invalid workspace":
				if err == nil || repo.writes != 0 || calls != 0 {
					t.Fatal("invalid scope reached main site")
				}
			default:
				if err != nil || repo.writes != 0 || calls != 0 {
					t.Fatalf("empty/inapplicable scope contacted main site: %v %d", err, calls)
				}
			}
		})
	}
}

func TestInventoryCheckRepositoryScopePostgres(t *testing.T) {
	repo, status := scopedAdditionalPGSetup(t)
	ctx := t.Context()
	if _, err := repo.db.Exec(ctx, `CREATE TABLE users(id text primary key,current_admin_account_id text); INSERT INTO users VALUES('u','w'); CREATE TABLE admin_accounts(user_id text,id text primary key,created_at timestamptz,platform text); INSERT INTO admin_accounts VALUES('u','w','2026-11-27T01:00:00Z','sub2api'); INSERT INTO upstream_sites VALUES('other','u','w','Other','https://other.invalid')`); err != nil {
		t.Fatal(err)
	}
	for _, x := range []RealConnection{{ID: "other", UserID: "u", WorkspaceAdminAccountID: "w", UpstreamSiteID: "other", UpstreamGroupID: "1", AdminAccountID: "102", AdminPlatform: "sub2api", Status: "active"}, {ID: "foreign", UserID: "u", WorkspaceAdminAccountID: "different", UpstreamSiteID: "site", AdminAccountID: "101", AdminPlatform: "sub2api", Status: "active"}} {
		x.CreatedAt = "2026-11-27T01:00:00Z"
		if err := repo.SaveRealConnection(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	var created time.Time
	if err := repo.db.QueryRow(ctx, `SELECT created_at FROM admin_accounts WHERE id='w'`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	scope := workspacescope.Scope{UserID: "u", AdminAccountID: "w", CreatedAt: created.Format(time.RFC3339Nano)}
	if err := repo.ValidateInventoryWorkspace(ctx, scope, true); err != nil {
		t.Fatal(err)
	}
	single, err := repo.ReadScopedConnectionSnapshots(ctx, "u", "w", ScopedConnectionScope{SiteID: "site"})
	if err != nil || len(single) != 2 {
		t.Fatalf("single rows %d %v", len(single), err)
	}
	all, err := repo.ReadScopedConnectionSnapshots(ctx, "u", "w", ScopedConnectionScope{WorkspaceWide: true})
	if err != nil || len(all) != 3 {
		t.Fatalf("all rows %d %v", len(all), err)
	}
	// Same-value confirmed write must defeat an older observation.
	applied, err := repo.ApplyInventoryConnectionStatuses(ctx, scope, true, single, map[string]string{"a": "active", "b": "missing"})
	if err != nil || len(applied.ConfirmedConnectionIDs) != 2 {
		t.Fatalf("apply %+v %v", applied, err)
	}
	old, err := repo.ApplyScopedConnectionStatuses(ctx, all, map[string]string{"a": "missing", "b": "active"})
	if err != nil || len(old.ChangedConnectionIDs) != 2 {
		t.Fatalf("older snapshot overwrote confirmed rows: %+v %v", old, err)
	}
	for id, want := range map[string]string{"a": "active", "b": "missing", "other": "active", "foreign": "active"} {
		got, e := status(id)
		if e != nil || got != want {
			t.Fatalf("%s status %s want %s (%v)", id, got, want, e)
		}
	}
	if _, err := repo.db.Exec(ctx, `UPDATE users SET current_admin_account_id='other' WHERE id='u'`); err != nil {
		t.Fatal(err)
	}
	_, err = repo.ApplyInventoryConnectionStatuses(ctx, scope, true, all, map[string]string{"other": "missing"})
	if err == nil {
		t.Fatal("stale selected workspace allowed write")
	}
	if got, _ := status("other"); got != "active" {
		t.Fatal("other status changed")
	}
}

func TestInventoryCheckConcurrentBusy(t *testing.T) {
	repo := &inventoryTestRepo{scopedAdditionalRepo: scopedAdditionalRepo{snapshots: []ScopedConnectionSnapshot{scopedTestRow("a", "101")}}, valid: true}
	started := make(chan struct{})
	release := make(chan struct{})
	svc, _ := scopedTestService(&repo.scopedAdditionalRepo, upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: "http://inventory.invalid", AccessToken: "fixture"}, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/auth/me" {
			close(started)
			<-release
			return scopedResponse(`{"code":0,"data":{"role":"admin"}}`, 200), nil
		}
		return scopedResponse(`{"code":0,"data":{"items":[],"total":0}}`, 200), nil
	})
	svc.connRepository = repo
	req := ScopedCheckRequest{WorkspaceAdminAccountID: "w", WorkspaceCreatedAt: "2026-11-27T01:00:00Z", WorkspaceWide: true, Mode: "inventory"}
	done := make(chan error, 1)
	go func() { _, err := svc.CheckScopedRealConnections(t.Context(), "u", req); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("check did not start")
	}
	other := req
	other.WorkspaceWide = false
	other.SiteID = "site"
	_, err := svc.CheckScopedRealConnections(t.Context(), "u", other)
	close(release)
	if err == nil {
		t.Fatal("overlapping check was accepted")
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if repo.writes != 1 {
		t.Fatal("did not save exactly one check")
	}
}
