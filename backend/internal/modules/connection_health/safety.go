package connection_health

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Abnormal queue sources are deliberately separate from the normal scanner and
// priority queues. Emergency clearing is only allowed to touch this source.
const (
	SafetySourceHealthIncident = "health_incident"

	QueueKindConfirmation   = "confirmation"
	QueueKindCanary         = "canary"
	QueueKindStatusIntent   = "sub2api_status_intent"
	QueueKindPriorityIntent = "incident_priority_intent"

	QueueStateQueued      = "queued"
	QueueStateClaimed     = "claimed"
	QueueStateDispatching = "dispatching"
	QueueStateCompleted   = "completed"
	QueueStateCancelled   = "cancelled"
	QueueStateGuardHeld   = "guard-held"

	CircuitClosed   = "closed"
	CircuitOpen     = "open"
	CircuitHalfOpen = "half_open"

	safetyQueueClaimTTL = 2 * time.Minute
)

var (
	errInvalidSafetySettings = errors.New("invalid connection health safety settings")
	errStaleMutation         = errors.New("stale automatic mutation generation")
	errMutationLeaseBusy     = errors.New("sub2api account mutation is dispatching")
)

// SafetySettings is persisted per workspace. The values are bounded so a
// configuration change cannot disable confirmation or exceed the existing 5/2
// probe concurrency.
type SafetySettings struct {
	UserID                       string    `json:"-"`
	WorkspaceID                  string    `json:"-"`
	ConfirmationObservationCount int       `json:"confirmationObservationCount"`
	ConfirmationDelaysSeconds    []int     `json:"confirmationDelaysSeconds"`
	ConfirmationJitterSeconds    int       `json:"confirmationJitterSeconds"`
	AbnormalQueueCapacity        int       `json:"abnormalQueueCapacity"`
	ManualReservedSlots          int       `json:"manualReservedSlots"`
	UpdatedAt                    time.Time `json:"updatedAt"`
	UpdatedBy                    string    `json:"updatedBy,omitempty"`
}

type SafetyWorkspaceView struct {
	Settings             SafetySettings        `json:"settings"`
	Queue                SafetyQueueSummary    `json:"queue"`
	LatestEmergencyClear *EmergencyClearResult `json:"latestEmergencyClear,omitempty"`
}

type SafetyQueueSummary struct {
	Queued      int `json:"queued"`
	Claimed     int `json:"claimed"`
	Dispatching int `json:"dispatching"`
	GuardHeld   int `json:"guardHeld"`
	Incidents   int `json:"incidents"`
}

func DefaultSafetySettings() SafetySettings {
	return SafetySettings{
		ConfirmationObservationCount: 4,
		ConfirmationDelaysSeconds:    []int{2, 5, 10},
		ConfirmationJitterSeconds:    1,
		AbnormalQueueCapacity:        64,
		ManualReservedSlots:          1,
	}
}

func (s SafetySettings) Validate() error {
	if s.ConfirmationObservationCount < 3 || s.ConfirmationObservationCount > 5 {
		return fmt.Errorf("confirmation observation count must be 3-5: %w", errInvalidSafetySettings)
	}
	if len(s.ConfirmationDelaysSeconds) != s.ConfirmationObservationCount-1 {
		return fmt.Errorf("confirmation delays must contain count-1 values: %w", errInvalidSafetySettings)
	}
	total := 0
	for _, delay := range s.ConfirmationDelaysSeconds {
		if delay < 1 || delay > 30 {
			return fmt.Errorf("confirmation delay must be 1-30 seconds: %w", errInvalidSafetySettings)
		}
		total += delay
	}
	if total > 60 {
		return fmt.Errorf("confirmation delays must total at most 60 seconds: %w", errInvalidSafetySettings)
	}
	if s.ConfirmationJitterSeconds < 0 || s.ConfirmationJitterSeconds > 3 {
		return fmt.Errorf("confirmation jitter must be 0-3 seconds: %w", errInvalidSafetySettings)
	}
	if s.AbnormalQueueCapacity < 16 || s.AbnormalQueueCapacity > 256 {
		return fmt.Errorf("abnormal queue capacity must be 16-256: %w", errInvalidSafetySettings)
	}
	if s.ManualReservedSlots < 0 || s.ManualReservedSlots > 1 {
		return fmt.Errorf("manual reserved slots must be 0-1: %w", errInvalidSafetySettings)
	}
	return nil
}

func normalizeSafetySettings(settings SafetySettings) SafetySettings {
	defaults := DefaultSafetySettings()
	if settings.ConfirmationObservationCount == 0 {
		settings.ConfirmationObservationCount = defaults.ConfirmationObservationCount
	}
	if len(settings.ConfirmationDelaysSeconds) == 0 {
		settings.ConfirmationDelaysSeconds = append([]int(nil), defaults.ConfirmationDelaysSeconds...)
	}
	if settings.ConfirmationJitterSeconds == 0 && settings.UpdatedAt.IsZero() {
		settings.ConfirmationJitterSeconds = defaults.ConfirmationJitterSeconds
	}
	if settings.AbnormalQueueCapacity == 0 {
		settings.AbnormalQueueCapacity = defaults.AbnormalQueueCapacity
	}
	if settings.ManualReservedSlots == 0 && settings.UpdatedAt.IsZero() {
		settings.ManualReservedSlots = defaults.ManualReservedSlots
	}
	return settings
}

type AbnormalQueueItem struct {
	ID                 string     `json:"id"`
	UserID             string     `json:"-"`
	WorkspaceID        string     `json:"workspaceId"`
	TargetID           string     `json:"targetId"`
	AccountID          string     `json:"accountId"`
	ModelName          string     `json:"modelName"`
	ProviderFamily     string     `json:"providerFamily"`
	ProbePrompt        string     `json:"-"`
	MaxProbeTokens     int        `json:"-"`
	Kind               string     `json:"kind"`
	Source             string     `json:"source"`
	IncidentID         string     `json:"incidentId"`
	FaultDomain        string     `json:"faultDomain"`
	ObservationEpoch   int64      `json:"observationEpoch"`
	NormalGeneration   int64      `json:"normalGeneration"`
	QueueEpoch         int64      `json:"queueEpoch"`
	Attempt            int        `json:"attempt"`
	RequiredAttempts   int        `json:"requiredAttempts"`
	ConfirmationDelays []int      `json:"-"`
	ConfirmationJitter int        `json:"-"`
	NextAttemptAt      time.Time  `json:"nextAttemptAt"`
	ActionKey          string     `json:"actionKey"`
	MutationGeneration int64      `json:"mutationGeneration"`
	State              string     `json:"state"`
	ClaimedBy          string     `json:"-"`
	ClaimExpiresAt     *time.Time `json:"-"`
	ExpectedResult     string     `json:"expectedResult"`
	LastResult         string     `json:"lastResult"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

type IncidentCircuitState struct {
	ID                     string    `json:"id"`
	UserID                 string    `json:"-"`
	WorkspaceID            string    `json:"workspaceId"`
	FaultDomain            string    `json:"faultDomain"`
	State                  string    `json:"state"`
	NormalGeneration       int64     `json:"normalGeneration"`
	CanaryTargetID         string    `json:"canaryTargetId"`
	SuccessfulCanaryTarget string    `json:"successfulCanaryTargetId"`
	UpdatedAt              time.Time `json:"updatedAt"`
}

type SafetyInventorySnapshot struct {
	UserID      string
	WorkspaceID string
	Generation  int64
	Complete    bool
	ExpiresAt   time.Time
	Accounts    []SafetyInventoryAccount
}

type SafetyInventoryAccount struct {
	AccountID              string
	TargetID               string
	Active                 bool
	Schedulable            bool
	StatusKnown            bool
	SchedulableKnown       bool
	CapabilityKnown        bool
	MembershipKnown        bool
	Models                 []string
	GroupIDs               []string
	LastSuccessAt          time.Time
	ConfirmedFailureModels int
}

type FloorReservationRequest struct {
	UserID             string
	WorkspaceID        string
	AccountID          string
	IncidentID         string
	ControlledGroupIDs []string
	ControlledModels   []string
	ExpectedGeneration int64
	ReservationTTL     time.Duration
}

type FloorReservation struct {
	ID         string
	Generation int64
	GuardHeld  bool
	Reason     string
}

type RepositoryMutationLease struct {
	Generation   int64
	FencingToken int64
	Validate     func(context.Context) error
	Release      func()
}

type EmergencyClearResult struct {
	WorkspaceID string    `json:"workspaceId"`
	QueueEpoch  int64     `json:"queueEpoch"`
	Cancelled   int       `json:"cancelled"`
	Incidents   int       `json:"incidents"`
	Dispatching int       `json:"dispatching"`
	CompletedAt time.Time `json:"completedAt"`
	Idempotent  bool      `json:"idempotent"`
}

type MutationKey struct {
	UserID      string
	WorkspaceID string
	AccountID   string
}

type safetyMutationRepository interface {
	GetSafetySettings(ctx context.Context, userID, workspaceID string) (SafetySettings, error)
	UpsertSafetySettings(ctx context.Context, settings SafetySettings) error
	GetLatestEmergencyClear(ctx context.Context, userID, workspaceID string) (*EmergencyClearResult, error)
	GetSafetyQueueSummary(ctx context.Context, userID, workspaceID string) (SafetyQueueSummary, error)
	GetAbnormalQueueEpoch(ctx context.Context, userID, workspaceID string) (int64, error)
	EnqueueAbnormalQueueItem(ctx context.Context, item AbnormalQueueItem, capacity int) (AbnormalQueueItem, bool, error)
	ClaimAbnormalQueueItem(ctx context.Context, workerID string, now time.Time) (*AbnormalQueueItem, error)
	HeartbeatAbnormalQueueClaim(ctx context.Context, id, workerID string) error
	RequeueAbnormalQueueItem(ctx context.Context, item AbnormalQueueItem, workerID string) error
	RescheduleDispatchedAbnormalQueueItem(ctx context.Context, item AbnormalQueueItem, workerID string) error
	RescheduleUncertainStatusDispatch(ctx context.Context, item AbnormalQueueItem, workerID string) error
	CancelAbnormalQueueItem(ctx context.Context, id, workerID, reason string) error
	MarkAbnormalQueueDispatching(ctx context.Context, id, workerID string, queueEpoch int64) (bool, error)
	CompleteAbnormalQueueItem(ctx context.Context, id, workerID, result string) error
	ObserveIncidentFailure(ctx context.Context, item AbnormalQueueItem, canaryTargetID string) (IncidentCircuitState, bool, error)
	GetIncidentCircuit(ctx context.Context, userID, workspaceID, faultDomain string) (*IncidentCircuitState, error)
	TargetCircuitOpen(ctx context.Context, userID, workspaceID, targetID string) (bool, error)
	GetTargetFaultEndpoint(ctx context.Context, userID, workspaceID, targetID string) (string, error)
	UpsertTargetFaultEndpoint(ctx context.Context, userID, workspaceID, targetID, endpoint string) error
	AnyIncidentCircuitOpen(ctx context.Context, userID, workspaceID string) (bool, error)
	AdvanceIncidentCanary(ctx context.Context, item AbnormalQueueItem, succeeded bool, nextTargetID string, now time.Time) (IncidentCircuitState, error)
	PersistSafetyInventorySnapshot(ctx context.Context, snapshot SafetyInventorySnapshot) error
	ReserveSafetyFloor(ctx context.Context, request FloorReservationRequest, now time.Time) (FloorReservation, error)
	MarkFloorReservationDispatching(ctx context.Context, reservationID string, now time.Time) error
	CompleteFloorReservation(ctx context.Context, reservationID string, readback bool, snapshotInvalidated bool, now time.Time) error
	ReleaseFloorReservation(ctx context.Context, reservationID string) error
	AbandonFloorReservationBeforeDispatch(ctx context.Context, reservationID string) error
	ResolveIncidentFloorReservations(ctx context.Context, userID, workspaceID, accountID, incidentID string, snapshotInvalidated bool, now time.Time) error
	EmergencyClear(ctx context.Context, userID, workspaceID, idempotencyKey string, now time.Time) (EmergencyClearResult, error)
	AcquireMutationLease(ctx context.Context, userID, workspaceID, accountID string, wait bool) (RepositoryMutationLease, error)
	BumpMutationGeneration(ctx context.Context, userID, workspaceID, accountID string) (int64, error)
	MutationGeneration(ctx context.Context, userID, workspaceID, accountID string) (int64, error)
}

func (k MutationKey) String() string { return k.UserID + "|" + k.WorkspaceID + "|" + k.AccountID }

type mutationLock struct {
	mu         sync.Mutex
	generation atomic.Int64
}

type MutationSession struct {
	Generation   int64
	FencingToken int64
	validate     func(context.Context) error
	release      func()
	once         sync.Once
}

func (s *MutationSession) Validate(ctx context.Context) error {
	if s == nil || s.validate == nil {
		return nil
	}
	return s.validate(ctx)
}

func (s *MutationSession) Release() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.release != nil {
			s.release()
		}
	})
}

// MutationCoordinator is the process-local half of the Sub2API account fencing
// contract. Repository-backed leases/generations extend it across processes.
type MutationCoordinator struct {
	repo  safetyMutationRepository
	mu    sync.Mutex
	locks map[string]*mutationLock
}

func NewMutationCoordinator(repo safetyMutationRepository) *MutationCoordinator {
	return &MutationCoordinator{repo: repo, locks: make(map[string]*mutationLock)}
}

func (c *MutationCoordinator) lockFor(key MutationKey) *mutationLock {
	c.mu.Lock()
	defer c.mu.Unlock()
	lock := c.locks[key.String()]
	if lock == nil {
		lock = &mutationLock{}
		c.locks[key.String()] = lock
	}
	return lock
}

func (c *MutationCoordinator) BeginAutomatic(ctx context.Context, key MutationKey, wait bool) (*MutationSession, error) {
	lock := c.lockFor(key)
	if !wait && !tryMutex(&lock.mu) {
		return nil, errMutationLeaseBusy
	}
	if wait {
		if err := lockMutex(ctx, &lock.mu); err != nil {
			return nil, err
		}
	}
	if c.repo != nil {
		lease, err := c.repo.AcquireMutationLease(ctx, key.UserID, key.WorkspaceID, key.AccountID, wait)
		if err != nil {
			lock.mu.Unlock()
			return nil, err
		}
		lock.generation.Store(lease.Generation)
		return &MutationSession{
			Generation: lease.Generation, FencingToken: lease.FencingToken, validate: lease.Validate,
			release: func() { lease.Release(); lock.mu.Unlock() },
		}, nil
	}
	// Automatic work belongs to the current manual generation. It must never
	// create a new one: doing so would let a queued automatic action outlive a
	// manual command that was issued immediately before it acquired the lock.
	generation := lock.generation.Load()
	return &MutationSession{Generation: generation, validate: func(context.Context) error {
		if lock.generation.Load() != generation {
			return errStaleMutation
		}
		return nil
	}, release: lock.mu.Unlock}, nil
}

func (c *MutationCoordinator) BeginManual(ctx context.Context, key MutationKey) (*MutationSession, error) {
	lock := c.lockFor(key)
	waitCtx, cancel := context.WithTimeout(ctx, 2*probeTimeout())
	defer cancel()
	if c.repo != nil {
		generation, err := c.repo.BumpMutationGeneration(ctx, key.UserID, key.WorkspaceID, key.AccountID)
		if err != nil {
			return nil, err
		}
		if err := lockMutex(waitCtx, &lock.mu); err != nil {
			return nil, err
		}
		lease, err := c.repo.AcquireMutationLease(waitCtx, key.UserID, key.WorkspaceID, key.AccountID, true)
		if err != nil {
			lock.mu.Unlock()
			return nil, err
		}
		if lease.Generation != generation {
			lease.Release()
			lock.mu.Unlock()
			return nil, errStaleMutation
		}
		lock.generation.Store(generation)
		return &MutationSession{
			Generation: generation, FencingToken: lease.FencingToken, validate: lease.Validate,
			release: func() { lease.Release(); lock.mu.Unlock() },
		}, nil
	}
	generation := lock.generation.Add(1)
	if err := lockMutex(waitCtx, &lock.mu); err != nil {
		return nil, err
	}
	return &MutationSession{Generation: generation, validate: func(context.Context) error {
		if lock.generation.Load() != generation {
			return errStaleMutation
		}
		return nil
	}, release: lock.mu.Unlock}, nil
}

func (c *MutationCoordinator) Generation(ctx context.Context, key MutationKey) (int64, error) {
	if c.repo != nil {
		return c.repo.MutationGeneration(ctx, key.UserID, key.WorkspaceID, key.AccountID)
	}
	return c.lockFor(key).generation.Load(), nil
}

func (c *MutationCoordinator) ValidateGeneration(ctx context.Context, key MutationKey, generation int64) error {
	current, err := c.Generation(ctx, key)
	if err != nil {
		return err
	}
	if current != generation {
		return errStaleMutation
	}
	return nil
}

func tryMutex(mu *sync.Mutex) bool { return mu.TryLock() }

func lockMutex(ctx context.Context, mu *sync.Mutex) error {
	for {
		if mu.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func probeTimeout() time.Duration { return 10 * time.Second }

// confirmationDelay returns the configured minimum delay plus a bounded jitter.
// The random component is intentionally only additive: attempts never fire early.
func confirmationDelay(settings SafetySettings, attempt int, random func(int) int) time.Duration {
	if attempt <= 0 || attempt > len(settings.ConfirmationDelaysSeconds) {
		return 0
	}
	jitter := 0
	if settings.ConfirmationJitterSeconds > 0 {
		if random == nil {
			random = rand.Intn
		}
		jitter = random(settings.ConfirmationJitterSeconds + 1)
	}
	return time.Duration(settings.ConfirmationDelaysSeconds[attempt-1]+jitter) * time.Second
}

func stableSurvivor(accounts []SurvivorCandidate) (SurvivorCandidate, bool) {
	eligible := make([]SurvivorCandidate, 0, len(accounts))
	for _, account := range accounts {
		if account.StatusKnown && account.SchedulableKnown && account.CapabilityKnown && account.MembershipKnown && account.Active && account.Schedulable {
			eligible = append(eligible, account)
		}
	}
	if len(eligible) == 0 {
		return SurvivorCandidate{}, false
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if !eligible[i].LastSuccessAt.Equal(eligible[j].LastSuccessAt) {
			return eligible[i].LastSuccessAt.After(eligible[j].LastSuccessAt)
		}
		if eligible[i].ConfirmedFailureModels != eligible[j].ConfirmedFailureModels {
			return eligible[i].ConfirmedFailureModels < eligible[j].ConfirmedFailureModels
		}
		return eligible[i].AccountID < eligible[j].AccountID
	})
	return eligible[0], true
}

type SurvivorCandidate struct {
	AccountID              string
	Active                 bool
	Schedulable            bool
	StatusKnown            bool
	SchedulableKnown       bool
	CapabilityKnown        bool
	MembershipKnown        bool
	LastSuccessAt          time.Time
	ConfirmedFailureModels int
}
