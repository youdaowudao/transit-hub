package dashboard

import (
	"context"
	"errors"
	"sort"
	"time"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

type costRecoveryDateReader interface {
	ListCostRecoveryDates(ctx context.Context, userID, adminAccountID, siteID, recoveryBusinessDate string) ([]string, error)
}

type costRecoveryWorkspace struct {
	userID         string
	adminAccountID string
}

type costRecoveryJob struct {
	done        chan struct{}
	changed     chan struct{}
	registering int
	seen        map[string]struct{}
	pending     []string
	err         error
}

// RecoverSiteCostsAfterSync joins the workspace's current recovery round.
// The upstream callback already runs asynchronously; callers wait for that
// round so its errors and completion are observable without another worker.
func (s *MetricsService) RecoverSiteCostsAfterSync(ctx context.Context, userID, adminAccountID, siteID string, oldMetrics, newMetrics upstream.Metrics, oldStatus, newStatus upstream.Status) error {
	if oldMetrics.TodayConsumeStatus != "unreadable" && oldStatus != upstream.StatusError {
		return nil
	}
	date := businesstime.DateAt(s.currentTime())
	if newStatus != upstream.StatusConnected || newMetrics.TodayConsume.Value == nil ||
		newMetrics.TodayConsumeDate != date || (newMetrics.TodayConsumeStatus != "" && newMetrics.TodayConsumeStatus != "ok") {
		return nil
	}
	reader, ok := s.metricsRepo.(costRecoveryDateReader)
	if !ok {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	workspace := costRecoveryWorkspace{userID: userID, adminAccountID: adminAccountID}
	s.costRecoveryMu.Lock()
	if s.costRecoveryJobs == nil {
		s.costRecoveryJobs = make(map[costRecoveryWorkspace]*costRecoveryJob)
	}
	job, exists := s.costRecoveryJobs[workspace]
	if !exists {
		job = &costRecoveryJob{done: make(chan struct{}), changed: make(chan struct{}, 1), seen: make(map[string]struct{})}
		s.costRecoveryJobs[workspace] = job
	}
	// Register before the database read. The current round must not end while
	// another restored site's target dates are still being discovered.
	job.registering++
	s.costRecoveryMu.Unlock()
	dates, readErr := reader.ListCostRecoveryDates(ctx, userID, adminAccountID, siteID, date)
	s.costRecoveryMu.Lock()
	job.registering--
	job.err = errors.Join(job.err, readErr)
	for _, date := range dates {
		if _, seen := job.seen[date]; !seen {
			job.seen[date] = struct{}{}
			job.pending = append(job.pending, date)
		}
	}
	select {
	case job.changed <- struct{}{}:
	default:
	}
	s.costRecoveryMu.Unlock()
	if !exists {
		s.runCostRecoveryJob(ctx, workspace, job)
		return job.err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-job.done:
		return job.err
	}
}

func (s *MetricsService) runCostRecoveryJob(ctx context.Context, workspace costRecoveryWorkspace, job *costRecoveryJob) {
	first := true
	for {
		s.costRecoveryMu.Lock()
		if ctx.Err() != nil {
			job.err = errors.Join(job.err, ctx.Err())
			job.pending = nil
		}
		if len(job.pending) == 0 {
			if job.registering == 0 {
				delete(s.costRecoveryJobs, workspace)
				close(job.done)
				s.costRecoveryMu.Unlock()
				return
			}
			s.costRecoveryMu.Unlock()
			<-job.changed
			continue
		}
		sort.Strings(job.pending)
		date := job.pending[0]
		job.pending = job.pending[1:]
		s.costRecoveryMu.Unlock()

		if !first {
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
		first = false
		ref := ActiveSessionRef{UserID: workspace.userID, AdminAccountID: workspace.adminAccountID}
		err := s.finalizeBusinessDate(ctx, ref, date, SnapshotSourceDatedQuery)
		s.costRecoveryMu.Lock()
		job.err = errors.Join(job.err, err)
		s.costRecoveryMu.Unlock()
	}
}

type costFinalizationKey struct {
	costRecoveryWorkspace
	date string
}

type costFinalizationLock struct {
	gate chan struct{}
	refs int
}

// Each caller takes the same workspace/date lock, including scheduled,
// startup and recovery finalizations. Waiting callers run once the previous
// attempt ends: a preceding attempt may have begun before the site recovered.
func (s *MetricsService) acquireCostFinalization(ctx context.Context, ref ActiveSessionRef, date string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := costFinalizationKey{costRecoveryWorkspace: costRecoveryWorkspace{userID: ref.UserID, adminAccountID: ref.AdminAccountID}, date: date}
	s.finalizationMu.Lock()
	if s.finalizationLocks == nil {
		s.finalizationLocks = make(map[costFinalizationKey]*costFinalizationLock)
	}
	lock := s.finalizationLocks[key]
	if lock == nil {
		lock = &costFinalizationLock{gate: make(chan struct{}, 1)}
		s.finalizationLocks[key] = lock
	}
	lock.refs++
	s.finalizationMu.Unlock()
	dropRef := func() {
		s.finalizationMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.finalizationLocks, key)
		}
		s.finalizationMu.Unlock()
	}
	select {
	case <-ctx.Done():
		dropRef()
		return nil, ctx.Err()
	case lock.gate <- struct{}{}:
	}
	release := func() {
		<-lock.gate
		dropRef()
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
