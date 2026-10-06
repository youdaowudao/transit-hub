package connection_health

import (
	"context"
	"fmt"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func taskARuntimeGenerationService(repo *taskAReviewGenerationRepository, priority bool) (*Service, *fakePlatformActioner, *fakeTargetPriorityActioner) {
	platform, priorities := &fakePlatformActioner{}, &fakeTargetPriorityActioner{}
	id := "sub2api:ws1:acc-1"
	account := upstream.AdminGroupAccountInfo{ID: "acc-1", Status: "inactive", Models: "gpt-4o"}
	if priority {
		account.Status, account.Priority = "active", intPointer(100000)
		repo.priorityStates["user1|ws1|"+id] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: id, OriginalPriority: 7, LastAppliedPriority: 100000}
	} else {
		repo.targetActionStates["user1|ws1|"+id] = TargetActionState{UserID: "user1", AdminAccountID: "ws1", TargetID: id, OriginalStatus: "active", LastAppliedStatus: "inactive"}
	}
	reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {account}}}
	service := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo.fakeRepository)
	service.repo, service.priorityActions = repo, priorities
	service.dispatcher = newRemoteActionDispatcher(nil, nil, platform)
	return service, platform, priorities
}

func TestTaskARuntimeReviewPriorityRestoreKeepsInitialGeneration(t *testing.T) {
	for _, generation := range []int64{1, 2, 3} {
		t.Run(fmt.Sprint(generation), func(t *testing.T) {
			repo := &taskAReviewGenerationRepository{fakeRepository: newFakeRepository(), generation: 1, nextGeneration: generation}
			service, _, priorities := taskARuntimeGenerationService(repo, true)
			service.runSchedulerTick(context.Background())
			wantCalls := 0
			if generation == 1 {
				wantCalls = 1
			}
			if len(priorities.calls) != wantCalls || len(repo.guards) != 1 || repo.guards[0] != 1 {
				t.Fatalf("priority restore must use pre-policy generation, calls=%v guards=%v current=%d", priorities.calls, repo.guards, generation)
			}
		})
	}
}

func TestTaskARuntimeReviewEmptyGroupRestoreKeepsInitialGeneration(t *testing.T) {
	for _, generation := range []int64{1, 2, 3} {
		t.Run(fmt.Sprint(generation), func(t *testing.T) {
			repo := &taskAReviewGenerationRepository{fakeRepository: newFakeRepository(), generation: 1, nextGeneration: generation}
			service, platform, _ := taskARuntimeGenerationService(repo, false)
			ctx, err := service.captureAllWorkspaceRules(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_, _ = repo.ListEnabledPolicies(ctx)
			stored := repo.targetActionStates["user1|ws1|sub2api:ws1:acc-1"]
			service.restoreEmptySub2APIGroups(ctx, []TargetActionState{stored}, make(adminInventoryCache))
			wantCalls := 0
			if generation == 1 {
				wantCalls = 1
			}
			if len(platform.sub2APICalls) != wantCalls || len(repo.guards) != 1 || repo.guards[0] != 1 {
				t.Fatalf("empty-group restore must use pre-policy generation, calls=%v guards=%v current=%d", platform.sub2APICalls, repo.guards, generation)
			}
		})
	}
}

type taskARuntimePolicyReadGenerationRepository struct {
	*taskAReviewGenerationRepository
}

func (r *taskARuntimePolicyReadGenerationRepository) ListPolicies(ctx context.Context, user, workspace string) ([]Policy, error) {
	policies, err := r.fakeRepository.ListPolicies(ctx, user, workspace)
	r.generation = r.nextGeneration
	return policies, err
}

func TestTaskARuntimeReviewCurrentWorkspacePriorityCapturesBeforeDisabledPolicyRead(t *testing.T) {
	for _, generation := range []int64{1, 2, 3} {
		t.Run(fmt.Sprint(generation), func(t *testing.T) {
			base := &taskAReviewGenerationRepository{fakeRepository: newFakeRepository(), generation: 1, nextGeneration: generation}
			base.policies = []Policy{{ID: "disabled", UserID: "user1", AdminAccountID: "ws1", RuleVersion: RuleVersionV2, ConfigGeneration: 1}}
			service, _, priorities := taskARuntimeGenerationService(base, true)
			service.repo = &taskARuntimePolicyReadGenerationRepository{base}
			if err := service.syncCurrentWorkspacePrioritiesWithResult(context.Background(), "user1", "ws1"); err != nil {
				t.Fatal(err)
			}
			wantCalls := 0
			if generation == 1 {
				wantCalls = 1
			}
			if len(priorities.calls) != wantCalls || len(base.guards) != 1 || base.guards[0] != 1 {
				t.Fatalf("disabled policy read lost initial generation: calls=%v guards=%v", priorities.calls, base.guards)
			}
		})
	}
}

type taskARuntimeMissingWorkspaceSettingsRepository struct {
	*taskAReviewGenerationRepository
}

func (r *taskARuntimeMissingWorkspaceSettingsRepository) ListWorkspaceHealthSettings(context.Context) ([]WorkspaceHealthSettings, error) {
	return nil, nil
}

func TestTaskARuntimeReviewAbsentInitialSettingsKeepDefaultGeneration(t *testing.T) {
	repo := &taskAReviewGenerationRepository{fakeRepository: newFakeRepository(), generation: 1}
	service, platform, _ := taskARuntimeGenerationService(repo, false)
	service.repo = &taskARuntimeMissingWorkspaceSettingsRepository{repo}
	ctx, err := service.captureAllWorkspaceRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stored := repo.targetActionStates["user1|ws1|sub2api:ws1:acc-1"]
	service.restoreEmptySub2APIGroups(ctx, []TargetActionState{stored}, make(adminInventoryCache))
	if len(platform.sub2APICalls) != 0 || len(repo.guards) != 1 || repo.guards[0] != 0 {
		t.Fatalf("settings created after snapshot replaced initial default generation: calls=%v guards=%v", platform.sub2APICalls, repo.guards)
	}
}

func TestTaskARuntimeReviewModelProjectionIgnoresOnlyForeignFailureOrigin(t *testing.T) {
	now := time.Now().UTC()
	old, last := now.Add(-25*time.Hour), now.Add(-time.Minute)
	for _, version := range []string{RuleVersionV2, RuleVersionLegacy} {
		for _, sameProtocol := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/same=%v", version, sameProtocol), func(t *testing.T) {
				p := Policy{ID: "p", RuleVersion: version, ProbeIntervalSeconds: 60, Enabled: true}
				target := AdminProbeTarget{TargetID: "sub2api:ws1:a", Platform: "sub2api", TestConfiguration: defaultTestConfiguration(), Schedulable: boolPointer(true)}
				target.TestConfiguration.Protocol = TestProtocolResponses
				spec := probeModelSpec{modelName: "m", policy: p, policies: []Policy{p}}
				counterProtocol := TestProtocolChatCompletions
				if sameProtocol {
					counterProtocol = TestProtocolResponses
				}
				state := ConnectionHealthState{State: StateHealthy, RuleVersion: version, CurrentWeight: 100, FailingSince: &old, LastProbeAt: &last, CounterProtocol: protocolPointer(counterProtocol), LastProbeDecisionKey: probeDecisionKey(target, spec)}
				models, _ := modelHealthForSpecs(map[string]ConnectionHealthState{"m": state}, []probeModelSpec{spec}, target, now, nil, true)
				want := now
				if sameProtocol {
					want = last.Add(time.Hour)
				}
				if len(models) != 1 || models[0].NextProbeAt == nil || !models[0].NextProbeAt.Equal(want) || state.FailingSince == nil || !state.FailingSince.Equal(old) {
					t.Fatalf("protocol scheduling projection changed durable origin or borrowed foreign one: models=%+v origin=%v want=%s", models, state.FailingSince, want)
				}
			})
		}
	}
}

func TestTaskARuntimeReviewRecheckPendingWinsEstimatedBudget(t *testing.T) {
	for _, budget := range []int{1, 2} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			repo := newFakeRepository()
			p := Policy{ID: "p", UserID: "user1", AdminAccountID: "ws1", RuleVersion: RuleVersionV2, Enabled: true, DailyProbeBudget: budget, ProbeIntervalSeconds: 60}
			target := AdminProbeTarget{TargetID: "sub2api:ws1:a", Platform: "sub2api", TestConfiguration: defaultTestConfiguration(), Schedulable: boolPointer(true)}
			repo.states[target.TargetID] = map[string]ConnectionHealthState{"pending": {State: StateSuspect, RuleVersion: RuleVersionV2, CurrentWeight: 100, RecheckPending: true}}
			specs := []probeModelSpec{{modelName: "normal", policy: p, policies: []Policy{p}}, {modelName: "pending", policy: p, policies: []Policy{p}}}
			service := &Service{repo: repo}
			due := service.recheckAdminProbeSpecs(context.Background(), "user1", "ws1", target, specs, time.Now())
			if len(due) != budget || due[0].modelName != "pending" || (budget == 2 && due[1].modelName != "normal") || len(repo.budgetClaims) != 0 {
				t.Fatalf("execution recheck must sort fresh pending state before estimating without claiming: due=%+v claims=%v", due, repo.budgetClaims)
			}
		})
	}
}

type taskARuntimeExhaustedBudgetRepository struct{ *fakeRepository }

func (r *taskARuntimeExhaustedBudgetRepository) CountProbesToday(_ context.Context, _, _, policy string, _ time.Time) (int, error) {
	if policy == "exhausted" {
		return 1, nil
	}
	return 0, nil
}

func TestTaskARuntimeReviewBudgetFilteringPrecedesModelCapAndKeepsFullModels(t *testing.T) {
	repo := &taskARuntimeExhaustedBudgetRepository{newFakeRepository()}
	service := &Service{repo: repo}
	usable := Policy{ID: "usable", Enabled: true, RuleVersion: RuleVersionV2, DailyProbeBudget: 100}
	exhausted := usable
	exhausted.ID, exhausted.DailyProbeBudget = "exhausted", 1
	specs := []probeModelSpec{}
	// Five ordinary tasks precede all pending tasks in the original inventory.
	for i := 0; i < 5; i++ {
		specs = append(specs, probeModelSpec{modelName: fmt.Sprint("ordinary-", i), policy: usable, policies: []Policy{usable}})
	}
	for i := 0; i < 105; i++ {
		policy := usable
		if i < 5 {
			policy = exhausted
		}
		specs = append(specs, probeModelSpec{modelName: fmt.Sprint("pending-", i), policy: policy, policies: []Policy{policy}, recheckPending: true})
	}
	job := adminProbeJob{userID: "user1", adminAccountID: "ws1", target: AdminProbeTarget{TargetID: "sub2api:ws1:a", Platform: "sub2api"}, models: specs, dueSpecs: specs}
	jobs := service.selectAdminProbeJobsWithBudget(context.Background(), []adminProbeJob{job}, time.Now(), 100)
	if len(jobs) != 1 || len(jobs[0].models) != 110 || len(jobs[0].dueSpecs) != 100 || jobs[0].dueSpecs[0].modelName != "pending-5" || jobs[0].dueSpecs[99].modelName != "pending-104" || len(repo.budgetClaims) != 0 {
		t.Fatalf("pending order must survive exhausted budgets before model cap, retaining full configuration: jobs=%+v claims=%v", jobs, repo.budgetClaims)
	}
}
