package chanshui

import (
	"testing"
	"time"
)

func TestBatchContractTransientFinishFailureAdvancesWithoutReplay(t *testing.T) {
	s, f, _ := queueFixture(t)
	owner := Owner{"user-a", "workspace-a"}
	a, b := queueInput("a"), queueInput("b")
	if _, err := s.Start(t.Context(), owner, a); err != nil {
		t.Fatal(err)
	}
	nextStarted(t, f, a.ReportID)
	if _, err := s.Start(t.Context(), owner, b); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.finishFailure = a.ReportID
	f.mu.Unlock()
	f.release(a.ReportID, false)
	waitQueue(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.active != nil && s.active.ReportID == a.ReportID && s.active.Diagnostic != ""
	})
	f.mu.Lock()
	if len(f.order) != 1 {
		f.mu.Unlock()
		t.Fatal("advanced before finish was saved")
	}
	f.finishFailure = ""
	f.mu.Unlock()
	// No GET, cancel, repeat Start, or user action is allowed to advance recovery.
	select {
	case id := <-f.started:
		if id != b.ReportID {
			t.Fatalf("replayed model or out-of-order start: %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("automatic recovery did not start the next report")
	}
	f.release(b.ReportID, true)
	waitQueue(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.active == nil })
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.order) != 2 || f.maxRunning != 1 || f.statuses[a.ReportID] != "interrupted" {
		t.Fatalf("unexpected recovery: %v, max %d, statuses %v", f.order, f.maxRunning, f.statuses)
	}
}
