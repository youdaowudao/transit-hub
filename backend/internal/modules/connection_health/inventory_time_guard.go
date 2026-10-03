package connection_health

import "transithub/backend/internal/modules/upstream"

// Apply the clock guard only after the complete group/account read. This also
// covers a bad Date on a page with no accounts and invalidates earlier groups.
func guardAdminInventoryTimes(inventory *adminWorkspaceInventory) {
	if inventory == nil || inventory.session.Platform != upstream.PlatformSub2API {
		return
	}
	responses := make([]upstream.InventoryResponseTime, 0)
	seen := make(map[*upstream.InventoryTimeEvidence]struct{})
	completeEvidence := true
	for _, group := range inventory.groups {
		evidence := group.group.InventoryTimeEvidence
		if evidence == nil {
			// An account-only timestamp cannot prove the group-list response
			// or an empty member page. Production reads always carry this set.
			completeEvidence = false
			continue
		}
		if _, exists := seen[evidence]; exists {
			continue
		}
		seen[evidence] = struct{}{}
		responses = append(responses, evidence.Responses()...)
	}
	if completeEvidence && upstream.Sub2APIInventoryTimeTrusted(inventory.session.BaseURL, responses) {
		return
	}
	for groupIndex := range inventory.groups {
		for accountIndex := range inventory.groups[groupIndex].accounts {
			upstream.InvalidateSub2APIAccountTimes(&inventory.groups[groupIndex].accounts[accountIndex])
		}
	}
}
