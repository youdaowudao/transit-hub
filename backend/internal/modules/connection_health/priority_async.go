package connection_health

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const (
	priorityAsyncRunTimeout   = 2 * time.Minute
	priorityStateWriteTimeout = 5 * time.Second
	priorityAsyncWorkerCount  = 2
	priorityAsyncQueueSize    = 64
)

var errPriorityAsyncPanic = errors.New("priority asynchronous synchronization panic")

var errPriorityStateWritePanic = errors.New("priority synchronization state write panic")

type priorityAsyncJob struct {
	service          *Service
	userID           string
	adminAccountID   string
	pendingSignature string
	healthSync       bool
}

// priorityAsyncDispatcher provides one process-wide bounded queue. A save can only
// reserve one workspace job; subsequent saves mark that workspace for one newer run.
// Workers exit when idle, preventing an inactive deployment from retaining workers.
var priorityAsyncDispatcher = struct {
	mu      sync.Mutex
	queue   chan priorityAsyncJob
	workers int
}{queue: make(chan priorityAsyncJob, priorityAsyncQueueSize)}

func enqueuePriorityAsync(job priorityAsyncJob) bool {
	select {
	case priorityAsyncDispatcher.queue <- job:
	default:
		return false
	}
	startPriorityAsyncWorkers()
	return true
}

func startPriorityAsyncWorkers() {
	priorityAsyncDispatcher.mu.Lock()
	defer priorityAsyncDispatcher.mu.Unlock()
	for priorityAsyncDispatcher.workers < priorityAsyncWorkerCount && len(priorityAsyncDispatcher.queue) > 0 {
		priorityAsyncDispatcher.workers++
		go runPriorityAsyncWorker()
	}
}

func runPriorityAsyncWorker() {
	for {
		select {
		case job := <-priorityAsyncDispatcher.queue:
			if job.healthSync {
				job.service.runQueuedHealthPrioritySync(job.userID, job.adminAccountID)
			} else {
				job.service.runQueuedPrioritySync(job.userID, job.adminAccountID, job.pendingSignature)
			}
		default:
			priorityAsyncDispatcher.mu.Lock()
			if len(priorityAsyncDispatcher.queue) == 0 {
				priorityAsyncDispatcher.workers--
				priorityAsyncDispatcher.mu.Unlock()
				return
			}
			priorityAsyncDispatcher.mu.Unlock()
		}
	}
}

func prioritySyncErrorDetail(err error) string {
	if err == nil {
		return ErrorUnknown
	}
	var requestErr requestError
	if errors.As(err, &requestErr) {
		return requestErr.Error()
	}
	var upstreamErr *upstream.RequestError
	if errors.As(err, &upstreamErr) && upstreamErr.MessageKey != "" {
		return upstreamErr.MessageKey
	}
	return ErrorUnknown
}

func (s *Service) priorityWorkspaceGenerationCurrent(ctx context.Context, userID string, adminAccountID string, pendingSignature string) (bool, error) {
	if pendingSignature == "" {
		return true, nil
	}
	current, err := s.repo.IsPriorityWorkspaceGenerationCurrent(ctx, userID, adminAccountID, pendingSignature)
	if err != nil {
		log.Printf("[connection-health] priority sync generation check failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
		return false, err
	}
	return current, nil
}

// Priority status must survive a cancelled HTTP/scheduler context. The write remains
// generation-guarded in SQL, so it cannot overwrite a newer save.
func (s *Service) markPriorityWorkspaceSyncFailed(userID string, adminAccountID string, pendingSignature string, syncErr error, failedCount int) {
	if pendingSignature == "" {
		s.markPriorityWorkspaceHealthSyncFailed(userID, adminAccountID, syncErr, failedCount)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), priorityStateWriteTimeout)
	defer cancel()
	errorDetail := prioritySyncErrorDetail(syncErr)
	marked, err, panicked := s.tryMarkPriorityWorkspaceSyncFailed(ctx, userID, adminAccountID, pendingSignature, errorDetail, failedCount)
	if panicked {
		// The row update is generation-guarded and idempotent for the same signature.
		// Retrying once makes a transient repository panic visible to the page as failed;
		// a persistent panic still returns safely and leaves reconciliation to the scheduler.
		log.Printf("[connection-health] priority sync failure state panic recovered user_id=%s admin_account_id=%s", userID, adminAccountID)
		marked, err, panicked = s.tryMarkPriorityWorkspaceSyncFailed(ctx, userID, adminAccountID, pendingSignature, errorDetail, failedCount)
		if panicked {
			log.Printf("[connection-health] priority sync failure state retry panic recovered user_id=%s admin_account_id=%s", userID, adminAccountID)
			return
		}
	}
	if err != nil {
		log.Printf("[connection-health] priority sync mark failed state failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
		return
	}
	if !marked {
		log.Printf("[connection-health] priority sync failure state skipped stale generation user_id=%s admin_account_id=%s", userID, adminAccountID)
	}
}

func (s *Service) markPriorityWorkspaceHealthSyncFailed(userID string, adminAccountID string, syncErr error, failedCount int) {
	ctx, cancel := context.WithTimeout(context.Background(), priorityStateWriteTimeout)
	defer cancel()
	allowWrite, err := s.priorityWorkspaceEmptySignatureWritable(ctx, userID, adminAccountID)
	if err != nil || !allowWrite {
		return
	}
	s.markPriorityWorkspaceHealthSyncFailedDirect(userID, adminAccountID, syncErr, failedCount)
}

func (s *Service) markPriorityWorkspaceHealthSyncFailedDirect(userID string, adminAccountID string, syncErr error, failedCount int) {
	ctx, cancel := context.WithTimeout(context.Background(), priorityStateWriteTimeout)
	defer cancel()
	errorDetail := prioritySyncErrorDetail(syncErr)
	marked, err := s.repo.MarkPriorityWorkspaceHealthSyncFailed(ctx, userID, adminAccountID, errorDetail, failedCount)
	if err != nil {
		log.Printf("[connection-health] priority sync health failure state failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
		return
	}
	if !marked {
		return
	}
}

func (s *Service) tryMarkPriorityWorkspaceSyncFailed(ctx context.Context, userID string, adminAccountID string, pendingSignature string, errorDetail string, failedCount int) (marked bool, err error, panicked bool) {
	defer func() {
		if recover() != nil {
			marked = false
			err = errPriorityStateWritePanic
			panicked = true
		}
	}()
	marked, err = s.repo.MarkPriorityWorkspaceSyncFailed(ctx, userID, adminAccountID, pendingSignature, errorDetail, failedCount)
	return marked, err, false
}

func (s *Service) markPriorityWorkspaceSyncSucceeded(userID string, adminAccountID string, pendingSignature string) {
	if pendingSignature == "" {
		s.markPriorityWorkspaceHealthSyncSucceeded(userID, adminAccountID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), priorityStateWriteTimeout)
	defer cancel()
	marked, err := s.repo.MarkPriorityWorkspaceSyncSucceeded(ctx, userID, adminAccountID, pendingSignature)
	if err != nil {
		log.Printf("[connection-health] priority sync mark success state failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
		return
	}
	if !marked {
		log.Printf("[connection-health] priority sync success state skipped stale generation user_id=%s admin_account_id=%s", userID, adminAccountID)
	}
}

func (s *Service) markPriorityWorkspaceHealthSyncSucceeded(userID string, adminAccountID string) {
	ctx, cancel := context.WithTimeout(context.Background(), priorityStateWriteTimeout)
	defer cancel()
	allowWrite, err := s.priorityWorkspaceEmptySignatureWritable(ctx, userID, adminAccountID)
	if err != nil || !allowWrite {
		return
	}
	marked, err := s.repo.MarkPriorityWorkspaceHealthSyncSucceeded(ctx, userID, adminAccountID)
	if err != nil {
		log.Printf("[connection-health] priority sync health success state failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
		return
	}
	if !marked {
		return
	}
}

func (s *Service) pendingPrioritySyncGeneration(ctx context.Context, userID string, adminAccountID string) (string, error) {
	state, err := s.repo.GetPriorityWorkspaceSyncState(ctx, userID, adminAccountID)
	if err != nil {
		return "", err
	}
	if state == nil {
		return "", nil
	}
	return state.PendingSignature, nil
}

func (s *Service) priorityWorkspaceEmptySignatureWritable(ctx context.Context, userID string, adminAccountID string) (bool, error) {
	state, err := s.repo.GetPriorityWorkspaceSyncState(ctx, userID, adminAccountID)
	if err != nil {
		log.Printf("[connection-health] priority sync load workspace state failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, err)
		return false, err
	}
	if state == nil {
		return false, nil
	}
	return state.PendingSignature == "", nil
}

// triggerPrioritySync starts one workspace worker after the local transaction commits.
// Every queued job carries the transaction's generation, so an older failure cannot
// mark a newer save as failed or hold it in backoff.
func (s *Service) triggerPrioritySync(userID string, adminAccountID string, pendingSignature string) {
	if pendingSignature == "" {
		return
	}
	if s.priorityActions == nil || s.platformGroups == nil {
		s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, pendingSignature, requestError(ErrorPrioritySyncUnavailable), 1)
		return
	}
	key := userID + "\x00" + adminAccountID
	s.priorityTriggerMu.Lock()
	if s.priorityTriggerRunning == nil {
		s.priorityTriggerRunning = make(map[string]bool)
	}
	if s.priorityTriggerPending == nil {
		s.priorityTriggerPending = make(map[string]string)
	}
	if s.priorityTriggerRunning[key] {
		s.priorityTriggerPending[key] = pendingSignature
		s.priorityTriggerMu.Unlock()
		return
	}
	s.priorityTriggerRunning[key] = true
	s.priorityTriggerMu.Unlock()

	if enqueuePriorityAsync(priorityAsyncJob{service: s, userID: userID, adminAccountID: adminAccountID, pendingSignature: pendingSignature}) {
		return
	}
	s.priorityTriggerMu.Lock()
	delete(s.priorityTriggerRunning, key)
	delete(s.priorityTriggerPending, key)
	s.priorityTriggerMu.Unlock()
	s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, pendingSignature, requestError(ErrorPrioritySyncUnavailable), 1)
}

// triggerHealthPrioritySyncAfterCommit queues a health-driven reconciliation after
// the health state has been committed. It deliberately has no generation signature:
// health sync must not overwrite configuration generations, and its durable failure
// state is guarded by an empty pending signature.
func (s *Service) triggerHealthPrioritySyncAfterCommit(userID string, adminAccountID string) {
	if s.priorityActions == nil || s.platformGroups == nil {
		s.markPriorityWorkspaceHealthSyncFailed(userID, adminAccountID, requestError(ErrorPrioritySyncUnavailable), 1)
		return
	}
	key := userID + "\x00" + adminAccountID
	s.priorityTriggerMu.Lock()
	if s.priorityHealthRunning == nil {
		s.priorityHealthRunning = make(map[string]bool)
	}
	if s.priorityHealthPending == nil {
		s.priorityHealthPending = make(map[string]bool)
	}
	if s.priorityHealthRunning[key] {
		s.priorityHealthPending[key] = true
		s.priorityTriggerMu.Unlock()
		return
	}
	s.priorityHealthRunning[key] = true
	s.priorityTriggerMu.Unlock()

	if enqueuePriorityAsync(priorityAsyncJob{
		service:        s,
		userID:         userID,
		adminAccountID: adminAccountID,
		healthSync:     true,
	}) {
		return
	}
	s.startHealthPriorityFailureFallback(key, userID, adminAccountID)
}

func (s *Service) runQueuedPrioritySync(userID string, adminAccountID string, pendingSignature string) {
	s.runQueuedPrioritySyncWithKind(userID, adminAccountID, pendingSignature, false)
}

func (s *Service) runQueuedHealthPrioritySync(userID string, adminAccountID string) {
	s.runQueuedPrioritySyncWithKind(userID, adminAccountID, "", true)
}

func (s *Service) startHealthPriorityFailureFallback(key string, userID string, adminAccountID string) {
	go s.runHealthPriorityFailureFallback(key, userID, adminAccountID)
}

func (s *Service) runHealthPriorityFailureFallback(key string, userID string, adminAccountID string) {
	defer func() {
		if recover() != nil {
			log.Printf("[connection-health] health priority failure fallback panic recovered user_id=%s admin_account_id=%s", userID, adminAccountID)
		}
		s.finishHealthPriorityFailureFallback(key, userID, adminAccountID)
	}()
	s.markPriorityWorkspaceHealthSyncFailed(userID, adminAccountID, requestError(ErrorPrioritySyncUnavailable), 1)
}

func (s *Service) finishHealthPriorityFailureFallback(key string, userID string, adminAccountID string) {
	s.priorityTriggerMu.Lock()
	nextHealthSync := s.priorityHealthPending[key]
	if nextHealthSync {
		delete(s.priorityHealthPending, key)
	} else {
		delete(s.priorityHealthRunning, key)
	}
	s.priorityTriggerMu.Unlock()
	if !nextHealthSync {
		return
	}
	if enqueuePriorityAsync(priorityAsyncJob{
		service:        s,
		userID:         userID,
		adminAccountID: adminAccountID,
		healthSync:     true,
	}) {
		return
	}
	s.priorityTriggerMu.Lock()
	delete(s.priorityHealthRunning, key)
	s.priorityTriggerMu.Unlock()
}

func (s *Service) runQueuedPrioritySyncWithKind(userID string, adminAccountID string, pendingSignature string, healthSync bool) {
	key := userID + "\x00" + adminAccountID
	// This recovery covers not only the synchronization itself but also failure-state
	// persistence. The deferred trigger cleanup always runs first, so a broken error
	// path cannot leave this workspace permanently reserved in the in-memory queue.
	defer func() {
		if recover() != nil {
			log.Printf("[connection-health] asynchronous priority job panic recovered user_id=%s admin_account_id=%s", userID, adminAccountID)
		}
	}()
	defer s.finishQueuedPrioritySync(key, userID, adminAccountID, healthSync)
	runErr := func() (syncErr error) {
		defer func() {
			if recover() != nil {
				log.Printf("[connection-health] asynchronous priority sync panic recovered user_id=%s admin_account_id=%s", userID, adminAccountID)
				syncErr = errPriorityAsyncPanic
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), priorityAsyncRunTimeout)
		defer cancel()
		return s.syncCurrentWorkspacePrioritiesWithResult(ctx, userID, adminAccountID, pendingSignature)
	}()
	if runErr != nil {
		log.Printf("[connection-health] asynchronous priority sync failed user_id=%s admin_account_id=%s err=%v", userID, adminAccountID, runErr)
		s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, pendingSignature, runErr, 1)
	}

}

// finishQueuedPrioritySync releases the in-memory reservation on every exit path.
// Only a save that arrived after the job was reserved gets an immediate second run;
// ordinary failures continue through the persisted scheduler backoff.
func (s *Service) finishQueuedPrioritySync(key string, userID string, adminAccountID string, healthSync bool) {
	s.priorityTriggerMu.Lock()
	if healthSync {
		nextHealthSync := s.priorityHealthPending[key]
		if nextHealthSync {
			delete(s.priorityHealthPending, key)
		} else {
			delete(s.priorityHealthRunning, key)
		}
		s.priorityTriggerMu.Unlock()
		if !nextHealthSync {
			return
		}
		if enqueuePriorityAsync(priorityAsyncJob{
			service:        s,
			userID:         userID,
			adminAccountID: adminAccountID,
			healthSync:     true,
		}) {
			return
		}
		s.startHealthPriorityFailureFallback(key, userID, adminAccountID)
		return
	}
	nextPendingSignature := s.priorityTriggerPending[key]
	if nextPendingSignature != "" {
		delete(s.priorityTriggerPending, key)
	} else {
		delete(s.priorityTriggerRunning, key)
	}
	s.priorityTriggerMu.Unlock()
	if nextPendingSignature == "" {
		return
	}
	if enqueuePriorityAsync(priorityAsyncJob{service: s, userID: userID, adminAccountID: adminAccountID, pendingSignature: nextPendingSignature}) {
		return
	}
	s.priorityTriggerMu.Lock()
	delete(s.priorityTriggerRunning, key)
	delete(s.priorityTriggerPending, key)
	s.priorityTriggerMu.Unlock()
	s.markPriorityWorkspaceSyncFailed(userID, adminAccountID, nextPendingSignature, requestError(ErrorPrioritySyncUnavailable), 1)
}
