package connection_health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

func TestHiddenHealthStatsKeepMembershipAndDropActiveContribution(t *testing.T) {
	priority := 10
	account := upstream.AdminGroupAccountInfo{ID: "manual", Name: "manual", Status: "active", Models: "gpt-4o", Priority: &priority}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1"}, {ID: "g2"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {account}, "g2": {account}},
	}
	service, repo := hiddenHealthFixture(t, reader)
	repo.groupExclusions = []GroupTargetExclusion{{UserID: hiddenHealthUser, AdminAccountID: hiddenHealthWorkspace, AdminGroupID: "g1", TargetID: hiddenHealthTarget}}
	saveHiddenHealthTarget(t, repo, true)
	for _, read := range []func() ([]AdminGroupHealth, error){
		func() ([]AdminGroupHealth, error) { return service.AdminGroups(t.Context(), hiddenHealthUser) },
		func() ([]AdminGroupHealth, error) { return service.AdminGroupsFresh(t.Context(), hiddenHealthUser) },
	} {
		groups, err := read()
		if err != nil || len(groups) != 2 {
			t.Fatalf("groups missing: %v (%v)", groups, err)
		}
		for _, group := range groups {
			if group.AccountCount != 1 || len(group.Accounts) != 1 {
				t.Fatalf("membership must remain: %+v", group)
			}
			target := group.Accounts[0]
			if group.MonitoredAccountCount != 0 || target.HasEnabledProbePolicy || len(target.UnprobedModels) != 0 || len(target.EffectivePolicyIDs) != 0 {
				t.Errorf("%s: screened member still contributes to health and effective policies", group.ID)
			}
			if target.TargetID != hiddenHealthTarget || target.Priority == nil || *target.Priority != priority || len(target.AssignedPolicyIDs) == 0 || (group.ExcludedAccountCount == 1) != (group.ID == "g1") {
				t.Errorf("%s: membership values changed: %+v", group.ID, target)
			}
		}
	}
	overview, err := service.Overview(t.Context(), hiddenHealthUser)
	if err != nil || overview.TotalConnections != 0 {
		t.Errorf("screened target must not count in overview: %+v (%v)", overview, err)
	}
	repo.listErr = errors.New("fixture read failed")
	if _, err := service.AdminGroups(t.Context(), hiddenHealthUser); !errors.Is(err, requestError(ErrorHiddenAccountsRead)) {
		t.Errorf("unreadable scope must return the specific error, got %v", err)
	}
}

func TestHiddenHealthStatsFailureContracts(t *testing.T) {
	service, repo := hiddenHealthFixture(t, &mutableSub2APISchedulerReader{priority: 10})
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	repo.listErr = errors.New("internal screening fixture detail")
	for _, tc := range []struct {
		path, accept string
		status       int
	}{
		{path: "/admin-groups", status: 400},
		{path: "/admin-groups/refresh", status: 500},
		{path: "/admin-groups/refresh", accept: "text/event-stream", status: 200},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/connection-health"+tc.path, nil)
		request.Header.Set("Accept", tc.accept)
		request = request.WithContext(authctx.WithUserID(request.Context(), hiddenHealthUser))
		response := httptest.NewRecorder()
		mux := http.NewServeMux()
		RegisterRoutes(mux, service)
		mux.ServeHTTP(response, request)
		body := response.Body.String()
		if response.Code != tc.status || !strings.Contains(body, ErrorHiddenAccountsRead) || strings.Contains(body, repo.listErr.Error()) {
			t.Errorf("%s %s: HTTP %d %s", tc.path, tc.accept, response.Code, body)
		}
		if tc.accept != "" && (!strings.Contains(body, `"status":"failed"`) || strings.Contains(body, `"groups":`)) {
			t.Errorf("SSE must end in failure without a replacement list: %s", body)
		}
	}
}

func TestHiddenHealthSchedulerRecordsAvailabilityFromPartialInventory(t *testing.T) {
	for _, test := range []struct {
		name      string
		readErr   error
		wantCount int
	}{
		{name: "empty list", wantCount: 1},
		{name: "list read failure", readErr: errors.New("fixture hidden list unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newHiddenFakeRepository()
			repo.listErr = test.readErr
			policy := monitoringScopeTestPolicy("partial")
			repo.policies = []Policy{policy}
			repo.groupAssignments = []GroupPolicyAssignment{
				{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "unavailable", PolicyID: policy.ID},
				{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "empty", PolicyID: policy.ID},
			}
			retryAfter := time.Now().Add(time.Hour)
			repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{
				UserID: "user1", AdminAccountID: "ws1", PendingSignature: "waiting", NextReconcileAt: &retryAfter,
			}
			reader := fakePlatformGroupReader{
				groups:   []upstream.AdminGroupInfo{{ID: "unavailable"}, {ID: "empty"}},
				errByGrp: map[string]error{"unavailable": errors.New("fixture group unavailable")},
				accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
					"empty": {{ID: "inactive", Status: "inactive", Priority: intPointer(20), Schedulable: boolPointer(true), TempUnschedulableKnown: true, Models: "gpt-4o"}},
				},
			}
			priority := &fakeTargetPriorityActioner{}
			service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo.fakeRepository)
			service.repo = repo
			service.priorityActions = priority
			t.Cleanup(func() {
				if err := service.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
				schedulerStatsByService.Delete(service)
			})

			service.runSchedulerTick(t.Context())
			stats := service.schedulerStatistics()
			stats.mu.Lock()
			defer stats.mu.Unlock()
			if len(stats.unknown) != test.wantCount || stats.unknown["user1|ws1|unavailable"] != test.wantCount ||
				len(stats.zero) != test.wantCount || stats.zero["user1|ws1|empty"] != test.wantCount {
				t.Errorf("availability from first inventory: unknown=%v zero_available=%v, want %d per expected group", stats.unknown, stats.zero, test.wantCount)
			}
			if len(priority.calls) != 0 || repo.priorityWorkspaces["user1|ws1"].PendingSignature != "waiting" {
				t.Error("observing a partial inventory must not bypass Priority backoff")
			}
		})
	}

	t.Run("screening the only available account", func(t *testing.T) {
		reader := fakePlatformGroupReader{
			groups:   []upstream.AdminGroupInfo{{ID: "g1"}, {ID: "g2"}},
			errByGrp: map[string]error{"g2": errors.New("fixture group unavailable")},
			accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
				"g1": {{ID: "manual", Status: "active", Priority: intPointer(20), Schedulable: boolPointer(true), TempUnschedulableKnown: true, Models: "gpt-4o"}},
			},
		}
		service, repo := hiddenHealthFixture(t, reader)
		repo.assignments = nil
		t.Cleanup(func() {
			if err := service.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
			schedulerStatsByService.Delete(service)
		})

		assertStats := func(stage string, wantZero, wantUnknown int) {
			t.Helper()
			stats := service.schedulerStatistics()
			stats.mu.Lock()
			defer stats.mu.Unlock()
			if stats.zero["user1|ws1|g1"] != wantZero || stats.unknown["user1|ws1|g1"] != 0 || stats.unknown["user1|ws1|g2"] != wantUnknown {
				t.Fatalf("%s: zero_available=%v unknown=%v, want g1 zero_available=%d, g1 unknown=0, g2 unknown=%d", stage, stats.zero, stats.unknown, wantZero, wantUnknown)
			}
		}

		service.runSchedulerTick(t.Context())
		assertStats("before screening", 0, 1)

		saveHiddenHealthTarget(t, repo, true)
		service.runSchedulerTick(t.Context())
		assertStats("after screening", 1, 2)
	})
}
