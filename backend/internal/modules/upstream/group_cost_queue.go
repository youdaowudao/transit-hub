package upstream

import (
	"context"
	"log"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

// The queue only retains local identity, dates and cancellation ownership. Login
// credentials are read when a worker actually starts, never retained in waiting work.
type groupCostWorkKey struct{ userID, adminAccountID, siteID string }

type groupCostWork struct {
	key          groupCostWorkKey
	date         string
	queuedAt     time.Time
	running      bool
	followupDate string
	ctx          context.Context
	cancel       context.CancelFunc
}

type groupCostQueue struct {
	ctx     context.Context
	cancel  context.CancelFunc
	pending []*groupCostWork
	entries map[groupCostWorkKey]*groupCostWork
	wake    chan struct{}
	done    chan struct{}
	workers int
}

type groupCostWorkProgress struct {
	status string
	depth  int
}

func costWorkKey(site *Site) groupCostWorkKey {
	return groupCostWorkKey{site.UserID, site.AdminAccountID, site.ID}
}

func (s *Service) enqueueGroupCostSampling(key groupCostWorkKey, date string) {
	if s.groupCostStore() == nil || date == "" {
		return
	}
	queued, depth := false, 0
	s.groupCostQueueMu.Lock()
	defer func() {
		s.groupCostQueueMu.Unlock()
		if queued {
			log.Printf("[upstream-cost] outcome=queued site_id=%s workspace_id=%s date=%s queue_depth=%d", key.siteID, key.adminAccountID, date, depth)
		}
	}()
	if s.groupCostClosed || s.groupCostDisabled {
		return
	}
	q := s.groupCostQueue
	if q == nil || q.ctx.Err() != nil {
		var previousDone <-chan struct{}
		if q != nil {
			previousDone = q.done
		}
		ctx, cancel := context.WithCancel(context.Background())
		q = &groupCostQueue{ctx: ctx, cancel: cancel, entries: make(map[groupCostWorkKey]*groupCostWork), wake: make(chan struct{}, 1), done: make(chan struct{}), workers: 2}
		s.groupCostQueue = q
		for range 2 {
			go s.runGroupCostWorker(q, previousDone)
		}
	}
	if work := q.entries[key]; work != nil {
		if work.running {
			work.followupDate = date
		} else {
			work.date = date
		}
		return
	}
	ctx, cancel := context.WithCancel(q.ctx)
	work := &groupCostWork{key: key, date: date, queuedAt: time.Now(), ctx: ctx, cancel: cancel}
	q.entries[key] = work
	q.pending = append(q.pending, work)
	s.wakeGroupCostWorkerLocked(q)
	queued, depth = true, len(q.pending)
}

func (s *Service) wakeGroupCostWorkerLocked(q *groupCostQueue) {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (s *Service) runGroupCostWorker(q *groupCostQueue, previousDone <-chan struct{}) {
	defer func() {
		s.groupCostQueueMu.Lock()
		q.workers--
		if q.workers == 0 {
			close(q.done)
		}
		s.groupCostQueueMu.Unlock()
	}()
	// A re-enabled generation cannot overlap the previous two workers. Its done
	// also includes every older generation, so shutdown has a single wait target.
	if previousDone != nil {
		<-previousDone
	}
	for {
		s.groupCostQueueMu.Lock()
		if q.ctx.Err() != nil {
			s.groupCostQueueMu.Unlock()
			return
		}
		var work *groupCostWork
		if len(q.pending) > 0 {
			work, q.pending = q.pending[0], q.pending[1:]
			work.running = true
			if len(q.pending) > 0 {
				s.wakeGroupCostWorkerLocked(q)
			}
		}
		s.groupCostQueueMu.Unlock()
		if work == nil {
			select {
			case <-q.ctx.Done():
				return
			case <-q.wake:
				continue
			}
		}
		s.runGroupCostWork(work)
		s.groupCostQueueMu.Lock()
		if q.entries[work.key] == work {
			if work.followupDate != "" && work.ctx.Err() == nil && q.ctx.Err() == nil {
				work.date, work.followupDate, work.running, work.queuedAt = work.followupDate, "", false, time.Now()
				q.pending = append(q.pending, work)
				s.wakeGroupCostWorkerLocked(q)
			} else {
				delete(q.entries, work.key)
				work.cancel()
			}
		}
		s.groupCostQueueMu.Unlock()
	}
}

func (s *Service) runGroupCostWork(work *groupCostWork) {
	outcome := groupCostAttemptOutcome{kind: "discarded", reason: "panic"}
	defer func() {
		if recover() != nil {
			// Panic values can include credentials or raw response content.
			outcome = groupCostAttemptOutcome{kind: "discarded", reason: "panic"}
		}
		if outcome.wait == 0 {
			outcome.wait = time.Since(work.queuedAt) - outcome.read - outcome.save
		}
		log.Printf("[upstream-cost] outcome=%s reason=%s site_id=%s workspace_id=%s date=%s wait_ms=%d read_ms=%d save_ms=%d groups=%d next_allowed_at=%s last_success_age_ms=%d", outcome.kind, outcome.reason, work.key.siteID, work.key.adminAccountID, work.date, outcome.wait.Milliseconds(), outcome.read.Milliseconds(), outcome.save.Milliseconds(), outcome.groups, outcome.nextAllowed.UTC().Format(time.RFC3339), outcome.lastSuccessAge.Milliseconds())
	}()
	if work.ctx.Err() != nil {
		outcome.kind, outcome.reason = "cancelled", "cancelled"
		return
	}
	readCtx, cancel := context.WithTimeout(work.ctx, persistenceTimeout)
	defer cancel()
	site, err := s.readGroupCostCandidate(readCtx, work.key)
	valid := err == nil && site != nil && readCtx.Err() == nil
	cancel()
	if !valid {
		outcome.kind, outcome.reason = "no_request", "site_unavailable"
		if err != nil {
			outcome.reason = "site_read_failed"
		}
		if work.ctx.Err() != nil {
			outcome.kind, outcome.reason = "cancelled", "cancelled"
		}
		return
	}
	if businesstime.DateAt(s.keyUsageNow()) != work.date {
		outcome.kind, outcome.reason = "discarded", "date_changed"
		return
	}
	outcome = s.sampleGroupCostsContext(work.ctx, *site, work.date)
	outcome.wait = time.Since(work.queuedAt) - outcome.read - outcome.save
}

func (s *Service) readGroupCostCandidate(ctx context.Context, key groupCostWorkKey) (*Site, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.cache.Get(ctx, key.siteID)
	if err != nil {
		return nil, err
	}
	_, deleted := s.deletedSites[key.siteID]
	if deleted || current == nil || costWorkKey(current) != key || !current.IsEnabled() {
		return nil, nil
	}
	copy := cloneSite(current)
	return &copy, nil
}

func (s *Service) cancelGroupCostWork(key groupCostWorkKey) {
	s.groupCostQueueMu.Lock()
	var cancelled *groupCostWork
	defer func() {
		s.groupCostQueueMu.Unlock()
		if cancelled != nil {
			log.Printf("[upstream-cost] outcome=cancelled site_id=%s workspace_id=%s date=%s", key.siteID, key.adminAccountID, cancelled.date)
		}
	}()
	q := s.groupCostQueue
	if q == nil {
		return
	}
	work := q.entries[key]
	if work == nil {
		return
	}
	delete(q.entries, key)
	work.followupDate = ""
	work.cancel()
	for i, pending := range q.pending {
		if pending == work {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			break
		}
	}
	cancelled = work
}

func (s *Service) groupCostWorkSnapshot(key groupCostWorkKey) groupCostWorkProgress {
	s.groupCostQueueMu.Lock()
	defer s.groupCostQueueMu.Unlock()
	q := s.groupCostQueue
	if q == nil {
		return groupCostWorkProgress{}
	}
	work := q.entries[key]
	if work == nil || work.ctx.Err() != nil {
		return groupCostWorkProgress{}
	}
	status := "queued"
	if work.running {
		status = "running"
	}
	return groupCostWorkProgress{status: status, depth: len(q.pending)}
}

// Caller holds only groupCostQueueMu; no cache IO or service-lock acquisition.
func (s *Service) stopGroupCostQueueLocked() <-chan struct{} {
	q := s.groupCostQueue
	if q == nil {
		return nil
	}
	q.cancel()
	for _, work := range q.entries {
		work.followupDate = ""
		work.cancel()
	}
	q.pending = nil
	clear(q.entries)
	return q.done
}

func (s *Service) ShutdownGroupCostSampling(ctx context.Context) error {
	s.groupCostQueueMu.Lock()
	s.groupCostClosed = true
	done := s.stopGroupCostQueueLocked()
	s.groupCostQueueMu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
