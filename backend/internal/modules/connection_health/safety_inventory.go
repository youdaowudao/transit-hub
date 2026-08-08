package connection_health

import (
	"context"
	"sort"
	"strings"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func strictSub2APIStatus(status string) (active bool, known bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "enabled", "1":
		return true, true
	case "inactive", "disabled", "2":
		return false, true
	default:
		return false, false
	}
}

func (s *Service) persistSafetyInventorySnapshot(ctx context.Context, userID, workspaceID string, inventory *adminWorkspaceInventory, generation int64, expiresAt time.Time) error {
	if s.safetyRepo == nil || inventory == nil || inventory.session.Platform != upstream.PlatformSub2API {
		return nil
	}
	type aggregate struct {
		account             SafetyInventoryAccount
		models              map[string]struct{}
		groups              map[string]struct{}
		statusSeen          bool
		schedSeen           bool
		schedMissing        bool
		schedSplit          bool
		capabilitySeen      bool
		capabilityMissing   bool
		capabilitySplit     bool
		capabilitySignature string
	}
	byAccount := make(map[string]*aggregate)
	complete := inventory.complete
	for _, group := range inventory.groups {
		if group.err != nil {
			complete = false
			continue
		}
		for _, upstreamAccount := range group.accounts {
			entry := byAccount[upstreamAccount.ID]
			if entry == nil {
				entry = &aggregate{
					account: SafetyInventoryAccount{
						AccountID: upstreamAccount.ID,
						TargetID:  buildTargetID(string(inventory.session.Platform), workspaceID, upstreamAccount.ID),
					},
					models: make(map[string]struct{}), groups: make(map[string]struct{}),
				}
				byAccount[upstreamAccount.ID] = entry
			}
			active, statusKnown := strictSub2APIStatus(upstreamAccount.Status)
			if entry.statusSeen && (entry.account.StatusKnown != statusKnown || (statusKnown && entry.account.Active != active)) {
				entry.account.StatusKnown = false
			} else if !entry.statusSeen {
				entry.account.Active = active
				entry.account.StatusKnown = statusKnown
				entry.statusSeen = true
			}
			if upstreamAccount.Schedulable != nil {
				if entry.schedSeen && entry.account.Schedulable != *upstreamAccount.Schedulable {
					entry.schedSplit = true
				} else if !entry.schedSeen {
					entry.account.Schedulable = *upstreamAccount.Schedulable
					entry.schedSeen = true
				}
			} else {
				entry.schedMissing = true
			}
			models := uniqueSortedSafetyValues(splitModelList(upstreamAccount.Models))
			if len(models) > 0 {
				signature := strings.Join(models, "\x00")
				if entry.capabilitySeen && entry.capabilitySignature != signature {
					entry.capabilitySplit = true
				} else if !entry.capabilitySeen {
					entry.capabilitySignature = signature
					entry.capabilitySeen = true
				}
				for _, model := range models {
					entry.models[model] = struct{}{}
				}
			} else {
				entry.capabilityMissing = true
			}
			entry.groups[group.group.ID] = struct{}{}
		}
	}
	states, err := s.repo.ListStatesByWorkspace(ctx, userID, workspaceID)
	if err != nil {
		return err
	}
	statesByTarget := make(map[string][]ConnectionHealthState)
	for _, state := range states {
		statesByTarget[state.ConnectionID] = append(statesByTarget[state.ConnectionID], state)
	}
	accounts := make([]SafetyInventoryAccount, 0, len(byAccount))
	for _, entry := range byAccount {
		entry.account.MembershipKnown = complete
		entry.account.SchedulableKnown = entry.schedSeen && !entry.schedMissing && !entry.schedSplit
		entry.account.CapabilityKnown = entry.capabilitySeen && !entry.capabilityMissing && !entry.capabilitySplit
		for model := range entry.models {
			entry.account.Models = append(entry.account.Models, model)
		}
		for groupID := range entry.groups {
			entry.account.GroupIDs = append(entry.account.GroupIDs, groupID)
		}
		sort.Strings(entry.account.Models)
		sort.Strings(entry.account.GroupIDs)
		for _, state := range statesByTarget[entry.account.TargetID] {
			if state.LastSuccessAt != nil && state.LastSuccessAt.After(entry.account.LastSuccessAt) {
				entry.account.LastSuccessAt = *state.LastSuccessAt
			}
			// Only a completed, readback-confirmed incident action counts as a
			// confirmed failure for survivor stickiness. First observations and
			// confirmation attempts must not reshuffle the fallback account.
			if state.State == StateSuspended {
				entry.account.ConfirmedFailureModels++
			}
		}
		accounts = append(accounts, entry.account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].AccountID < accounts[j].AccountID })
	return s.safetyRepo.PersistSafetyInventorySnapshot(ctx, SafetyInventorySnapshot{
		UserID: userID, WorkspaceID: workspaceID, Generation: generation,
		Complete: complete, ExpiresAt: expiresAt, Accounts: accounts,
	})
}
