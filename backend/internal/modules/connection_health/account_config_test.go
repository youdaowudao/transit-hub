package connection_health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

type accountIntelligenceWeightHandlerFixture struct {
	service            *Service
	mux                *http.ServeMux
	priorityActions    *fakeTargetPriorityActioner
	schedulableActions *fakeTargetSchedulableActioner
}

func newAccountIntelligenceWeightHandlerFixture(
	repository healthRepository,
	adminAccountID string,
	accountsByGroup map[string][]upstream.AdminGroupAccountInfo,
) accountIntelligenceWeightHandlerFixture {
	return newAccountIntelligenceWeightHandlerFixtureForPlatform(
		repository, adminAccountID, upstream.PlatformSub2API, accountsByGroup,
	)
}

func newAccountIntelligenceWeightHandlerFixtureForPlatform(
	repository healthRepository,
	adminAccountID string,
	platform upstream.Platform,
	accountsByGroup map[string][]upstream.AdminGroupAccountInfo,
) accountIntelligenceWeightHandlerFixture {
	groups := make([]upstream.AdminGroupInfo, 0, len(accountsByGroup))
	for groupID := range accountsByGroup {
		groups = append(groups, upstream.AdminGroupInfo{
			ID: groupID, Name: groupID, Platform: string(platform),
		})
	}
	priorityActions := &fakeTargetPriorityActioner{}
	schedulableActions := &fakeTargetSchedulableActioner{}
	service := &Service{
		repo: repository,
		mySites: fakeMySitesReader{session: upstream.Session{
			Platform: platform,
		}},
		accounts: fakeAdminAccountResolver{id: adminAccountID},
		platformGroups: fakePlatformGroupReader{
			groups: groups, accountsByGrp: accountsByGroup,
		},
		priorityActions:    priorityActions,
		schedulableActions: schedulableActions,
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	return accountIntelligenceWeightHandlerFixture{
		service: service, mux: mux,
		priorityActions: priorityActions, schedulableActions: schedulableActions,
	}
}

func (f accountIntelligenceWeightHandlerFixture) put(t *testing.T, userID string, targetID string, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/connection-health/targets/"+targetID+"/intelligence-weight",
		strings.NewReader(body),
	)
	request = request.WithContext(authctx.WithUserID(request.Context(), userID))
	response := httptest.NewRecorder()
	f.mux.ServeHTTP(response, request)
	return response
}

func TestHandlerAccountIntelligenceWeightDistinguishesNullFromInvalidInputAndHasZeroProductionSideEffects(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repository := NewRepository(pool)
	ctx := context.Background()
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	fixture := newAccountIntelligenceWeightHandlerFixture(
		repository,
		"ws1",
		map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "1515", Name: "account-1515", Status: "active"}},
		},
	)
	targetID := "sub2api:ws1:1515"

	for _, requestCase := range []struct {
		name string
		body string
	}{
		{name: "missing", body: `{}`},
		{name: "empty string", body: `{"intelligenceWeight":""}`},
		{name: "string integer", body: `{"intelligenceWeight":"80"}`},
		{name: "fraction", body: `{"intelligenceWeight":1.5}`},
		{name: "negative", body: `{"intelligenceWeight":-1}`},
		{name: "over one hundred", body: `{"intelligenceWeight":101}`},
	} {
		t.Run("rejects "+requestCase.name, func(t *testing.T) {
			response := fixture.put(t, "user-a", targetID, requestCase.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400 body=%s", response.Code, response.Body.String())
			}
		})
	}

	var rowsBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_account_configs`).Scan(&rowsBefore); err != nil {
		t.Fatalf("count account configs after invalid input: %v", err)
	}
	if rowsBefore != 0 {
		t.Fatalf("invalid requests wrote %d account configs, want 0", rowsBefore)
	}

	for _, requestCase := range []struct {
		name string
		body string
		want *int
	}{
		{name: "explicit null", body: `{"intelligenceWeight":null}`, want: nil},
		{name: "zero", body: `{"intelligenceWeight":0}`, want: intPointer(0)},
		{name: "one", body: `{"intelligenceWeight":1}`, want: intPointer(1)},
		{name: "one hundred", body: `{"intelligenceWeight":100}`, want: intPointer(100)},
		{name: "clear to null", body: `{"intelligenceWeight":null}`, want: nil},
	} {
		t.Run("accepts "+requestCase.name, func(t *testing.T) {
			response := fixture.put(t, "user-a", targetID, requestCase.body)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d want 200 body=%s", response.Code, response.Body.String())
			}
			var result struct {
				TargetID           string `json:"targetId"`
				IntelligenceWeight *int   `json:"intelligenceWeight"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if result.TargetID != targetID || !equalOptionalInt(result.IntelligenceWeight, requestCase.want) {
				t.Fatalf("authoritative result=%+v want target=%s weight=%v", result, targetID, requestCase.want)
			}
			var stored *int
			if err := pool.QueryRow(ctx, `
				SELECT intelligence_weight
				FROM connection_health_account_configs
				WHERE user_id = 'user-a' AND admin_account_id = 'ws1' AND target_id = $1
			`, targetID).Scan(&stored); err != nil {
				t.Fatalf("read stored weight: %v", err)
			}
			if !equalOptionalInt(stored, requestCase.want) {
				t.Fatalf("stored weight=%v want %v", stored, requestCase.want)
			}
		})
	}

	var rowsAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_account_configs`).Scan(&rowsAfter); err != nil {
		t.Fatalf("count account configs after upsert: %v", err)
	}
	if rowsAfter != 1 {
		t.Fatalf("repeated saves must upsert one row, got %d", rowsAfter)
	}

	for _, table := range []string{
		"connection_health_states",
		"connection_health_events",
		"connection_health_question_answer_records",
		"connection_health_priority_sync_states",
	} {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("intelligence weight save changed %s rows=%d", table, count)
		}
	}
	if len(fixture.priorityActions.calls) != 0 || fixture.schedulableActions.calls != 0 {
		t.Fatalf("local save invoked production actions: priority=%d schedulable=%d", len(fixture.priorityActions.calls), fixture.schedulableActions.calls)
	}
}

func TestHandlerAccountIntelligenceWeightRejectsForeignWorkspacePlatformAndMissingTarget(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repository := NewRepository(pool)
	ctx := context.Background()
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	fixture := newAccountIntelligenceWeightHandlerFixture(
		repository,
		"ws1",
		map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "1515", Name: "account-1515", Status: "active"}},
		},
	)
	for _, targetID := range []string{
		"sub2api:other:1515",
		"newapi:ws1:1515",
		"sub2api:ws1:missing",
	} {
		response := fixture.put(t, "user-a", targetID, `{"intelligenceWeight":80}`)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("target %s status=%d want 400 body=%s", targetID, response.Code, response.Body.String())
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_account_configs`).Scan(&count); err != nil {
		t.Fatalf("count rejected target configs: %v", err)
	}
	if count != 0 {
		t.Fatalf("rejected targets wrote %d configs", count)
	}

	newAPIFixture := newAccountIntelligenceWeightHandlerFixtureForPlatform(
		repository,
		"ws1",
		upstream.PlatformNewAPI,
		map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "1515", Name: "legacy-newapi-account", Status: "active"}},
		},
	)
	response := newAPIFixture.put(t, "user-a", "newapi:ws1:1515", `{"intelligenceWeight":80}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("NewAPI workspace status=%d want 400 body=%s", response.Code, response.Body.String())
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM connection_health_account_configs`).Scan(&count); err != nil {
		t.Fatalf("count configs after NewAPI workspace rejection: %v", err)
	}
	if count != 0 {
		t.Fatalf("NewAPI workspace wrote %d configs, want 0", count)
	}
}

func TestAdminGroupsAccountIntelligenceWeightProjectsSameZeroAndSurfacesReadError(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repository := NewRepository(pool)
	ctx := context.Background()
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	accounts := map[string][]upstream.AdminGroupAccountInfo{
		"g1": {
			{ID: "1515", Name: "shared", Status: "active"},
			{ID: "1616", Name: "unscored", Status: "active"},
		},
		"g2": {{ID: "1515", Name: "shared", Status: "active"}},
	}
	fixture := newAccountIntelligenceWeightHandlerFixture(repository, "ws1", accounts)
	response := fixture.put(t, "user-a", "sub2api:ws1:1515", `{"intelligenceWeight":0}`)
	if response.Code != http.StatusOK {
		t.Fatalf("seed zero status=%d body=%s", response.Code, response.Body.String())
	}

	groups, err := fixture.service.AdminGroups(ctx, "user-a")
	if err != nil {
		t.Fatalf("AdminGroups: %v", err)
	}
	encoded, err := json.Marshal(groups)
	if err != nil {
		t.Fatalf("encode admin groups: %v", err)
	}
	var payload []struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode admin groups payload: %v", err)
	}
	sharedSeen := 0
	unscoredSeen := 0
	for _, group := range payload {
		for _, account := range group.Accounts {
			targetID, _ := account["targetId"].(string)
			weight, present := account["intelligenceWeight"]
			if !present {
				t.Fatalf("account %s omitted required intelligenceWeight: %#v", targetID, account)
			}
			switch targetID {
			case "sub2api:ws1:1515":
				sharedSeen++
				if weight != float64(0) {
					t.Fatalf("shared target projected weight=%#v want numeric zero", weight)
				}
			case "sub2api:ws1:1616":
				unscoredSeen++
				if weight != nil {
					t.Fatalf("missing config projected weight=%#v want null", weight)
				}
			}
		}
	}
	if sharedSeen != 2 || unscoredSeen != 1 {
		t.Fatalf("projection counts shared=%d unscored=%d", sharedSeen, unscoredSeen)
	}

	fakeRepository := newFakeRepository()
	fakeFixture := newAccountIntelligenceWeightHandlerFixture(fakeRepository, "ws1", accounts)
	if _, err := fakeFixture.service.AdminGroups(ctx, "user-a"); err != nil {
		t.Fatalf("AdminGroups with call-count repository: %v", err)
	}
	if fakeRepository.listAccountConfigsCalls != 1 {
		t.Fatalf("account configs read calls=%d want exactly 1 per workspace aggregation", fakeRepository.listAccountConfigsCalls)
	}

	if _, err := pool.Exec(ctx, `DROP TABLE connection_health_account_configs`); err != nil {
		t.Fatalf("drop isolated config table to simulate read failure: %v", err)
	}
	if groups, err := fixture.service.AdminGroups(ctx, "user-a"); err == nil {
		t.Fatalf("account config read failure was masked as unscored groups: %+v", groups)
	}
}

func equalOptionalInt(got *int, want *int) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}
