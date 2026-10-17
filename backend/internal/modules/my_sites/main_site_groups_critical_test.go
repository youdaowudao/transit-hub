package my_sites

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"transithub/backend/internal/modules/upstream"
)

type criticalGroupRepo struct {
	testStateRepo
	testConnRepo
	reassignErr error
	reassigns   int
}

func (r *criticalGroupRepo) ReassignRealConnectionGroupsChecked(_ context.Context, expected RealConnection, ids, names []string, remove bool) error {
	r.reassigns++
	if r.reassignErr != nil {
		return r.reassignErr
	}
	c := expected
	c.OwnGroupIDs = ids
	c.OwnGroupNames = names
	r.connection = &c
	return nil
}

type criticalGroupChanger struct {
	result GroupChangeResult
	calls  int
}

func (c *criticalGroupChanger) ChangeManagedSub2APIAccountGroups(context.Context, string, string, upstream.Session, string, string, []string, []string) (GroupChangeResult, error) {
	c.calls++
	return c.result, nil
}

func TestMainSiteGroupsCriticalFiveOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, main, kind string
		uncertain        bool
		want             string
		saves            int
	}{
		{name: "completed", main: "changed", want: "completed", saves: 1},
		{name: "unchanged", main: "unchanged", want: "unchanged"},
		{name: "main unknown", main: "unknown", want: "main_unknown"},
		{name: "record removed", main: "changed", kind: "removed", want: "main_changed_hub_not_saved", saves: 1},
		{name: "record changed", main: "changed", kind: "changed", want: "main_changed_hub_not_saved", saves: 1},
		{name: "storage rejected", main: "changed", kind: "storage", want: "main_changed_hub_not_saved", saves: 1},
		{name: "commit unknown", main: "changed", kind: "storage", uncertain: true, want: "hub_unknown", saves: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/admin/groups" {
					t.Errorf("unexpected remote %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
					return
				}
				writeConnectionTestJSON(w, map[string]any{"data": []map[string]any{{"id": 10, "name": "旧分组", "platform": "openai", "status": "active"}, {"id": 11, "name": "新分组", "platform": "openai", "status": "active"}}})
			}))
			defer remote.Close()
			session := platformTestSession(upstream.PlatformSub2API, remote.URL)
			expired := int64(1)
			session.ExpiresAt = &expired
			session.RefreshToken = "critical-test-refresh"
			old := RealConnection{ID: "connection", UserID: "u", WorkspaceAdminAccountID: "w", UpstreamSiteID: "s", UpstreamGroupID: "1", UpstreamGroupName: "上游组", AdminAccountID: "101", GroupType: "openai", AdminPlatform: "sub2api", ProvisioningMode: "managed", Status: "active", OwnGroupIDs: []string{"10"}, OwnGroupNames: []string{"旧分组"}, PricingMappingEnabled: true}
			repo := &criticalGroupRepo{testStateRepo: testStateRepo{state: &State{Session: session, Mappings: []GroupMapping{{OwnGroup: "旧分组", UpstreamTargets: []UpstreamGroupRef{{SiteID: "s", GroupName: "上游组"}}}}}}, testConnRepo: testConnRepo{connection: &old}}
			if tc.kind != "" {
				repo.reassignErr = &GroupReassignmentError{Kind: tc.kind, Step: "保存对接分组", Cause: errors.New("injected storage failure"), Uncertain: tc.uncertain}
			}
			changer := &criticalGroupChanger{result: GroupChangeResult{Outcome: tc.main, Groups: []MainSiteGroup{{ID: "11", Name: "新分组"}}, WriteError: errors.New("主站具体原因")}}
			s := NewService(repo, upstream.NewPlatformService(upstream.NewHTTPClient(remote.Client())), nil)
			s.SetAdminAccountResolver(testAdminResolver{currentID: "w"})
			s.SetManagedAccountGroupChanger(changer)
			expected := expectedConnectionIdentity(old)
			result, err := s.ChangeConnectionMainSiteGroups(t.Context(), "u", old.ID, MainSiteGroupsRequest{WorkspaceAdminAccountID: "w", ExpectedConnection: &expected, ExpectedMainSiteGroupIDs: []string{"10"}, TargetGroupIDs: []string{"11"}, RemoveOldPricingMapping: true})
			if err != nil || result.Outcome != tc.want || result.WorkspaceAdminAccountID != "w" {
				t.Fatalf("outcome=%s workspace=%s err=%v", result.Outcome, result.WorkspaceAdminAccountID, err)
			}
			if changer.calls != 1 || repo.reassigns != tc.saves || len(repo.saves) != 0 || requests != 1 {
				t.Fatalf("unexpected side effects changer=%d reassign=%d stateSave=%d remote=%d", changer.calls, repo.reassigns, len(repo.saves), requests)
			}
			if tc.want != "completed" && !reflect.DeepEqual(repo.connection.OwnGroupIDs, old.OwnGroupIDs) {
				t.Fatal("failed/unknown operation rewrote local groups")
			}
			if tc.kind == "removed" && !strings.Contains(result.Detail, "解除") {
				t.Fatal("missing removal cause")
			}
			if tc.kind == "changed" && !strings.Contains(result.Detail, "改动") {
				t.Fatal("missing changed cause")
			}
		})
	}
}

func TestMainSiteGroupsCriticalPostgresAtomicity(t *testing.T) {
	for _, mode := range []string{"remove", "retain", "shared", "not-pricing", "changed", "removed", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			pool, repo := openMySitesCleanupPostgres(t)
			ctx := context.Background()
			prepareCleanupState(t, repo, []UpstreamGroupRef{{SiteID: "s", GroupName: "up"}, {SiteID: "unrelated", GroupName: "keep"}})
			old := RealConnection{ID: "c", UserID: "user-1", WorkspaceAdminAccountID: "workspace-1", UpstreamSiteID: "s", UpstreamGroupID: "1", UpstreamGroupName: "up", AdminAccountID: "101", OwnGroupIDs: []string{"10"}, OwnGroupNames: []string{"own-a"}, GroupType: "openai", ProvisioningMode: "managed", Status: "active", AdminPlatform: "sub2api", PricingMappingEnabled: mode != "not-pricing", CreatedAt: time.Now().Format(time.RFC3339Nano)}
			if err := repo.SaveRealConnection(ctx, old); err != nil {
				t.Fatal(err)
			}
			if mode == "shared" {
				other := old
				other.ID = "other"
				if err := repo.SaveRealConnection(ctx, other); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed" {
				if _, err := pool.Exec(ctx, `UPDATE real_connections SET own_group_ids='["99"]' WHERE id='c'`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "removed" {
				if _, err := pool.Exec(ctx, `DELETE FROM real_connections WHERE id='c'`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "rollback" {
				if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_group_change() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected group update'; END $$; CREATE TRIGGER reject_group_change BEFORE UPDATE ON real_connections FOR EACH ROW EXECUTE FUNCTION reject_group_change()`); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := repo.Get(ctx, "user-1", "workspace-1")
			err := repo.ReassignRealConnectionGroupsChecked(ctx, old, []string{"11"}, []string{"own-b"}, mode != "retain")
			state, getErr := repo.Get(ctx, "user-1", "workspace-1")
			if getErr != nil {
				t.Fatal(getErr)
			}
			if mode == "changed" || mode == "removed" || mode == "rollback" {
				if err == nil {
					t.Fatal("unsafe save succeeded")
				}
				if !reflect.DeepEqual(before, state) {
					t.Fatal("failed save changed mappings")
				}
				if mode == "changed" && !strings.Contains(err.Error(), "改动") {
					t.Fatal(err)
				}
				if mode == "removed" && !strings.Contains(err.Error(), "解除") {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			saved, getErr := repo.GetRealConnection(ctx, "c", "user-1", "workspace-1")
			if getErr != nil || saved == nil || !reflect.DeepEqual(saved.OwnGroupIDs, []string{"11"}) || !reflect.DeepEqual(saved.OwnGroupNames, []string{"own-b"}) {
				t.Fatal("groups not saved atomically")
			}
			if mode == "not-pricing" {
				if !reflect.DeepEqual(before, state) {
					t.Fatal("non-pricing changed mappings")
				}
				return
			}
			mapping := map[string][]UpstreamGroupRef{}
			for _, m := range state.Mappings {
				mapping[m.OwnGroup] = m.UpstreamTargets
			}
			if !reflect.DeepEqual(mapping["own-b"], []UpstreamGroupRef{{SiteID: "s", GroupName: "up"}}) {
				t.Fatal("new source missing")
			}
			wantOld := 1
			if mode == "retain" || mode == "shared" {
				wantOld = 2
			}
			if len(mapping["own-a"]) != wantOld {
				t.Fatalf("old source count=%d want%d", len(mapping["own-a"]), wantOld)
			}
		})
	}
}

// A concurrent Hub sync must not silently replace the window's checked snapshot.
type criticalGroupVersionRaceRepo struct {
	*criticalGroupRepo
	reads int
}

func (r *criticalGroupVersionRaceRepo) GetRealConnection(ctx context.Context, id, user, workspace string) (*RealConnection, error) {
	r.reads++
	if r.reads == 2 {
		changed := *r.connection
		changed.OwnGroupIDs = []string{"99"}
		changed.OwnGroupNames = []string{"另一个窗口刚同步的分组"}
		r.connection = &changed
	}
	return r.testConnRepo.GetRealConnection(ctx, id, user, workspace)
}
func TestMainSiteGroupsCriticalConcurrentSnapshotChangeNeverWrites(t *testing.T) {
	for _, mode := range []string{"change", "sync"} {
		t.Run(mode, func(t *testing.T) {
			f := newGroupAPIFixture(t)
			req := f.request()
			repo := &criticalGroupVersionRaceRepo{criticalGroupRepo: f.repo}
			f.service.connRepository = repo
			var err error
			if mode == "change" {
				_, err = f.service.ChangeConnectionMainSiteGroups(t.Context(), "u", "c", req)
			} else {
				_, err = f.service.SyncConnectionHubGroups(t.Context(), "u", "c", req)
			}
			var blocked *scopedRequestError
			if !errors.As(err, &blocked) || blocked.Message != "对接资料已变化" {
				t.Fatalf("changed window snapshot bypassed B9: err=%v", err)
			}
			if f.changer.calls != 0 || repo.reassigns != 0 || len(repo.saves) != 0 {
				t.Fatalf("unconfirmed snapshot wrote data: main=%d local=%d state=%d", f.changer.calls, repo.reassigns, len(repo.saves))
			}
			if !reflect.DeepEqual(repo.connection.OwnGroupIDs, []string{"99"}) || !reflect.DeepEqual(repo.connection.OwnGroupNames, []string{"另一个窗口刚同步的分组"}) {
				t.Fatal("concurrent saved groups were overwritten")
			}
		})
	}
}
