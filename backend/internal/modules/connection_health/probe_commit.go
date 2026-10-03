package connection_health

import (
	"context"
	"time"
)

type TargetProbeCommit struct {
	UserID         string
	AdminAccountID string
	Target         AdminProbeTarget
	ModelName      string
	Policy         Policy
	Outcome        ProbeOutcome
	DecisionKey    string
	Event          ConnectionHealthEvent
	Now            time.Time
}

type TargetProbeCommitResult struct {
	Configuration EffectiveTestConfiguration
	State         ConnectionHealthState
	Transition    TransitionOutput
	PreviousState State
	Disposition   string
	EventID       string
}

func nullableProbeDisposition(disposition string) *string {
	if disposition == "" {
		return nil
	}
	return &disposition
}

func buildProbeCommitResult(input TargetProbeCommit, current *ConnectionHealthState, configuration EffectiveTestConfiguration) (TargetProbeCommitResult, ConnectionHealthEvent) {
	if current == nil {
		state := defaultTargetState(input.UserID, input.AdminAccountID, input.Target, input.ModelName)
		current = &state
	}
	result := TargetProbeCommitResult{Configuration: configuration, State: *current, PreviousState: current.State, Disposition: "applied", EventID: input.Event.ID}
	event := input.Event
	event.RequestProtocol = protocolPointer(input.Outcome.Protocol)
	event.RequestTimeoutSeconds = intPtr(input.Outcome.ProbeTimeoutSeconds)
	event.Result = string(input.Outcome.Result)
	event.FromState = string(current.State)
	event.LatencyMs = intPtr(input.Outcome.LatencyMs)
	if input.Outcome.Result != ResultOK && input.Outcome.Result != ResultSlowResponse {
		event.ErrorKey = string(input.Outcome.Result)
		event.ErrorDetail = input.Outcome.Detail
	}
	if !sameEffectiveTestConfiguration(input.Target.TestConfiguration, configuration) {
		result.Disposition = "stale"
	} else {
		result.State, result.Transition = applyProbeOutcome(*current, input.Outcome, input.Policy, input.Now)
		result.State.LastProbeDecisionKey = input.DecisionKey
		result.State.UpdatedAt = input.Now
		if input.Outcome.Result == ResultInvalidResponse {
			result.Disposition = "invalid"
		}
	}
	event.ToState = string(result.State.State)
	event.ProbeDisposition = result.Disposition
	event.CreatedAt = input.Now
	return result, event
}

// CommitTargetProbe never waits on model IO. W protects absent configuration
// rows as well as existing rows, so adding a rule to an empty group is visible.
func (r *Repository) CommitTargetProbe(ctx context.Context, input TargetProbeCommit) (TargetProbeCommitResult, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, input.UserID, input.AdminAccountID)
	if err != nil {
		return TargetProbeCommitResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	configs, err := listGroupTestConfigurationsTx(ctx, tx, input.UserID, input.AdminAccountID)
	if err != nil {
		return TargetProbeCommitResult{}, err
	}
	configuration := ResolveGroupTestConfiguration(input.Target.TestMemberships, input.Target.InventoryComplete, configs)
	current, err := getStateTx(ctx, tx, input.Target.TargetID, input.ModelName)
	if err != nil {
		return TargetProbeCommitResult{}, err
	}
	if current != nil && (current.UserID != input.UserID || current.AdminAccountID != input.AdminAccountID) {
		return TargetProbeCommitResult{}, requestError(ErrorNotFound)
	}
	result, event := buildProbeCommitResult(input, current, configuration)
	if result.Disposition != "stale" {
		if err := upsertStateWithExecutor(ctx, tx, result.State); err != nil {
			return TargetProbeCommitResult{}, err
		}
	}
	if err := insertEventWithExecutor(ctx, tx, event); err != nil {
		return TargetProbeCommitResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TargetProbeCommitResult{}, err
	}
	return result, nil
}

// Adding the action audit must not rewrite a stale state snapshot after another
// accepted result has committed. The event ID is fixed by the probe transaction.
func eventTimestamp(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func (r *Repository) DecorateProbeEventAction(ctx context.Context, userID, workspace, eventID, action, groupID, groupName string) error {
	tx, err := r.beginWorkspaceTransaction(ctx, userID, workspace)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `UPDATE connection_health_events SET remote_action=$4, admin_group_id=CASE WHEN $5='' THEN admin_group_id ELSE $5 END, own_group_name=CASE WHEN $5='' THEN own_group_name ELSE $6 END WHERE id=$1 AND user_id=$2 AND admin_account_id=$3 AND probe_disposition='applied'`, eventID, userID, workspace, action, groupID, groupName)
	if err != nil {
		return err
	}
	if !targetActionAuditOnly(action) {
		_, err = tx.Exec(ctx, `UPDATE connection_health_states state SET last_remote_action=$4 FROM connection_health_events event WHERE event.id=$1 AND event.user_id=$2 AND event.admin_account_id=$3 AND state.connection_id=event.connection_id AND state.model_name=event.model_name AND state.user_id=event.user_id AND state.admin_account_id=event.admin_account_id AND state.last_applied_probe_at=event.created_at AND event.probe_disposition='applied'`, eventID, userID, workspace, action)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
