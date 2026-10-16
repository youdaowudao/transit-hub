package upstream

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Match the production group guard: merge onto the durable row and return the
// actual result, retaining newer group results when the baseline has changed.
type staleGuardRepo struct{ singaporeFailureRepository }

func (r *staleGuardRepo) SaveExistingSiteWithGroupGuard(_ context.Context, expected, candidate, baseline Site, _ ...*RateNoticeInput) (Site, string, bool, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !unchangedSiteConfiguration(r.durable, expected) {
		return Site{}, "", false, false, nil
	}
	accepted := unchangedGroupBaseline(r.durable, baseline)
	next := mergeExistingSiteChanges(r.durable, expected, candidate)
	if !accepted {
		keepCurrentGroupResult(&next, r.durable)
	}
	r.durable = cloneSite(&next)
	return next, "fixture-version", true, accepted, nil
}

func staleErrorSite() *Site {
	site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: "http://fixture.invalid", AccessToken: "fixture-old"})
	site.Account = "同一账号"
	site.RequestedPlatform = PlatformSub2API
	return site
}

func staleErrorPlatform(t *testing.T, status *atomic.Int32, gate func(*http.Request)) *PlatformService {
	t.Helper()
	return singaporeCostPlatform(t, time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC), func(r *http.Request) (int, any) {
		if gate != nil {
			gate(r)
		}
		if code := status.Load(); code != 0 {
			return int(code), map[string]any{"message": "fixture upstream unavailable"}
		}
		switch r.URL.Path {
		case "/api/v1/auth/me":
			return 200, map[string]any{"data": map[string]any{"id": 1, "balance": 20}}
		case "/api/v1/usage/dashboard/stats":
			return 200, map[string]any{"data": map[string]any{"today_actual_cost": 1}}
		case "/api/v1/usage/stats":
			return 200, map[string]any{"data": map[string]any{"total_actual_cost": 9}}
		case "/api/v1/groups/available", "/api/v1/groups/rates":
			return 200, map[string]any{"data": []any{}}
		}
		return 404, map[string]any{}
	})
}

func configureStaleErrorService(t *testing.T, svc *Service) {
	t.Helper()
	svc.now = func() time.Time { return time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC) }
	svc.SetBackgroundTasksDisabled(true)
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	t.Cleanup(svc.Close)
}

func staleDurable(repo *singaporeFailureRepository) Site {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return cloneSite(&repo.durable)
}

func assertStaleErrorState(t *testing.T, response Response, cache *syncTestCache, durable Site, status Status, reason string) {
	t.Helper()
	cached, err := cache.Get(t.Context(), response.ID)
	if err != nil || cached == nil {
		t.Fatal("cache read failed")
	}
	for _, result := range []struct {
		name   string
		status Status
		key    *string
	}{{"response", response.Status, response.ErrorKey}, {"Redis", cached.Status, cached.ErrorKey}, {"database", durable.Status, durable.ErrorKey}} {
		if result.status != status {
			t.Errorf("%s status=%s, want %s", result.name, result.status, status)
		}
		if reason == "" {
			if result.key != nil {
				t.Errorf("%s retained stale error %q", result.name, *result.key)
			}
		} else if result.key == nil || *result.key != reason {
			t.Errorf("%s error=%v, want %q", result.name, result.key, reason)
		}
	}
}

// T1 and T2: failure followed by success clears the reason through both saves.
func TestSyncStaleErrorRecovery(t *testing.T) {
	for _, guarded := range []bool{true, false} {
		name := "T2_conditional"
		if guarded {
			name = "T1_group_guard"
		}
		t.Run(name, func(t *testing.T) {
			var status atomic.Int32
			site := staleErrorSite()
			cache := newSyncTestCache(site)
			repo := &singaporeFailureRepository{durable: cloneSite(site)}
			var svc *Service
			if guarded {
				guard := &staleGuardRepo{singaporeFailureRepository: singaporeFailureRepository{durable: cloneSite(site)}}
				repo = &guard.singaporeFailureRepository
				svc = NewService(staleErrorPlatform(t, &status, nil), guard, nil, cache)
			} else {
				svc = NewService(staleErrorPlatform(t, &status, nil), repo, nil, cache)
			}
			configureStaleErrorService(t, svc)
			status.Store(503)
			failed, err := svc.sync(t.Context(), site.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertStaleErrorState(t, failed, cache, staleDurable(repo), StatusError, "admin.upstream.errors.upstreamServerError")
			status.Store(0)
			recovered, err := svc.sync(t.Context(), site.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertStaleErrorState(t, recovered, cache, staleDurable(repo), StatusConnected, "")
		})
	}
}

// T3: identical real failures retain the reason; a different failure replaces it.
func TestSyncStaleErrorRepeatedAndChangedFailure(t *testing.T) {
	var status atomic.Int32
	site := staleErrorSite()
	cache := newSyncTestCache(site)
	repo := &staleGuardRepo{singaporeFailureRepository: singaporeFailureRepository{durable: cloneSite(site)}}
	svc := NewService(staleErrorPlatform(t, &status, nil), repo, nil, cache)
	configureStaleErrorService(t, svc)
	for _, code := range []int32{503, 503, 429} {
		status.Store(code)
		response, err := svc.sync(t.Context(), site.ID)
		if err != nil {
			t.Fatal(err)
		}
		reason := "admin.upstream.errors.upstreamServerError"
		if code == 429 {
			reason = "admin.upstream.errors.rateLimited"
		}
		assertStaleErrorState(t, response, cache, staleDurable(&repo.singaporeFailureRepository), StatusError, reason)
	}
}

// T4: a known failed save restores the previous state and retains its reason.
func TestSyncStaleErrorKnownSaveFailure(t *testing.T) {
	var status atomic.Int32
	site := staleErrorSite()
	cache := newSyncTestCache(site)
	repo := &singaporeFailureRepository{durable: cloneSite(site)}
	svc := NewService(staleErrorPlatform(t, &status, nil), repo, nil, cache)
	configureStaleErrorService(t, svc)
	status.Store(503)
	if _, err := svc.sync(t.Context(), site.ID); err != nil {
		t.Fatal(err)
	}
	repo.failure = "known"
	status.Store(0)
	_, err := svc.sync(t.Context(), site.ID)
	var saveErr *RequestError
	if !errors.As(err, &saveErr) || !strings.Contains(saveErr.UpstreamMessage, "保存本站资料失败") {
		t.Fatalf("expected known save failure, got %v", err)
	}
	cached, err := cache.Get(t.Context(), site.ID)
	if err != nil || cached == nil {
		t.Fatal("cache read failed")
	}
	durable := staleDurable(repo)
	if cached.Status != StatusError || durable.Status != StatusError || cached.ErrorKey == nil || !reflect.DeepEqual(cached.ErrorKey, durable.ErrorKey) {
		t.Fatal("known failed save lost the previous Redis state or reason")
	}
}

// T5 uses an isolated schema supplied by CI, never the fixed development DB.
func TestSyncStaleErrorPostgres(t *testing.T) {
	pool := singaporeCostPGPool(t)
	repo := NewRepository(pool)
	site := staleErrorSite()
	site.Status = StatusError
	reason := "admin.upstream.errors.networkTimeout"
	site.ErrorKey = &reason
	if _, err := repo.SaveSiteWithSnapshotVersion(t.Context(), *site); err != nil {
		t.Fatal(err)
	}
	cache := newSyncTestCache(site)
	var status atomic.Int32
	svc := NewService(staleErrorPlatform(t, &status, nil), repo, nil, cache)
	configureStaleErrorService(t, svc)
	response, err := svc.sync(t.Context(), site.ID)
	if err != nil {
		t.Fatal(err)
	}
	var savedStatus Status
	var savedReason *string
	if err := pool.QueryRow(t.Context(), `SELECT status,error_key FROM upstream_sites WHERE id=$1`, site.ID).Scan(&savedStatus, &savedReason); err != nil {
		t.Fatal(err)
	}
	assertStaleErrorState(t, response, cache, Site{Status: savedStatus, ErrorKey: savedReason}, StatusConnected, "")
}

type staleSyncResult struct {
	response Response
	err      error
}

func staleWait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for in-process sync fixture")
		var zero T
		return zero
	}
}

func staleGatedSync(t *testing.T) (*Service, *staleGuardRepo, *syncTestCache, *Site, func(), <-chan staleSyncResult) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var gated atomic.Bool
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var status atomic.Int32
	platform := staleErrorPlatform(t, &status, func(r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), "fixture-old") && gated.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	})
	site := staleErrorSite()
	site.Status = StatusError
	reason := "admin.upstream.errors.networkTimeout"
	site.ErrorKey = &reason
	cache := newSyncTestCache(site)
	repo := &staleGuardRepo{singaporeFailureRepository: singaporeFailureRepository{durable: cloneSite(site)}}
	svc := NewService(platform, repo, nil, cache)
	configureStaleErrorService(t, svc)
	done := make(chan staleSyncResult, 1)
	t.Cleanup(func() { unblock() })
	go func() {
		response, err := svc.sync(t.Context(), site.ID)
		done <- staleSyncResult{response, err}
	}()
	staleWait(t, entered)
	return svc, repo, cache, site, unblock, done
}

func TestSyncStaleErrorSettingsDuringSync(t *testing.T) {
	// T6 also proves S3: the transient Redis entry differs only in status.
	svc, repo, cache, site, unblock, done := staleGatedSync(t)
	expected := cloneSite(site)
	expected.Status = StatusSyncing
	transient, err := cache.Get(t.Context(), site.ID)
	if err != nil || !reflect.DeepEqual(transient, &expected) {
		t.Error("sync transient changed fields other than status")
	}
	settings := site.Settings
	settings.BalanceThreshold = float64Pointer(7)
	if _, err := svc.UpdateSettings(t.Context(), "user", site.ID, settings); err != nil {
		t.Fatal(err)
	}
	durable := staleDurable(&repo.singaporeFailureRepository)
	cached, err := cache.Get(t.Context(), site.ID)
	if err != nil || cached == nil || cached.ErrorKey == nil || !reflect.DeepEqual(cached.ErrorKey, durable.ErrorKey) || !reflect.DeepEqual(cached.Settings, settings) || !reflect.DeepEqual(durable.Settings, settings) {
		t.Error("settings during sync lost the previous reason or failed to save")
	}
	unblock()
	result := staleWait(t, done)
	if result.err != nil {
		t.Fatal(result.err)
	}
	assertStaleErrorState(t, result.response, cache, staleDurable(&repo.singaporeFailureRepository), StatusConnected, "")
}

func TestSyncStaleErrorReloginDuringSync(t *testing.T) {
	// T7: no intervening group update, so the re-login result is accepted.
	svc, repo, cache, site, unblock, done := staleGatedSync(t)
	response, err := svc.Update(t.Context(), "user", site.ID, UpdateRequest{Name: site.Name, SiteURL: site.BaseURL, Platform: PlatformSub2API, AuthMode: AuthModeToken, Account: site.Account, AccessToken: "fixture-new", RechargeRate: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertStaleErrorState(t, response, cache, staleDurable(&repo.singaporeFailureRepository), StatusConnected, "")
	unblock()
	result := staleWait(t, done)
	var requestErr *RequestError
	if !errors.As(result.err, &requestErr) || requestErr.StatusCode != 409 || !strings.Contains(requestErr.UpstreamMessage, "站点登录资料已更新") {
		t.Fatalf("old sync did not reject the changed session: %v", result.err)
	}
	assertStaleErrorState(t, result.response, cache, staleDurable(&repo.singaporeFailureRepository), StatusConnected, "")
}
