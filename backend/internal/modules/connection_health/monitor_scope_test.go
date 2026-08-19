package connection_health

import (
	"context"
	"errors"
	"testing"

	"transithub/backend/internal/modules/upstream"
)

func monitoringScopeTestPolicy(id string) Policy {
	return Policy{
		ID: id, UserID: "user1", AdminAccountID: "ws1", Enabled: true,
		StrategyMode: StrategyModeHealthProbe,
		ModelTargets: []ModelTarget{{PolicyID: id, ModelName: "gpt-4o", Enabled: true}},
	}
}

func monitoringScopeTestInventory(groups ...adminInventoryGroup) adminWorkspaceInventory {
	return adminWorkspaceInventory{
		session: upstream.Session{Platform: upstream.PlatformSub2API},
		groups:  groups,
	}
}

func monitoringScopeContains(scope adminMonitoringScope, groupID string, accountID string) bool {
	targetID := buildTargetID(string(upstream.PlatformSub2API), "ws1", accountID)
	_, exists := scope.monitoredByGroup[groupID][targetID]
	return exists
}

func TestBuildAdminMonitoringScope_GroupPolicyExcludesTarget(t *testing.T) {
	policy := monitoringScopeTestPolicy("group-policy")
	inventory := monitoringScopeTestInventory(adminInventoryGroup{
		group:    upstream.AdminGroupInfo{ID: "g1", Name: "group-1"},
		accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Models: "gpt-4o"}},
	})
	targetID := buildTargetID(string(upstream.PlatformSub2API), "ws1", "acc-1")

	scope, err := buildAdminMonitoringScope(
		"user1", "ws1", inventory, []Policy{policy}, nil,
		[]GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: policy.ID}},
		[]GroupTargetExclusion{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", TargetID: targetID}},
	)
	if err != nil {
		t.Fatalf("build scope: %v", err)
	}
	if !scope.complete || monitoringScopeContains(scope, "g1", "acc-1") {
		t.Fatalf("excluded group target entered monitoring scope: %+v", scope)
	}
}

func TestBuildAdminMonitoringScope_DoesNotUseUnassignedSiblingGroup(t *testing.T) {
	policy := monitoringScopeTestPolicy("group-policy")
	inventory := monitoringScopeTestInventory(
		adminInventoryGroup{
			group:    upstream.AdminGroupInfo{ID: "g1", Name: "monitored"},
			accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Models: "gpt-4o"}},
		},
		adminInventoryGroup{
			group:    upstream.AdminGroupInfo{ID: "g2", Name: "not-monitored"},
			accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Models: "gpt-4o"}, {ID: "acc-2", Models: "gpt-4o"}},
		},
	)

	scope, err := buildAdminMonitoringScope(
		"user1", "ws1", inventory, []Policy{policy}, nil,
		[]GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: policy.ID}}, nil,
	)
	if err != nil {
		t.Fatalf("build scope: %v", err)
	}
	if !monitoringScopeContains(scope, "g1", "acc-1") {
		t.Fatalf("assigned group target missing from monitoring scope: %+v", scope)
	}
	if monitoringScopeContains(scope, "g2", "acc-1") || monitoringScopeContains(scope, "g2", "acc-2") {
		t.Fatalf("unassigned sibling group entered monitoring scope: %+v", scope)
	}
}

func TestBuildAdminMonitoringScope_MultiplierOnlyIsNotMonitoring(t *testing.T) {
	policy := monitoringScopeTestPolicy("price-policy")
	policy.StrategyMode = StrategyModeMultiplierOnly
	inventory := monitoringScopeTestInventory(adminInventoryGroup{
		group:    upstream.AdminGroupInfo{ID: "g1", Name: "group-1"},
		accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Models: "gpt-4o"}},
	})

	scope, err := buildAdminMonitoringScope(
		"user1", "ws1", inventory, []Policy{policy}, nil,
		[]GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: policy.ID}}, nil,
	)
	if err != nil {
		t.Fatalf("build scope: %v", err)
	}
	if len(scope.monitoredByGroup) != 0 {
		t.Fatalf("multiplier-only policy formed monitoring scope: %+v", scope)
	}
}

func TestBuildAdminMonitoringScope_DirectPolicyCoversAllCurrentMembershipGroups(t *testing.T) {
	policy := monitoringScopeTestPolicy("direct-policy")
	targetID := buildTargetID(string(upstream.PlatformSub2API), "ws1", "acc-1")
	inventory := monitoringScopeTestInventory(
		adminInventoryGroup{
			group:    upstream.AdminGroupInfo{ID: "g1", Name: "group-1"},
			accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Models: "gpt-4o"}},
		},
		adminInventoryGroup{
			group:    upstream.AdminGroupInfo{ID: "g2", Name: "group-2"},
			accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Models: "gpt-4o"}},
		},
	)

	scope, err := buildAdminMonitoringScope(
		"user1", "ws1", inventory, []Policy{policy},
		[]PolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", TargetID: targetID, PolicyID: policy.ID}}, nil,
		[]GroupTargetExclusion{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g2", TargetID: targetID}},
	)
	if err != nil {
		t.Fatalf("build scope: %v", err)
	}
	if !monitoringScopeContains(scope, "g1", "acc-1") || !monitoringScopeContains(scope, "g2", "acc-1") {
		t.Fatalf("direct policy did not cover every current membership group: %+v", scope)
	}
}

func TestBuildAdminMonitoringScope_Sub2APIManualPriorityIsHardExcluded(t *testing.T) {
	groupPolicy := monitoringScopeTestPolicy("group-policy")
	directPolicy := monitoringScopeTestPolicy("direct-policy")
	manualPriority := 1
	managedPriority := 10
	manualTargetID := buildTargetID(string(upstream.PlatformSub2API), "ws1", "manual")
	inventory := monitoringScopeTestInventory(adminInventoryGroup{
		group: upstream.AdminGroupInfo{ID: "g1", Name: "group-1"},
		accounts: []upstream.AdminGroupAccountInfo{
			{ID: "manual", Models: "gpt-4o", Priority: &manualPriority},
			{ID: "managed", Models: "gpt-4o", Priority: &managedPriority},
		},
	})

	scope, err := buildAdminMonitoringScope(
		"user1", "ws1", inventory, []Policy{groupPolicy, directPolicy},
		[]PolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", TargetID: manualTargetID, PolicyID: directPolicy.ID}},
		[]GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: groupPolicy.ID}},
		nil,
	)
	if err != nil {
		t.Fatalf("build scope: %v", err)
	}
	if monitoringScopeContains(scope, "g1", "manual") {
		t.Fatalf("Sub2API priority 1 target entered monitoring scope despite direct and group policies: %+v", scope)
	}
	if !monitoringScopeContains(scope, "g1", "managed") {
		t.Fatalf("Sub2API priority 10 target should remain monitored: %+v", scope)
	}
}

func TestBuildAdminMonitoringScope_RequiresApplicableEnabledModel(t *testing.T) {
	policy := monitoringScopeTestPolicy("group-policy")
	inventory := monitoringScopeTestInventory(adminInventoryGroup{
		group:    upstream.AdminGroupInfo{ID: "g1", Name: "group-1"},
		accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Models: "claude-3"}},
	})

	scope, err := buildAdminMonitoringScope(
		"user1", "ws1", inventory, []Policy{policy}, nil,
		[]GroupPolicyAssignment{{UserID: "user1", AdminAccountID: "ws1", AdminGroupID: "g1", PolicyID: policy.ID}}, nil,
	)
	if err != nil {
		t.Fatalf("build scope: %v", err)
	}
	if monitoringScopeContains(scope, "g1", "acc-1") {
		t.Fatalf("target without a matching enabled model entered monitoring scope: %+v", scope)
	}
}

func TestBuildAdminMonitoringScope_FiltersEveryInputByWorkspace(t *testing.T) {
	foreign := monitoringScopeTestPolicy("foreign-policy")
	foreign.UserID = "other-user"
	foreign.AdminAccountID = "other-workspace"
	inventory := monitoringScopeTestInventory(adminInventoryGroup{
		group:    upstream.AdminGroupInfo{ID: "g1", Name: "group-1"},
		accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Models: "gpt-4o"}},
	})

	scope, err := buildAdminMonitoringScope(
		"user1", "ws1", inventory, []Policy{foreign},
		[]PolicyAssignment{{UserID: "other-user", AdminAccountID: "other-workspace", TargetID: "sub2api:ws1:acc-1", PolicyID: foreign.ID}},
		[]GroupPolicyAssignment{{UserID: "other-user", AdminAccountID: "other-workspace", AdminGroupID: "g1", PolicyID: foreign.ID}},
		[]GroupTargetExclusion{{UserID: "other-user", AdminAccountID: "other-workspace", AdminGroupID: "g1", TargetID: "sub2api:ws1:acc-1"}},
	)
	if err != nil {
		t.Fatalf("build scope: %v", err)
	}
	if len(scope.monitoredByGroup) != 0 {
		t.Fatalf("foreign workspace configuration entered monitoring scope: %+v", scope)
	}
}

func TestLoadAdminMonitoringScope_FailsWhenAnyConfigurationReadIsIncomplete(t *testing.T) {
	inventory := monitoringScopeTestInventory(adminInventoryGroup{
		group:    upstream.AdminGroupInfo{ID: "g1", Name: "group-1"},
		accounts: []upstream.AdminGroupAccountInfo{{ID: "acc-1", Models: "gpt-4o"}},
	})
	tests := []struct {
		name   string
		inject func(*fakeRepository)
	}{
		{name: "policies", inject: func(repo *fakeRepository) { repo.listPoliciesErr = errors.New("policies unavailable") }},
		{name: "target assignments", inject: func(repo *fakeRepository) { repo.listAssignmentsErr = errors.New("target assignments unavailable") }},
		{name: "group assignments", inject: func(repo *fakeRepository) { repo.listGroupAssignmentsErr = errors.New("group assignments unavailable") }},
		{name: "group exclusions", inject: func(repo *fakeRepository) { repo.listGroupExclusionsErr = errors.New("group exclusions unavailable") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepository()
			tt.inject(repo)
			service := &Service{repo: repo}

			scope, err := service.loadAdminMonitoringScope(context.Background(), "user1", "ws1", inventory)
			if err == nil || scope.complete {
				t.Fatalf("incomplete %s read produced a complete scope: scope=%+v err=%v", tt.name, scope, err)
			}
		})
	}
}
