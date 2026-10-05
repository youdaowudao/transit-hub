package connection_health

import (
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"transithub/backend/internal/modules/upstream"
)

const priorityWaitingTimeout = 5 * time.Minute
const priorityRoundLogInterval = 10 * time.Minute

type priorityTargetFailure struct {
	targetID  string
	accountID string
	reason    string
	err       error
}

type priorityRoundLogRecord struct {
	signature string
	loggedAt  time.Time
}

func classifyPriorityTargetFailure(err error, visible bool) string {
	var requestErr *upstream.RequestError
	if errors.As(err, &requestErr) {
		return "write_failed"
	}
	if !visible {
		return "not_visible"
	}
	if errors.Is(err, ErrRemoteActionPending) || errors.Is(err, ErrRemoteActionLeaseLost) || errors.Is(err, ErrRemoteActionEvidenceChanged) {
		return "waiting"
	}
	return "other"
}

func priorityTargetFailureError(targets []priorityTargetFailure) requestError {
	for _, category := range []struct{ reason, key string }{
		{"not_visible", ErrorPriorityTargetNotVisible},
		{"write_failed", ErrorPriorityWriteFailed},
		{"waiting_timeout", ErrorPriorityWaitingTimeout},
	} {
		for _, target := range targets {
			if target.reason == category.reason {
				return requestError(category.key)
			}
		}
	}
	return requestError(ErrorPriorityTargetFailed)
}

func (s *Service) priorityStatusLock(userID, workspace string) *sync.Mutex {
	lock, _ := s.priorityStatusLocks.LoadOrStore(priorityRuntimeLeaseKey(userID, workspace), &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func (s *Service) markPriorityStatus(userID, workspace string, targets []priorityTargetFailure, mark func() (bool, error)) (bool, error) {
	lock := s.priorityStatusLock(userID, workspace)
	lock.Lock()
	defer lock.Unlock()
	marked, err := mark()
	if marked && err == nil {
		s.replacePriorityFailureTargets(userID, workspace, targets)
	}
	return marked, err
}

// Caller holds the workspace status lock. Published slices are immutable.
func (s *Service) replacePriorityFailureTargets(userID, workspace string, targets []priorityTargetFailure) {
	key := priorityRuntimeLeaseKey(userID, workspace)
	if len(targets) == 0 {
		s.priorityFailureTargets.Delete(key)
		return
	}
	copy := append([]priorityTargetFailure(nil), targets...)
	sort.Slice(copy, func(i, j int) bool { return copy[i].accountID < copy[j].accountID })
	if len(copy) > 20 {
		copy = copy[:20]
	}
	s.priorityFailureTargets.Store(key, copy)
}

func (s *Service) logPrioritySyncRound(userID, workspace string, failures, waiting []priorityTargetFailure, now time.Time) {
	key := priorityRuntimeLeaseKey(userID, workspace)
	if len(failures) == 0 && len(waiting) == 0 {
		s.priorityRoundLogs.Delete(key)
		return
	}
	targets := append(append([]priorityTargetFailure(nil), failures...), waiting...)
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].accountID != targets[j].accountID {
			return targets[i].accountID < targets[j].accountID
		}
		return targets[i].reason < targets[j].reason
	})
	parts := make([]string, 0, len(targets))
	otherErr := ""
	for _, target := range targets {
		parts = append(parts, target.accountID+":"+target.reason)
		if otherErr == "" && target.reason == "other" && target.err != nil {
			otherErr = target.err.Error()
		}
	}
	signature := strings.Join(parts, ",")
	if previous, exists := s.priorityRoundLogs.Load(key); exists {
		last := previous.(priorityRoundLogRecord)
		if last.signature == signature && now.Sub(last.loggedAt) < priorityRoundLogInterval {
			return
		}
	}
	s.priorityRoundLogs.Store(key, priorityRoundLogRecord{signature: signature, loggedAt: now})
	if len(parts) > 20 {
		parts = parts[:20]
	}
	log.Printf("[connection-health] priority sync round incomplete workspace=%s failed=%d deferred=%d targets=%s other_err=%s", workspace, len(failures), len(waiting), strings.Join(parts, ","), otherErr)
}
