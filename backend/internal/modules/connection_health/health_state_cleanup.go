package connection_health

import (
	"context"
	"log"
	"sort"

	"transithub/backend/internal/modules/upstream"
)

type schedulerScan struct {
	policies           []Policy
	assignments        []PolicyAssignment
	groupAssignments   []GroupPolicyAssignment
	exclusions         []GroupTargetExclusion
	inventoryCache     adminInventoryCache
	priorityStates     []PrioritySyncState
	targetActionStates []TargetActionState
	rules              workspaceRuleSnapshot
}

func (s *Service) finishSchedulerScan(ctx context.Context, scan *schedulerScan) {
	if scan == nil {
		return
	}
	ctx = context.WithValue(ctx, workspaceRuleSnapshotKey{}, scan.rules)
	s.cleanupUnmonitoredHealthStates(ctx, scan)
}

func (s *Service) cleanupUnmonitoredHealthStates(ctx context.Context, scan *schedulerScan) {
	workspaces, err := s.repo.ListSub2APIHealthWorkspaces(ctx)
	if err != nil {
		log.Printf("[connection-health] list health cleanup workspaces failed: %v", err)
		return
	}
	assignedWorkspaces := make(map[string]bool)
	probePolicies := make(map[string]Policy)
	for _, policy := range scan.policies {
		if hasEnabledModelTarget([]Policy{policy}) {
			probePolicies[policy.ID] = policy
		}
	}
	for _, assignment := range scan.assignments {
		if policy, exists := probePolicies[assignment.PolicyID]; exists && policy.UserID == assignment.UserID && policy.AdminAccountID == assignment.AdminAccountID {
			assignedWorkspaces[assignment.UserID+"|"+assignment.AdminAccountID] = true
		}
	}
	for _, assignment := range scan.groupAssignments {
		if policy, exists := probePolicies[assignment.PolicyID]; exists && policy.UserID == assignment.UserID && policy.AdminAccountID == assignment.AdminAccountID {
			assignedWorkspaces[assignment.UserID+"|"+assignment.AdminAccountID] = true
		}
	}
	initialCheckpoints := make(map[string]bool)
	for _, checkpoint := range scan.priorityStates {
		initialCheckpoints[checkpoint.UserID+"|"+checkpoint.AdminAccountID+"|"+checkpoint.TargetID] = true
	}
	for _, checkpoint := range scan.targetActionStates {
		initialCheckpoints[checkpoint.UserID+"|"+checkpoint.AdminAccountID+"|"+checkpoint.TargetID] = true
	}
	for _, workspace := range workspaces {
		if ctx.Err() != nil {
			return
		}
		key := workspace.userID + "|" + workspace.adminAccountID
		var scope adminMonitoringScope
		if assignedWorkspaces[key] {
			cached, exists := scan.inventoryCache[key]
			if !exists || cached.err != nil || cached.inventory == nil || cached.inventory.session.Platform != upstream.PlatformSub2API || !adminInventoryComplete(*cached.inventory) {
				continue
			}
			scope, err = buildAdminMonitoringScope(workspace.userID, workspace.adminAccountID, *cached.inventory, scan.policies, scan.assignments, scan.groupAssignments, scan.exclusions)
			if err != nil {
				continue
			}
		}
		captured, captureErr := s.capturedWorkspaceRules(ctx, workspace.userID, workspace.adminAccountID, scan.policies)
		if captureErr != nil {
			continue
		}
		states, statesErr := s.repo.ListStatesByWorkspace(ctx, workspace.userID, workspace.adminAccountID)
		if statesErr != nil {
			log.Printf("[connection-health] list health cleanup states failed workspace=%s err=%v", key, statesErr)
			continue
		}
		targetSet := make(map[string]bool)
		for _, state := range states {
			parsed, valid := parseTargetID(state.ConnectionID)
			if valid && parsed.platform == string(upstream.PlatformSub2API) && parsed.adminAccountID == workspace.adminAccountID {
				targetSet[state.ConnectionID] = true
			}
		}
		targets := make([]string, 0, len(targetSet))
		for targetID := range targetSet {
			targets = append(targets, targetID)
		}
		sort.Strings(targets)
		for _, targetID := range targets {
			if initialCheckpoints[key+"|"+targetID] || adminMonitoringScopeContainsTarget(scope, targetID) || s.scheduledProbeInFlight(workspace.userID, targetID) {
				continue
			}
			leasedCtx, release, acquired, leaseErr := s.acquireActionTargetLease(ctx, targetID, false)
			if leaseErr != nil || !acquired {
				continue
			}
			func() {
				defer release()
				deleted, deleteErr := s.repo.DeleteUnmonitoredStates(leasedCtx, workspace.userID, workspace.adminAccountID, targetID, captured.ConfigGeneration)
				if deleteErr != nil {
					log.Printf("[connection-health] clear unmonitored health failed workspace=%s target_id=%s err=%v", key, targetID, deleteErr)
					return
				}
				log.Printf("[connection-health] cleared unmonitored health workspace=%s target_id=%s deleted=%d", key, targetID, deleted)
			}()
		}
	}
}
