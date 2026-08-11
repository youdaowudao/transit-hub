package connection_health

import (
	"context"
	"testing"
	"time"
)

type probeLimiterAcquireResult struct {
	release func()
	ok      bool
}

func waitForProbeManualWaiters(t *testing.T, limiter *probeConcurrencyLimiter, workspaceKey string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		limiter.mu.Lock()
		usage := limiter.usageLocked(workspaceKey)
		got := usage.manualWaiters
		limiter.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("manual waiters did not reach %d", want)
}

func TestProbeLimiter_ManualWaitsForWorkspaceSlotAndResumes(t *testing.T) {
	limiter := newProbeConcurrencyLimiter(5, 2)
	workspaceKey := "user|workspace"

	releaseFirst, ok := limiter.acquireAutomatic(context.Background(), workspaceKey)
	if !ok {
		t.Fatal("first automatic slot was not acquired")
	}
	releaseSecond, ok := limiter.acquireAutomatic(context.Background(), workspaceKey)
	if !ok {
		t.Fatal("second automatic slot was not acquired")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan probeLimiterAcquireResult, 1)
	queuedCh := make(chan struct{}, 1)
	go func() {
		release, acquired := limiter.acquireManual(ctx, workspaceKey, func() { queuedCh <- struct{}{} })
		resultCh <- probeLimiterAcquireResult{release: release, ok: acquired}
	}()
	waitForProbeManualWaiters(t, limiter, workspaceKey, 1)
	select {
	case <-queuedCh:
	case <-time.After(time.Second):
		t.Fatal("queued manual probe did not report its queued phase")
	}

	select {
	case <-resultCh:
		t.Fatal("manual probe must wait while both workspace slots are occupied")
	default:
	}

	releaseFirst()
	select {
	case result := <-resultCh:
		if !result.ok || result.release == nil {
			t.Fatal("manual probe did not acquire the released slot")
		}
		result.release()
	case <-time.After(time.Second):
		t.Fatal("manual probe did not resume after a slot was released")
	}
	releaseSecond()
}

func TestProbeLimiter_ImmediateManualProbeDoesNotReportQueued(t *testing.T) {
	limiter := newProbeConcurrencyLimiter(5, 2)
	release, ok := limiter.acquireManual(context.Background(), "user|workspace", func() {
		t.Fatal("immediate manual probe reported a queued phase")
	})
	if !ok || release == nil {
		t.Fatal("immediate manual probe did not acquire a slot")
	}
	release()
}

func TestProbeLimiter_AutomaticDoesNotPassWaitingManualProbe(t *testing.T) {
	limiter := newProbeConcurrencyLimiter(5, 1)
	workspaceKey := "user|workspace"

	releaseCurrent, ok := limiter.acquireAutomatic(context.Background(), workspaceKey)
	if !ok {
		t.Fatal("initial automatic slot was not acquired")
	}

	manualCtx, cancelManual := context.WithCancel(context.Background())
	defer cancelManual()
	manualCh := make(chan probeLimiterAcquireResult, 1)
	go func() {
		release, acquired := limiter.acquireManual(manualCtx, workspaceKey, nil)
		manualCh <- probeLimiterAcquireResult{release: release, ok: acquired}
	}()
	waitForProbeManualWaiters(t, limiter, workspaceKey, 1)

	automaticCtx, cancelAutomatic := context.WithCancel(context.Background())
	defer cancelAutomatic()
	automaticCh := make(chan probeLimiterAcquireResult, 1)
	go func() {
		release, acquired := limiter.acquireAutomatic(automaticCtx, workspaceKey)
		automaticCh <- probeLimiterAcquireResult{release: release, ok: acquired}
	}()

	releaseCurrent()
	select {
	case result := <-manualCh:
		if !result.ok || result.release == nil {
			t.Fatal("waiting manual probe did not acquire the next slot")
		}
		select {
		case automatic := <-automaticCh:
			if automatic.ok && automatic.release != nil {
				automatic.release()
			}
			t.Fatal("automatic probe passed a waiting manual probe")
		default:
		}
		result.release()
	case automatic := <-automaticCh:
		if automatic.ok && automatic.release != nil {
			automatic.release()
		}
		t.Fatal("automatic probe acquired the slot before the waiting manual probe")
	case <-time.After(time.Second):
		t.Fatal("waiting manual probe did not resume")
	}

	select {
	case result := <-automaticCh:
		if !result.ok || result.release == nil {
			t.Fatal("automatic probe did not resume after the manual probe finished")
		}
		result.release()
	case <-time.After(time.Second):
		t.Fatal("automatic probe remained blocked after the manual probe finished")
	}
}

func TestProbeLimiter_GlobalSlotGoesToWaitingManualProbeFirst(t *testing.T) {
	limiter := newProbeConcurrencyLimiter(2, 1)
	releaseA, ok := limiter.acquireAutomatic(context.Background(), "workspace-a")
	if !ok {
		t.Fatal("workspace-a slot was not acquired")
	}
	releaseB, ok := limiter.acquireAutomatic(context.Background(), "workspace-b")
	if !ok {
		t.Fatal("workspace-b slot was not acquired")
	}

	manualCtx, cancelManual := context.WithCancel(context.Background())
	defer cancelManual()
	manualCh := make(chan probeLimiterAcquireResult, 1)
	go func() {
		release, acquired := limiter.acquireManual(manualCtx, "workspace-c", nil)
		manualCh <- probeLimiterAcquireResult{release: release, ok: acquired}
	}()
	waitForProbeManualWaiters(t, limiter, "workspace-c", 1)

	automaticCtx, cancelAutomatic := context.WithCancel(context.Background())
	defer cancelAutomatic()
	automaticCh := make(chan probeLimiterAcquireResult, 1)
	go func() {
		release, acquired := limiter.acquireAutomatic(automaticCtx, "workspace-d")
		automaticCh <- probeLimiterAcquireResult{release: release, ok: acquired}
	}()

	releaseA()
	select {
	case result := <-manualCh:
		if !result.ok || result.release == nil {
			t.Fatal("waiting manual probe did not acquire the released global slot")
		}
		select {
		case automatic := <-automaticCh:
			if automatic.ok && automatic.release != nil {
				automatic.release()
			}
			t.Fatal("automatic probe passed a globally waiting manual probe")
		default:
		}
		result.release()
	case automatic := <-automaticCh:
		if automatic.ok && automatic.release != nil {
			automatic.release()
		}
		t.Fatal("automatic probe acquired the global slot before the waiting manual probe")
	case <-time.After(time.Second):
		t.Fatal("waiting manual probe did not acquire the released global slot")
	}

	select {
	case result := <-automaticCh:
		if !result.ok || result.release == nil {
			t.Fatal("automatic probe did not resume after the manual probe released the global slot")
		}
		result.release()
	case <-time.After(time.Second):
		t.Fatal("automatic probe remained blocked after the manual probe finished")
	}
	releaseB()
}

func TestProbeLimiter_ManualCancellationLeavesNoWaiterOrSlot(t *testing.T) {
	limiter := newProbeConcurrencyLimiter(1, 1)
	workspaceKey := "user|workspace"

	releaseCurrent, ok := limiter.acquireAutomatic(context.Background(), workspaceKey)
	if !ok {
		t.Fatal("initial automatic slot was not acquired")
	}

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan probeLimiterAcquireResult, 1)
	go func() {
		release, acquired := limiter.acquireManual(ctx, workspaceKey, nil)
		resultCh <- probeLimiterAcquireResult{release: release, ok: acquired}
	}()
	waitForProbeManualWaiters(t, limiter, workspaceKey, 1)
	cancel()

	select {
	case result := <-resultCh:
		if result.ok || result.release != nil {
			t.Fatal("cancelled manual probe unexpectedly acquired a slot")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled manual probe did not leave the queue")
	}
	waitForProbeManualWaiters(t, limiter, workspaceKey, 0)

	limiter.mu.Lock()
	if limiter.globalManualWaiters != 0 || limiter.globalActive != 1 {
		t.Fatalf("cancelled waiter leaked limiter state: global_active=%d manual_waiters=%d", limiter.globalActive, limiter.globalManualWaiters)
	}
	limiter.mu.Unlock()

	releaseCurrent()
	releaseNext, ok := limiter.acquireAutomatic(context.Background(), workspaceKey)
	if !ok {
		t.Fatal("slot was not reusable after manual cancellation")
	}
	releaseNext()
}

func TestProbeLimiter_ReleaseIsIdempotentAndCapsRemainHard(t *testing.T) {
	limiter := newProbeConcurrencyLimiter(2, 1)
	releaseA, ok := limiter.acquireAutomatic(context.Background(), "workspace-a")
	if !ok {
		t.Fatal("workspace-a slot was not acquired")
	}
	releaseB, ok := limiter.acquireAutomatic(context.Background(), "workspace-b")
	if !ok {
		t.Fatal("workspace-b slot was not acquired")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if release, acquired := limiter.acquireAutomatic(ctx, "workspace-c"); acquired || release != nil {
		if release != nil {
			release()
		}
		t.Fatal("global concurrency cap was exceeded")
	}

	releaseA()
	releaseA()
	releaseB()
	limiter.mu.Lock()
	if limiter.globalActive != 0 || limiter.workspaces["workspace-a"].active != 0 || limiter.workspaces["workspace-b"].active != 0 {
		t.Fatalf("release leaked or underflowed limiter state: global=%d a=%d b=%d", limiter.globalActive, limiter.workspaces["workspace-a"].active, limiter.workspaces["workspace-b"].active)
	}
	limiter.mu.Unlock()
}
