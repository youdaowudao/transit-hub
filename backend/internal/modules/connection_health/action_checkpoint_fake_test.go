package connection_health

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

func (f *fakeRepository) ClearDeletedAccountCheckpoint(ctx context.Context, scope RemoteActionScope, snapshotStartedAt, now time.Time) (bool, error) {
	f.actionMu.Lock()
	defer f.actionMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if actionCheckpointBecameVisible(ctx, scope) {
		return false, nil
	}
	id, valid := scopedActionAccountID(scope.TargetID, scope.AdminAccountID)
	if !valid {
		return false, nil
	}
	aliases := make(map[string]RemoteActionCheckpoints)
	for _, state := range f.priorityStates {
		aliasID, valid := scopedActionAccountID(state.TargetID, scope.AdminAccountID)
		if state.UserID == scope.UserID && state.AdminAccountID == scope.AdminAccountID && valid && aliasID == id {
			pair := aliases[state.TargetID]
			copy := state
			pair.Priority = &copy
			aliases[state.TargetID] = pair
		}
	}
	for _, state := range f.targetActionStates {
		aliasID, valid := scopedActionAccountID(state.TargetID, scope.AdminAccountID)
		if state.UserID == scope.UserID && state.AdminAccountID == scope.AdminAccountID && valid && aliasID == id {
			pair := aliases[state.TargetID]
			copy := state
			pair.Target = &copy
			aliases[state.TargetID] = pair
		}
	}
	before := make(map[string]RemoteActionCheckpoints, len(aliases))
	for targetID, pair := range aliases {
		before[targetID] = pair
	}
	cleared := clearDeletedAccountCheckpointAliases(aliases, scope, snapshotStartedAt, now)
	if cleared {
		for targetID, pair := range aliases {
			f.writeActionPair(RemoteActionScope{scope.UserID, scope.AdminAccountID, targetID}, before[targetID], pair)
		}
	}
	return cleared, nil
}

func (f *fakeRepository) AcquireActionLease(ctx context.Context, key string, wait bool) (*RuntimeLeaseHandle, bool, error) {
	if strings.HasPrefix(key, "connection-health:target:") && !wait && f.targetLeaseBlocked {
		return nil, false, nil
	}
	for {
		f.actionMu.Lock()
		if f.actionLeases == nil {
			f.actionLeases = make(map[string]*RuntimeLeaseHandle)
		}
		if f.actionLeases[key] == nil {
			owner, err := newID()
			if err != nil {
				f.actionMu.Unlock()
				return nil, false, err
			}
			leaseParent := ctx
			if detached, _ := ctx.Value(detachedActionLeaseKey{}).(bool); detached {
				leaseParent = context.WithoutCancel(ctx)
			}
			leaseCtx, cancel := context.WithCancel(leaseParent)
			lost := make(chan struct{})
			var once sync.Once
			lose := func() { once.Do(func() { close(lost); cancel() }) }
			handle := &RuntimeLeaseHandle{Key: key, OwnerID: owner, Context: leaseCtx, Lost: lost, lose: lose}
			handle.release = func() {
				lose()
				f.actionMu.Lock()
				defer f.actionMu.Unlock()
				if f.actionLeases[key] == handle {
					delete(f.actionLeases, key)
				}
			}
			f.actionLeases[key] = handle
			if strings.HasPrefix(key, "connection-health:priority-sync:") {
				rest := strings.TrimPrefix(key, "connection-health:priority-sync:")
				parts := strings.SplitN(rest, ":", 2)
				if len(parts) == 2 {
					if size, err := strconv.Atoi(parts[0]); err == nil && size <= len(parts[1]) {
						f.priorityLeaseMu.Lock()
						f.priorityLeaseCount[parts[1][:size]+"|"+parts[1][size:]]++
						f.priorityLeaseMu.Unlock()
					}
				}
			}
			f.actionMu.Unlock()
			return handle, true, nil
		}
		f.actionMu.Unlock()
		if !wait {
			return nil, false, nil
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false, ctx.Err()
		case <-timer.C:
		}
	}
}

// Expiring without delivering Lost models a paused worker whose notification
// goroutine has not run. The next permit must still reject that original owner.
func (f *fakeRepository) expireActionLeaseForTest(owner string, notify bool) {
	f.actionMu.Lock()
	defer f.actionMu.Unlock()
	for key, handle := range f.actionLeases {
		if handle.OwnerID == owner {
			delete(f.actionLeases, key)
			if notify {
				handle.lose()
			}
		}
	}
}

func (f *fakeRepository) actionPair(scope RemoteActionScope) RemoteActionCheckpoints {
	key := scope.UserID + "|" + scope.AdminAccountID + "|" + scope.TargetID
	pair := RemoteActionCheckpoints{}
	if state, exists := f.priorityStates[key]; exists {
		pair.Priority = &state
	}
	if state, exists := f.targetActionStates[key]; exists {
		pair.Target = &state
	}
	return pair
}

func (f *fakeRepository) writeActionPair(scope RemoteActionScope, before, after RemoteActionCheckpoints) {
	key := scope.UserID + "|" + scope.AdminAccountID + "|" + scope.TargetID
	now := time.Now()
	if f.priorityStates == nil {
		f.priorityStates = make(map[string]PrioritySyncState)
	}
	if f.targetActionStates == nil {
		f.targetActionStates = make(map[string]TargetActionState)
	}
	if after.Priority == nil && before.Priority != nil {
		delete(f.priorityStates, key)
	}
	if after.Target == nil && before.Target != nil {
		delete(f.targetActionStates, key)
	}
	if !reflect.DeepEqual(before.Priority, after.Priority) && after.Priority != nil {
		after.Priority.UpdatedAt = now
		f.priorityStates[key] = *after.Priority
	}
	if !reflect.DeepEqual(before.Target, after.Target) && after.Target != nil {
		after.Target.UpdatedAt = now
		f.targetActionStates[key] = *after.Target
	}
}

func cloneActionPair(pair RemoteActionCheckpoints) RemoteActionCheckpoints {
	copy := RemoteActionCheckpoints{}
	if pair.Priority != nil {
		state := *pair.Priority
		copy.Priority = &state
	}
	if pair.Target != nil {
		state := *pair.Target
		copy.Target = &state
	}
	return copy
}

func (f *fakeRepository) leaseValidForClaim(claim RemoteActionClaim) bool {
	handle := f.actionLeases[claim.LeaseKey]
	valid := handle != nil && handle.OwnerID == claim.OwnerID && handle.Context.Err() == nil
	if claim.MutationLeaseKey != "" {
		m := f.actionLeases[claim.MutationLeaseKey]
		valid = valid && m != nil && m.OwnerID == claim.MutationOwnerID && m.Context.Err() == nil
	}
	if claim.WorkspaceLeaseKey != "" {
		w := f.actionLeases[claim.WorkspaceLeaseKey]
		valid = valid && w != nil && w.OwnerID == claim.WorkspaceOwnerID && w.Context.Err() == nil
	}
	return valid
}

func (f *fakeRepository) MutateIdlePriorityCheckpoint(ctx context.Context, change IdlePriorityCheckpointMutation) (RemoteActionCheckpoints, error) {
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	f.actionMu.Lock()
	defer f.actionMu.Unlock()
	if err := ctx.Err(); err != nil {
		return RemoteActionCheckpoints{}, err
	}
	if change.ConfigGeneration != nil && f.fakeConfigGenerationKnown(change.UserID, change.AdminAccountID) && f.fakeWorkspaceHealthSettings(change.UserID, change.AdminAccountID).ConfigGeneration != *change.ConfigGeneration {
		return RemoteActionCheckpoints{}, ErrRemoteActionEvidenceChanged
	}
	before := f.actionPair(change.RemoteActionScope)
	pair := cloneActionPair(before)
	if err := mutateIdlePriorityCheckpoint(&pair, change); err != nil {
		return pair, err
	}
	f.writeActionPair(change.RemoteActionScope, before, pair)
	return pair, nil
}

func (f *fakeRepository) ClaimRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	// This mutex is the fake workspace transaction shared with configuration saves.
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	f.actionMu.Lock()
	defer f.actionMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !f.leaseValidForClaim(claim) {
		return false, ErrRemoteActionLeaseLost
	}
	if claim.Guard.ConfigGeneration != nil && f.fakeConfigGenerationKnown(claim.UserID, claim.AdminAccountID) && f.fakeWorkspaceHealthSettings(claim.UserID, claim.AdminAccountID).ConfigGeneration != *claim.Guard.ConfigGeneration {
		return false, ErrRemoteActionEvidenceChanged
	}
	if claim.Guard.ExpectedPriorityGeneration != nil {
		if f.priorityGenerationErr != nil {
			return false, f.priorityGenerationErr
		}
		f.priorityWorkspaceMu.Lock()
		workspace := f.priorityWorkspaces[claim.UserID+"|"+claim.AdminAccountID]
		f.priorityWorkspaceMu.Unlock()
		if workspace.PendingSignature != *claim.Guard.ExpectedPriorityGeneration {
			return false, ErrRemoteActionEvidenceChanged
		}
	}
	if claim.Guard.Required {
		configs := []GroupTestConfig{}
		for _, config := range f.testConfigurations {
			if config.UserID == claim.UserID && config.AdminAccountID == claim.AdminAccountID {
				configs = append(configs, config)
			}
		}
		current := ResolveGroupTestConfiguration(claim.Guard.Memberships, claim.Guard.InventoryComplete && f.testConfigurationErr == nil, configs)
		if current.Protocol != claim.Guard.Configuration.Protocol || current.ProbeTimeoutSeconds != claim.Guard.Configuration.ProbeTimeoutSeconds || (current.Status != "default" && current.Status != "inherited") {
			return false, ErrRemoteActionEvidenceChanged
		}
		if len(claim.Guard.Models) == 0 {
			return false, ErrRemoteActionEvidenceChanged
		}
		for _, model := range claim.Guard.Models {
			state, exists := f.states[claim.TargetID][model]
			if !exists || !healthEvidenceMatches(state, current.Protocol) {
				return false, ErrRemoteActionEvidenceChanged
			}
			for _, expected := range claim.Guard.ExpectedStates {
				if expected.ModelName == model && !sameRemoteHealthDecisionState(state, expected) {
					return false, ErrRemoteActionEvidenceChanged
				}
			}
		}
	}
	before := f.actionPair(claim.RemoteActionScope)
	pair := cloneActionPair(before)
	claimed, err := claimRemoteAction(&pair, claim)
	if err == nil {
		f.writeActionPair(claim.RemoteActionScope, before, pair)
	}
	return claimed, err
}

func (f *fakeRepository) PermitRemoteAction(ctx context.Context, claim RemoteActionClaim) (bool, error) {
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	f.actionMu.Lock()
	defer f.actionMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !f.leaseValidForClaim(claim) {
		return false, ErrRemoteActionLeaseLost
	}
	if claim.Guard.ConfigGeneration != nil && f.fakeConfigGenerationKnown(claim.UserID, claim.AdminAccountID) && f.fakeWorkspaceHealthSettings(claim.UserID, claim.AdminAccountID).ConfigGeneration != *claim.Guard.ConfigGeneration {
		return false, ErrRemoteActionEvidenceChanged
	}

	before := f.actionPair(claim.RemoteActionScope)
	pair := cloneActionPair(before)
	if !permitRemoteAction(&pair, claim) {
		return false, ErrRemoteActionPending
	}
	f.writeActionPair(claim.RemoteActionScope, before, pair)
	return true, nil
}

func (f *fakeRepository) RecordRemoteActionReceipt(_ context.Context, claim RemoteActionClaim, phase RemoteDispatchPhase) error {
	f.actionMu.Lock()
	defer f.actionMu.Unlock()
	before := f.actionPair(claim.RemoteActionScope)
	pair := cloneActionPair(before)
	if !receiptRemoteAction(&pair, claim, phase, time.Now()) {
		return ErrRemoteActionPending
	}
	f.writeActionPair(claim.RemoteActionScope, before, pair)
	return nil
}

func (f *fakeRepository) ReconcileRemoteAction(_ context.Context, observation RemoteActionObservation) (RemoteActionCheckpoints, error) {
	f.actionMu.Lock()
	defer f.actionMu.Unlock()
	before := f.actionPair(observation.RemoteActionScope)
	pair := cloneActionPair(before)
	reconcileRemoteAction(&pair, observation, func(owner string) bool {
		for _, lease := range f.actionLeases {
			if lease.OwnerID == owner && lease.Context.Err() == nil {
				return true
			}
		}
		return false
	})
	f.writeActionPair(observation.RemoteActionScope, before, pair)
	return pair, nil
}
