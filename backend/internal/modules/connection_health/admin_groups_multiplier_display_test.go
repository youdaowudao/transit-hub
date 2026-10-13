package connection_health

import (
	"context"
	"math"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

func TestAdminGroups_UpstreamMultiplierDisplayWithoutPriorityStrategy(t *testing.T) {
	for _, name := range []string{"unassigned", "disabled_policy", "health_only", "excluded", "manual_priority", "inactive", "unschedulable"} {
		t.Run(name, func(t *testing.T) {
			repo := newFakeRepository()
			policy := probePolicy()
			policy.PriorityMode = PriorityModeMultiplier
			policy.StrategyMode = StrategyModeHealthProbe
			policy.Enabled = name != "disabled_policy"
			if name == "health_only" {
				policy.PriorityMode = PriorityModeNone
			}
			repo.policies = []Policy{policy}
			if name != "unassigned" && name != "inactive" && name != "unschedulable" {
				repo.groupAssignments = []GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: policy.ID}}
			}
			if name == "excluded" {
				repo.groupExclusions = []GroupTargetExclusion{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", TargetID: "sub2api:ws1:100"}}
			}
			priority, status, schedulable := 50, "active", true
			if name == "manual_priority" {
				priority = 5
			}
			if name == "inactive" {
				status = "inactive"
			}
			if name == "unschedulable" {
				schedulable = false
			}
			raw, recharge, unrelated := 0.8, 0.1, 9.0
			reader := fakePlatformGroupReader{
				groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "own", Multiplier: &unrelated}},
				accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{
					ID: "100", Name: "linked", Status: status, Schedulable: &schedulable,
					Priority: &priority, RateMultiplier: &unrelated, BaseURL: "https://probe.invalid", Models: "gpt-4o",
				}}},
			}
			mySites := fakeAdminGroupKeyReader{
				fakeMySitesReader: fakeMySitesReader{
					session:     upstream.Session{Platform: upstream.PlatformSub2API},
					connections: []my_sites.RealConnection{{UserID: "user1", WorkspaceAdminAccountID: "ws1", AdminAccountID: "100", AdminPlatform: "sub2api", UpstreamSiteID: "site1", UpstreamKeyID: "key1"}},
				},
				keysBySite: map[string][]upstream.Sub2APIKeyItem{"site1": {{ID: "key1", GroupID: "upstream1", GroupName: "upstream"}}},
			}
			service := newAdminGroupsService(reader, mySites, repo)
			service.sites = fakeSiteLookup{site: &upstream.Site{ID: "site1", RechargeRate: recharge, Metrics: upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "upstream1", Name: "upstream", Multiplier: &raw}}}}}
			groups, err := service.AdminGroups(context.Background(), "user1")
			if err != nil || len(groups) != 1 || len(groups[0].Accounts) != 1 {
				t.Fatalf("AdminGroups = %+v, err = %v", groups, err)
			}
			account := groups[0].Accounts[0]
			if account.UpstreamKeyGroupMultiplier == nil || *account.UpstreamKeyGroupMultiplier != raw || account.MultiplierResolutionStatus != MultiplierResolutionResolved {
				t.Fatalf("existing upstream resolution must remain available: %+v", account)
			}
			if account.EffectiveMultiplier == nil || math.Abs(*account.EffectiveMultiplier-0.08) > 1e-9 {
				t.Errorf("display multiplier = %v, want upstream 0.8 * recharge 0.1 = 0.08", account.EffectiveMultiplier)
			}
			if account.MultiplierSource != MultiplierSourceNone || account.PriorityManaged || account.PrioritySyncBlocked || hasMultiplierPriorityPolicy(policiesForIDs(account.EffectivePolicyIDs, map[string]Policy{policy.ID: policy})) {
				t.Errorf("display must not enable multiplier scheduling: %+v", account)
			}
			if account.Priority == nil || *account.Priority != priority || account.Schedulable == nil || *account.Schedulable != schedulable || account.Status != status {
				t.Errorf("display must preserve main-site values: %+v", account)
			}
			if len(repo.priorityStates) != 0 || len(repo.targetActionStates) != 0 || len(repo.states) != 0 || len(repo.events) != 0 {
				t.Error("read-only display must not create priority, action, health or event records")
			}
		})
	}
}

func TestAdminGroups_UpstreamMultiplierDisplayKeepsResolutionBoundaries(t *testing.T) {
	for _, status := range []string{MultiplierResolutionResolved, MultiplierResolutionStale, MultiplierResolutionUnassociated, MultiplierResolutionMissing, MultiplierResolutionConflict, MultiplierResolutionDisabled, MultiplierResolutionUnavailable, MultiplierResolutionUpdating} {
		t.Run(status, func(t *testing.T) {
			repo := newFakeRepository()
			service := newAdminGroupsService(fakePlatformGroupReader{
				groups:        []upstream.AdminGroupInfo{{ID: "g1"}},
				accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "100"}}},
			}, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
			value := 0.08
			service.adminMultiplierCache = map[string]adminMultiplierCacheEntry{
				"user1\x00ws1\x00sub2api": {
					lookup:    upstreamMultiplierLookup{byAccount: map[string]upstreamMultiplierResolution{"100": {status: status, info: upstreamKeyGroupInfo{effectiveMultiplier: &value}}}},
					expiresAt: time.Now().Add(time.Minute),
				},
			}
			groups, err := service.AdminGroups(context.Background(), "user1")
			if err != nil || len(groups) != 1 || len(groups[0].Accounts) != 1 {
				t.Fatalf("AdminGroups = %+v, err = %v", groups, err)
			}
			account := groups[0].Accounts[0]
			if status == MultiplierResolutionResolved || status == MultiplierResolutionStale {
				if account.EffectiveMultiplier == nil || *account.EffectiveMultiplier != value {
					t.Errorf("readable upstream value must be displayed: %+v", account)
				}
			} else if account.EffectiveMultiplier != nil {
				t.Errorf("unresolved metadata must not manufacture a display value: %+v", account)
			}
			if account.MultiplierSource != MultiplierSourceNone || account.MultiplierResolutionStatus != status || account.PrioritySyncBlocked || account.PriorityManaged {
				t.Errorf("display must preserve resolution and scheduling boundaries: %+v", account)
			}
		})
	}
}

func TestAdminGroups_UpstreamMultiplierDisplayPreservesIdlePriorityCheckpoint(t *testing.T) {
	for _, status := range []string{MultiplierResolutionResolved, MultiplierResolutionStale, MultiplierResolutionUnassociated} {
		t.Run(status, func(t *testing.T) {
			repo := newFakeRepository()
			checkpoint := PrioritySyncState{UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:100", OriginalPriority: 7, LastAppliedPriority: 99, EffectiveMultiplier: 0.06}
			repo.priorityStates["user1|ws1|sub2api:ws1:100"] = checkpoint
			priority := 99
			service := newAdminGroupsService(fakePlatformGroupReader{
				groups:        []upstream.AdminGroupInfo{{ID: "g1"}},
				accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{"g1": {{ID: "100", Priority: &priority}}},
			}, fakeMySitesReader{session: upstream.Session{Platform: upstream.PlatformSub2API}}, repo)
			value := 0.08
			service.adminMultiplierCache = map[string]adminMultiplierCacheEntry{
				"user1\x00ws1\x00sub2api": {
					lookup:    upstreamMultiplierLookup{byAccount: map[string]upstreamMultiplierResolution{"100": {status: status, info: upstreamKeyGroupInfo{effectiveMultiplier: &value}}}},
					expiresAt: time.Now().Add(time.Minute),
				},
			}
			groups, err := service.AdminGroups(context.Background(), "user1")
			if err != nil || len(groups) != 1 || len(groups[0].Accounts) != 1 {
				t.Fatalf("AdminGroups = %+v, err = %v", groups, err)
			}
			account := groups[0].Accounts[0]
			want := value
			if status == MultiplierResolutionUnassociated {
				want = checkpoint.EffectiveMultiplier
			}
			if account.EffectiveMultiplier == nil || *account.EffectiveMultiplier != want || account.MultiplierSource != MultiplierSourceNone {
				t.Errorf("display must prefer readable upstream and preserve existing history otherwise: %+v", account)
			}
			if repo.priorityStates["user1|ws1|sub2api:ws1:100"] != checkpoint || *account.Priority != priority || !account.PriorityManaged || account.PrioritySyncBlocked {
				t.Error("display must not change an idle priority checkpoint or main-site priority")
			}
		})
	}
}
