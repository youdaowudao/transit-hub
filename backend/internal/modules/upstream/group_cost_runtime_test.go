package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

type groupCostRuntimeCache struct {
	*syncTestCache
	costMu          sync.Mutex
	states          map[string]GroupCostSamplingState
	samples         map[string][]GroupCostSample
	stateErr        error
	sampleErrKey    string
	rootErr         error
	appendFailAt    int
	stateSaveErr    error
	panicSite       string
	panicReadSite   string
	panicAppendSite string
	lockCalls       int
	appendCalls     int
	writes          int
	saved           chan string
}

func (c *groupCostRuntimeCache) Get(ctx context.Context, id string) (*Site, error) {
	if id == c.panicReadSite {
		panic("fixture-secret-read-panic")
	}
	if c.rootErr != nil {
		return nil, c.rootErr
	}
	return c.syncTestCache.Get(ctx, id)
}
func (c *groupCostRuntimeCache) TryStartGroupCostSampling(ctx context.Context, id string, _ time.Duration) (bool, error) {
	c.costMu.Lock()
	defer c.costMu.Unlock()
	c.lockCalls++
	if id == c.panicSite {
		panic("fixture-secret-must-not-log")
	}
	return ctx.Err() == nil, ctx.Err()
}
func (c *groupCostRuntimeCache) GetGroupCostSamplingState(ctx context.Context, id string) (GroupCostSamplingState, error) {
	c.costMu.Lock()
	defer c.costMu.Unlock()
	if ctx.Err() != nil {
		return GroupCostSamplingState{}, ctx.Err()
	}
	return c.states[id], c.stateErr
}
func (c *groupCostRuntimeCache) SetGroupCostSamplingState(ctx context.Context, id string, state GroupCostSamplingState, _ time.Duration) error {
	c.costMu.Lock()
	defer c.costMu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if c.stateSaveErr != nil {
		return c.stateSaveErr
	}
	c.states[id] = state
	c.writes++
	if state.LastAttemptStatus == "ok" {
		c.saved <- id
	}
	return nil
}
func (c *groupCostRuntimeCache) AppendGroupCostSamples(ctx context.Context, id, group, _ string, values []GroupCostSample, _ int, _ time.Duration) error {
	c.costMu.Lock()
	defer c.costMu.Unlock()
	c.appendCalls++
	if id == c.panicAppendSite {
		panic("fixture-secret-append-panic")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if c.appendCalls == c.appendFailAt {
		return errors.New("fixture cache append unavailable")
	}
	c.samples[id+":"+group] = append(c.samples[id+":"+group], values...)
	c.writes++
	return nil
}
func (c *groupCostRuntimeCache) ListGroupCostSamples(ctx context.Context, id, group, _ string) ([]GroupCostSample, error) {
	c.costMu.Lock()
	defer c.costMu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if id+":"+group == c.sampleErrKey {
		return nil, errors.New("fixture sample decode failed token=fixture-secret")
	}
	return append([]GroupCostSample(nil), c.samples[id+":"+group]...), nil
}
func (*groupCostRuntimeCache) DeleteGroupCostSamples(context.Context, string) error { return nil }
func (*groupCostRuntimeCache) ClearGroupCostSamples(context.Context) error          { return nil }

func groupCostRuntimeFixture(t *testing.T, count int, hook func(*http.Request)) (*Service, *groupCostRuntimeCache, *atomic.Int64) {
	t.Helper()
	clock := &atomic.Int64{}
	clock.Store(time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC).UnixNano())
	p := singaporeCostPlatform(t, time.Unix(0, clock.Load()), func(r *http.Request) (int, any) {
		if hook != nil {
			hook(r)
		}
		if r.URL.Path == "/api/v1/usage/stats" {
			return 200, map[string]any{"data": map[string]any{"total_actual_cost": 2}}
		}
		date := businesstime.DateAt(time.Unix(0, clock.Load()))
		return 200, map[string]any{"data": map[string]any{"start_date": date, "end_date": date, "groups": []any{map[string]any{"group_id": 1, "actual_cost": 2}}}}
	})
	p.now = func() time.Time { return time.Unix(0, clock.Load()) }
	cache := &groupCostRuntimeCache{syncTestCache: newSyncTestCache(), states: map[string]GroupCostSamplingState{}, samples: map[string][]GroupCostSample{}, saved: make(chan string, 100)}
	for i := range count {
		id := fmt.Sprint(i)
		site := newTestSite(id, "user", "workspace", 1, &Session{Platform: PlatformSub2API, BaseURL: "http://site-" + id + ".invalid", AccessToken: "fixture-old"})
		site.Metrics.Groups = []GroupInfo{{ID: "1", Name: "组"}}
		_ = cache.Set(t.Context(), site)
	}
	svc := NewService(p, nil, nil, cache)
	svc.now = func() time.Time { return time.Unix(0, clock.Load()) }
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	t.Cleanup(svc.Close)
	return svc, cache, clock
}
func groupCostAwait(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case id := <-ch:
		return id
	case <-time.After(2 * time.Second):
		t.Fatal("cost worker did not finish fixture stage")
		return ""
	}
}
func groupCostWaitIdle(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		s.groupCostQueueMu.Lock()
		empty := s.groupCostQueue == nil || len(s.groupCostQueue.entries) == 0
		s.groupCostQueueMu.Unlock()
		if empty {
			return
		}
		select {
		case <-deadline:
			t.Fatal("cost work retained after completion")
		case <-tick.C:
		}
	}
}
func groupCostEnqueue(s *Service, id string) {
	s.enqueueGroupCostSampling(groupCostWorkKey{"user", "workspace", id}, s.keyUsageDate())
}

func TestGroupCostQueueCoalescesLatestCredentialsAndIndependentReadBudget(t *testing.T) {
	entered := make(chan string, 10)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var thirdRequests atomic.Int32
	svc, cache, clock := groupCostRuntimeFixture(t, 3, func(r *http.Request) {
		if r.URL.Path != "/api/v1/usage/dashboard/snapshot-v2" {
			return
		}
		if r.URL.Host != "site-2.invalid" {
			entered <- r.URL.Host
			select {
			case <-release:
			case <-r.Context().Done():
			}
		} else {
			thirdRequests.Add(1)
			if r.Header.Get("Authorization") != "Bearer fixture-new" {
				t.Error("queued credentials were stale")
			}
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) < 14*time.Second {
				t.Error("queue wait consumed request budget")
			}
		}
	})
	groupCostEnqueue(svc, "0")
	groupCostEnqueue(svc, "1")
	groupCostAwait(t, entered)
	groupCostAwait(t, entered)
	for range 100 {
		groupCostEnqueue(svc, "2")
		groupCostEnqueue(svc, "0")
	}
	svc.groupCostQueueMu.Lock()
	if len(svc.groupCostQueue.entries) != 3 || len(svc.groupCostQueue.pending) != 1 || svc.groupCostQueue.entries[groupCostWorkKey{"user", "workspace", "0"}].followupDate == "" {
		t.Error("duplicate queue entries or missing single followup")
	}
	svc.groupCostQueueMu.Unlock()
	current, _ := cache.Get(t.Context(), "2")
	changed := cloneSite(current)
	changed.Session.AccessToken = "fixture-new"
	_ = cache.Set(t.Context(), &changed)
	clock.Add(int64(46 * time.Second))
	unblock()
	for range 3 {
		groupCostAwait(t, cache.saved)
	}
	groupCostWaitIdle(t, svc)
	if thirdRequests.Load() != 1 {
		t.Error("duplicate trigger generated repeated sampling")
	}
	cache.costMu.Lock()
	defer cache.costMu.Unlock()
	if cache.lockCalls != 3 {
		t.Fatalf("followup bypassed interval: locks=%d", cache.lockCalls)
	}
}

func TestGroupCostQueuePanicAndSaveFailureReleaseNextSite(t *testing.T) {
	logs := failureFixCaptureLogs(t)
	svc, cache, _ := groupCostRuntimeFixture(t, 4, nil)
	cache.panicSite = "0"
	cache.appendFailAt = 1
	for i := range 4 {
		groupCostEnqueue(svc, fmt.Sprint(i))
	}
	groupCostWaitIdle(t, svc)
	cache.costMu.Lock()
	defer cache.costMu.Unlock()
	if len(cache.states) != 2 {
		t.Fatalf("panic/save failure prevented successors: states=%d", len(cache.states))
	}
	if strings.Contains(logs.String(), "fixture-secret-must-not-log") || !strings.Contains(logs.String(), "reason=panic") || !strings.Contains(logs.String(), "outcome=save_failed") {
		t.Fatal("panic/save logging violated safe terminal evidence")
	}
}

func TestGroupCostQueueCancelledOwnerCannotRemoveReplacement(t *testing.T) {
	entered := make(chan string, 10)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	svc, cache, _ := groupCostRuntimeFixture(t, 1, func(r *http.Request) {
		if r.URL.Path == "/api/v1/usage/dashboard/snapshot-v2" {
			entered <- "0"
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	})
	key := groupCostWorkKey{"user", "workspace", "0"}
	groupCostEnqueue(svc, "0")
	groupCostAwait(t, entered)
	svc.cancelGroupCostWork(key)
	groupCostEnqueue(svc, "0")
	groupCostAwait(t, entered)
	unblock()
	groupCostAwait(t, cache.saved)
	groupCostWaitIdle(t, svc)
	cache.costMu.Lock()
	defer cache.costMu.Unlock()
	if len(cache.samples["0:1"]) != 1 {
		t.Fatal("cancelled owner published or removed replacement")
	}
}

func TestGroupCostQueueDisableReenableShutdownAndDate(t *testing.T) {
	for _, mode := range []string{"disabled", "shutdown", "cross_day", "cross_day_read", "workspace_cleanup", "site_disable"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan string, 4)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			svc, cache, clock := groupCostRuntimeFixture(t, 3, func(r *http.Request) {
				if r.URL.Path == "/api/v1/usage/dashboard/snapshot-v2" {
					entered <- r.URL.Host
					select {
					case <-release:
					case <-r.Context().Done():
					}
				}
			})
			groupCostEnqueue(svc, "0")
			groupCostEnqueue(svc, "1")
			groupCostAwait(t, entered)
			groupCostAwait(t, entered)
			groupCostEnqueue(svc, "2")
			switch mode {
			case "disabled":
				svc.SetBackgroundTasksDisabled(true)
				groupCostEnqueue(svc, "2")
			case "shutdown":
				if err := svc.ShutdownGroupCostSampling(t.Context()); err != nil {
					t.Fatal(err)
				}
				groupCostEnqueue(svc, "2")
			case "cross_day", "cross_day_read":
				clock.Add(int64(24 * time.Hour))
			case "workspace_cleanup":
				if err := svc.CleanupDeletedWorkspaceSites(t.Context(), "user", []string{"0", "1", "2"}); err != nil {
					t.Fatal(err)
				}
			case "site_disable":
				for _, id := range []string{"0", "1", "2"} {
					if _, err := svc.UpdateEnabled(t.Context(), "user", id, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			unblock()
			groupCostWaitIdle(t, svc)
			cache.costMu.Lock()
			writes := cache.writes
			cache.costMu.Unlock()
			if writes != 0 {
				t.Fatalf("cancelled/cross-day work wrote %d results", writes)
			}
			if mode == "disabled" {
				svc.SetBackgroundTasksDisabled(false)
				groupCostEnqueue(svc, "2")
				if groupCostAwait(t, cache.saved) != "2" {
					t.Fatal("reenable did not accept future trigger")
				}
				groupCostWaitIdle(t, svc)
			}
			if mode == "shutdown" {
				svc.groupCostQueueMu.Lock()
				closed := svc.groupCostClosed
				workers := svc.groupCostQueue.workers
				svc.groupCostQueueMu.Unlock()
				if !closed || workers != 0 {
					t.Fatal("shutdown retained workers")
				}
			}
		})
	}
}

func TestGroupCostRetryPreservesOtherBackoffsAndPartialSaveEvidence(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		class string
		want  time.Duration
	}{{"network", 20 * time.Minute}, {"invalid_response", 2 * time.Hour}, {"auth_401", 24 * time.Hour}, {"auth_403", 24 * time.Hour}} {
		state := GroupCostSamplingState{LastSuccessAt: now.Add(-time.Hour), SummaryAuthFailures: 2, SummaryNetworkFailures: 2}
		state.RecordSummaryFailure(tc.class, now)
		if !state.SummaryNextAllowedAt.Equal(now.Add(tc.want)) || !state.LastSuccessAt.Equal(now.Add(-time.Hour)) {
			t.Fatalf("legacy backoff changed: %s %+v", tc.class, state)
		}
	}
	for _, failState := range []bool{false, true} {
		t.Run(fmt.Sprint(failState), func(t *testing.T) {
			svc, cache, _ := groupCostRuntimeFixture(t, 1, nil)
			old := GroupCostSamplingState{LastSuccessAt: now.Add(-time.Hour)}
			cache.states["0"] = old
			if failState {
				cache.stateSaveErr = errors.New("cache state unavailable")
			} else {
				cache.appendFailAt = 1
			}
			site, _ := cache.Get(t.Context(), "0")
			outcome := svc.sampleGroupCostsContext(t.Context(), *site, svc.keyUsageDate())
			if outcome.kind != "save_failed" || !cache.states["0"].LastSuccessAt.Equal(old.LastSuccessAt) {
				t.Fatal("partial failure was published as confirmed success")
			}
		})
	}
}

type groupCostDeleteRollbackRepository struct{ enabledTestRepository }

func (*groupCostDeleteRollbackRepository) DeleteSite(context.Context, string, string) error {
	return errors.New("fixture delete rejected")
}

func TestGroupCostQueueDeleteRollbackDoesNotReviveCancelledAttempt(t *testing.T) {
	entered := make(chan string, 4)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	svc, cache, _ := groupCostRuntimeFixture(t, 1, func(r *http.Request) {
		if r.URL.Path == "/api/v1/usage/dashboard/snapshot-v2" {
			entered <- "0"
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	})
	svc.repository = &groupCostDeleteRollbackRepository{}
	svc.SetSiteReferenceChecker(&failureFixReferences{})
	groupCostEnqueue(svc, "0")
	groupCostAwait(t, entered)
	if err := svc.Remove(t.Context(), "user", "0"); err == nil {
		t.Fatal("delete rollback falsely reported success")
	}
	unblock()
	groupCostWaitIdle(t, svc)
	site, _ := cache.Get(t.Context(), "0")
	cache.costMu.Lock()
	writes := cache.writes
	cache.costMu.Unlock()
	if site == nil || writes != 0 || svc.isSiteDeleted("0") {
		t.Fatal("rollback lost original site or revived old attempt")
	}
	groupCostEnqueue(svc, "0")
	groupCostAwait(t, cache.saved)
	groupCostWaitIdle(t, svc)
}

func TestGroupCostQueueRunningGroupChangeAndNewerAttemptDiscardOldResult(t *testing.T) {
	for _, mode := range []string{"groups", "newer_attempt"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan string, 4)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			svc, cache, clock := groupCostRuntimeFixture(t, 1, func(r *http.Request) {
				if r.URL.Path == "/api/v1/usage/dashboard/snapshot-v2" {
					entered <- "0"
					<-release
				}
			})
			groupCostEnqueue(svc, "0")
			groupCostAwait(t, entered)
			if mode == "groups" {
				site, _ := cache.Get(t.Context(), "0")
				copy := cloneSite(site)
				copy.Metrics.Groups = []GroupInfo{{ID: "2", Name: "new group"}}
				_ = cache.Set(t.Context(), &copy)
			} else {
				cache.costMu.Lock()
				cache.states["0"] = GroupCostSamplingState{LastAttemptAt: time.Unix(0, clock.Load()).Add(time.Minute)}
				cache.costMu.Unlock()
			}
			unblock()
			groupCostWaitIdle(t, svc)
			cache.costMu.Lock()
			defer cache.costMu.Unlock()
			if cache.writes != 0 {
				t.Fatal("old sample overwrote new group set or newer attempt")
			}
		})
	}
}

func TestGroupCostRetryReadTimeoutCanSaveFailureWithIndependentContext(t *testing.T) {
	svc, cache, clock := groupCostRuntimeFixture(t, 1, nil)
	svc.platformService.httpClient.client.Transport = keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })
	site, _ := cache.Get(t.Context(), "0")
	outcome := svc.sampleGroupCostsContext(t.Context(), *site, svc.keyUsageDate())
	state := cache.states["0"]
	if outcome.kind != "failed" || state.LastAttemptStatus != "failed" || !state.LastAttemptAt.Equal(time.Unix(0, clock.Load())) || state.LastReason == "" || cache.writes != 1 {
		t.Fatal("network timeout prevented independent failure persistence")
	}
}

func TestGroupCostQueuePanicDuringLockedReadOrSaveDoesNotStrandService(t *testing.T) {
	for _, stage := range []string{"read", "save"} {
		t.Run(stage, func(t *testing.T) {
			logs := failureFixCaptureLogs(t)
			svc, cache, _ := groupCostRuntimeFixture(t, 2, nil)
			if stage == "read" {
				cache.panicReadSite = "0"
			} else {
				cache.panicAppendSite = "0"
			}
			groupCostEnqueue(svc, "0")
			groupCostEnqueue(svc, "1")
			if groupCostAwait(t, cache.saved) != "1" {
				t.Fatal("panic stranded another station")
			}
			groupCostWaitIdle(t, svc)
			if strings.Contains(logs.String(), "fixture-secret-") {
				t.Fatal("raw panic content leaked")
			}
		})
	}
}
