package chanshui

import (
	"context"
	"sync"
	"time"
)

func lockContext(ctx context.Context, mu *sync.Mutex) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if mu.TryLock() {
			if err := ctx.Err(); err != nil {
				mu.Unlock()
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func admissionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 30*time.Second)
}

func (s *Service) lockAdmission(ctx context.Context) error {
	if err := lockContext(ctx, &s.admission); err != nil {
		return problem(503, "queue", "等待检测受理或结束保存超时，本次尚未受理，请核查原编号和运行状态后重试")
	}
	return nil
}

func activeExited(a *activeTask) bool {
	return a != nil && !a.Preparing && (a.Job == nil || a.Job.Info().Exited)
}
func activeDone(a *activeTask) bool {
	if activeExited(a) {
		return true
	}
	if a != nil && !a.Preparing && a.Job != nil {
		select {
		case <-a.Job.Done:
			return true
		default:
		}
	}
	return false
}
func (s *Service) mayFinalizeBatchLocked(owner Owner, batchID string, finishing *activeTask, blocked *blockedReport) bool {
	if batchID == "" {
		return false
	}
	if a := s.active; a != nil && a.Owner == owner && a.BatchID == batchID && a != finishing {
		return false
	}
	for _, b := range s.blocked {
		if b.Owner == owner && b.BatchID == batchID && b != blocked {
			return false
		}
	}
	return true
}

func (s *Service) registerBlockedLocked(owner Owner, id, batchID, retryID, message string, cancelQueued bool) *blockedReport {
	if s.blocked == nil {
		s.blocked = map[reportKey]*blockedReport{}
	}
	key := reportKey{owner, id}
	if existing := s.blocked[key]; existing != nil {
		return existing // Repeated acceptance never refreshes an exhausted event.
	}
	entry := &blockedReport{Owner: owner, ID: id, BatchID: batchID, RetryID: retryID, Message: message, CancelQueued: cancelQueued}
	s.blocked[key] = entry
	s.scheduleRecoveryLocked()
	return entry
}

func (s *Service) scheduleRecoveryLocked() {
	if s.closing || s.recoveryRunning {
		return
	}
	pending := activeExited(s.active) && !s.active.RecoveryAttempted
	for _, b := range s.blocked {
		if !b.RecoveryAttempted {
			pending = true
			break
		}
	}
	if !pending {
		return
	}
	s.recoveryRunning = true
	s.recoveryDeadline = time.Now().Add(40 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), s.recoveryDeadline.Add(-10*time.Second))
	s.recoveryCancel = cancel
	go s.recoverWithBudget(ctx, cancel, s.recoveryDeadline)
}

func (s *Service) recoverWithBudget(ctx context.Context, cancel context.CancelFunc, deadline time.Time) {
	defer cancel()
	s.mu.Lock()
	var active *activeTask
	if activeExited(s.active) && !s.active.RecoveryAttempted {
		active = s.active
		active.RecoveryAttempted = true
		active.recoveryDeadlineMs = deadline.UnixMilli()
	}
	entries := []*blockedReport{}
	for _, b := range s.blocked {
		if !b.RecoveryAttempted {
			b.RecoveryAttempted = true
			b.recoveryDeadlineMs = deadline.UnixMilli()
			entries = append(entries, b)
		}
	}
	s.mu.Unlock()
	for pass := 0; pass < 3 && ctx.Err() == nil; pass++ {
		if s.lockAdmission(ctx) != nil {
			break
		}
		s.mu.Lock()
		closing := s.closing
		s.mu.Unlock()
		if closing {
			s.admission.Unlock()
			break
		}
		if active != nil {
			if err := s.recoverExited(ctx, active); err == nil {
				active = nil
			}
		}
		remaining := entries[:0]
		for _, b := range entries {
			if ctx.Err() != nil {
				remaining = append(remaining, b)
				continue
			}
			if err := s.retryBlockedLocked(ctx, b); err != nil {
				remaining = append(remaining, b)
			}
		}
		entries = remaining
		s.admission.Unlock()
		if active == nil && len(entries) == 0 {
			break
		}
		if pass < 2 {
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(pass+1) * time.Second):
			}
		}
	}
	// The cycle's selection and completion are matched under the same mutex:
	// newly registered intents get a new cycle; attempted old faults do not.
	s.mu.Lock()
	s.recoveryRunning = false
	s.recoveryDeadline = time.Time{}
	s.recoveryCancel = nil
	closing := s.closing
	s.scheduleRecoveryLocked()
	s.mu.Unlock()
	if closing {
		return
	}
	tail, stop := context.WithDeadline(context.Background(), deadline)
	defer stop()
	if s.lockAdmission(tail) == nil {
		next := s.reserveNextLocked()
		s.admission.Unlock()
		if next != nil {
			go s.startReserved(next)
		}
	}
}

func (s *Service) RecoverRuntime(ctx context.Context, owner Owner) (any, error) {
	if !s.cfg.ChanshuiEnabled {
		return nil, problem(503, "configuration", "当前实例不能运行检测；请打开正常调度站实例")
	}
	status := s.queryStatus(owner)
	if status.State == "retained" {
		p := status.problem()
		p.Status = 503
		return nil, p
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.active
	if active != nil && active.Owner == owner && activePhase(active, time.Now().UnixMilli(), s.recoveryRunning, unixMilli(s.recoveryDeadline)) == "unknown" {
		return nil, problem(503, "exit", exitUnknownReason)
	}
	if s.closing {
		return nil, problem(503, "shutdown", "检测服务正在结束旧任务，请稍后核查")
	}
	own := activeExited(active) && active.Owner == owner
	for _, b := range s.blocked {
		if b.Owner == owner {
			own = true
		}
	}
	if !own {
		return nil, problem(409, "recovery", "当前工作区没有可重新保存的已结束检测状态；如果仍未确认退出，请先核查运行环境")
	}
	if !s.recoveryRunning {
		if status.State != "" {
			return nil, status.problem()
		}
		if activeExited(active) && active.Owner == owner {
			active.RecoveryAttempted = false
		}
		for _, b := range s.blocked {
			if b.Owner == owner {
				b.RecoveryAttempted = false
			}
		}
		s.scheduleRecoveryLocked()
	}
	return map[string]any{"accepted": true, "recoveryRunning": s.recoveryRunning, "recoveryDeadlineMs": unixMilli(s.recoveryDeadline)}, nil
}
func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
