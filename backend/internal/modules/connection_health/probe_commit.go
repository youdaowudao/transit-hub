package connection_health

import (
	"context"
	"strings"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type TargetProbeCommit struct {
	ConfigGeneration int64
	UserID           string
	AdminAccountID   string
	Target           AdminProbeTarget
	ModelName        string
	Policy           Policy
	Outcome          ProbeOutcome
	DecisionKey      string
	Event            ConnectionHealthEvent
	Now              time.Time
}

type TargetProbeCommitResult struct {
	ConfigGeneration int64
	Configuration    EffectiveTestConfiguration
	State            ConnectionHealthState
	Transition       TransitionOutput
	PreviousState    State
	Disposition      string
	EventID          string
}

func nullableProbeDisposition(disposition string) *string {
	if disposition == "" {
		return nil
	}
	return &disposition
}

// Keep the request diagnostic identical wherever the same outcome is used:
// immediate DTO, durable failed health, last attempt and event audit. A success
// retains this audit note without populating the state's error fields.
func probeOutcomeWithAuditMetadata(outcome ProbeOutcome) ProbeOutcome {
	const note = "上游未返回流式"
	if outcome.NonStreaming && !strings.HasSuffix(outcome.Detail, note) {
		if outcome.Detail == "" {
			outcome.Detail = note
		} else {
			outcome.Detail += "；" + note
		}
	}
	return outcome
}

func buildProbeCommitResult(input TargetProbeCommit, current *ConnectionHealthState, configuration EffectiveTestConfiguration) (TargetProbeCommitResult, ConnectionHealthEvent) {
	input.Outcome = probeOutcomeWithAuditMetadata(applyOutcomeDelay(input.Outcome, input.Policy))
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
	event.FirstTokenMs = input.Outcome.FirstTokenMs
	event.FirstEventMs = input.Outcome.FirstEventMs
	event.RuleVersion = input.Policy.RuleVersion
	if event.RuleVersion == "" {
		event.RuleVersion = RuleVersionLegacy
	}
	if input.Outcome.Result != ResultOK && input.Outcome.Result != ResultSlowResponse {
		event.ErrorKey = string(input.Outcome.Result)
		event.ErrorDetail = input.Outcome.Detail
	}
	if input.Outcome.NonStreaming {
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
	result.State.RecheckPending = false
	if result.Disposition == "applied" && result.State.State == StateSuspect && current.State == StateHealthy {
		result.State.RecheckPending = true
	}
	preset := RulePresetForPolicy(input.Policy)
	longFailure := result.State.FailingSince != nil && input.Now.Sub(*result.State.FailingSince) >= time.Duration(preset.LongFailureAfterSeconds)*time.Second
	event.LongFailure = &longFailure
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
	if err := ensureWorkspaceHealthSettings(ctx, tx, input.UserID, input.AdminAccountID); err != nil {
		return TargetProbeCommitResult{}, err
	}
	settings, err := getWorkspaceHealthSettings(ctx, tx, input.UserID, input.AdminAccountID)
	if err != nil {
		return TargetProbeCommitResult{}, err
	}
	configs, err := listGroupTestConfigurationsTx(ctx, tx, input.UserID, input.AdminAccountID)
	if err != nil {
		return TargetProbeCommitResult{}, err
	}
	configuration := ResolveGroupTestConfiguration(string(upstream.PlatformSub2API), input.Target.TestMemberships, input.Target.InventoryComplete, configs)
	current, err := getStateTx(ctx, tx, input.Target.TargetID, input.ModelName)
	if err != nil {
		return TargetProbeCommitResult{}, err
	}
	if current != nil && (current.UserID != input.UserID || current.AdminAccountID != input.AdminAccountID) {
		return TargetProbeCommitResult{}, requestError(ErrorNotFound)
	}
	comparison := configuration
	if input.ConfigGeneration != settings.ConfigGeneration {
		comparison = EffectiveTestConfiguration{Status: "stale"}
	}
	result, event := buildProbeCommitResult(input, current, comparison)
	result.Configuration = configuration
	result.ConfigGeneration = settings.ConfigGeneration
	if result.Disposition == "stale" {
		if _, err := tx.Exec(ctx, `UPDATE connection_health_states SET recheck_pending=false WHERE connection_id=$1 AND model_name=$2 AND user_id=$3 AND admin_account_id=$4`, input.Target.TargetID, input.ModelName, input.UserID, input.AdminAccountID); err != nil {
			return TargetProbeCommitResult{}, err
		}
	} else {
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

// ValidateTargetProbeBatch closes the local commit/batch-computation gap. It
// never performs upstream IO; Claim and Permit enforce the same generation again.
func (r *Repository) ValidateTargetProbeBatch(ctx context.Context, userID, workspace, targetID string, models []string, generation int64) (bool, error) {
	tx, err := r.beginWorkspaceTransaction(ctx, userID, workspace)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err := ensureWorkspaceHealthSettings(ctx, tx, userID, workspace); err != nil {
		return false, err
	}
	settings, err := getWorkspaceHealthSettings(ctx, tx, userID, workspace)
	if err != nil {
		return false, err
	}
	current := settings.ConfigGeneration == generation
	if !current {
		if _, err := tx.Exec(ctx, `UPDATE connection_health_states SET recheck_pending=false WHERE user_id=$1 AND admin_account_id=$2 AND connection_id=$3 AND model_name=ANY($4::text[])`, userID, workspace, targetID, models); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return current, nil
}
