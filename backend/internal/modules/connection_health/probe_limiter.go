package connection_health

import (
	"context"
	"sync"
)

type probeWorkspaceUsage struct {
	active                int
	manualWaiters         int
	manualReservedWaiters int
}

type probeConcurrencyLimiter struct {
	globalCap             int
	perWorkspaceCap       int
	mu                    sync.Mutex
	globalActive          int
	globalWaiters         int
	globalReservedWaiters int
	workspaces            map[string]*probeWorkspaceUsage
	stateChanged          chan struct{}
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

// acquire is retained for existing automatic callers and tests. New code should
// state whether the request is automatic or manual so reserved capacity applies.
func (l *probeConcurrencyLimiter) acquire(ctx context.Context, workspaceKey string, wait bool) (func(), bool) {
	return l.acquireAutomatic(ctx, workspaceKey, wait, 0)
}

func (l *probeConcurrencyLimiter) acquireAutomatic(ctx context.Context, workspaceKey string, wait bool, manualReservedSlots int) (func(), bool) {
	if manualReservedSlots < 0 {
		manualReservedSlots = 0
	}
	if manualReservedSlots > l.perWorkspaceCap {
		manualReservedSlots = l.perWorkspaceCap
	}
	for {
		l.mu.Lock()
		usage := l.usageLocked(workspaceKey)
		workspaceAvailable := usage.active < l.perWorkspaceCap
		globalAvailable := l.globalActive < l.globalCap
		// Automatic work may borrow idle reserved capacity. Once a manual request
		// is waiting, new automatic work leaves the configured workspace slot free.
		manualPriority := usage.manualReservedWaiters > 0 && manualReservedSlots > 0 && usage.active >= l.perWorkspaceCap-manualReservedSlots
		globalReserved := l.globalReservedWaiters
		if globalReserved > l.globalCap {
			globalReserved = l.globalCap
		}
		globalPriority := globalReserved > 0 && l.globalActive >= l.globalCap-globalReserved
		if workspaceAvailable && globalAvailable && !manualPriority && !globalPriority {
			usage.active++
			l.globalActive++
			l.signalLocked()
			l.mu.Unlock()
			return l.releaseFunc(workspaceKey), true
		}
		if !wait {
			l.mu.Unlock()
			return nil, false
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

func (l *probeConcurrencyLimiter) acquireManual(ctx context.Context, workspaceKey string, reservedSlots int) (func(), bool) {
	if reservedSlots < 0 {
		reservedSlots = 0
	}
	if reservedSlots > 1 {
		reservedSlots = 1
	}
	l.mu.Lock()
	usage := l.usageLocked(workspaceKey)
	usage.manualWaiters++
	l.globalWaiters++
	if reservedSlots > 0 {
		usage.manualReservedWaiters++
		l.globalReservedWaiters++
	}
	l.signalLocked()
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		usage := l.usageLocked(workspaceKey)
		usage.manualWaiters--
		l.globalWaiters--
		if reservedSlots > 0 {
			usage.manualReservedWaiters--
			l.globalReservedWaiters--
		}
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
