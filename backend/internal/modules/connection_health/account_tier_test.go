package connection_health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"transithub/backend/internal/modules/admin_accounts"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

func (f fakeAdminAccountResolver) Current(_ context.Context, userID string) (*admin_accounts.Account, error) {
	return &admin_accounts.Account{ID: f.id, UserID: userID, Platform: string(upstream.PlatformSub2API)}, nil
}

type tierNonSub2APIAccountResolver struct{ fakeAdminAccountResolver }

func (f tierNonSub2APIAccountResolver) Current(_ context.Context, userID string) (*admin_accounts.Account, error) {
	return &admin_accounts.Account{ID: f.id, UserID: userID, Platform: string(upstream.PlatformNewAPI)}, nil
}

type tierLocalOnlySitesReader struct{ fakeMySitesReader }

func (tierLocalOnlySitesReader) RequireSession(context.Context, string, string) (upstream.Session, error) {
	panic("tier configuration must not acquire or refresh an upstream session")
}

func (f *fakeRepository) GetAccountTier(_ context.Context, userID, workspaceID, targetID string) (int, error) {
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	if tier := f.accountTiers[userID+"|"+workspaceID+"|"+targetID]; tier != 0 {
		return tier, nil
	}
	return 2, nil
}

func (f *fakeRepository) SaveAccountTier(_ context.Context, userID, workspaceID, targetID string, tier int) error {
	if f.accountTiers == nil {
		f.accountTiers = make(map[string]int)
	}
	f.accountTiers[userID+"|"+workspaceID+"|"+targetID] = tier
	return nil
}

func (f *fakeRepository) ListAccountTiers(_ context.Context, userID, workspaceID string) (map[string]int, error) {
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	result := make(map[string]int)
	prefix := userID + "|" + workspaceID + "|"
	for key, tier := range f.accountTiers {
		if strings.HasPrefix(key, prefix) {
			result[strings.TrimPrefix(key, prefix)] = tier
		}
	}
	return result, nil
}

func tierRequest(service *Service, method, userID, targetID, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	request := httptest.NewRequest(method, "/api/connection-health/targets/"+targetID+"/tier", strings.NewReader(body))
	if userID != "" {
		request = request.WithContext(authctx.WithUserID(request.Context(), userID))
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func requireTierResponse(t *testing.T, response *httptest.ResponseRecorder, targetID string, want int) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("tier request status=%d body=%s", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, map[string]any{"targetId": targetID, "accountTier": float64(want)}) {
		t.Fatalf("tier response=%v want target %s tier %d only", result, targetID, want)
	}
}

// Same account is both a protected manual target and visible in two groups.
// Tier changes enqueue sorting while preserving existing protected action state.
func accountTierFixture() (*Service, *fakeRepository, *fakeTargetPriorityActioner) {
	repo := newFakeRepository()
	targetID := "sub2api:ws1:shared"
	account := upstream.AdminGroupAccountInfo{ID: "shared", Name: "Tier fixture", Status: "disabled", Schedulable: boolPointer(false), Priority: intPointer(7)}
	reader := fakePlatformGroupReader{
		groups:        []upstream.AdminGroupInfo{{ID: "g1", Name: "One"}, {ID: "g2", Name: "Two"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {account}, "g2": {account}},
	}
	svc := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
	actions := &fakeTargetPriorityActioner{}
	svc.priorityActions = actions
	repo.priorityStates["user1|ws1|"+targetID] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, Conflict: true, OriginalPriority: 7, LastAppliedPriority: 4, LastConflictPriority: intPointer(7)}
	repo.priorityStates["user1|ws1|sub2api:ws1:multiplier-only"] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:multiplier-only", OriginalPriority: 50, LastAppliedPriority: 4, EffectiveMultiplier: 0.5, PendingPriority: intPointer(3)}
	repo.priorityWorkspaces["user1|ws1"] = PriorityWorkspaceSyncState{UserID: "user1", AdminAccountID: "ws1", AppliedSignature: "before", PendingSignature: "protected-pending", LastDecision: "blocked", LastError: "protected-safety-state"}
	repo.states[targetID] = map[string]ConnectionHealthState{"model": {UserID: "user1", AdminAccountID: "ws1", ConnectionID: targetID, ModelName: "model", State: StateSuspended}}
	repo.targetActionStates["user1|ws1|"+targetID] = TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, OriginalStatus: "active", LastAppliedStatus: "disabled", Conflict: true}
	return svc, repo, actions
}

func TestAccountTierDefaultAndBothDirectionsPreserveProtectedState(t *testing.T) {
	svc, repo, actions := accountTierFixture()
	targetID := "sub2api:ws1:shared"
	snapshot := func() string {
		data, err := json.Marshal([]any{repo.states, repo.priorityStates, repo.targetActionStates, repo.events, repo.assignments, repo.groupAssignments})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	before := snapshot()
	requireTierResponse(t, tierRequest(svc, "GET", "user1", targetID, ""), targetID, 2)
	if len(repo.accountTiers) != 0 {
		t.Fatal("default read must not create a first-layer record")
	}
	for _, tier := range []int{1, 2, 1} {
		requireTierResponse(t, tierRequest(svc, "PUT", "user1", targetID, fmt.Sprintf(`{"accountTier":%d}`, tier)), targetID, tier)
		waitForPriorityAsyncIdle(t)
		if repo.fakeWorkspaceHealthSettings("user1", "ws1").ConfigGeneration < 1 || repo.priorityWorkspaces["user1|ws1"].LastActionSource != "account_tier_save" {
			t.Fatal("tier change did not atomically invalidate decisions and request sorting")
		}
		requireTierResponse(t, tierRequest(svc, "GET", "user1", targetID, ""), targetID, tier)
		if got := snapshot(); got != before {
			t.Fatalf("tier %d changed protected business state\nbefore=%s\nafter=%s", tier, before, got)
		}
		if len(actions.calls) != 0 {
			t.Fatalf("tier save called Priority writer: %+v", actions.calls)
		}
	}
}

func TestAccountTierAdminGroupsUsesOneValueAndPreservesAllOtherFields(t *testing.T) {
	svc, _, actions := accountTierFixture()
	read := func(want int) []map[string]any {
		t.Helper()
		groups, err := svc.AdminGroups(context.Background(), "user1")
		if err != nil {
			t.Fatal(err)
		}
		if len(groups) != 2 {
			t.Fatalf("groups=%d want 2", len(groups))
		}
		result := []map[string]any{}
		for _, group := range groups {
			if len(group.Accounts) != 1 {
				t.Fatalf("group %s lost account", group.ID)
			}
			data, _ := json.Marshal(group.Accounts[0])
			var row map[string]any
			if err := json.Unmarshal(data, &row); err != nil {
				t.Fatal(err)
			}
			if row["accountTier"] != float64(want) {
				t.Fatalf("group %s accountTier=%v want %d", group.ID, row["accountTier"], want)
			}
			if _, exists := row["intelligenceWeight"]; exists {
				t.Fatal("retired weight leaked")
			}
			delete(row, "accountTier")
			result = append(result, row)
		}
		return result
	}
	before := read(2)
	for _, tier := range []int{1, 2} {
		requireTierResponse(t, tierRequest(svc, "PUT", "user1", "sub2api:ws1:shared", fmt.Sprintf(`{"accountTier":%d}`, tier)), "sub2api:ws1:shared", tier)
		if after := read(tier); !reflect.DeepEqual(before, after) {
			t.Fatalf("tier save changed account health, status, schedulable, Priority or display fields\nbefore=%v\nafter=%v", before, after)
		}
	}
	if len(actions.calls) != 0 {
		t.Fatal("aggregation or tier save wrote Priority")
	}
}

func TestAccountTierRejectsInvalidAndForeignRequestsWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name, user, target, body string
		status                   int
	}{
		{"zero", "user1", "sub2api:ws1:shared", `{"accountTier":0}`, 400},
		{"third", "user1", "sub2api:ws1:shared", `{"accountTier":3}`, 400},
		{"negative", "user1", "sub2api:ws1:shared", `{"accountTier":-1}`, 400},
		{"fraction", "user1", "sub2api:ws1:shared", `{"accountTier":1.5}`, 400},
		{"null", "user1", "sub2api:ws1:shared", `{"accountTier":null}`, 400},
		{"missing", "user1", "sub2api:ws1:shared", `{}`, 400},
		{"string", "user1", "sub2api:ws1:shared", `{"accountTier":"1"}`, 400},
		{"malformed", "user1", "sub2api:ws1:shared", `not-json`, 400},
		{"foreign", "user1", "sub2api:ws2:shared", `{"accountTier":1}`, 400},
		{"newapi", "user1", "newapi:ws1:shared", `{"accountTier":1}`, 400},
		{"anonymous", "", "sub2api:ws1:shared", `{"accountTier":1}`, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, actions := accountTierFixture()
			response := tierRequest(svc, "PUT", tc.user, tc.target, tc.body)
			if response.Code != tc.status {
				t.Fatalf("status=%d want %d body=%s", response.Code, tc.status, response.Body.String())
			}
			if len(repo.accountTiers) != 0 || len(actions.calls) != 0 {
				t.Fatal("rejected request wrote data or Priority")
			}
		})
	}
	for _, tc := range []struct {
		user, target string
		status       int
	}{{"user1", "sub2api:ws2:shared", 400}, {"", "sub2api:ws1:shared", 401}} {
		svc, _, _ := accountTierFixture()
		if response := tierRequest(svc, "GET", tc.user, tc.target, ""); response.Code != tc.status {
			t.Fatalf("GET status=%d want %d", response.Code, tc.status)
		}
	}
}

func TestAccountTierUserAndWorkspaceReadWriteIsolation(t *testing.T) {
	svc, repo, _ := accountTierFixture()
	for _, tc := range []struct {
		user, ws, target string
		tier             int
	}{
		{"user1", "ws1", "sub2api:ws1:shared", 1},
		{"user1", "ws2", "sub2api:ws2:shared", 2},
		{"user2", "ws1", "sub2api:ws1:shared", 2},
	} {
		svc.accounts = fakeAdminAccountResolver{id: tc.ws}
		requireTierResponse(t, tierRequest(svc, "GET", tc.user, tc.target, ""), tc.target, 2)
		requireTierResponse(t, tierRequest(svc, "PUT", tc.user, tc.target, fmt.Sprintf(`{"accountTier":%d}`, tc.tier)), tc.target, tc.tier)
		waitForPriorityAsyncIdle(t)
	}
	svc.accounts = fakeAdminAccountResolver{id: "ws1"}
	requireTierResponse(t, tierRequest(svc, "GET", "user1", "sub2api:ws1:shared", ""), "sub2api:ws1:shared", 1)
	if len(repo.accountTiers) != 3 {
		t.Fatalf("rows=%d want 3 isolated keys", len(repo.accountTiers))
	}
}

func TestAccountTierRejectsForgedSub2APITargetInNonSub2APIWorkspace(t *testing.T) {
	svc, repo, actions := accountTierFixture()
	svc.accounts = tierNonSub2APIAccountResolver{fakeAdminAccountResolver{id: "ws1"}}
	svc.mySites = fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformNewAPI}}
	for _, method := range []string{"GET", "PUT"} {
		response := tierRequest(svc, method, "user1", "sub2api:ws1:shared", `{"accountTier":1}`)
		if response.Code != http.StatusBadRequest {
			t.Errorf("forged Sub2API target in non-Sub2API workspace: %s status=%d want 400", method, response.Code)
		}
	}
	if len(repo.accountTiers) != 0 || len(actions.calls) != 0 {
		t.Fatal("platform-rejected request wrote tier data or Priority")
	}
}
