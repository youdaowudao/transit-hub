package connection_health

import (
	"context"
	"time"
)

// A failed credential lookup did not send a model request. Its retry timestamp
// and presentation must stay separate from durable probe failures and attempts.
func currentCredentialFailure(state ConnectionHealthState) (string, *time.Time) {
	if state.LastCredentialFailureAt != nil {
		if isCredentialUnavailableReason(state.LastCredentialFailureReason) && (state.LastProbeAt == nil || state.LastCredentialFailureAt.After(*state.LastProbeAt)) {
			return state.LastCredentialFailureReason, state.LastCredentialFailureAt
		}
		return "", nil
	}
	// The first protocol-tagged request proves preparation succeeded, even if
	// its invalid response preserves the legacy error column. updated_at can
	// be slightly later than last_probe_at because it records database commit.
	if state.LastProbeProtocol != nil {
		return "", nil
	}
	// Preserve retry cadence for rows written before the separate diagnostic.
	if isCredentialUnavailableReason(state.LastErrorKey) && !state.UpdatedAt.IsZero() && (state.LastProbeAt == nil || state.UpdatedAt.After(*state.LastProbeAt)) {
		return state.LastErrorKey, &state.UpdatedAt
	}
	return "", nil
}

func modelCredentialUnavailableReason(model ModelHealth) string {
	if isCredentialUnavailableReason(model.CredentialUnavailableReason) {
		return model.CredentialUnavailableReason
	}
	if !model.Configured && isCredentialUnavailableReason(model.LastErrorKey) {
		return model.LastErrorKey
	}
	return ""
}

// RecordTargetCredentialFailure accepts only initial-row metadata from its
// caller. Existing health and ownership fields always come from the current
// transaction, and the UPDATE never writes them back.
func (r *Repository) RecordTargetCredentialFailure(ctx context.Context, initial ConnectionHealthState, reason string, at time.Time) (ConnectionHealthState, error) {
	if !isCredentialUnavailableReason(reason) || at.IsZero() {
		return ConnectionHealthState{}, requestError(ErrorRequest)
	}
	tx, err := r.beginWorkspaceTransaction(ctx, initial.UserID, initial.AdminAccountID)
	if err != nil {
		return ConnectionHealthState{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := getStateTx(ctx, tx, initial.ConnectionID, initial.ModelName)
	if err != nil {
		return ConnectionHealthState{}, err
	}
	if current != nil && (current.UserID != initial.UserID || current.AdminAccountID != initial.AdminAccountID) {
		return ConnectionHealthState{}, requestError(ErrorNotFound)
	}
	if current == nil {
		initial.HealthEvidenceStatus, initial.HealthEvidenceProtocol = HealthEvidenceInvalid, nil
		initial.LastCredentialFailureAt, initial.LastCredentialFailureReason = nil, ""
		if err := upsertStateWithExecutor(ctx, tx, initial); err != nil {
			return ConnectionHealthState{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE connection_health_states
 SET last_credential_failure_at=$5, last_credential_failure_reason=$6,
     updated_at=GREATEST(updated_at,$5)
 WHERE connection_id=$1 AND model_name=$2 AND user_id=$3 AND admin_account_id=$4
   AND (last_credential_failure_at IS NULL OR last_credential_failure_at<=$5)`,
		initial.ConnectionID, initial.ModelName, initial.UserID, initial.AdminAccountID, at, reason); err != nil {
		return ConnectionHealthState{}, err
	}
	current, err = getStateTx(ctx, tx, initial.ConnectionID, initial.ModelName)
	if err != nil {
		return ConnectionHealthState{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ConnectionHealthState{}, err
	}
	return *current, nil
}
