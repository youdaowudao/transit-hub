package connection_health

import (
	"context"
	"errors"
	"reflect"
	"time"
)

type RemoteDispatchPhase string

const (
	DispatchPrepared          RemoteDispatchPhase = "prepared"
	DispatchSending           RemoteDispatchPhase = "sending"
	DispatchUncertain         RemoteDispatchPhase = "uncertain"
	DispatchNotSent           RemoteDispatchPhase = "not_sent"
	DispatchConfirmedApplied  RemoteDispatchPhase = "confirmed_applied"
	DispatchConfirmedRejected RemoteDispatchPhase = "confirmed_rejected"
)

const (
	ActionKindPriority               = "priority"
	ActionKindTarget                 = "target"
	RemoteActionAwaitingConfirmation = "remote_action_awaiting_confirmation"
	RemoteActionTargetNotVisible     = "remote_action_target_not_visible"
)

var ErrRemoteActionPending = errors.New("remote action requires confirmation")
var ErrRemoteActionLeaseLost = errors.New("remote action lease lost")
var ErrRemoteActionEvidenceChanged = errors.New("remote action evidence changed")

type RemoteActionScope struct {
	UserID         string
	AdminAccountID string
	TargetID       string
}

type RemoteActionClaim struct {
	RemoteActionScope
	Kind       string
	DispatchID string
	OwnerID    string
	LeaseKey   string
	Priority   *PrioritySyncState
	Target     *TargetActionState
	Guard      RemoteActionHealthGuard
}

// Only health-driven decisions need protocol evidence. Exiting management and
// the empty-group safeguard retain their independent, existing conditions.
type RemoteActionHealthGuard struct {
	Required                   bool
	Memberships                []TestConfigurationSource
	InventoryComplete          bool
	Configuration              EffectiveTestConfiguration
	Models                     []string
	ExpectedStates             []ConnectionHealthState
	ExpectedPriorityGeneration *string
}

type RemoteActionCheckpoints struct {
	Priority *PrioritySyncState
	Target   *TargetActionState
}

type RemoteActionObservation struct {
	RemoteActionScope
	InventoryComplete bool
	Visible           bool
	SnapshotStartedAt time.Time
	Priority          *int
	Status            string
	Weight            *int
}

type actionCheckpointRepository interface {
	ClaimRemoteAction(context.Context, RemoteActionClaim) (bool, error)
	PermitRemoteAction(context.Context, RemoteActionClaim) (bool, error)
	RecordRemoteActionReceipt(context.Context, RemoteActionClaim, RemoteDispatchPhase) error
	ReconcileRemoteAction(context.Context, RemoteActionObservation) (RemoteActionCheckpoints, error)
}

func sameRemoteHealthDecisionState(current, expected ConnectionHealthState) bool {
	return current.State == expected.State && current.CurrentWeight == expected.CurrentWeight &&
		current.ConsecutiveFailures == expected.ConsecutiveFailures && current.ConsecutiveSuccesses == expected.ConsecutiveSuccesses &&
		current.HealthEvidenceStatus == expected.HealthEvidenceStatus && reflect.DeepEqual(current.HealthEvidenceProtocol, expected.HealthEvidenceProtocol) &&
		reflect.DeepEqual(current.LastSuccessProtocol, expected.LastSuccessProtocol) && reflect.DeepEqual(current.LastSuccessLatencyMs, expected.LastSuccessLatencyMs)
}

func priorityActionPending(state *PrioritySyncState) bool {
	return state != nil && (state.PendingPriority != nil || state.PendingDispatchID != "")
}

func targetActionPending(state *TargetActionState) bool {
	return state != nil && (state.PendingStatus != "" || state.PendingWeight != nil || state.PendingDispatchID != "")
}

func (pair RemoteActionCheckpoints) pendingCount() int {
	count := 0
	if priorityActionPending(pair.Priority) {
		count++
	}
	if targetActionPending(pair.Target) {
		count++
	}
	return count
}

func (pair RemoteActionCheckpoints) dispatch(kind string) (string, string, RemoteDispatchPhase, bool) {
	if kind == ActionKindPriority && priorityActionPending(pair.Priority) {
		return pair.Priority.PendingDispatchID, pair.Priority.PendingOwnerID, pair.Priority.PendingDispatchPhase, true
	}
	if kind == ActionKindTarget && targetActionPending(pair.Target) {
		return pair.Target.PendingDispatchID, pair.Target.PendingOwnerID, pair.Target.PendingDispatchPhase, true
	}
	return "", "", "", false
}

func claimRemoteAction(pair *RemoteActionCheckpoints, claim RemoteActionClaim) (bool, error) {
	if claim.DispatchID == "" || claim.OwnerID == "" || claim.LeaseKey == "" {
		return false, ErrRemoteActionLeaseLost
	}
	if pair.pendingCount() != 0 {
		id, owner, phase, pending := pair.dispatch(claim.Kind)
		if pair.pendingCount() == 1 && pending && id == claim.DispatchID && owner == claim.OwnerID && phase == DispatchPrepared && actionClaimIntentionMatches(*pair, claim) {
			return true, nil
		}
		return false, ErrRemoteActionPending
	}
	switch claim.Kind {
	case ActionKindPriority:
		if claim.Priority == nil || claim.Priority.PendingPriority == nil {
			return false, errors.New("missing priority intention")
		}
		state := *claim.Priority
		if state.UserID != claim.UserID || state.AdminAccountID != claim.AdminAccountID || state.TargetID != claim.TargetID {
			return false, ErrRemoteActionEvidenceChanged
		}
		if pair.Priority != nil && (pair.Priority.Conflict || pair.Priority.LastAppliedPriority != state.LastAppliedPriority || pair.Priority.OriginalPriority != state.OriginalPriority) {
			return false, ErrRemoteActionEvidenceChanged
		}
		state.PendingDispatchID, state.PendingOwnerID, state.PendingDispatchPhase = claim.DispatchID, claim.OwnerID, DispatchPrepared
		pair.Priority = &state
	case ActionKindTarget:
		if claim.Target == nil || claim.Target.PendingStatus == "" {
			return false, errors.New("missing target intention")
		}
		state := *claim.Target
		if state.UserID != claim.UserID || state.AdminAccountID != claim.AdminAccountID || state.TargetID != claim.TargetID {
			return false, ErrRemoteActionEvidenceChanged
		}
		if pair.Target != nil && (pair.Target.Conflict || pair.Target.LastAppliedStatus != state.LastAppliedStatus || pair.Target.OriginalStatus != state.OriginalStatus) {
			return false, ErrRemoteActionEvidenceChanged
		}
		state.PendingDispatchID, state.PendingOwnerID, state.PendingDispatchPhase = claim.DispatchID, claim.OwnerID, DispatchPrepared
		pair.Target = &state
	default:
		return false, errors.New("unknown remote action")
	}
	return true, nil
}

func permitRemoteAction(pair *RemoteActionCheckpoints, claim RemoteActionClaim) bool {
	id, owner, phase, pending := pair.dispatch(claim.Kind)
	if pair.pendingCount() != 1 || !pending || id != claim.DispatchID || owner != claim.OwnerID || phase != DispatchPrepared || !actionClaimIntentionMatches(*pair, claim) {
		return false
	}
	if claim.Kind == ActionKindPriority {
		pair.Priority.PendingDispatchPhase = DispatchSending
	} else {
		pair.Target.PendingDispatchPhase = DispatchSending
	}
	return true
}

func actionClaimIntentionMatches(pair RemoteActionCheckpoints, claim RemoteActionClaim) bool {
	if claim.Kind == ActionKindPriority {
		return pair.Priority != nil && claim.Priority != nil && pair.Priority.UserID == claim.UserID && pair.Priority.AdminAccountID == claim.AdminAccountID && pair.Priority.TargetID == claim.TargetID && equalIntPointers(pair.Priority.PendingPriority, claim.Priority.PendingPriority)
	}
	if claim.Kind == ActionKindTarget {
		return pair.Target != nil && claim.Target != nil && pair.Target.UserID == claim.UserID && pair.Target.AdminAccountID == claim.AdminAccountID && pair.Target.TargetID == claim.TargetID && pair.Target.PendingStatus == claim.Target.PendingStatus && equalIntPointers(pair.Target.PendingWeight, claim.Target.PendingWeight)
	}
	return false
}

func terminalDispatchPhase(phase RemoteDispatchPhase) bool {
	return phase == DispatchNotSent || phase == DispatchConfirmedApplied || phase == DispatchConfirmedRejected
}

func receiptRemoteAction(pair *RemoteActionCheckpoints, claim RemoteActionClaim, phase RemoteDispatchPhase, now time.Time) bool {
	id, owner, current, pending := pair.dispatch(claim.Kind)
	if !pending || id == "" || id != claim.DispatchID || owner != claim.OwnerID {
		return false
	}
	if phase != DispatchUncertain && !terminalDispatchPhase(phase) {
		return false
	}
	if terminalDispatchPhase(current) {
		return current == phase
	}
	if claim.Kind == ActionKindPriority {
		pair.Priority.PendingDispatchPhase = phase
		pair.Priority.UpdatedAt = now
	} else {
		pair.Target.PendingDispatchPhase = phase
		pair.Target.UpdatedAt = now
	}
	return true
}

func clearPriorityDispatch(state *PrioritySyncState) {
	state.PendingPriority = nil
	state.PendingDispatchID = ""
	state.PendingOwnerID = ""
	state.PendingDispatchPhase = ""
}

func clearTargetDispatch(state *TargetActionState) {
	state.PendingStatus = ""
	state.PendingWeight = nil
	state.PendingDispatchID = ""
	state.PendingOwnerID = ""
	state.PendingDispatchPhase = ""
}

func validatePriorityCheckpointUpdate(pair RemoteActionCheckpoints, state PrioritySyncState) error {
	if !isSub2APIActionTarget(state.TargetID) {
		return nil
	}
	if priorityActionPending(&state) && !priorityActionPending(pair.Priority) {
		return ErrRemoteActionPending
	}
	if priorityActionPending(pair.Priority) {
		old := pair.Priority
		if state.PendingDispatchID != old.PendingDispatchID || state.PendingOwnerID != old.PendingOwnerID || state.PendingDispatchPhase != old.PendingDispatchPhase || !equalIntPointers(state.PendingPriority, old.PendingPriority) || state.LastAppliedPriority != old.LastAppliedPriority || state.OriginalPriority != old.OriginalPriority {
			return ErrRemoteActionPending
		}
	}
	return nil
}

func validateTargetCheckpointUpdate(pair RemoteActionCheckpoints, state TargetActionState) error {
	if !isSub2APIActionTarget(state.TargetID) {
		return nil
	}
	if targetActionPending(&state) && !targetActionPending(pair.Target) {
		return ErrRemoteActionPending
	}
	if targetActionPending(pair.Target) {
		old := pair.Target
		if state.PendingDispatchID != old.PendingDispatchID || state.PendingOwnerID != old.PendingOwnerID || state.PendingDispatchPhase != old.PendingDispatchPhase || state.PendingStatus != old.PendingStatus || !equalIntPointers(state.PendingWeight, old.PendingWeight) || state.LastAppliedStatus != old.LastAppliedStatus || !equalIntPointers(state.LastAppliedWeight, old.LastAppliedWeight) || state.OriginalStatus != old.OriginalStatus || !equalIntPointers(state.OriginalWeight, old.OriginalWeight) {
			return ErrRemoteActionPending
		}
	}
	return nil
}

// A confirmed receipt is not reconciliation. Its subsequent complete visible
// snapshot must match the expected or original value before the claim releases.
func reconcileRemoteAction(pair *RemoteActionCheckpoints, observation RemoteActionObservation, ownerValid func(string) bool) bool {
	if !observation.InventoryComplete || !observation.Visible || pair.pendingCount() != 1 {
		return false
	}
	if state := pair.Priority; priorityActionPending(state) {
		if state.PendingDispatchID == "" || state.PendingOwnerID == "" {
			return false
		}
		if state.PendingDispatchPhase == DispatchPrepared && !ownerValid(state.PendingOwnerID) {
			clearPriorityDispatch(state)
			return true
		}
		if !terminalDispatchPhase(state.PendingDispatchPhase) || !observation.SnapshotStartedAt.After(state.UpdatedAt) || observation.Priority == nil {
			return false
		}
		expected := state.LastAppliedPriority
		if state.PendingDispatchPhase == DispatchConfirmedApplied && state.PendingPriority != nil {
			expected = *state.PendingPriority
		}
		if *observation.Priority != expected {
			state.Conflict = true
			state.LastConflictPriority = cloneIntPointer(observation.Priority)
			return false
		}
		state.LastAppliedPriority = expected
		clearPriorityDispatch(state)
		return true
	}
	if state := pair.Target; targetActionPending(state) {
		if state.PendingDispatchID == "" || state.PendingOwnerID == "" {
			return false
		}
		if state.PendingDispatchPhase == DispatchPrepared && !ownerValid(state.PendingOwnerID) {
			clearTargetDispatch(state)
			return true
		}
		if !terminalDispatchPhase(state.PendingDispatchPhase) || !observation.SnapshotStartedAt.After(state.UpdatedAt) || observation.Status == "" {
			return false
		}
		expected := state.LastAppliedStatus
		if state.PendingDispatchPhase == DispatchConfirmedApplied {
			expected = state.PendingStatus
		}
		if normalizeTargetStatus("sub2api", observation.Status) != normalizeTargetStatus("sub2api", expected) {
			state.Conflict = true
			return false
		}
		state.LastAppliedStatus = expected
		if state.PendingDispatchPhase == DispatchConfirmedApplied {
			state.LastAppliedWeight = cloneIntPointer(state.PendingWeight)
		}
		clearTargetDispatch(state)
		return true
	}
	return false
}
