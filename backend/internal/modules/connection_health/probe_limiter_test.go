package connection_health

import (
	"context"
	"testing"
)

func TestProbeLimiter_ManualReservationCanBeEnabledOrDisabled(t *testing.T) {
	limiter := newProbeConcurrencyLimiter(5, 2)
	workspace := "user|workspace"

	limiter.mu.Lock()
	usage := limiter.usageLocked(workspace)
	usage.manualWaiters = 1
	usage.manualReservedWaiters = 1
	limiter.globalWaiters = 1
	limiter.globalReservedWaiters = 1
	limiter.mu.Unlock()

	releaseFirst, acquired := limiter.acquireAutomatic(context.Background(), workspace, false, 1)
	if !acquired {
		t.Fatal("automatic work may use the non-reserved workspace slot")
	}
	defer releaseFirst()
	if _, acquired := limiter.acquireAutomatic(context.Background(), workspace, false, 1); acquired {
		t.Fatal("second automatic request must leave one slot for a waiting manual request")
	}

	limiter.mu.Lock()
	usage.manualReservedWaiters = 0
	limiter.globalReservedWaiters = 0
	limiter.mu.Unlock()
	releaseSecond, acquired := limiter.acquireAutomatic(context.Background(), workspace, false, 0)
	if !acquired {
		t.Fatal("manualReservedSlots=0 must allow automatic work to use both workspace slots")
	}
	releaseSecond()
}

func TestProbeLimiter_NeverExceedsGlobalOrWorkspaceCaps(t *testing.T) {
	limiter := newProbeConcurrencyLimiter(2, 1)
	releaseA, ok := limiter.acquireAutomatic(context.Background(), "a", false, 0)
	if !ok {
		t.Fatal("first workspace slot was not acquired")
	}
	releaseB, ok := limiter.acquireAutomatic(context.Background(), "b", false, 0)
	if !ok {
		t.Fatal("second global slot was not acquired")
	}
	if _, ok := limiter.acquireAutomatic(context.Background(), "c", false, 0); ok {
		t.Fatal("global cap was exceeded")
	}
	if _, ok := limiter.acquireAutomatic(context.Background(), "a", false, 0); ok {
		t.Fatal("workspace cap was exceeded")
	}
	releaseA()
	releaseB()
}
