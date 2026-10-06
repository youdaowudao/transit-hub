package connection_health

import (
	"context"
	"sync"
)

type probeConcurrencyLimiter struct {
	globalCap           int
	perWorkspaceCap     int
	mu                  sync.Mutex
	globalActive        int
	globalManualWaiters int
	workspaces          map[string]*probeWorkspaceUsage
	stateChanged        chan struct{}
}

type probeWorkspaceUsage struct {
	active        int
	manualWaiters int
	cap           int
	capVersion    int64
	capLoaded     bool
}

func newProbeConcurrencyLimiter(globalLimit int, perWorkspaceLimit int) *probeConcurrencyLimiter {
	if globalLimit <= 0 {
		globalLimit = 12
	}
	if perWorkspaceLimit <= 0 {
		perWorkspaceLimit = 6
	}
	return &probeConcurrencyLimiter{
		globalCap: globalLimit, perWorkspaceCap: perWorkspaceLimit,
		workspaces: make(map[string]*probeWorkspaceUsage), stateChanged: make(chan struct{}),
	}
}

func (l *probeConcurrencyLimiter) signalLocked() {
	close(l.stateChanged)
	l.stateChanged = make(chan struct{})
}

func (l *probeConcurrencyLimiter) usageLocked(workspaceKey string) *probeWorkspaceUsage {
	usage := l.workspaces[workspaceKey]
	if usage == nil {
		usage = &probeWorkspaceUsage{cap: l.perWorkspaceCap}
		l.workspaces[workspaceKey] = usage
	}
	return usage
}

func (l *probeConcurrencyLimiter) acquireAutomatic(ctx context.Context, workspaceKey string) (func(), bool) {
	for {
		l.mu.Lock()
		usage := l.usageLocked(workspaceKey)
		workspaceAvailable := usage.active < usage.cap
		globalAvailable := l.globalActive < l.globalCap

		// 手动请求已经在等时，自动任务为它保留当前 workspace 和全局的下一个名额。
		workspaceManualPriority := usage.manualWaiters > 0 && usage.active >= usage.cap-1
		globalReserved := min(l.globalManualWaiters, l.globalCap)
		globalManualPriority := globalReserved > 0 && l.globalActive >= l.globalCap-globalReserved
		if workspaceAvailable && globalAvailable && !workspaceManualPriority && !globalManualPriority {
			usage.active++
			l.globalActive++
			l.signalLocked()
			l.mu.Unlock()
			return l.releaseFunc(workspaceKey), true
		}

		changed := l.stateChanged
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false
		case <-changed:
		}
	}
}

func (l *probeConcurrencyLimiter) acquireManual(ctx context.Context, workspaceKey string, onQueued func()) (func(), bool) {
	queuedReported := false
	l.mu.Lock()
	usage := l.usageLocked(workspaceKey)
	usage.manualWaiters++
	l.globalManualWaiters++
	l.signalLocked()
	l.mu.Unlock()

	defer func() {
		l.mu.Lock()
		usage := l.usageLocked(workspaceKey)
		usage.manualWaiters--
		l.globalManualWaiters--
		l.signalLocked()
		l.mu.Unlock()
	}()

	for {
		l.mu.Lock()
		usage := l.usageLocked(workspaceKey)
		if usage.active < usage.cap && l.globalActive < l.globalCap {
			usage.active++
			l.globalActive++
			l.signalLocked()
			l.mu.Unlock()
			return l.releaseFunc(workspaceKey), true
		}
		changed := l.stateChanged
		l.mu.Unlock()
		if !queuedReported {
			queuedReported = true
			if onQueued != nil {
				onQueued()
			}
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-changed:
		}
	}
}

func (l *probeConcurrencyLimiter) releaseFunc(workspaceKey string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			usage := l.usageLocked(workspaceKey)
			if usage.active > 0 {
				usage.active--
			}
			if l.globalActive > 0 {
				l.globalActive--
			}
			l.signalLocked()
			l.mu.Unlock()
		})
	}
}

func (s *Service) sharedProbeLimiter() *probeConcurrencyLimiter {
	s.probeLimiterMu.Lock()
	defer s.probeLimiterMu.Unlock()
	if s.probeLimiter == nil {
		s.probeLimiter = newProbeConcurrencyLimiter(12, 6)
	}
	return s.probeLimiter
}

// SetWorkspaceCap keeps running requests and wakes queued requests. A late read
// or save callback may never replace a newer settings version.
func (l *probeConcurrencyLimiter) SetWorkspaceCap(workspaceKey string, limit int, version int64) {
	if limit < 1 || limit > 10 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	usage := l.usageLocked(workspaceKey)
	if usage.capLoaded && version <= usage.capVersion {
		return
	}
	usage.cap, usage.capVersion, usage.capLoaded = limit, version, true
	l.signalLocked()
}

func (s *Service) SetProbeGlobalConcurrency(limit int) {
	if limit <= 0 {
		limit = 12
	}
	limiter := s.sharedProbeLimiter()
	limiter.mu.Lock()
	limiter.globalCap = limit
	limiter.signalLocked()
	limiter.mu.Unlock()
}

func (s *Service) loadWorkspaceProbeCap(ctx context.Context, userID, workspace string) error {
	if _, available := s.repo.(interface {
		GetWorkspaceHealthSettings(context.Context, string, string) (WorkspaceHealthSettings, error)
	}); !available {
		return nil
	}
	settings, err := s.workspaceHealthSettings(ctx, userID, workspace)
	if err != nil {
		return err
	}
	s.sharedProbeLimiter().SetWorkspaceCap(userID+"|"+workspace, settings.ProbeConcurrency, settings.ProbeConcurrencyVersion)
	return nil
}

func (s *Service) applyWorkspaceProbeSettings(settings WorkspaceHealthSettings) {
	s.sharedProbeLimiter().SetWorkspaceCap(settings.UserID+"|"+settings.AdminAccountID, settings.ProbeConcurrency, settings.ProbeConcurrencyVersion)
}
