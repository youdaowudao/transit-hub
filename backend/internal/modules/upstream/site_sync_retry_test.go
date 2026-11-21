package upstream

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type rateLimitTimer struct {
	delay time.Duration
	fire  func()
}

func newRateLimitSyncFixture(t *testing.T, statuses *atomic.Int32) (*Service, *Site, *atomic.Int32, chan rateLimitTimer) {
	t.Helper()
	site := staleErrorSite()
	attempts := &atomic.Int32{}
	platform := staleErrorPlatform(t, statuses, func(request *http.Request) {
		if request.URL.Path == "/api/v1/auth/me" {
			attempts.Add(1)
		}
	})
	service := NewService(platform, nil, nil, newSyncTestCache(site))
	service.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{site.UserID: site.AdminAccountID}})
	timers := make(chan rateLimitTimer, 24)
	service.afterFunc = func(delay time.Duration, callback func()) *time.Timer {
		timers <- rateLimitTimer{delay: delay, fire: callback}
		return nil
	}
	t.Cleanup(service.Close)
	return service, site, attempts, timers
}

func expectRateLimitTimer(t *testing.T, timers <-chan rateLimitTimer) rateLimitTimer {
	t.Helper()
	select {
	case timer := <-timers:
		if timer.delay != 10*time.Second {
			t.Fatalf("retry scheduled after %s, want 10s", timer.delay)
		}
		return timer
	default:
		t.Fatal("upstream rate limit did not schedule a retry")
		return rateLimitTimer{}
	}
}

func TestSiteReadRateLimitRetriesAndStops(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		nextStatus  int32
		attempts    int32
		finalKey    string
		finalStatus Status
	}{
		{"recovers", 0, 2, "", StatusConnected},
		{"exhausts", 429, 6, ErrorRateLimited, StatusError},
		{"different failure", 403, 2, ErrorForbidden, StatusError},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var status atomic.Int32
			status.Store(429)
			service, site, attempts, timers := newRateLimitSyncFixture(t, &status)
			first, err := service.Sync(t.Context(), site.UserID, site.ID)
			if err != nil || first.ErrorKey == nil || *first.ErrorKey != ErrorRateLimited {
				t.Fatalf("initial 429 lost: %+v, %v", first, err)
			}
			if first.SyncRetry == nil || first.SyncRetry.Phase != syncRetryWaiting || first.SyncRetry.Attempted != 1 {
				t.Fatalf("initial retry progress = %+v", first.SyncRetry)
			}
			timer := expectRateLimitTimer(t, timers)
			if _, err := service.Sync(t.Context(), site.UserID, site.ID); err != nil || attempts.Load() != 1 {
				t.Fatalf("duplicate click made an early upstream request: %d, %v", attempts.Load(), err)
			}
			status.Store(scenario.nextStatus)
			for attempt := int32(2); attempt <= scenario.attempts; attempt++ {
				timer.fire()
				if attempts.Load() != attempt {
					t.Fatalf("upstream attempts = %d, want %d", attempts.Load(), attempt)
				}
				if attempt < scenario.attempts {
					timer = expectRateLimitTimer(t, timers)
				}
			}
			got, err := service.ReadSiteSnapshot(t.Context(), site.UserID, site.ID)
			if err != nil || got.Status != scenario.finalStatus {
				t.Fatalf("snapshot status = %s, want %s, error %v", got.Status, scenario.finalStatus, err)
			}
			if scenario.finalKey == "" && (got.ErrorKey != nil || got.LastSyncedAt == nil) ||
				scenario.finalKey != "" && (got.ErrorKey == nil || *got.ErrorKey != scenario.finalKey) {
				t.Fatalf("wrong final result: %+v", got)
			}
			switch scenario.finalKey {
			case "":
				if got.SyncRetry != nil {
					t.Fatalf("recovered site retained retry progress: %+v", got.SyncRetry)
				}
			case ErrorRateLimited:
				if got.SyncRetry == nil || got.SyncRetry.Phase != syncRetryExhausted || got.SyncRetry.Attempted != 6 {
					t.Fatalf("wrong exhaustion result: %+v", got.SyncRetry)
				}
			default:
				if got.SyncRetry == nil || got.SyncRetry.Phase != syncRetryStopped || got.SyncRetry.ErrorKey != scenario.finalKey || got.SyncRetry.ErrorDetail == "" {
					t.Fatalf("retry did not retain the new error: %+v", got.SyncRetry)
				}
			}
			if len(timers) != 0 {
				t.Fatalf("retry remained scheduled after the final attempt")
			}
		})
	}
}

func TestSiteReadRateLimitBatchAndRegularScheduleShareRetry(t *testing.T) {
	const regularInterval = 2 * time.Minute
	var status atomic.Int32
	status.Store(429)
	service, site, attempts, timers := newRateLimitSyncFixture(t, &status)
	service.mu.Lock()
	service.refreshConfigs[siteRefreshWorkspace(site)] = RefreshConfig{Enabled: true, Interval: regularInterval}
	service.scheduleSyncLocked(site.ID, site)
	service.mu.Unlock()
	if len(timers) != 1 {
		t.Fatalf("initial schedule created %d timers", len(timers))
	}
	normal := <-timers
	if normal.delay != regularInterval {
		t.Fatalf("initial delay = %s", normal.delay)
	}
	normal.fire()
	retry := expectRateLimitTimer(t, timers)
	results, err := service.SyncAll(t.Context(), site.UserID)
	if err != nil || len(results) != 1 || results[0].SyncRetry == nil || results[0].SyncRetry.Attempted != 1 {
		t.Fatalf("batch did not share current retry: %+v, %v", results, err)
	}
	var failed *Response
	err = service.SyncAllStream(t.Context(), site.UserID, func(event SyncEvent) {
		if event.Event == SyncEventError {
			failed = event.Site
		}
	})
	if err != nil || failed == nil || failed.SyncRetry == nil || failed.SyncRetry.Attempted != 1 || attempts.Load() != 1 {
		t.Fatalf("stream did not return the same 429 and progress: %+v, %v, attempts=%d", failed, err, attempts.Load())
	}
	status.Store(0)
	retry.fire()
	retry.fire()
	if len(timers) != 1 {
		t.Fatalf("recovery left %d schedules, want one regular timer", len(timers))
	}
	if next := <-timers; next.delay != regularInterval || attempts.Load() != 2 {
		t.Fatalf("recovery scheduled %s and made %d attempts", next.delay, attempts.Load())
	}
}

func TestSiteReadRateLimitCanceledTimerCannotRefresh(t *testing.T) {
	for _, action := range []string{"disable", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			var status atomic.Int32
			status.Store(429)
			service, site, attempts, timers := newRateLimitSyncFixture(t, &status)
			if _, err := service.Sync(t.Context(), site.UserID, site.ID); err != nil {
				t.Fatal(err)
			}
			retry := expectRateLimitTimer(t, timers)
			if action == "disable" {
				if _, err := service.UpdateEnabled(t.Context(), site.UserID, site.ID, false); err != nil {
					t.Fatal(err)
				}
			} else {
				service.StopSiteSyncRetries()
			}
			retry.fire()
			got, err := service.ReadSiteSnapshot(t.Context(), site.UserID, site.ID)
			if err != nil || got.SyncRetry != nil || attempts.Load() != 1 || len(timers) != 0 {
				t.Fatalf("canceled retry remained active: %+v, attempts=%d, timers=%d, err=%v", got.SyncRetry, attempts.Load(), len(timers), err)
			}
		})
	}
}

func TestSiteReadRateLimitInFlightResultCannotPublishAfterReenable(t *testing.T) {
	var status atomic.Int32
	status.Store(429)
	service, site, attempts, timers := newRateLimitSyncFixture(t, &status)
	entered := make(chan struct{})
	release := make(chan struct{})
	var unblock sync.Once
	releaseRead := func() { unblock.Do(func() { close(release) }) }
	service.platformService = staleErrorPlatform(t, &status, func(request *http.Request) {
		if request.URL.Path == "/api/v1/auth/me" && attempts.Add(1) == 2 {
			close(entered)
			<-release
		}
	})
	var callbacks atomic.Int32
	service.AfterSync = func(context.Context, string, string, string, string, Metrics, Metrics, Status, Status) {
		callbacks.Add(1)
	}
	first, err := service.Sync(t.Context(), site.UserID, site.ID)
	if err != nil || first.SyncRetry == nil {
		t.Fatalf("initial 429 did not schedule retry: %v", err)
	}
	timer := expectRateLimitTimer(t, timers)
	status.Store(0)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		timer.fire()
	}()
	defer func() { releaseRead(); <-finished }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not enter upstream read")
	}
	for _, enabled := range []bool{false, true} {
		if _, err := service.UpdateEnabled(t.Context(), site.UserID, site.ID, enabled); err != nil {
			t.Fatal(err)
		}
	}
	releaseRead()
	<-finished
	actual, err := service.ReadSiteSnapshot(t.Context(), site.UserID, site.ID)
	if err != nil || actual.Enabled == nil || !*actual.Enabled || actual.LastSyncedAt != nil ||
		actual.Status != StatusError || actual.ErrorKey == nil || *actual.ErrorKey != ErrorRateLimited ||
		actual.SyncRetry != nil || callbacks.Load() != 0 || len(timers) != 0 {
		t.Fatalf("old retry published after reenable: status=%s, synced=%v, callbacks=%d, timers=%d, err=%v",
			actual.Status, actual.LastSyncedAt != nil, callbacks.Load(), len(timers), err)
	}
}

func TestSiteReadRateLimitDoesNotRetryKnownBusinessRejectionOrInternalSync(t *testing.T) {
	t.Run("internal caller", func(t *testing.T) {
		var status atomic.Int32
		status.Store(429)
		service, site, attempts, timers := newRateLimitSyncFixture(t, &status)
		result := service.SyncSites(t.Context(), site.UserID, site.AdminAccountID, []string{site.ID}, true)
		if len(result) != 1 || result[0].Status != "unavailable" || attempts.Load() != 1 || len(timers) != 0 {
			t.Fatalf("internal synchronization changed: %+v, attempts %d, timers %d", result, attempts.Load(), len(timers))
		}
	})
	t.Run("known quota error", func(t *testing.T) {
		var status atomic.Int32
		service, site, _, timers := newRateLimitSyncFixture(t, &status)
		service.platformService = singaporeCostPlatform(t, time.Now(), func(*http.Request) (int, any) {
			return http.StatusTooManyRequests, map[string]string{"code": "INSUFFICIENT_QUOTA", "message": "quota exhausted"}
		})
		result, err := service.Sync(t.Context(), site.UserID, site.ID)
		if err != nil || result.ErrorKey == nil || *result.ErrorKey != ErrorUpstreamKeyQuotaExhausted || len(timers) != 0 {
			t.Fatalf("quota rejection was treated as a retryable rate limit: %+v, %v", result, err)
		}
	})
}
