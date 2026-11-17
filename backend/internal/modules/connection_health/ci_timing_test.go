package connection_health

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestRefreshHeartbeatProductionDefaultAndTestOverride(t *testing.T) {
	for _, initial := range []time.Duration{0, -time.Second, 50 * time.Millisecond} {
		t.Run(initial.String(), func(t *testing.T) {
			s := &Service{refreshHeartbeat: initial}
			want := 30 * time.Second
			if initial > 0 {
				want = initial
			}
			if got := s.adminGroupsRefreshHeartbeatDuration(); got != want {
				t.Fatalf("heartbeat default/override changed: %v want %v", got, want)
			}
			s.initializeAdminGroupsRefreshRuntime()
			defer s.refreshRootCancel()
			if s.refreshHeartbeat != want || s.adminGroupsRefreshHeartbeatDuration() != want {
				t.Fatalf("initialization changed heartbeat contract: %v want %v", s.refreshHeartbeat, want)
			}
		})
	}
}

func TestModelControlStorageContextIndependentFiveSecondDeadline(t *testing.T) {
	for _, mode := range []string{"active", "already-cancelled", "cancel-later", "shorter-parent"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				type key struct{}
				parent := context.WithValue(context.Background(), key{}, "request-value")
				var cancelParent context.CancelFunc
				if mode == "shorter-parent" {
					parent, cancelParent = context.WithTimeout(parent, time.Second)
				} else {
					parent, cancelParent = context.WithCancel(parent)
				}
				defer cancelParent()
				if mode == "already-cancelled" {
					cancelParent()
				}
				start := time.Now()
				child, cancel := modelControlStorageContext(parent)
				defer cancel()
				deadline, ok := child.Deadline()
				if !ok || deadline.Sub(start) != 5*time.Second || child.Err() != nil || child.Value(key{}) != "request-value" {
					t.Fatalf("recovery must retain values and get its own exact five seconds: deadline=%v err=%v", deadline.Sub(start), child.Err())
				}
				if mode == "cancel-later" {
					cancelParent()
				}
				time.Sleep(5*time.Second - time.Nanosecond)
				if child.Err() != nil {
					t.Fatalf("recovery expired early or inherited parent cancellation: %v", child.Err())
				}
				<-child.Done()
				if time.Since(start) != 5*time.Second || !errors.Is(child.Err(), context.DeadlineExceeded) {
					t.Fatalf("recovery did not actually time out at five seconds: %v %v", time.Since(start), child.Err())
				}
			})
		})
	}
}

func TestModelControlStorageContextReturnedCancelOnlyCancelsItsChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, parentCancel := context.WithCancel(context.Background())
		defer parentCancel()
		first, cancelFirst := modelControlStorageContext(parent)
		second, cancelSecond := modelControlStorageContext(parent)
		defer cancelSecond()
		cancelFirst()
		if !errors.Is(first.Err(), context.Canceled) || second.Err() != nil || parent.Err() != nil {
			t.Fatal("returned cancel must stop only its own recovery")
		}
	})
}

func TestModelControlBlockedStorageRejectsInvalidRecoveryContexts(t *testing.T) {
	for _, mode := range []string{"no-deadline", "too-long", "cancelled", "expired", "valid"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				cancel := func() {}
				switch mode {
				case "too-long":
					ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
				case "cancelled":
					ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
					cancel()
				case "expired":
					ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				case "valid":
					ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
				}
				defer cancel()
				storage := &c3ReviewBlockedStorage{}
				err := storage.wait(ctx)
				if err == nil || storage.bounded.Load() != (mode == "valid") {
					t.Fatalf("fixture accepted an invalid recovery context: mode=%s bounded=%v err=%v", mode, storage.bounded.Load(), err)
				}
				if mode == "valid" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("valid short fixture must inject the storage timeout: %v", err)
				}
			})
		})
	}
}
