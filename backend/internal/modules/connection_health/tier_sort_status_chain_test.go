package connection_health

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type fullChainStatusReader struct {
	fakePlatformGroupReader
	credentialReads atomic.Int32
}

func (r *fullChainStatusReader) ResolveProbeCredential(upstream.Session, upstream.AdminGroupAccountInfo) (upstream.ProbeCredential, error) {
	r.credentialReads.Add(1)
	return upstream.ProbeCredential{}, errors.New("fixture credential unavailable")
}

func TestTierSortFullChainLegacyPrimaryRestoresDuringSkippedAndCredentialFailedTicks(t *testing.T) {
	for _, reason := range []string{"cooldown", "long-failure-not-due", "budget-exhausted", "credential-failed"} {
		t.Run(reason, func(t *testing.T) {
			s, r, actions, baseReader, p, checkpoint := taskBStatusRestoreFixture(RuleVersionLegacy, 1, 20)
			reader := &fullChainStatusReader{fakePlatformGroupReader: baseReader}
			s.platformGroups = reader
			p.ProbeIntervalSeconds, p.DailyProbeBudget = 60, 1
			r.policies = []Policy{p}
			assignPolicyToTarget(r, p, checkpoint.TargetID)
			st := fullChainHealthState(r, RuleVersionLegacy, StateSuspended)
			st.CurrentWeight, st.ConsecutiveFailures = 0, 3
			now := time.Now().UTC()
			switch reason {
			case "cooldown":
				future := now.Add(time.Hour)
				st.CooldownUntil = &future
			case "long-failure-not-due":
				origin, last := now.Add(-48*time.Hour), now.Add(-time.Minute)
				st.FailingSince, st.LastProbeAt, st.ConsecutiveFailures = &origin, &last, 20
			case "budget-exhausted":
				r.budgetClaims["user1|ws1|"+p.ID+"|"+probeBudgetDayStart(now).Format(time.RFC3339)] = 1
			}
			r.states[checkpoint.TargetID][st.ModelName] = st
			actions.afterSub2APIWrite = func(id, status string) {
				if id == "a" {
					reader.accountsByGrp["g1"][0].Status = status
				}
			}
			s.runSchedulerTick(t.Context())
			if len(actions.sub2APICalls) != 1 || actions.sub2APICalls[0].status != "active" || reader.accountsByGrp["g1"][0].Status != "active" {
				t.Fatalf("tick with %s failed to restore the original status: %+v", reason, actions.sub2APICalls)
			}
			wantCredentialReads := int32(0)
			if reason == "credential-failed" {
				wantCredentialReads = 1
			}
			if reader.credentialReads.Load() != wantCredentialReads {
				t.Fatalf("%s did not exercise its actual scheduling barrier: credential reads=%d want=%d", reason, reader.credentialReads.Load(), wantCredentialReads)
			}
			got := r.states[checkpoint.TargetID][st.ModelName]
			if got.State != st.State || got.CurrentWeight != st.CurrentWeight || got.ConsecutiveFailures != st.ConsecutiveFailures || !reflect.DeepEqual(got.LastProbeAt, st.LastProbeAt) || !reflect.DeepEqual(got.FailingSince, st.FailingSince) {
				t.Fatalf("restoring with no probe outcome changed health: before=%+v after=%+v", st, got)
			}
			if reason == "credential-failed" && got.LastCredentialFailureAt == nil {
				t.Fatal("credential failure was not actually recorded")
			}
			s.runSchedulerTick(t.Context())
			if len(actions.sub2APICalls) != 1 || reader.accountsByGrp["g1"][0].Status != "active" {
				t.Fatal("confirmation tick replayed the original-state restoration")
			}
			if _, exists := r.targetActionStates["user1|ws1|"+checkpoint.TargetID]; exists {
				t.Fatal("fresh confirmed restored status retained its old checkpoint")
			}
			for _, event := range r.events {
				if event.Result != "policy_unmanaged_restore" && event.Result != string(ResultUnsupported) {
					t.Fatalf("tick fabricated a probe outcome: %+v", event)
				}
			}
		})
	}
}

func TestTierSortFullChainLegacyToV2RechecksFloorOnSamePrimary(t *testing.T) {
	for _, survivor := range []bool{false, true} {
		t.Run(map[bool]string{false: "last-primary", true: "with-survivor"}[survivor], func(t *testing.T) {
			r := newFakeRepository()
			r.accountTiers = map[string]int{"user1|ws1|sub2api:ws1:acc-1": 1}
			actions := &fakePlatformActioner{}
			s := &Service{repo: r, dispatcher: newRemoteActionDispatcher(nil, nil, actions)}
			target := sub2APISuspendedTargetFixture(r, "acc-1")
			accounts := []upstream.AdminGroupAccountInfo{{ID: "acc-1", Status: "active", Priority: intPointer(20), Models: "model-a"}}
			if survivor {
				accounts = append(accounts, upstream.AdminGroupAccountInfo{ID: "acc-2", Status: "active", Priority: intPointer(21), Models: "model-a"})
			}
			inventory := sub2APITestInventory(adminInventoryGroup{group: upstream.AdminGroupInfo{ID: "g1"}, accounts: accounts})
			stageAFloorService(s, inventory)
			p := r.policies[0]
			p.RuleVersion, p.AutoDegradeEnabled, p.AutoRemoteActionEnabled = RuleVersionLegacy, true, true
			p.ModelTargets = []ModelTarget{{ModelName: "model-a", Enabled: true}}
			r.policies = []Policy{p}
			spec := sub2APIActionTestSpec()
			spec.policy = p
			if _, err := s.reconcileTargetRemoteActionWithFloor(t.Context(), "user1", "ws1", inventory.session, target, []probeModelSpec{spec}, newWorkspaceFloorGuard(), inventory, fullFloorTestMonitoringScope(*inventory)); err != nil || len(actions.sub2APICalls) != 0 {
				t.Fatalf("legacy primary was disabled: %v %+v", err, actions.sub2APICalls)
			}
			p.RuleVersion = RuleVersionV2
			r.policies = []Policy{p}
			r.bumpFakeConfigGeneration("user1", "ws1")
			spec.policy = r.policies[0]
			target.ConfigGeneration, target.ConfigGenerationKnown = spec.policy.ConfigGeneration, true
			if _, err := s.reconcileTargetRemoteActionWithFloor(context.Background(), "user1", "ws1", inventory.session, target, []probeModelSpec{spec}, newWorkspaceFloorGuard(), inventory, fullFloorTestMonitoringScope(*inventory)); err != nil {
				t.Fatal(err)
			}
			if (len(actions.sub2APICalls) == 1) != survivor {
				t.Fatalf("switching the same primary to V2 bypassed or disabled floor: survivor=%v calls=%+v", survivor, actions.sub2APICalls)
			}
			if survivor && actions.sub2APICalls[0].status != "inactive" {
				t.Fatalf("new-rule closure wrote the wrong direction: %+v", actions.sub2APICalls)
			}
		})
	}
}
