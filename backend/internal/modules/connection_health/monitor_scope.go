package connection_health

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"transithub/backend/internal/modules/upstream"
)

type adminMonitoringScope struct {
	monitoredByGroup map[string]map[string]struct{}
	complete         bool
	fingerprint      string
}

func adminMonitoringScopeContainsTarget(scope adminMonitoringScope, targetID string) bool {
	if !scope.complete {
		return false
	}
	for _, monitoredTargets := range scope.monitoredByGroup {
		if _, monitored := monitoredTargets[targetID]; monitored {
			return true
		}
	}
	return false
}

func accountHardExcludedFromAdminMonitoring(platform string, account upstream.AdminGroupAccountInfo) bool {
	if platform != string(upstream.PlatformSub2API) || account.Priority == nil {
		return false
	}
	return *account.Priority >= 1 && *account.Priority <= 9
}

func firstIncompleteAdminInventoryGroup(inventory adminWorkspaceInventory) (string, string, bool) {
	for _, groupInventory := range inventory.groups {
		if groupInventory.err != nil {
			return groupInventory.group.ID, groupInventory.group.Name, true
		}
	}
	return "", "", false
}

func buildAdminMonitoringScope(
	userID string,
	adminAccountID string,
	inventory adminWorkspaceInventory,
	policies []Policy,
	targetAssignments []PolicyAssignment,
	groupAssignments []GroupPolicyAssignment,
	exclusions []GroupTargetExclusion,
) (adminMonitoringScope, error) {
	if !adminInventoryComplete(inventory) {
		return adminMonitoringScope{}, fmt.Errorf("admin inventory is incomplete")
	}

	workspacePolicies := make([]Policy, 0, len(policies))
	for _, policy := range policies {
		if policy.UserID == userID && policy.AdminAccountID == adminAccountID && policy.Enabled {
			workspacePolicies = append(workspacePolicies, policy)
		}
	}
	workspaceTargetAssignments := make([]PolicyAssignment, 0, len(targetAssignments))
	for _, assignment := range targetAssignments {
		if assignment.UserID == userID && assignment.AdminAccountID == adminAccountID {
			workspaceTargetAssignments = append(workspaceTargetAssignments, assignment)
		}
	}
	workspaceGroupAssignments := make([]GroupPolicyAssignment, 0, len(groupAssignments))
	for _, assignment := range groupAssignments {
		if assignment.UserID == userID && assignment.AdminAccountID == adminAccountID {
			workspaceGroupAssignments = append(workspaceGroupAssignments, assignment)
		}
	}
	workspaceExclusions := make([]GroupTargetExclusion, 0, len(exclusions))
	for _, exclusion := range exclusions {
		if exclusion.UserID == userID && exclusion.AdminAccountID == adminAccountID {
			workspaceExclusions = append(workspaceExclusions, exclusion)
		}
	}

	workspaceKey := userID + "|" + adminAccountID
	directByTarget := assignedEnabledPoliciesByTarget(workspacePolicies, workspaceTargetAssignments)[workspaceKey]
	inheritedByGroup := assignedEnabledPoliciesByGroup(workspacePolicies, workspaceGroupAssignments)[workspaceKey]
	excludedByGroup := groupTargetExclusionIndex(workspaceExclusions)[workspaceKey]
	platform := string(inventory.session.Platform)
	monitoredByGroup := make(map[string]map[string]struct{})
	fingerprintEntries := make([]string, 0)

	for _, groupInventory := range inventory.groups {
		if strings.TrimSpace(groupInventory.group.ID) == "" {
			return adminMonitoringScope{}, fmt.Errorf("admin inventory contains a group without id")
		}
		for _, account := range groupInventory.accounts {
			if strings.TrimSpace(account.ID) == "" {
				return adminMonitoringScope{}, fmt.Errorf("admin inventory group %s contains an account without id", groupInventory.group.ID)
			}
			if accountHardExcludedFromAdminMonitoring(platform, account) {
				continue
			}
			targetID := buildTargetID(platform, adminAccountID, account.ID)
			directPolicies := directByTarget[targetID]
			inheritedPolicies := inheritedByGroup[groupInventory.group.ID]
			if !hasEnabledTargetPolicy(directPolicies) && excludedByGroup[groupInventory.group.ID][targetID] {
				inheritedPolicies = nil
			}
			effectivePolicies := effectivePoliciesForTarget(directPolicies, inheritedPolicies)
			if len(candidateModelSpecsForPlatform(splitModelList(account.Models), effectivePolicies, platform)) == 0 {
				continue
			}
			if monitoredByGroup[groupInventory.group.ID] == nil {
				monitoredByGroup[groupInventory.group.ID] = make(map[string]struct{})
			}
			monitoredByGroup[groupInventory.group.ID][targetID] = struct{}{}

			source := "group"
			if hasEnabledTargetPolicy(directPolicies) {
				source = "target"
			}
			policyIDs := make([]string, 0, len(effectivePolicies))
			for _, policy := range effectivePolicies {
				policyIDs = append(policyIDs, policy.ID)
			}
			sort.Strings(policyIDs)
			fingerprintEntries = append(fingerprintEntries, strings.Join([]string{
				groupInventory.group.ID, targetID, source, strings.Join(policyIDs, ","),
			}, "\x00"))
		}
	}

	sort.Strings(fingerprintEntries)
	return adminMonitoringScope{
		monitoredByGroup: monitoredByGroup,
		complete:         true,
		fingerprint:      strings.Join(fingerprintEntries, "\x01"),
	}, nil
}

func (s *Service) loadAdminMonitoringScope(
	ctx context.Context,
	userID string,
	adminAccountID string,
	inventory adminWorkspaceInventory,
) (adminMonitoringScope, error) {
	policies, err := s.repo.ListPolicies(ctx, userID, adminAccountID)
	if err != nil {
		return adminMonitoringScope{}, err
	}
	targetAssignments, err := s.repo.ListPolicyAssignmentsByWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		return adminMonitoringScope{}, err
	}
	groupAssignments, err := s.repo.ListGroupPolicyAssignmentsByWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		return adminMonitoringScope{}, err
	}
	exclusions, err := s.repo.ListGroupTargetExclusionsByWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		return adminMonitoringScope{}, err
	}
	return buildAdminMonitoringScope(
		userID, adminAccountID, inventory, policies, targetAssignments, groupAssignments, exclusions,
	)
}
