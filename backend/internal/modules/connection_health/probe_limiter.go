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
}

func newProbeConcurrencyLimiter(globalLimit int, perWorkspaceLimit int) *probeConcurrencyLimiter {
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
		usage = &probeWorkspaceUsage{}
		l.workspaces[workspaceKey] = usage
	}
	return usage
}

func (l *probeConcurrencyLimiter) acquireAutomatic(ctx context.Context, workspaceKey string) (func(), bool) {
	for {
		l.mu.Lock()
		usage := l.usageLocked(workspaceKey)
		workspaceAvailable := usage.active < l.perWorkspaceCap
		globalAvailable := l.globalActive < l.globalCap

		// 手动请求已经在等时，自动任务为它保留当前 workspace 和全局的下一个名额。
		workspaceManualPriority := usage.manualWaiters > 0 && usage.active >= l.perWorkspaceCap-1
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
		if usage.active < l.perWorkspaceCap && l.globalActive < l.globalCap {
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
		s.probeLimiter = newProbeConcurrencyLimiter(globalProbeConcurrency, perSiteProbeConcurrency)
	}
	return s.probeLimiter
}
