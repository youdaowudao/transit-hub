package connection_health

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

func TestTierSortFullChainNoPrimaryGoldenIncludesOnlyAndUpdatingPeer(t *testing.T) {
	for _, version := range []string{RuleVersionV2, RuleVersionLegacy} {
		t.Run(version, func(t *testing.T) {
			cases := []taskBPriorityCase{{id: "a", state: StateHealthy, multiplier: float64Ptr(.13), latency: 10}, {id: "b", state: StateHealthy, multiplier: float64Ptr(.13), latency: 20}, {id: "unknown", state: StateHealthy}, {id: "stopped", state: StateSuspended, multiplier: float64Ptr(.1)}, {id: "manual", current: 5, state: StateHealthy}, {id: "no-evidence", noEvidence: true, multiplier: float64Ptr(.1)}, {id: "updating", current: 60, state: StateHealthy, multiplier: float64Ptr(.001), status: MultiplierResolutionUpdating}, {id: "only", state: StateHealthy, current: 50}}
			s, r, f, items, states := taskBPriorityFixture(version, cases)
			items["sub2api:ws1:only"].policies[0].StrategyMode = StrategyModeMultiplierOnly
			items["sub2api:ws1:only"].multipliers = []float64{.2}
			blocked := PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:updating", OriginalPriority: 70, LastAppliedPriority: 50, EffectiveMultiplier: .001}
			r.priorityStates["user1|ws1|"+blocked.TargetID] = blocked
			items[blocked.TargetID].fallbackMultipliers = []float64{.0001}
			s.syncWorkspacePriorities(t.Context(), upstream.Session{Platform: upstream.PlatformSub2API}, "user1", "ws1", items, true, states, []PrioritySyncState{blocked})
			want := map[string]int{"a": 10, "b": 10, "unknown": 99, "stopped": 100000, "only": 1}
			if version == RuleVersionLegacy {
				want["b"] = 11
			}
			if got := taskBWrittenPriorities(f); !reflect.DeepEqual(got, want) || !reflect.DeepEqual(r.priorityStates["user1|ws1|"+blocked.TargetID], blocked) {
				t.Fatalf("no-primary mixed golden got=%v want=%v blocked=%+v", got, want, r.priorityStates["user1|ws1|"+blocked.TargetID])
			}
		})
	}
}

type fullChainSettingsGroups struct{ *taskBAccountSettingsPlatform }

func (*fullChainSettingsGroups) FetchAdminAllGroups(upstream.Session) ([]upstream.AdminGroupInfo, error) {
	return []upstream.AdminGroupInfo{{ID: "g1", Multiplier: float64Ptr(.1)}}, nil
}

func fullChainSettingsFixture(priority, tier int) (*Service, *fakeRepository, *taskBAccountSettingsPlatform) {
	s, r, f := taskBAccountAPIFixture(priority, tier)
	s.platformGroups = &fullChainSettingsGroups{f}
	// Empty, complete external metadata means unassociated, not unavailable.
	s.mySites = fakeAdminGroupKeyReader{fakeMySitesReader: fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}}
	s.sites = fakeSiteLookup{}
	r.groupSortSettings["user1|ws1|g1"] = GroupProbeSortSetting{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", FallbackMultiplier: float64Ptr(.1)}
	return s, r, f
}

func fullChainSync(t *testing.T, s *Service, r *fakeRepository) {
	t.Helper()
	states, err := r.ListPrioritySyncStates(t.Context(), "user1", "ws1")
	if err != nil {
		t.Fatal(err)
	}
	s.syncMultiplierPriorities(t.Context(), r.policies, r.assignments, r.groupAssignments, r.groupExclusions, states)
}

func fullChainHealthState(r *fakeRepository, version string, state State) ConnectionHealthState {
	st := ConnectionHealthState{UserID: "user1", AdminAccountID: "ws1", ConnectionID: "sub2api:ws1:a", ModelName: "gpt-4o", State: state, RuleVersion: version, HealthEvidenceStatus: HealthEvidenceLegacy, CurrentWeight: 100}
	r.states[st.ConnectionID] = map[string]ConnectionHealthState{st.ModelName: st}
	return st
}

type fullChainPreparedOwnerLostRepository struct {
	*fakeRepository
	armed         bool
	claim         RemoteActionClaim
	permits       int
	receiptPhases []RemoteDispatchPhase
}

func (r *fullChainPreparedOwnerLostRepository) ClaimRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	claimed, err := r.fakeRepository.ClaimRemoteAction(ctx, claim)
	if err != nil || !claimed || !r.armed {
		return claimed, err
	}
	// Leave the real prepared checkpoint as a worker would after losing its
	// owner before obtaining permission. No receipt or HTTP success is injected.
	r.armed, r.claim = false, claim
	r.expireActionLeaseForTest(claim.OwnerID, false)
	return false, errors.New("fixture prepared owner lost before permit")
}

func (r *fullChainPreparedOwnerLostRepository) PermitRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	r.permits++
	return r.fakeRepository.PermitRemoteAction(ctx, claim)
}

func (r *fullChainPreparedOwnerLostRepository) RecordRemoteActionReceipt(ctx context.Context, claim RemoteActionClaim, phase RemoteDispatchPhase) error {
	r.receiptPhases = append(r.receiptPhases, phase)
	return r.fakeRepository.RecordRemoteActionReceipt(ctx, claim, phase)
}

func TestTierSortFullChainNoEffectToOnlyAndWithdrawalRestoresOriginal(t *testing.T) {
	for _, entry := range []string{"health", "page"} {
		for _, outcome := range []string{upstream.MutationNotSent, upstream.MutationConfirmedRejected, "prepared-owner-lost"} {
			for _, mixed := range []bool{false, true} {
				for _, exit := range []string{"withdraw", "exclude"} {
					t.Run(fmt.Sprintf("%s/%s/mixed-%v/%s", entry, outcome, mixed, exit), func(t *testing.T) {
						s, r, f := fullChainSettingsFixture(50, 2)
						var prepared *fullChainPreparedOwnerLostRepository
						if outcome == "prepared-owner-lost" {
							prepared = &fullChainPreparedOwnerLostRepository{fakeRepository: r, armed: true}
							s.repo = prepared
						} else {
							f.writeErr = &upstream.RequestError{MessageKey: "fixture.rejected", MutationOutcome: outcome}
						}
						if entry == "page" {
							want := AccountEditNotSent
							if prepared != nil {
								// A claim error remains pending until owner validity is
								// reconciled; it is never presented as a successful write.
								want = AccountEditPending
							}
							taskBRequireAccountResult(t, taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"manual","priority":5}`), want)
						} else {
							fullChainHealthState(r, RuleVersionV2, StateHealthy)
							fullChainSync(t, s, r)
							st := r.priorityStates["user1|ws1|sub2api:ws1:a"]
							if st.OriginalPriority != 50 || st.LastAppliedPriority != 0 || !priorityActionPending(&st) {
								t.Fatalf("health first failure lost zero-write origin: %+v", st)
							}
						}
						if prepared != nil {
							st := r.priorityStates["user1|ws1|sub2api:ws1:a"]
							if prepared.armed || prepared.claim.Kind != ActionKindPriority || st.OriginalPriority != 50 || st.LastAppliedPriority != 0 || st.PendingDispatchPhase != DispatchPrepared || st.PendingDispatchID != prepared.claim.DispatchID || st.PendingOwnerID == "" || st.PendingOwnerID != prepared.claim.OwnerID || !priorityActionPending(&st) {
								t.Fatalf("first owner loss did not retain the real zero-write prepared claim: %+v claim=%+v", st, prepared.claim)
							}
							r.actionMu.Lock()
							ownerValid := false
							for _, lease := range r.actionLeases {
								ownerValid = ownerValid || lease.OwnerID == st.PendingOwnerID
							}
							r.actionMu.Unlock()
							if ownerValid || prepared.permits != 0 || len(prepared.receiptPhases) != 0 || len(f.priorityWrites) != 0 || f.priority != 50 {
								t.Fatalf("prepared owner loss reached a permit, receipt or HTTP write: owner=%v permits=%d receipts=%v actual=%d writes=%v", ownerValid, prepared.permits, prepared.receiptPhases, f.priority, f.priorityWrites)
							}
						}
						if entry == "health" || prepared != nil {
							pair, err := s.reconcileActionObservation(t.Context(), RemoteActionObservation{RemoteActionScope: RemoteActionScope{"user1", "ws1", "sub2api:ws1:a"}})
							if err != nil || pair.Priority != nil || (prepared != nil && (!pair.reconciledWithoutRemoteEffect || pair.pendingCount() != 0)) {
								t.Fatalf("first no-effect did not atomically close: %+v %v", pair, err)
							}
						}
						if _, ok := r.priorityStates["user1|ws1|sub2api:ws1:a"]; ok || f.priority != 50 {
							t.Fatal("first no-effect retained a baseline or changed actual")
						}
						f.writeErr = nil
						p := r.policies[0]
						only := p
						only.ID, only.StrategyMode = "only", StrategyModeMultiplierOnly
						r.policies = []Policy{only}
						if mixed {
							r.policies = []Policy{p, only}
						}
						r.assignments = nil
						r.groupAssignments = nil
						for _, policy := range r.policies {
							r.groupAssignments = append(r.groupAssignments, GroupPolicyAssignment{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: policy.ID})
						}
						r.bumpFakeConfigGeneration("user1", "ws1")
						fullChainSync(t, s, r)
						fullChainSync(t, s, r)
						st := r.priorityStates["user1|ws1|sub2api:ws1:a"]
						if f.priority != 1 || st.OriginalPriority != 50 || st.LastAppliedPriority != 1 || priorityActionPending(&st) || st.Conflict {
							t.Fatalf("only transition failed to confirm old initialization: actual=%d state=%+v calls=%v", f.priority, st, f.priorityWrites)
						}
						before := len(f.priorityWrites)
						if exit == "withdraw" {
							r.groupAssignments = nil
						} else {
							r.groupExclusions = []GroupTargetExclusion{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", TargetID: "sub2api:ws1:a"}}
						}
						r.bumpFakeConfigGeneration("user1", "ws1")
						fullChainSync(t, s, r)
						st = r.priorityStates["user1|ws1|sub2api:ws1:a"]
						if f.priority != 50 || len(f.priorityWrites) != before+1 || !strings.HasPrefix(st.PendingDispatchID, "priority-release:") || st.LastAppliedPriority != 1 {
							t.Fatalf("old only exit did not send exact original once: actual=%d state=%+v calls=%v", f.priority, st, f.priorityWrites)
						}
						fullChainSync(t, s, r)
						if _, ok := r.priorityStates["user1|ws1|sub2api:ws1:a"]; ok || len(f.priorityWrites) != before+1 {
							t.Fatal("confirmed exit retained or replayed the old run")
						}
						if prepared != nil && (prepared.permits != 2 || !reflect.DeepEqual(prepared.receiptPhases, []RemoteDispatchPhase{DispatchConfirmedApplied, DispatchConfirmedApplied}) || !reflect.DeepEqual(f.priorityWrites, []int{1, 50})) {
							t.Fatalf("only and restore did not use genuine subsequent sends: permits=%d receipts=%v writes=%v", prepared.permits, prepared.receiptPhases, f.priorityWrites)
						}
					})
				}
			}
		}
	}
}

func TestTierSortFullChainLegacyBaselineAutoKeeps50AndExitRestores50(t *testing.T) {
	for _, tier := range []int{1, 2} {
		for _, outcome := range []string{"applied", upstream.MutationNotSent, upstream.MutationConfirmedRejected} {
			t.Run(fmt.Sprintf("tier-%d/%s", tier, outcome), func(t *testing.T) {
				s, r, f := fullChainSettingsFixture(1, tier)
				r.priorityStates["user1|ws1|sub2api:ws1:a"] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:a", OriginalPriority: 50, LastAppliedPriority: 1}
				if outcome != "applied" {
					f.writeErr = &upstream.RequestError{MessageKey: "fixture.rejected", MutationOutcome: outcome}
				}
				want := AccountEditSuccess
				if outcome != "applied" {
					want = AccountEditNotSent
				}
				taskBRequireAccountResult(t, taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"auto"}`), want)
				waitForPriorityAsyncIdle(t)
				st := r.priorityStates["user1|ws1|sub2api:ws1:a"]
				if st.OriginalPriority != 50 || priorityActionPending(&st) || (outcome != "applied" && (st.LastAppliedPriority != 1 || f.priority != 1)) {
					t.Fatalf("page auto lost old restoration baseline: %+v actual=%d", st, f.priority)
				}
				f.writeErr = nil
				r.assignments = nil
				r.bumpFakeConfigGeneration("user1", "ws1")
				fullChainSync(t, s, r)
				fullChainSync(t, s, r)
				if f.priority != 50 {
					t.Fatalf("page auto exit restored %d, want original 50: %v", f.priority, f.priorityWrites)
				}
				if _, exists := r.priorityStates["user1|ws1|sub2api:ws1:a"]; exists {
					t.Fatal("confirmed page-auto exit retained a record")
				}
			})
		}
	}
}

func TestTierSortFullChainReleaseConfirmationSurvivesConfigurationABA(t *testing.T) {
	s, r, f := taskBAccountAPIFixture(1, 1)
	r.priorityStates["user1|ws1|sub2api:ws1:a"] = PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:a", OriginalPriority: 50, LastAppliedPriority: 1}
	f.readFailAfterWrite = true
	taskBRequireAccountResult(t, taskBAccountRequest(s, "user1", "sub2api:ws1:a", "priority-owner", `{"mode":"manual","priority":5}`), AccountEditPending)
	r.bumpFakeConfigGeneration("user1", "ws1")
	r.bumpFakeConfigGeneration("user1", "ws1")
	f.readErr, f.readFailAfterWrite = nil, false
	fresh, err := s.loadAdminInventory(context.Background(), "user1", "ws1", adminInventoryCache{})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := findActionInventoryTarget("sub2api:ws1:a", *fresh)
	if pair, err := s.reconcileActionObservation(t.Context(), targetObservation("user1", "ws1", target, fresh)); err != nil || pair.Priority != nil || f.priority != 5 || len(f.priorityWrites) != 1 {
		t.Fatalf("confirmed release borrowed a sending-generation requirement: %+v %v calls=%v", pair, err, f.priorityWrites)
	}
	// Settling a confirmed old action does not grant its old generation a new send.
	if len(r.states) != 0 || len(r.events) != 1 || r.events[0].Result != "account_edit_pending" {
		t.Fatalf("late settlement invented a health result or extra user audit: %v %v", r.states, r.events)
	}
}
