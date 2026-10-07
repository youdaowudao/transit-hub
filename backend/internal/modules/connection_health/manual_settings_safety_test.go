package connection_health

import (
	"context"
	"errors"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

type manualSettingsPermitFaultRepository struct {
	*fakeRepository
	fault    string
	claim    RemoteActionClaim
	claimErr error
	cancel   context.CancelFunc
}

func (r *manualSettingsPermitFaultRepository) ClaimRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	r.claim = claim
	ok, err := r.fakeRepository.ClaimRemoteAction(ctx, claim)
	r.claimErr = err
	return ok, err
}

func (r *manualSettingsPermitFaultRepository) PermitRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	r.claim = claim
	if r.cancel != nil {
		r.cancel()
	}
	if r.fault == "generation-aba" {
		r.testConfigurationMu.Lock()
		r.bumpFakeConfigGeneration(claim.UserID, claim.AdminAccountID)
		r.bumpFakeConfigGeneration(claim.UserID, claim.AdminAccountID)
		r.testConfigurationMu.Unlock()
	}
	var owner, key string
	switch r.fault {
	case "workspace-aba":
		owner, key = claim.WorkspaceOwnerID, claim.WorkspaceLeaseKey
	case "target-aba":
		owner, key = claim.OwnerID, claim.LeaseKey
	case "mutation-aba":
		owner, key = claim.MutationOwnerID, claim.MutationLeaseKey
	}
	if owner != "" {
		r.expireActionLeaseForTest(owner, false)
		handle, acquired, err := r.AcquireActionLease(context.Background(), key, false)
		if err != nil || !acquired {
			return false, errors.New("fixture takeover failed")
		}
		defer handle.Release()
	}
	ok, err := r.fakeRepository.PermitRemoteAction(ctx, claim)
	if r.fault == "unknown-permit" && ok && err == nil {
		return false, errors.New("fixture permit commit uncertain")
	}
	return ok, err
}

func TestManualSettingsPriorityThreeLeaseAndGenerationABASendNothing(t *testing.T) {
	for _, fault := range []string{"workspace-aba", "target-aba", "mutation-aba", "generation-aba"} {
		t.Run(fault, func(t *testing.T) {
			s, base, platform := taskBAccountAPIFixture(50, 1)
			repo := &manualSettingsPermitFaultRepository{fakeRepository: base, fault: fault}
			s.repo = repo
			result, err := s.SetTargetPriorityOwner(t.Context(), "user1", "sub2api:ws1:a", TargetPriorityOwnerInput{Mode: "manual", Priority: intPointer(5)})
			if err != nil || result.Result != AccountEditNotSent || len(platform.priorityWrites) != 0 {
				t.Fatalf("fault=%s result=%+v err=%v calls=%v", fault, result, err, platform.priorityWrites)
			}
			if repo.claim.WorkspaceLeaseKey == "" || repo.claim.LeaseKey == "" || repo.claim.MutationLeaseKey == "" || repo.claim.WorkspaceLeaseKey == repo.claim.LeaseKey {
				t.Fatalf("manual claim omitted distinct authority: %+v", repo.claim)
			}
			if _, exists := base.priorityStates["user1|ws1|sub2api:ws1:a"]; exists {
				t.Fatal("never-sent first claim left a baseline")
			}
			if len(base.states) != 0 {
				t.Fatal("manual decision invented probe evidence")
			}
		})
	}
}

func TestManualSettingsUnknownPermitRetainsUncertainClaimWithoutReplay(t *testing.T) {
	s, base, platform := taskBAccountAPIFixture(50, 1)
	repo := &manualSettingsPermitFaultRepository{fakeRepository: base, fault: "unknown-permit"}
	s.repo = repo
	input := TargetPriorityOwnerInput{Mode: "manual", Priority: intPointer(5)}
	result, err := s.SetTargetPriorityOwner(t.Context(), "user1", "sub2api:ws1:a", input)
	if err != nil || result.Result != AccountEditPending || len(platform.priorityWrites) != 0 {
		t.Fatalf("unknown permit outcome=%+v err=%v calls=%v", result, err, platform.priorityWrites)
	}
	if state := base.priorityStates["user1|ws1|sub2api:ws1:a"]; state.PendingDispatchPhase != DispatchSending || state.LastAppliedPriority != 0 {
		t.Fatalf("unknown commit discarded claim: %+v", state)
	}
	if _, err = s.SetTargetPriorityOwner(t.Context(), "user1", "sub2api:ws1:a", input); !errors.Is(err, ErrRemoteActionPending) || len(platform.priorityWrites) != 0 {
		t.Fatalf("unknown commit replayed: %v calls=%v", err, platform.priorityWrites)
	}
}

func TestManualSettingsBrowserCancellationAfterAcceptanceDoesNotCancelWrite(t *testing.T) {
	s, base, platform := taskBAccountAPIFixture(50, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	repo := &manualSettingsPermitFaultRepository{fakeRepository: base, cancel: cancel}
	s.repo = repo
	result, err := s.SetTargetPriorityOwner(ctx, "user1", "sub2api:ws1:a", TargetPriorityOwnerInput{Mode: "manual", Priority: intPointer(5)})
	waitForPriorityAsyncIdle(t)
	if err != nil || result.Result != AccountEditSuccess || len(platform.priorityWrites) != 1 || platform.priorityWrites[0] != 5 {
		t.Fatalf("accepted action inherited browser cancellation: %+v %v calls=%v", result, err, platform.priorityWrites)
	}
}

func TestManualSettingsProjectionSharedOwnershipTrueFalseUnknown(t *testing.T) {
	for _, tc := range []struct {
		name             string
		incomplete, only bool
		want             *bool
	}{
		{"complete-health", false, false, boolPointer(false)},
		{"complete-only", false, true, boolPointer(true)},
		{"incomplete-health", true, false, nil},
		{"incomplete-known-only", true, true, boolPointer(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepository()
			p := sub2APIProbePolicy(false)
			p.PriorityMode = PriorityModeMultiplier
			if tc.only {
				p.StrategyMode = StrategyModeMultiplierOnly
			}
			repo.policies = []Policy{p}
			assignPolicyToTarget(repo, p, "sub2api:ws1:a")
			reader := fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1"}, {ID: "g2"}}, accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "a", Priority: intPointer(5), Models: "gpt-4o"}}, "g2": {{ID: "a", Priority: intPointer(5), Models: "gpt-4o"}}}}
			if tc.incomplete {
				reader.errByGrp = map[string]error{"g2": errors.New("fixture partial inventory")}
			}
			repo.priorityStates["user1|ws1|sub2api:ws1:a"] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:a", OriginalPriority: 50, LastAppliedPriority: 0, PendingPriority: intPointer(10), PendingDispatchID: "a", PendingDispatchPhase: DispatchUncertain}
			s := newAdminGroupsService(reader, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
			groups, err := s.AdminGroups(t.Context(), "user1")
			if err != nil {
				t.Fatal(err)
			}
			seen := 0
			for _, g := range groups {
				for _, account := range g.Accounts {
					seen++
					got := account.PriorityUsesMultiplierOnly
					if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
						t.Fatalf("shared ownership projection=%v want=%v", got, tc.want)
					}
					if !account.PriorityActionPending || account.PriorityExpected == nil || *account.PriorityExpected != 10 || account.Priority == nil || *account.Priority != 5 {
						t.Fatalf("projection lost pending/real current values: %+v", account)
					}
				}
			}
			if seen == 0 {
				t.Fatal("account disappeared")
			}
		})
	}
}
