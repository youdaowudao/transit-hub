package connection_health

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/upstream"
)

type mutationSafetyRepository struct {
	safetyMutationRepository
	mu          sync.Mutex
	generations map[string]int64
	tokens      map[string]int64
}

func newMutationSafetyRepository() *mutationSafetyRepository {
	return &mutationSafetyRepository{generations: make(map[string]int64), tokens: make(map[string]int64)}
}

func mutationRepositoryKey(userID, workspaceID, accountID string) string {
	return userID + "|" + workspaceID + "|" + accountID
}

func (r *mutationSafetyRepository) MutationGeneration(_ context.Context, userID, workspaceID, accountID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generations[mutationRepositoryKey(userID, workspaceID, accountID)], nil
}

func (r *mutationSafetyRepository) BumpMutationGeneration(_ context.Context, userID, workspaceID, accountID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := mutationRepositoryKey(userID, workspaceID, accountID)
	r.generations[key]++
	return r.generations[key], nil
}

func (r *mutationSafetyRepository) AcquireMutationLease(_ context.Context, userID, workspaceID, accountID string, _ bool) (RepositoryMutationLease, error) {
	r.mu.Lock()
	key := mutationRepositoryKey(userID, workspaceID, accountID)
	generation := r.generations[key]
	r.tokens[key]++
	token := r.tokens[key]
	r.mu.Unlock()
	return RepositoryMutationLease{
		Generation: generation, FencingToken: token,
		Validate: func(context.Context) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.generations[key] != generation || r.tokens[key] != token {
				return errStaleMutation
			}
			return nil
		},
		Release: func() {},
	}, nil
}

func (r *mutationSafetyRepository) AcquireManualMutationLease(_ context.Context, userID, workspaceID, accountID string) (RepositoryMutationLease, error) {
	r.mu.Lock()
	key := mutationRepositoryKey(userID, workspaceID, accountID)
	r.generations[key]++
	generation := r.generations[key]
	r.tokens[key]++
	token := r.tokens[key]
	r.mu.Unlock()
	return RepositoryMutationLease{
		Generation: generation, FencingToken: token,
		Validate: func(context.Context) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.generations[key] != generation || r.tokens[key] != token {
				return errStaleMutation
			}
			return nil
		},
		Release: func() {},
	}, nil
}

type mutatingSub2APIPlatformActioner struct {
	fakePlatformActioner
	afterStatus           func(string)
	returnErrorAfterWrite error
}

func (a *mutatingSub2APIPlatformActioner) UpdateSub2APIAdminAccountStatus(session upstream.Session, accountID string, status string) error {
	if err := a.fakePlatformActioner.UpdateSub2APIAdminAccountStatus(session, accountID, status); err != nil {
		return err
	}
	if a.afterStatus != nil {
		a.afterStatus(status)
	}
	return a.returnErrorAfterWrite
}

type mutatingPriorityActioner struct {
	calls                 []priorityUpdateCall
	afterUpdate           func(int)
	returnErrorAfterWrite error
}

func (a *mutatingPriorityActioner) UpdateAdminTargetPriority(_ upstream.Session, targetID string, priority int) error {
	a.calls = append(a.calls, priorityUpdateCall{targetID: targetID, priority: priority})
	if a.afterUpdate != nil {
		a.afterUpdate(priority)
	}
	return a.returnErrorAfterWrite
}

type queueWorkerSafetyRepository struct {
	safetyMutationRepository
	rescheduled        []AbnormalQueueItem
	completed          []string
	enqueued           []AbnormalQueueItem
	emergencyClearKeys []string
	enqueueErr         error
}

type faultDomainSafetyRepository struct {
	safetyMutationRepository
	epoch      int64
	endpoint   string
	targetOpen bool
	anyOpen    bool
	incidents  map[string]*IncidentCircuitState
}

func (r *faultDomainSafetyRepository) GetAbnormalQueueEpoch(context.Context, string, string) (int64, error) {
	return r.epoch, nil
}

func (r *faultDomainSafetyRepository) TargetCircuitOpen(context.Context, string, string, string) (bool, error) {
	return r.targetOpen, nil
}

func (r *faultDomainSafetyRepository) GetTargetFaultEndpoint(context.Context, string, string, string) (string, error) {
	return r.endpoint, nil
}

func (r *faultDomainSafetyRepository) AnyIncidentCircuitOpen(context.Context, string, string) (bool, error) {
	return r.anyOpen, nil
}

func (r *faultDomainSafetyRepository) GetIncidentCircuit(_ context.Context, _, _, domain string) (*IncidentCircuitState, error) {
	return r.incidents[domain], nil
}

func (r *queueWorkerSafetyRepository) RescheduleDispatchedAbnormalQueueItem(_ context.Context, item AbnormalQueueItem, _ string) error {
	r.rescheduled = append(r.rescheduled, item)
	return nil
}

func (r *queueWorkerSafetyRepository) CompleteAbnormalQueueItem(_ context.Context, _ string, _ string, result string) error {
	r.completed = append(r.completed, result)
	return nil
}

func (r *queueWorkerSafetyRepository) EnqueueAbnormalQueueItem(_ context.Context, item AbnormalQueueItem, _ int) (AbnormalQueueItem, bool, error) {
	if r.enqueueErr != nil {
		return AbnormalQueueItem{}, false, r.enqueueErr
	}
	r.enqueued = append(r.enqueued, item)
	return item, true, nil
}

func (r *queueWorkerSafetyRepository) GetSafetySettings(context.Context, string, string) (SafetySettings, error) {
	return DefaultSafetySettings(), nil
}

func (r *queueWorkerSafetyRepository) EmergencyClear(_ context.Context, _ string, workspaceID string, idempotencyKey string, now time.Time) (EmergencyClearResult, error) {
	r.emergencyClearKeys = append(r.emergencyClearKeys, idempotencyKey)
	return EmergencyClearResult{WorkspaceID: workspaceID, QueueEpoch: 2, CompletedAt: now}, nil
}

func TestSafetySettings_DefaultsAndBounds(t *testing.T) {
	defaults := DefaultSafetySettings()
	if err := defaults.Validate(); err != nil {
		t.Fatalf("default safety settings must be valid: %v", err)
	}
	if defaults.ConfirmationObservationCount != 4 || len(defaults.ConfirmationDelaysSeconds) != 3 ||
		defaults.ConfirmationDelaysSeconds[0] != 2 || defaults.ConfirmationDelaysSeconds[1] != 5 ||
		defaults.ConfirmationDelaysSeconds[2] != 10 || defaults.ConfirmationJitterSeconds != 1 ||
		defaults.AbnormalQueueCapacity != 64 || defaults.ManualReservedSlots != 1 {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}

	tests := []SafetySettings{
		{ConfirmationObservationCount: 2, ConfirmationDelaysSeconds: []int{1}, AbnormalQueueCapacity: 64},
		{ConfirmationObservationCount: 4, ConfirmationDelaysSeconds: []int{2, 5}, AbnormalQueueCapacity: 64},
		{ConfirmationObservationCount: 4, ConfirmationDelaysSeconds: []int{30, 30, 1}, AbnormalQueueCapacity: 64},
		{ConfirmationObservationCount: 4, ConfirmationDelaysSeconds: []int{2, 5, 10}, ConfirmationJitterSeconds: 4, AbnormalQueueCapacity: 64},
		{ConfirmationObservationCount: 4, ConfirmationDelaysSeconds: []int{2, 5, 10}, AbnormalQueueCapacity: 15},
		{ConfirmationObservationCount: 4, ConfirmationDelaysSeconds: []int{2, 5, 10}, AbnormalQueueCapacity: 64, ManualReservedSlots: 2},
	}
	for index, settings := range tests {
		if err := settings.Validate(); !errors.Is(err, errInvalidSafetySettings) {
			t.Fatalf("invalid settings case %d = %+v, want invalid settings error, got %v", index, settings, err)
		}
	}
}

func TestConfirmationDelay_IsNeverEarlierThanConfigured(t *testing.T) {
	settings := DefaultSafetySettings()
	if got := confirmationDelay(settings, 1, func(int) int { return 1 }); got != 3*time.Second {
		t.Fatalf("first confirmation delay = %s, want 3s", got)
	}
	if got := confirmationDelay(settings, 2, func(int) int { return 0 }); got != 5*time.Second {
		t.Fatalf("second confirmation delay = %s, want 5s", got)
	}
	if got := confirmationDelay(settings, 4, nil); got != 0 {
		t.Fatalf("out-of-range confirmation delay = %s, want 0", got)
	}
}

func TestConfirmationFaultEndpoint_NormalizesOnlyNonSecretEndpoint(t *testing.T) {
	if got := confirmationFaultEndpoint("https://EXAMPLE.test/root/"); got != "example.test/root/v1/chat/completions" {
		t.Fatalf("normalized endpoint = %q", got)
	}
	if got := confirmationFaultEndpoint("not a url"); got != "" {
		t.Fatalf("invalid endpoint = %q", got)
	}
}

func TestAutomaticProbeAllowed_HoldsUnknownTargetsWhenCircuitIsOpen(t *testing.T) {
	repo := &faultDomainSafetyRepository{epoch: 7, anyOpen: true, incidents: map[string]*IncidentCircuitState{}}
	service := &Service{safetyRepo: repo}
	allowed, err := service.automaticProbeAllowed(context.Background(), "user", "workspace", "target", "", 7)
	if err != nil || allowed {
		t.Fatalf("unknown target with open circuit allowed=%v err=%v", allowed, err)
	}
	repo.endpoint = "example.test/v1/chat/completions"
	allowed, err = service.automaticProbeAllowed(context.Background(), "user", "workspace", "target", "", 7)
	if err != nil || !allowed {
		t.Fatalf("known target in a different endpoint circuit allowed=%v err=%v", allowed, err)
	}
	repo.incidents[repo.endpoint+":server"] = &IncidentCircuitState{State: CircuitOpen}
	allowed, err = service.automaticProbeAllowed(context.Background(), "user", "workspace", "target", "", 7)
	if err != nil || allowed {
		t.Fatalf("known target in its endpoint circuit allowed=%v err=%v", allowed, err)
	}
}

func TestFinishSafetyConfirmation_RateLimitReschedulesWithoutConsumingAttempt(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	repo := &queueWorkerSafetyRepository{}
	service := &Service{safetyRepo: repo, now: func() time.Time { return now }}
	item := AbnormalQueueItem{ID: "queue-1", Attempt: 2, RequiredAttempts: 4, ExpectedResult: string(ResultServerError)}

	if err := service.finishSafetyConfirmation(context.Background(), "worker-1", item, ProbeOutcome{
		Result: ResultRateLimited, RetryAfterSeconds: 17,
	}); err != nil {
		t.Fatalf("reschedule rate-limited confirmation: %v", err)
	}
	if len(repo.rescheduled) != 1 || repo.rescheduled[0].Attempt != 2 || !repo.rescheduled[0].NextAttemptAt.Equal(now.Add(17*time.Second)) {
		t.Fatalf("rate limit must preserve attempt and Retry-After: %+v", repo.rescheduled)
	}
	if len(repo.completed) != 0 || len(repo.enqueued) != 0 {
		t.Fatalf("rate limit must not complete or enqueue a destructive intent: completed=%+v enqueued=%+v", repo.completed, repo.enqueued)
	}
}

func TestFinishSafetyConfirmation_AuthenticationFailureIsGuardHeld(t *testing.T) {
	repo := &queueWorkerSafetyRepository{}
	service := &Service{repo: newFakeRepository(), safetyRepo: repo}
	item := AbnormalQueueItem{
		ID: "queue-1", TargetID: "sub2api:ws1:acc-1", ModelName: "gpt-4o",
		Attempt: 3, RequiredAttempts: 4, ExpectedResult: string(ResultAuth),
	}

	if err := service.finishSafetyConfirmation(context.Background(), "worker-1", item, ProbeOutcome{Result: ResultAuth}); err != nil {
		t.Fatalf("finish authentication confirmation: %v", err)
	}
	if len(repo.completed) != 1 || repo.completed[0] != "confirmed:"+string(ResultAuth) {
		t.Fatalf("authentication confirmation completion = %+v", repo.completed)
	}
	if len(repo.enqueued) != 0 {
		t.Fatalf("authentication failure must not enqueue status intent: %+v", repo.enqueued)
	}
}

func TestFinishSafetyConfirmation_RetriesBeforeCompletingWhenStatusIntentAdmissionFails(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	repo := &queueWorkerSafetyRepository{enqueueErr: errors.New("database unavailable")}
	service := &Service{safetyRepo: repo, now: func() time.Time { return now }}
	item := AbnormalQueueItem{
		ID: "confirmation-1", UserID: "user1", WorkspaceID: "ws1",
		TargetID: "sub2api:ws1:acc-1", AccountID: "acc-1", ModelName: "gpt-4o",
		Attempt: 3, RequiredAttempts: 4, ExpectedResult: string(ResultServerError),
		QueueEpoch: 8, MutationGeneration: 3,
	}

	if err := service.finishSafetyConfirmation(context.Background(), "worker-1", item, ProbeOutcome{Result: ResultServerError}); err != nil {
		t.Fatalf("reschedule failed status intent admission: %v", err)
	}
	if len(repo.completed) != 0 || len(repo.rescheduled) != 1 {
		t.Fatalf("failed intent admission completed confirmation: completed=%+v rescheduled=%+v", repo.completed, repo.rescheduled)
	}
	if repo.rescheduled[0].LastResult != "status_intent_enqueue_failed" {
		t.Fatalf("reschedule reason = %q", repo.rescheduled[0].LastResult)
	}
}

func TestEnqueueConfirmedStatusIntent_ActionKeyIncludesEpochAndManualGeneration(t *testing.T) {
	repo := &queueWorkerSafetyRepository{}
	service := &Service{safetyRepo: repo}
	item := AbnormalQueueItem{
		UserID: "user1", WorkspaceID: "ws1", TargetID: "sub2api:ws1:acc-1",
		QueueEpoch: 8, MutationGeneration: 3,
	}
	if err := service.enqueueConfirmedStatusIntent(context.Background(), item); err != nil {
		t.Fatalf("enqueue status intent: %v", err)
	}
	if len(repo.enqueued) != 1 || repo.enqueued[0].ActionKey != "status:sub2api:ws1:acc-1:inactive:8:3" {
		t.Fatalf("status action key = %+v", repo.enqueued)
	}
}

func TestIncidentTargetActionMatchesOnlySameIncidentIntent(t *testing.T) {
	item := AbnormalQueueItem{
		ActionKey: "status:sub2api:ws1:acc-1:inactive", QueueEpoch: 8, MutationGeneration: 3,
	}
	matching := &TargetActionState{
		PendingStatus: "inactive", PendingSource: SafetySourceHealthIncident,
		PendingEpoch: 8, PendingMutationGeneration: 3,
		PendingActionKey: item.ActionKey,
	}
	if !incidentTargetActionMatches(item, matching) {
		t.Fatal("matching incident checkpoint was not accepted")
	}
	for name, candidate := range map[string]*TargetActionState{
		"normal source":    {PendingStatus: "inactive", PendingSource: "normal", PendingEpoch: 8, PendingMutationGeneration: 3, PendingActionKey: item.ActionKey},
		"old epoch":        {PendingStatus: "inactive", PendingSource: SafetySourceHealthIncident, PendingEpoch: 7, PendingMutationGeneration: 3, PendingActionKey: item.ActionKey},
		"old generation":   {PendingStatus: "inactive", PendingSource: SafetySourceHealthIncident, PendingEpoch: 8, PendingMutationGeneration: 2, PendingActionKey: item.ActionKey},
		"different action": {PendingStatus: "inactive", PendingSource: SafetySourceHealthIncident, PendingEpoch: 8, PendingMutationGeneration: 3, PendingActionKey: "status:other:inactive"},
		"missing status":   {PendingSource: SafetySourceHealthIncident, PendingEpoch: 8, PendingMutationGeneration: 3, PendingActionKey: item.ActionKey},
	} {
		t.Run(name, func(t *testing.T) {
			if incidentTargetActionMatches(item, candidate) {
				t.Fatalf("checkpoint %q was incorrectly accepted: %+v", name, candidate)
			}
		})
	}
}

func TestEmergencyClearAbnormalQueue_RequiresUUIDAndUsesCurrentWorkspace(t *testing.T) {
	repo := &queueWorkerSafetyRepository{}
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	service := &Service{
		repo: newFakeRepository(), safetyRepo: repo, accounts: fakeAdminAccountResolver{id: "ws1"},
		now: func() time.Time { return now },
	}
	for _, key := range []string{"", "   ", strings.Repeat("x", 129), "clear-key-1", "00000000-0000-0000-0000-00000000000z"} {
		if _, err := service.EmergencyClearAbnormalQueue(context.Background(), "user1", key); err == nil || err.Error() != ErrorRequest {
			t.Fatalf("invalid idempotency key length=%d error=%v", len(key), err)
		}
	}
	key := "01234567-89ab-4cde-8f01-23456789abcd"
	result, err := service.EmergencyClearAbnormalQueue(context.Background(), "user1", "  "+key+"  ")
	if err != nil {
		t.Fatalf("emergency clear: %v", err)
	}
	if result.WorkspaceID != "ws1" || result.QueueEpoch != 2 || !result.CompletedAt.Equal(now) {
		t.Fatalf("emergency clear result = %+v", result)
	}
	if len(repo.emergencyClearKeys) != 1 || repo.emergencyClearKeys[0] != key {
		t.Fatalf("emergency clear keys = %+v", repo.emergencyClearKeys)
	}
}

func TestClearStalePriorityPending_DropsLegacyAndOldGenerationRows(t *testing.T) {
	for _, test := range []struct {
		name       string
		source     string
		generation int64
		bump       bool
	}{
		{name: "legacy missing metadata"},
		{name: "manual generation advanced", source: "normal", generation: 0, bump: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newFakeRepository()
			safetyRepo := newMutationSafetyRepository()
			service := &Service{
				repo: repo, safetyRepo: safetyRepo, mutationCoordinator: NewMutationCoordinator(safetyRepo),
			}
			if test.bump {
				if _, err := safetyRepo.BumpMutationGeneration(context.Background(), "user1", "ws1", "acc-1"); err != nil {
					t.Fatalf("bump generation: %v", err)
				}
			}
			pending := 10
			state := PrioritySyncState{
				UserID: "user1", AdminAccountID: "ws1", TargetID: "sub2api:ws1:acc-1",
				PendingPriority: &pending, PendingSource: test.source, PendingMutationGeneration: test.generation,
			}
			if _, err := service.clearStalePriorityPending(context.Background(), upstream.Session{Platform: upstream.PlatformSub2API}, &state, "acc-1"); err != nil {
				t.Fatalf("clear stale pending: %v", err)
			}
			if state.PendingPriority != nil || state.PendingSource != "" || state.PendingMutationGeneration != 0 {
				t.Fatalf("stale pending was retained: %+v", state)
			}
		})
	}
}

func TestStableSurvivor_PrefersLatestSuccessThenFailureCountThenID(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	base := SurvivorCandidate{
		Active: true, Schedulable: true, StatusKnown: true, SchedulableKnown: true,
		CapabilityKnown: true, MembershipKnown: true,
	}
	candidates := []SurvivorCandidate{
		{AccountID: "b", Active: true, Schedulable: true, StatusKnown: true, SchedulableKnown: true, CapabilityKnown: true, MembershipKnown: true, LastSuccessAt: now, ConfirmedFailureModels: 1},
		{AccountID: "a", Active: true, Schedulable: true, StatusKnown: true, SchedulableKnown: true, CapabilityKnown: true, MembershipKnown: true, LastSuccessAt: now, ConfirmedFailureModels: 1},
		{AccountID: "later", Active: true, Schedulable: true, StatusKnown: true, SchedulableKnown: true, CapabilityKnown: true, MembershipKnown: true, LastSuccessAt: now.Add(time.Minute), ConfirmedFailureModels: 3},
		{AccountID: "unknown", Active: true, Schedulable: true, StatusKnown: false, SchedulableKnown: true, CapabilityKnown: true, MembershipKnown: true, LastSuccessAt: now.Add(time.Hour)},
		base,
	}
	selected, ok := stableSurvivor(candidates)
	if !ok || selected.AccountID != "later" {
		t.Fatalf("stable survivor = %+v, ok=%v; want later", selected, ok)
	}
	candidates = candidates[:2]
	selected, ok = stableSurvivor(candidates)
	if !ok || selected.AccountID != "a" {
		t.Fatalf("stable tie-break survivor = %+v, ok=%v; want a", selected, ok)
	}
}

func TestMutationCoordinator_ManualWaitsForActiveAutomaticBeforeAdvancingGeneration(t *testing.T) {
	coordinator := NewMutationCoordinator(nil)
	key := MutationKey{UserID: "user", WorkspaceID: "workspace", AccountID: "account"}
	automatic, err := coordinator.BeginAutomatic(context.Background(), key, true)
	if err != nil {
		t.Fatalf("begin automatic mutation: %v", err)
	}
	if automatic.Generation != 0 {
		t.Fatalf("initial automatic generation = %d, want 0", automatic.Generation)
	}

	manualResult := make(chan *MutationSession, 1)
	manualError := make(chan error, 1)
	go func() {
		manual, manualErr := coordinator.BeginManual(context.Background(), key)
		if manualErr != nil {
			manualError <- manualErr
			return
		}
		manualResult <- manual
	}()

	select {
	case manual := <-manualResult:
		manual.Release()
		t.Fatal("manual mutation acquired the account lock before the active automatic mutation released it")
	case err := <-manualError:
		t.Fatalf("begin manual mutation while automatic was active: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	if got := coordinator.lockFor(key).generation.Load(); got != 0 {
		t.Fatalf("manual wait advanced generation before acquiring the lock: %d", got)
	}
	if err := automatic.Validate(context.Background()); err != nil {
		t.Fatalf("active automatic mutation became stale while manual was only waiting: %v", err)
	}
	automatic.Release()

	select {
	case err := <-manualError:
		t.Fatalf("begin manual mutation: %v", err)
	case manual := <-manualResult:
		defer manual.Release()
		if manual.Generation != 1 || manual.Validate(context.Background()) != nil {
			t.Fatalf("manual mutation session = %+v", manual)
		}
	case <-time.After(time.Second):
		t.Fatal("manual mutation did not acquire the account lock")
	}
}

func TestMutationCoordinator_ManualWaitHonorsBoundedContext(t *testing.T) {
	coordinator := NewMutationCoordinator(nil)
	key := MutationKey{UserID: "user", WorkspaceID: "workspace", AccountID: "account"}
	automatic, err := coordinator.BeginAutomatic(context.Background(), key, true)
	if err != nil {
		t.Fatalf("begin automatic mutation: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err = coordinator.BeginManual(ctx, key)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("manual mutation wait error = %v, want deadline exceeded", err)
	}
	if err := automatic.Validate(context.Background()); err != nil {
		t.Fatalf("timed-out manual wait must not invalidate active automatic work: %v", err)
	}
	automatic.Release()
}

func TestAutomaticSub2APIStatusMutation_UsesGenerationAndReadback(t *testing.T) {
	repo := newFakeRepository()
	safetyRepo := newMutationSafetyRepository()
	schedulable := true
	accounts := map[string][]upstream.AdminGroupAccountInfo{
		"g1": {{ID: "acc-1", Status: "inactive", Schedulable: &schedulable, Models: "gpt-4o"}},
	}
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "default"}}, accountsByGrp: accounts,
	}
	platform := &mutatingSub2APIPlatformActioner{}
	platform.afterStatus = func(status string) {
		updated := accounts["g1"]
		updated[0].Status = status
		accounts["g1"] = updated
	}
	service := &Service{
		repo: repo, safetyRepo: safetyRepo, mutationCoordinator: NewMutationCoordinator(safetyRepo),
		platformGroups: reader, dispatcher: newRemoteActionDispatcher(nil, nil, platform),
	}
	session := upstream.Session{Platform: upstream.PlatformSub2API}
	target := AdminProbeTarget{TargetID: "sub2api:ws1:acc-1", Platform: string(upstream.PlatformSub2API), AccountID: "acc-1"}
	action, err := service.applyAutomaticTargetState(context.Background(), "user1", "ws1", session, target, nil, "active", 0)
	if err != nil || action != RemoteActionSub2APIStatusActive || len(platform.sub2APICalls) != 1 {
		t.Fatalf("coordinated status mutation action=%q calls=%+v err=%v", action, platform.sub2APICalls, err)
	}
	if accounts["g1"][0].Status != "active" {
		t.Fatalf("status readback was not observed: %+v", accounts["g1"][0])
	}
	if _, err := safetyRepo.BumpMutationGeneration(context.Background(), "user1", "ws1", "acc-1"); err != nil {
		t.Fatalf("bump manual generation: %v", err)
	}
	if _, err := service.applyAutomaticTargetState(context.Background(), "user1", "ws1", session, target, nil, "inactive", 0); !errors.Is(err, errStaleMutation) {
		t.Fatalf("stale status intent error = %v, want stale mutation", err)
	}
	if len(platform.sub2APICalls) != 1 {
		t.Fatalf("stale status intent wrote upstream: %+v", platform.sub2APICalls)
	}
}

func TestAutomaticSub2APIStatusMutation_AcceptsMatchingReadbackAfterWriteError(t *testing.T) {
	repo := newFakeRepository()
	safetyRepo := newMutationSafetyRepository()
	schedulable := true
	accounts := map[string][]upstream.AdminGroupAccountInfo{
		"g1": {{ID: "acc-1", Status: "active", Schedulable: &schedulable, Models: "gpt-4o"}},
	}
	platform := &mutatingSub2APIPlatformActioner{returnErrorAfterWrite: errors.New("response lost")}
	platform.afterStatus = func(status string) {
		updated := accounts["g1"]
		updated[0].Status = status
		accounts["g1"] = updated
	}
	service := &Service{
		repo: repo, safetyRepo: safetyRepo, mutationCoordinator: NewMutationCoordinator(safetyRepo),
		platformGroups: fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1"}}, accountsByGrp: accounts},
		dispatcher:     newRemoteActionDispatcher(nil, nil, platform),
	}
	target := AdminProbeTarget{TargetID: "sub2api:ws1:acc-1", Platform: string(upstream.PlatformSub2API), AccountID: "acc-1"}
	action, err := service.applyAutomaticTargetState(context.Background(), "user1", "ws1",
		upstream.Session{Platform: upstream.PlatformSub2API}, target, nil, "inactive", 0)
	if err != nil || action != RemoteActionSub2APIStatusInactive || len(platform.sub2APICalls) != 1 {
		t.Fatalf("matching readback after status error action=%q calls=%+v err=%v", action, platform.sub2APICalls, err)
	}
}

func TestAutomaticSub2APIPriorityMutation_ReadsBackFieldOnlyValue(t *testing.T) {
	repo := newFakeRepository()
	safetyRepo := newMutationSafetyRepository()
	priority := 7
	accounts := map[string][]upstream.AdminGroupAccountInfo{
		"g1": {{ID: "acc-1", Status: "active", Priority: &priority, Models: "gpt-4o"}},
	}
	reader := fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "g1", Name: "default"}}, accountsByGrp: accounts,
	}
	actioner := &mutatingPriorityActioner{}
	actioner.afterUpdate = func(next int) {
		updated := accounts["g1"]
		updated[0].Priority = &next
		accounts["g1"] = updated
	}
	service := &Service{
		repo: repo, safetyRepo: safetyRepo, mutationCoordinator: NewMutationCoordinator(safetyRepo),
		platformGroups: reader, priorityActions: actioner,
	}
	err := service.updateAutomaticTargetPriority(context.Background(), "user1", "ws1",
		upstream.Session{Platform: upstream.PlatformSub2API}, "sub2api:ws1:acc-1", "acc-1", 10, 0)
	if err != nil || len(actioner.calls) != 1 || accounts["g1"][0].Priority == nil || *accounts["g1"][0].Priority != 10 {
		t.Fatalf("coordinated priority mutation calls=%+v account=%+v err=%v", actioner.calls, accounts["g1"][0], err)
	}
}

func TestAutomaticSub2APIPriorityMutation_AcceptsMatchingReadbackAfterWriteError(t *testing.T) {
	repo := newFakeRepository()
	safetyRepo := newMutationSafetyRepository()
	priority := 7
	accounts := map[string][]upstream.AdminGroupAccountInfo{
		"g1": {{ID: "acc-1", Status: "active", Priority: &priority, Models: "gpt-4o"}},
	}
	actioner := &mutatingPriorityActioner{returnErrorAfterWrite: errors.New("response lost")}
	actioner.afterUpdate = func(next int) {
		updated := accounts["g1"]
		updated[0].Priority = &next
		accounts["g1"] = updated
	}
	service := &Service{
		repo: repo, safetyRepo: safetyRepo, mutationCoordinator: NewMutationCoordinator(safetyRepo),
		platformGroups:  fakePlatformGroupReader{groups: []upstream.AdminGroupInfo{{ID: "g1"}}, accountsByGrp: accounts},
		priorityActions: actioner,
	}
	if err := service.updateAutomaticTargetPriority(context.Background(), "user1", "ws1",
		upstream.Session{Platform: upstream.PlatformSub2API}, "sub2api:ws1:acc-1", "acc-1", 10, 0); err != nil {
		t.Fatalf("matching priority readback after response loss: %v", err)
	}
	if len(actioner.calls) != 1 {
		t.Fatalf("priority response loss retried write: %+v", actioner.calls)
	}
}
