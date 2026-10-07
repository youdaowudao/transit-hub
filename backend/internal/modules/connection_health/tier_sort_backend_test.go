package connection_health

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func TestTierSortBlockedMultiplierKeepsEveryTierAndCheckpoint(t *testing.T) {
	for _, status := range []string{MultiplierResolutionMissing, MultiplierResolutionUnavailable, MultiplierResolutionStale, MultiplierResolutionUpdating} {
		for _, tier := range []int{1, 2} {
			for _, current := range []int{50, 60} {
				s, repo, actions, items, states := taskBPriorityFixture(RuleVersionV2, []taskBPriorityCase{{id: "a", tier: tier, state: StateHealthy, current: current, multiplier: float64Ptr(.1), status: status}})
				id := "sub2api:ws1:a"
				stored := PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: id, OriginalPriority: 70, LastAppliedPriority: 50, EffectiveMultiplier: .01}
				repo.priorityStates["user1|ws1|"+id] = stored
				items[id].fallbackMultipliers = []float64{.001}
				s.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, states, []PrioritySyncState{stored})
				if len(actions.calls) != 0 || !reflect.DeepEqual(repo.priorityStates["user1|ws1|"+id], stored) {
					t.Fatalf("status=%s tier=%d current=%d crossed multiplier gate: calls=%v state=%+v", status, tier, current, actions.calls, repo.priorityStates["user1|ws1|"+id])
				}
			}
		}
	}
}

func TestTierSortMultiplierOnlyTransitionReclaimsUnwrittenHealthBaseline(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		s, repo, actions, items, _ := taskBPriorityFixture(RuleVersionV2, []taskBPriorityCase{{id: "a", current: 60, multiplier: float64Ptr(.1)}})
		id := "sub2api:ws1:a"
		only := items[id].policies[0]
		only.ID = "only"
		only.StrategyMode = StrategyModeMultiplierOnly
		if mixed {
			items[id].policies = append(items[id].policies, only)
		} else {
			items[id].policies = []Policy{only}
		}
		items[id].multipliers = []float64{.1}
		stored := PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: id, OriginalPriority: 50, LastAppliedPriority: 0}
		repo.priorityStates["user1|ws1|"+id] = stored
		s.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, nil, []PrioritySyncState{stored})
		got := repo.priorityStates["user1|ws1|"+id]
		if len(actions.calls) != 1 || actions.calls[0].priority != 1 || got.OriginalPriority != 60 || got.LastAppliedPriority != 60 || got.PendingPriority == nil || *got.PendingPriority != 1 {
			t.Fatalf("mixed=%v transition reused unwritten health baseline: calls=%v record=%+v", mixed, actions.calls, got)
		}
	}
}

func TestTierSortMultiplierOnlySameValueKeepsLegacyBaseline(t *testing.T) {
	for _, unwritten := range []bool{false, true} {
		s, repo, actions, items, _ := taskBPriorityFixture(RuleVersionV2, []taskBPriorityCase{{id: "a", current: 1, multiplier: float64Ptr(.1)}})
		id := "sub2api:ws1:a"
		items[id].policies[0].StrategyMode = StrategyModeMultiplierOnly
		items[id].multipliers = []float64{.1}
		var states []PrioritySyncState
		if unwritten {
			old := PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: id, OriginalPriority: 50}
			repo.priorityStates["user1|ws1|"+id] = old
			states = []PrioritySyncState{old}
		}
		s.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, nil, states)
		got, exists := repo.priorityStates["user1|ws1|"+id]
		if !exists || len(actions.calls) != 0 || got.OriginalPriority != 1 || got.LastAppliedPriority != 1 || got.EffectiveMultiplier != .1 || priorityActionPending(&got) {
			t.Fatalf("unwritten=%v old multiplier same-value baseline changed: exists=%v state=%+v calls=%v", unwritten, exists, got, actions.calls)
		}
	}
}

func TestTierSortNewAPIGoldenKeepsFiveBandsAndLegacyRanks(t *testing.T) {
	candidates := []healthPriorityCandidate{
		{targetID: "newapi:ws1:healthy", healthBand: 0, multiplier: .1, accountTier: 1},
		{targetID: "newapi:ws1:recovering", healthBand: 1, multiplier: .1, accountTier: 1},
		{targetID: "newapi:ws1:degraded", healthBand: 2, multiplier: .1, accountTier: 1},
		{targetID: "newapi:ws1:unconfigured-a", healthBand: 3, multiplier: .1},
		{targetID: "newapi:ws1:stopped", healthBand: 4, multiplier: .2},
		{targetID: "newapi:ws1:unconfigured-b", healthBand: 3, multiplier: .3},
	}
	got := encodeHealthPriorityCandidates(upstream.PlatformNewAPI, candidates)
	want := map[string]int{"newapi:ws1:healthy": 40999, "newapi:ws1:recovering": 30999, "newapi:ws1:degraded": 20999, "newapi:ws1:unconfigured-a": 10999, "newapi:ws1:unconfigured-b": 10998, "newapi:ws1:stopped": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NewAPI golden values changed: got=%v want=%v", got, want)
	}
	if candidates[3].targetID != "newapi:ws1:unconfigured-a" || candidates[4].targetID != "newapi:ws1:unconfigured-b" || candidates[5].targetID != "newapi:ws1:stopped" {
		t.Fatalf("NewAPI stopped and unconfigured display order merged: %+v", candidates)
	}
}

func TestTierSortIdleCASProtectsNewClaimAndChangedRecord(t *testing.T) {
	scope := RemoteActionScope{"user1", "ws1", "sub2api:ws1:a"}
	old := PrioritySyncState{UserID: scope.UserID, AdminAccountID: scope.AdminAccountID, TargetID: scope.TargetID, OriginalPriority: 50, LastAppliedPriority: 10}
	target := TargetActionState{UserID: scope.UserID, AdminAccountID: scope.AdminAccountID, TargetID: scope.TargetID, OriginalStatus: "active", LastAppliedStatus: "inactive", Conflict: true}
	for _, reason := range []string{"new-priority-claim", "new-target-claim", "changed-baseline", "changed-record-time"} {
		p, a := old, target
		switch reason {
		case "new-priority-claim":
			p.PendingDispatchID = "new"
		case "new-target-claim":
			a.PendingStatus = "active"
		case "changed-baseline":
			p.OriginalPriority = 60
		case "changed-record-time":
			p.UpdatedAt = time.Now()
		}
		pair := RemoteActionCheckpoints{Priority: &p, Target: &a}
		before := cloneActionPair(pair)
		err := mutateIdlePriorityCheckpoint(&pair, IdlePriorityCheckpointMutation{RemoteActionScope: scope, Expected: &old})
		if err == nil || !reflect.DeepEqual(pair, before) {
			t.Fatalf("%s erased changed paired state: %+v err=%v", reason, pair, err)
		}
	}
	p, a := old, target
	pair := RemoteActionCheckpoints{Priority: &p, Target: &a}
	if err := mutateIdlePriorityCheckpoint(&pair, IdlePriorityCheckpointMutation{RemoteActionScope: scope, Expected: &old}); err != nil || pair.Priority != nil || !reflect.DeepEqual(pair.Target, &target) {
		t.Fatalf("idle release failed or touched Target: %+v %v", pair, err)
	}
}

func TestTierSortResetClaimRequiresObservedCheckpointIdentity(t *testing.T) {
	scope := RemoteActionScope{"user1", "ws1", "sub2api:ws1:a"}
	generation := int64(3)
	old := PrioritySyncState{UserID: scope.UserID, AdminAccountID: scope.AdminAccountID, TargetID: scope.TargetID, OriginalPriority: 50}
	state := old
	state.OriginalPriority, state.LastAppliedPriority, state.PendingPriority = 60, 60, intPointer(1)
	claim := RemoteActionClaim{RemoteActionScope: scope, Kind: ActionKindPriority, DispatchID: "a", OwnerID: "owner", LeaseKey: "lease", ResetUnwrittenPriority: true, Priority: &state, Guard: RemoteActionHealthGuard{ConfigGeneration: &generation}, ExpectedPriorityCheckpoint: &old}
	for _, changed := range []bool{false, true} {
		current := old
		if changed {
			current.UpdatedAt = time.Now()
		}
		pair := RemoteActionCheckpoints{Priority: &current}
		ok, err := claimRemoteAction(&pair, claim)
		if changed {
			if ok || !errors.Is(err, ErrRemoteActionEvidenceChanged) {
				t.Fatalf("reset raced newer record: %v %v", ok, err)
			}
		} else if !ok || err != nil || pair.Priority.OriginalPriority != 60 {
			t.Fatalf("safe transition failed: %+v %v", pair, err)
		}
	}
}

type tierReadFailureRepository struct {
	*fakeRepository
	reads int
}

func (r *tierReadFailureRepository) ListAccountTiers(context.Context, string, string) (map[string]int, error) {
	r.reads++
	return nil, errors.New("fixture tier unavailable")
}

func TestTierSortReadFailureNeverDefaultsToBackup(t *testing.T) {
	s, repo, actions, items, states := taskBPriorityFixture(RuleVersionV2, []taskBPriorityCase{{id: "a", tier: 1, state: StateHealthy, multiplier: float64Ptr(.1)}})
	failed := &tierReadFailureRepository{fakeRepository: repo}
	s.repo = failed
	ctx := withAccountTierDecisionCache(t.Context())
	for i := 0; i < 2; i++ {
		s.syncWorkspacePriorities(ctx, upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, states, nil)
	}
	if len(actions.calls) != 0 || failed.reads != 1 || len(repo.priorityStates) != 0 {
		t.Fatalf("tier failure admitted default backup: calls=%v reads=%d checkpoints=%v", actions.calls, failed.reads, repo.priorityStates)
	}
}

type tierChangesConfigurationRepository struct{ *fakeRepository }

func (r *tierChangesConfigurationRepository) ListAccountTiers(ctx context.Context, user, workspace string) (map[string]int, error) {
	r.testConfigurationMu.Lock()
	r.bumpFakeConfigGeneration(user, workspace)
	r.testConfigurationMu.Unlock()
	return r.fakeRepository.ListAccountTiers(ctx, user, workspace)
}

func TestTierSortTierReadCannotLendNewGenerationToOldDecision(t *testing.T) {
	s, repo, actions, items, states := taskBPriorityFixture(RuleVersionV2, []taskBPriorityCase{{id: "a", tier: 1, state: StateHealthy, multiplier: float64Ptr(.1)}})
	s.repo = &tierChangesConfigurationRepository{fakeRepository: repo}
	s.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, states, nil)
	if len(actions.calls) != 0 || len(repo.priorityStates) != 0 || items["sub2api:ws1:a"].target.ConfigGeneration != 0 {
		t.Fatalf("old tier decision borrowed newer generation: calls=%v states=%v decision=%+v", actions.calls, repo.priorityStates, items["sub2api:ws1:a"].target)
	}
}

func TestTierSortBackgroundClaimCarriesAllThreeLeases(t *testing.T) {
	s, base, platform := taskBAccountAPIFixture(50, 1)
	s.mySites = fakeAdminGroupKeyReader{fakeMySitesReader: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}}
	s.sites = fakeSiteLookup{}
	repo := &manualSettingsPermitFaultRepository{fakeRepository: base, fault: "workspace-aba"}
	s.repo = repo
	id := "sub2api:ws1:a"
	base.states[id] = map[string]ConnectionHealthState{"gpt-4o": {ConnectionID: id, ModelName: "gpt-4o", UserID: "user1", AdminAccountID: "ws1", State: StateHealthy, CurrentWeight: 100, HealthEvidenceStatus: HealthEvidenceLegacy, RuleVersion: RuleVersionV2}}
	if err := s.syncCurrentWorkspacePrioritiesWithResult(t.Context(), "user1", "ws1"); err != nil {
		t.Fatal(err)
	}
	claim := repo.claim
	if claim.WorkspaceLeaseKey != priorityRuntimeLeaseKey("user1", "ws1") || claim.LeaseKey != "connection-health:target:"+id || claim.MutationLeaseKey != mutationRuntimeLeaseKey("user1", "ws1") {
		t.Fatalf("background ranking substituted workspace for target authority: %+v claimErr=%v", claim, repo.claimErr)
	}
	if len(platform.priorityWrites) != 0 {
		t.Fatalf("lost workspace authority still dispatched: %v", platform.priorityWrites)
	}
}
