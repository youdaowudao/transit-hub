package connection_health

import (
	"context"
	"reflect"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

func (f *fakeRepository) RecordTargetCredentialFailure(ctx context.Context, initial ConnectionHealthState, reason string, at time.Time) (ConnectionHealthState, error) {
	f.testConfigurationMu.Lock()
	defer f.testConfigurationMu.Unlock()
	if f.upsertStateErr != nil {
		return ConnectionHealthState{}, f.upsertStateErr
	}
	if !isCredentialUnavailableReason(reason) || at.IsZero() {
		return ConnectionHealthState{}, requestError(ErrorRequest)
	}
	current, err := f.GetState(ctx, initial.ConnectionID, initial.ModelName)
	if err != nil {
		return ConnectionHealthState{}, err
	}
	if current != nil && (current.UserID != initial.UserID || current.AdminAccountID != initial.AdminAccountID) {
		return ConnectionHealthState{}, requestError(ErrorNotFound)
	}
	if current == nil {
		initial.HealthEvidenceStatus, initial.HealthEvidenceProtocol = HealthEvidenceInvalid, nil
		initial.LastCredentialFailureAt, initial.LastCredentialFailureReason = nil, ""
		current = &initial
	}
	if current.LastCredentialFailureAt == nil || !current.LastCredentialFailureAt.After(at) {
		current.LastCredentialFailureAt, current.LastCredentialFailureReason = &at, reason
		current.RecheckPending = false
		if current.UpdatedAt.Before(at) {
			current.UpdatedAt = at
		}
	}
	if f.states[initial.ConnectionID] == nil {
		f.states[initial.ConnectionID] = make(map[string]ConnectionHealthState)
	}
	f.states[initial.ConnectionID][initial.ModelName] = *current
	return *current, nil
}

func TestProtocolCredentialPreparationOldSnapshotCannotReplaceNewHealth(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepository()
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	old := protocolEvidenceState(StateDegraded, 50, now)
	current := protocolEvidenceState(StateSuspended, 0, now.Add(time.Minute))
	current.UpdatedAt = now.Add(time.Minute)
	current.LastErrorKey, current.LastErrorDetail = string(ResultServerError), "new committed failure"
	current.LastRemoteAction = RemoteActionSub2APIStatusInactive
	repo.states[current.ConnectionID] = map[string]ConnectionHealthState{current.ModelName: current}
	stored, err := repo.RecordTargetCredentialFailure(ctx, old, upstream.ReasonCredentialUnavailable, now)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != current.State || stored.CurrentWeight != current.CurrentWeight || stored.ConsecutiveFailures != current.ConsecutiveFailures || stored.LastErrorKey != current.LastErrorKey || stored.LastErrorDetail != current.LastErrorDetail || stored.LastRemoteAction != current.LastRemoteAction || !reflect.DeepEqual(stored.LastAppliedProbeAt, current.LastAppliedProbeAt) || !stored.UpdatedAt.Equal(current.UpdatedAt) {
		t.Fatalf("late old snapshot replaced new result or moved updatedAt backwards: %+v", stored)
	}
	newer := now.Add(2 * time.Minute)
	stored, err = repo.RecordTargetCredentialFailure(ctx, old, upstream.ReasonSecureVerificationRequired, newer)
	if err != nil {
		t.Fatal(err)
	}
	stored, err = repo.RecordTargetCredentialFailure(ctx, old, upstream.ReasonCredentialUnavailable, now)
	if err != nil || stored.LastCredentialFailureReason != upstream.ReasonSecureVerificationRequired || stored.LastCredentialFailureAt == nil || !stored.LastCredentialFailureAt.Equal(newer) || !stored.UpdatedAt.Equal(newer) {
		t.Fatalf("late preparation replaced newer diagnostic: %+v err=%v", stored, err)
	}
}

func TestProtocolCredentialPreparationRealRequestClearsOnlyPreparationDisplay(t *testing.T) {
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	state := protocolEvidenceState(StateDegraded, 50, now)
	state.LastCredentialFailureAt, state.LastCredentialFailureReason = &now, upstream.ReasonCredentialUnavailable
	state.UpdatedAt = now
	before := toModelHealth(state.ModelName, state)
	if before.Configured || latestCredentialUnavailableReason([]ModelHealth{before}) != upstream.ReasonCredentialUnavailable {
		t.Fatal("independent credential preparation diagnostic missing")
	}
	for _, result := range []ResultKey{ResultOK, ResultInvalidResponse, ResultServerError} {
		t.Run(string(result), func(t *testing.T) {
			next, _ := applyProbeOutcome(state, ProbeOutcome{Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10, Result: result}, sub2APIProbePolicy(false), now.Add(time.Minute))
			model := toModelHealth(next.ModelName, next)
			if !model.Configured || model.CredentialUnavailableAt != nil || latestCredentialUnavailableReason([]ModelHealth{model}) != "" {
				t.Fatalf("real request did not resolve stale preparation diagnostic: %+v", model)
			}
			if reason, at := currentCredentialFailure(next); reason != "" || at != nil {
				t.Fatal("old credential diagnostic still changed probe cadence")
			}
		})
	}
}

func TestProtocolCredentialPreparationNewRowHasNoProbeOrHealthEvidence(t *testing.T) {
	repo := newFakeRepository()
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	target := AdminProbeTarget{TargetID: "sub2api:ws1:new", Platform: "sub2api", AccountID: "new"}
	state, err := repo.RecordTargetCredentialFailure(context.Background(), defaultTargetState("user1", "ws1", target, "m"), upstream.ReasonCredentialUnavailable, now)
	if err != nil {
		t.Fatal(err)
	}
	if state.HealthEvidenceStatus != HealthEvidenceInvalid || state.HealthEvidenceProtocol != nil || state.LastProbeAt != nil || state.LastAppliedProbeAt != nil || state.LastErrorKey != "" || len(repo.budgetClaims) != 0 {
		t.Fatalf("credential preparation invented a request or health evidence: %+v", state)
	}
	model := toModelHealth("m", state)
	applyCurrentHealthProjection(&model, state, defaultTestConfiguration())
	if model.CurrentHealthResult.Status != "unverified" || model.LastAttempt.At != nil || model.LastAttempt.ErrorKey != "" || model.Configured {
		t.Fatalf("credential preparation invented a displayed attempt: %+v", model)
	}
}

func TestProtocolCredentialPreparationLegacyFailureCannotReviveAfterRealInvalidRequest(t *testing.T) {
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	previousProbe := now.Add(-time.Minute)
	legacy := ConnectionHealthState{
		ModelName: "m", State: StateHealthy, CurrentWeight: 100,
		LastProbeAt: &previousProbe, LastErrorKey: upstream.ReasonCredentialUnavailable,
		UpdatedAt: now, HealthEvidenceStatus: HealthEvidenceLegacy,
	}
	if reason, at := currentCredentialFailure(legacy); reason != upstream.ReasonCredentialUnavailable || at == nil || !at.Equal(now) {
		t.Fatal("legacy preparation diagnostic lost compatibility before a new request")
	}
	for _, commitDelay := range []time.Duration{0, time.Millisecond} {
		t.Run(commitDelay.String(), func(t *testing.T) {
			actualRequestAt := now.Add(time.Minute)
			next, _ := applyProbeOutcome(legacy, ProbeOutcome{Protocol: TestProtocolChatCompletions, ProbeTimeoutSeconds: 10, Result: ResultInvalidResponse}, sub2APIProbePolicy(false), actualRequestAt)
			next.UpdatedAt = actualRequestAt.Add(commitDelay)
			model := toModelHealth("m", next)
			if next.LastErrorKey != legacy.LastErrorKey {
				t.Fatal("invalid response erased historical state error")
			}
			if !model.Configured || latestCredentialUnavailableReason([]ModelHealth{model}) != "" {
				t.Fatalf("old state error revived credential presentation after actual request: %+v", model)
			}
			if reason, at := currentCredentialFailure(next); reason != "" || at != nil {
				t.Fatal("database commit time revived old credential retry block")
			}
		})
	}
}
