package connection_health

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

func TestHiddenHealthFormalProbeEntrypoints(t *testing.T) {
	for _, tc := range []struct {
		name     string
		screened bool
		readErr  error
		want     string
	}{
		{name: "screened", screened: true, want: hiddenProbeErrorKey},
		{name: "unreadable", readErr: errors.New("fixture read failed"), want: ErrorHiddenAccountsRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &mutableSub2APISchedulerReader{priority: 10}
			service, repo := hiddenHealthFixture(t, reader)
			jobs := service.collectAdminProbeJobsWithGroups(t.Context(), repo.policies, repo.assignments, repo.groupAssignments, nil)
			if len(jobs) != 1 {
				t.Fatalf("before screening expected one queued job, got %d", len(jobs))
			}
			saveHiddenHealthTarget(t, repo, tc.screened)
			repo.listErr = tc.readErr
			for _, path := range []string{"/probe", "/probe-stream"} {
				response := hiddenRequest(service, http.MethodPost, "/api/connection-health/targets/"+hiddenHealthTarget+path, hiddenHealthUser, `{"models":["gpt-4o"]}`)
				if !strings.Contains(response.Body.String(), tc.want) {
					t.Errorf("%s: expected %q, got HTTP %d %s", path, tc.want, response.Code, response.Body.String())
				}
			}
			var wg sync.WaitGroup
			wg.Add(1)
			service.runAdminProbeJob(t.Context(), jobs[0], func() {}, &wg)
			wg.Wait()
			_, writes, resolutions := reader.snapshot()
			if writes != 0 || resolutions != 0 || len(repo.events) != 0 || len(repo.states) != 0 {
				t.Errorf("blocked batches resolved %d credentials, wrote %d priorities, created %d events and %d states", resolutions, writes, len(repo.events), len(repo.states))
			}
		})
	}
}

func TestHiddenHealthProbeBatchAdmittedBeforeSavePostgres(t *testing.T) {
	pool := stageAPostgresPool(t)
	ctx := t.Context()
	repo := NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS admin_accounts (id text PRIMARY KEY, user_id text NOT NULL);
		INSERT INTO admin_accounts (id, user_id) VALUES ('ws1', 'user1')`); err != nil {
		t.Fatal(err)
	}
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"g1": {{ID: "manual", Status: "active", Models: "gpt-first,gpt-second", Priority: intPtr(10), Schedulable: boolPointer(true)}},
		},
		credByAccount: map[string]upstream.ProbeCredential{
			"manual": {BaseURL: "https://fixture.invalid", Key: "fixture-only"},
		},
	}
	service, _ := hiddenHealthFixture(t, reader)
	service.repo = repo
	service.backgroundTasksDisabled = true
	policy, err := service.SavePolicy(ctx, hiddenHealthUser, PolicyInput{
		Name: "two models", Enabled: true, StrategyMode: StrategyModeHealthProbe,
		ModelTargets: []ModelTargetInput{
			{ModelName: "gpt-first", Enabled: true, ProviderFamily: ProviderOpenAI},
			{ModelName: "gpt-second", Enabled: true, ProviderFamily: ProviderOpenAI},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplacePolicyAssignments(ctx, hiddenHealthUser, hiddenHealthWorkspace, hiddenHealthTarget, []string{policy.ID}); err != nil {
		t.Fatal(err)
	}
	var requests int
	service.probeRunner.client.Transport = c1RoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			if _, err := repo.SaveAccountHidden(ctx, hiddenHealthUser, hiddenHealthWorkspace, hiddenHealthTarget, true, "manual"); err != nil {
				return nil, err
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`))}, nil
	})
	results, err := service.ProbeTarget(ctx, hiddenHealthUser, hiddenHealthTarget, []string{"gpt-first", "gpt-second"})
	if err != nil || len(results) != 2 || requests != 2 {
		t.Fatalf("batch admitted before the hide must finish both requests: requests=%d results=%d err=%v", requests, len(results), err)
	}
	for _, result := range results {
		if result.ProbeDisposition != "stale" {
			t.Errorf("%s: late result must be stale, got %q", result.ModelName, result.ProbeDisposition)
		}
	}
	states, err := repo.ListStatesByWorkspace(ctx, hiddenHealthUser, hiddenHealthWorkspace)
	if err != nil || len(states) != 0 {
		t.Fatalf("late results must not create health states: %d (%v)", len(states), err)
	}
	events, err := repo.ListRecentEventsByWorkspace(ctx, hiddenHealthUser, hiddenHealthWorkspace, 10)
	if err != nil || len(events) != 2 || events[0].ProbeDisposition != "stale" || events[1].ProbeDisposition != "stale" {
		t.Fatalf("both late attempts must be traceable as stale: %+v (%v)", events, err)
	}
	if _, err := service.ProbeTarget(ctx, hiddenHealthUser, hiddenHealthTarget, nil); !errors.Is(err, requestError(hiddenProbeErrorKey)) || requests != 2 {
		t.Fatalf("new batch must be blocked without another request: requests=%d err=%v", requests, err)
	}
}
