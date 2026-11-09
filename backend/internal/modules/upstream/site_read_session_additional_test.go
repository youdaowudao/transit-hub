package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type siteReadExtraCache struct {
	*keySnapshotTestCache
	oldReads    chan struct{}
	rejectPause bool
}

func (c *siteReadExtraCache) Get(ctx context.Context, id string) (*Site, error) {
	site, err := c.keySnapshotTestCache.Get(ctx, id)
	if site != nil && site.Session != nil && site.Session.AccessToken == "fixture-old" && c.oldReads != nil {
		select {
		case c.oldReads <- struct{}{}:
		default:
		}
	}
	return site, err
}

func (c *siteReadExtraCache) Set(ctx context.Context, site *Site) error {
	if c.rejectPause && SiteReadAuthError(site) != nil {
		return errors.New("fixture runtime storage unavailable")
	}
	return c.keySnapshotTestCache.Set(ctx, site)
}

func siteReadExtraResponse(r *http.Request, status int, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
}

func siteReadExtraService(t *testing.T, transport keyUsageRoundTripFunc) (*Service, *siteReadExtraCache, *singaporeFailureRepository, *Site) {
	t.Helper()
	now := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
	expires := now.Add(50 * time.Second).UnixMilli()
	site := newTestSite("site", "user", "workspace", 1, &Session{Platform: PlatformSub2API, AuthMode: AuthModeToken, BaseURL: "https://fixture.invalid", AccessToken: "fixture-old", RefreshToken: "fixture-refresh", ExpiresAt: &expires})
	cache := &siteReadExtraCache{keySnapshotTestCache: newKeySnapshotTestCache(site)}
	repo := &singaporeFailureRepository{durable: cloneSite(site)}
	platform := NewPlatformService(NewHTTPClient(&http.Client{Transport: transport}))
	platform.now = func() time.Time { return now }
	svc := NewService(platform, repo, nil, cache)
	svc.now = func() time.Time { return now }
	svc.SetBackgroundTasksDisabled(true)
	svc.SetAdminAccountResolver(&fakeAccountResolver{current: map[string]string{"user": "workspace"}})
	t.Cleanup(svc.Close)
	return svc, cache, repo, site
}

func siteReadExtraWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("fixture did not reach the expected read boundary")
	}
}

func siteReadExtraSuccess(r *http.Request) (*http.Response, error) {
	return siteReadExtraResponse(r, 200, map[string]any{"data": map[string]any{"access_token": "fixture-new", "refresh_token": "fixture-next", "expires_in": 3600}})
}

func siteReadExtraGate(t *testing.T) (chan struct{}, func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return gate, release
}

func TestSiteReadFourBusinessEntriesShareOneRotation(t *testing.T) {
	gate, release := siteReadExtraGate(t)
	var refreshes, reads atomic.Int32
	var svc *Service
	var cache *siteReadExtraCache
	var repo *singaporeFailureRepository
	var site *Site
	svc, cache, repo, site = siteReadExtraService(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			refreshes.Add(1)
			<-gate
			return siteReadExtraSuccess(r)
		}
		reads.Add(1)
		current, _ := cache.keySnapshotTestCache.Get(t.Context(), site.ID)
		if current.Session.AccessToken != "fixture-new" || staleDurable(repo).Session.AccessToken != "fixture-new" {
			t.Error("business statistics started before the shared login was saved")
		}
		return siteReadExtraResponse(r, 503, map[string]any{"message": "fixture statistics unavailable"})
	})
	cache.oldReads = make(chan struct{}, 16)
	done := make(chan struct{}, 4)
	go func() { _, _ = svc.Sync(t.Context(), "user", "site"); done <- struct{}{} }()
	go func() { _, _ = svc.KeyUsageForDate(t.Context(), "user", "workspace", "2031-02-03"); done <- struct{}{} }()
	go func() { _, _ = svc.FetchGroupDailyStats(t.Context(), "user", "site"); done <- struct{}{} }()
	go func() { svc.collectKeyUsageSnapshot(*site); done <- struct{}{} }()
	// Before the blocked response: Sync reads three times (ownership, syncing,
	// prepare), dated once, group twice, and today's collector twice. Reaching all
	// eight ensures every real entry has entered its common refresh boundary.
	for range 8 {
		siteReadExtraWait(t, cache.oldReads)
	}
	release()
	for range 4 {
		siteReadExtraWait(t, done)
	}
	if refreshes.Load() != 1 || reads.Load() == 0 || staleDurable(repo).Session.RefreshToken != "fixture-next" {
		t.Fatal("overlapping business entries repeated the refresh or lost its result")
	}
	value, _ := cache.GetKeyUsageSnapshot(t.Context(), site.ID)
	if value != nil && value.Complete {
		t.Fatal("failed downstream reads published a successful cost collection")
	}
}

func TestSiteReadDifferentSitesRefreshInParallel(t *testing.T) {
	gate, release := siteReadExtraGate(t)
	started := make(chan struct{}, 2)
	svc, cache, _, first := siteReadExtraService(t, func(r *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-gate
		return siteReadExtraSuccess(r)
	})
	// A cache-only repository is an existing test mode; this case isolates the
	// two site coordinators rather than the single-row persistence fixture.
	svc.repository = nil
	second := cloneSite(first)
	second.ID = "other-site"
	_ = cache.Set(t.Context(), &second)
	results := make(chan error, 2)
	for _, site := range []Site{*first, second} {
		go func(site Site) {
			_, err := svc.prepareSiteReadSession(t.Context(), site, time.Minute, true)
			results <- err
		}(site)
	}
	for range 2 {
		siteReadExtraWait(t, started)
	}
	release()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal("one site's refresh blocked or corrupted another site")
		}
	}
}

func TestSiteReadOldFlightDoesNotRemoveNewLoginFlight(t *testing.T) {
	oldGate, releaseOld := siteReadExtraGate(t)
	newGate, releaseNew := siteReadExtraGate(t)
	started := make(chan struct{}, 2)
	var calls atomic.Int32
	svc, cache, repo, original := siteReadExtraService(t, func(r *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		started <- struct{}{}
		if call == 1 {
			<-oldGate
		} else {
			<-newGate
		}
		return siteReadExtraSuccess(r)
	})
	oldResult := make(chan error, 1)
	go func() {
		_, err := svc.prepareSiteReadSession(t.Context(), *original, time.Minute, true)
		oldResult <- err
	}()
	siteReadExtraWait(t, started)
	fresh := cloneSite(original)
	session := copyReadSession(*fresh.Session)
	session.AccessToken, session.RefreshToken = "fixture-manual-login", "fixture-manual-refresh"
	fresh.Session = &session
	svc.mu.Lock()
	_ = cache.Set(t.Context(), &fresh)
	repo.mu.Lock()
	repo.durable = cloneSite(&fresh)
	repo.mu.Unlock()
	svc.mu.Unlock()
	newResult := make(chan error, 1)
	go func() { _, err := svc.prepareSiteReadSession(t.Context(), fresh, time.Minute, true); newResult <- err }()
	siteReadExtraWait(t, started)
	releaseOld()
	if <-oldResult == nil {
		t.Fatal("old flight overwrote the new login")
	}
	svc.mu.Lock()
	flight := svc.sessionRefreshFlights[original.ID]
	retained := flight != nil && flight.baseline.AccessToken == "fixture-manual-login"
	svc.mu.Unlock()
	if !retained {
		t.Fatal("finishing the old flight removed the new login's active flight")
	}
	releaseNew()
	if err := <-newResult; err != nil || calls.Load() != 2 {
		t.Fatal("new login could not finish its independent refresh")
	}
}

func TestSiteReadConcurrentRefreshSharesSavedResult(t *testing.T) {
	gate, release := siteReadExtraGate(t)
	var calls atomic.Int32
	svc, cache, repo, site := siteReadExtraService(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-gate
		return siteReadExtraSuccess(r)
	})
	cache.oldReads = make(chan struct{}, 8)
	results := make(chan error, 4)
	for range 4 {
		go func() {
			result, err := svc.prepareSiteReadSession(t.Context(), cloneSite(site), time.Minute, true)
			if err == nil && (!result.Persisted || result.Session.AccessToken != "fixture-new") {
				err = errors.New("shared refresh returned before its login was saved")
			}
			results <- err
		}()
	}
	for range 4 {
		siteReadExtraWait(t, cache.oldReads)
	}
	release()
	for range 4 {
		if err := <-results; err != nil {
			t.Fatalf("shared refresh failed: %v", err)
		}
	}
	if calls.Load() != 1 || staleDurable(repo).Session.RefreshToken != "fixture-next" {
		t.Fatal("concurrent callers repeated refresh or lost the saved replacement")
	}
	svc.mu.Lock()
	remaining := len(svc.sessionRefreshFlights)
	svc.mu.Unlock()
	if remaining != 0 {
		t.Fatal("completed refresh retained an in-flight entry")
	}
}

func TestSiteReadCanceledWaiterDoesNotCancelLeader(t *testing.T) {
	gate, release := siteReadExtraGate(t)
	var calls atomic.Int32
	svc, cache, _, site := siteReadExtraService(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-gate
		return siteReadExtraSuccess(r)
	})
	cache.oldReads = make(chan struct{}, 5)
	leader := make(chan error, 1)
	go func() { _, err := svc.prepareSiteReadSession(t.Context(), *site, time.Minute, true); leader <- err }()
	siteReadExtraWait(t, cache.oldReads)
	ctx, cancel := context.WithCancel(t.Context())
	waiter := make(chan error, 1)
	go func() { _, err := svc.prepareSiteReadSession(ctx, *site, time.Minute, true); waiter <- err }()
	siteReadExtraWait(t, cache.oldReads)
	cancel()
	if !errors.Is(<-waiter, context.Canceled) {
		t.Fatal("canceled waiter remained attached to the refresh")
	}
	select {
	case <-leader:
		t.Fatal("waiter cancellation stopped the leader")
	default:
	}
	release()
	if err := <-leader; err != nil || calls.Load() != 1 {
		t.Fatal("leader did not finish its single request")
	}
}

func TestSiteReadCanceledLeaderDoesNotCauseWaiterRetry(t *testing.T) {
	var calls atomic.Int32
	svc, cache, _, site := siteReadExtraService(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	cache.oldReads = make(chan struct{}, 5)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	leader, waiter := make(chan error, 1), make(chan error, 1)
	go func() { _, err := svc.prepareSiteReadSession(ctx, *site, time.Minute, true); leader <- err }()
	siteReadExtraWait(t, cache.oldReads)
	go func() { _, err := svc.prepareSiteReadSession(t.Context(), *site, time.Minute, true); waiter <- err }()
	siteReadExtraWait(t, cache.oldReads)
	cancel()
	if <-leader == nil || <-waiter == nil || calls.Load() != 1 {
		t.Fatal("leader cancellation silently succeeded or retried the saved login")
	}
}

func TestSiteReadCanceledAfterSuccessfulResponseStillSavesLogin(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	svc, cache, repo, site := siteReadExtraService(t, func(r *http.Request) (*http.Response, error) {
		response, err := siteReadExtraSuccess(r)
		cancel()
		return response, err
	})
	result, err := svc.prepareSiteReadSession(ctx, *site, time.Minute, true)
	current, _ := cache.Get(t.Context(), site.ID)
	if !errors.Is(err, context.Canceled) || !result.Persisted || current.Session.AccessToken != "fixture-new" || staleDurable(repo).Session.AccessToken != "fixture-new" {
		t.Fatal("caller cancellation discarded a successful refresh response")
	}
}

func TestSiteReadRefreshThresholdsRemainIndependent(t *testing.T) {
	var calls atomic.Int32
	svc, cache, repo, site := siteReadExtraService(t, func(r *http.Request) (*http.Response, error) { calls.Add(1); return siteReadExtraSuccess(r) })
	expires := svc.keyUsageNow().Add(5 * time.Minute).UnixMilli()
	site.Session.ExpiresAt = &expires
	_ = cache.Set(t.Context(), site)
	repo.durable = cloneSite(site)
	result, err := svc.prepareSiteReadSession(t.Context(), *site, time.Minute, true)
	if err != nil || result.Persisted || calls.Load() != 0 {
		t.Fatal("the ordinary read refreshed outside its original window")
	}
	if _, err := svc.prepareSiteReadSession(t.Context(), *site, 10*time.Minute, false); err != nil || calls.Load() != 1 {
		t.Fatal("the ordinary read swallowed the larger automatic-sync window")
	}
	for _, tc := range []struct {
		remaining time.Duration
		inclusive bool
		want      bool
	}{{time.Minute, true, true}, {time.Minute, false, false}, {time.Minute + time.Millisecond, true, false}, {time.Minute - time.Millisecond, false, true}} {
		expires := svc.keyUsageNow().Add(tc.remaining).UnixMilli()
		session := *site.Session
		session.ExpiresAt = &expires
		if svc.platformService.sessionNeedsRefresh(session, time.Minute, tc.inclusive) != tc.want {
			t.Fatal("refresh threshold boundary changed")
		}
	}
}

func TestSiteReadOldRefreshCannotPauseOrOverwriteNewLogin(t *testing.T) {
	for _, status := range []int{200, 401} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			started := make(chan struct{})
			gate, release := siteReadExtraGate(t)
			svc, cache, repo, site := siteReadExtraService(t, func(r *http.Request) (*http.Response, error) {
				close(started)
				<-gate
				if status == 200 {
					return siteReadExtraSuccess(r)
				}
				return siteReadExtraResponse(r, status, map[string]any{"message": "fixture old login rejected"})
			})
			finished := make(chan error, 1)
			go func() { _, err := svc.prepareSiteReadSession(t.Context(), *site, time.Minute, true); finished <- err }()
			siteReadExtraWait(t, started)
			svc.mu.Lock()
			fresh := cloneSite(site)
			session := copyReadSession(*fresh.Session)
			session.AccessToken, session.RefreshToken = "fixture-manual-login", "fixture-manual-refresh"
			fresh.Session = &session
			_ = cache.Set(t.Context(), &fresh)
			repo.mu.Lock()
			repo.durable = cloneSite(&fresh)
			repo.mu.Unlock()
			svc.mu.Unlock()
			release()
			if <-finished == nil {
				t.Fatal("old refresh was published over a newer login")
			}
			current, _ := cache.Get(t.Context(), site.ID)
			if current.Session.AccessToken != "fixture-manual-login" || current.Status != StatusConnected || current.ErrorKey != nil {
				t.Fatal("old result overwrote or paused a newer login")
			}
		})
	}
}

func TestSiteReadNon401RefreshFailuresNeverPersistPause(t *testing.T) {
	for _, status := range []int{403, 429, 503, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			svc, cache, _, site := siteReadExtraService(t, func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return siteReadExtraResponse(r, status, map[string]any{"message": "fixture unavailable", "data": map[string]any{}})
			})
			for range 2 {
				if _, err := svc.prepareSiteReadSession(t.Context(), *site, time.Minute, true); err == nil {
					t.Fatal("upstream rejection/incomplete response was hidden")
				}
			}
			current, _ := cache.Get(t.Context(), site.ID)
			if SiteReadAuthError(current) != nil || calls.Load() != 2 {
				t.Fatal("non-401 refresh failure became a persistent pause")
			}
		})
	}
}

func TestSiteReadBusinessNetworkAndMetricsFailuresDoNotPause(t *testing.T) {
	for _, failure := range []string{"business", "network", "metrics_401"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			svc, cache, repo, site := siteReadExtraService(t, func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if failure == "network" {
					return nil, errors.New("fixture network unavailable")
				}
				if failure == "business" {
					return siteReadExtraResponse(r, 200, map[string]any{"success": false, "message": "fixture business rejection"})
				}
				return siteReadExtraResponse(r, 401, map[string]any{"message": "fixture statistics authorization rejected"})
			})
			if failure == "metrics_401" {
				expires := svc.keyUsageNow().Add(time.Hour).UnixMilli()
				site.Session.ExpiresAt = &expires
				_ = cache.Set(t.Context(), site)
				repo.durable = cloneSite(site)
				for range 2 {
					_, _ = svc.Sync(t.Context(), "user", "site")
				}
			} else {
				for range 2 {
					if _, err := svc.prepareSiteReadSession(t.Context(), *site, time.Minute, true); err == nil {
						t.Fatal("an unsuccessful refresh became a successful read")
					}
				}
			}
			current, _ := cache.Get(t.Context(), site.ID)
			if SiteReadAuthError(current) != nil || calls.Load() != 2 {
				t.Fatal("business/network/metrics failure stopped the original retry behavior")
			}
		})
	}
}

func TestSiteReadRecoveryRetainsIdentityAndCancellationChecks(t *testing.T) {
	for _, mode := range []string{"unconfirmed_token", "password_email", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newSwitchFixture()
			authMode := AuthModeToken
			if mode == "password_email" {
				authMode = AuthModePassword
			}
			svc, cache, repo, site := switchSetup(t, fixture, PlatformSub2API, authMode, mode == "cancelled")
			key := ErrorRefreshTokenRejected
			site.Status, site.ErrorKey = StatusError, &key
			cache.add(site)
			repo.durable = cloneSite(site)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			if mode == "cancelled" {
				fixture.beforePrincipal = func(*http.Request) error { cancel(); return nil }
			}
			_, err := svc.Update(ctx, site.UserID, site.ID, switchDTO(site, authMode))
			current, _ := cache.Get(t.Context(), site.ID)
			if fixture.oldReads != 0 {
				t.Fatal("recovering a rejected login used the old saved credentials")
			}
			if mode == "password_email" {
				if err != nil || current.Status != StatusConnected || current.ErrorKey != nil || !current.Session.UserIDConfirmed {
					t.Fatal("existing same-email identity fallback did not recover the original account")
				}
			} else if err == nil || current.ErrorKey == nil || *current.ErrorKey != key || mode == "unconfirmed_token" && len(fixture.paths) != 0 {
				t.Fatal("missing identity evidence or cancellation cleared a saved refusal")
			}
		})
	}
}

func TestSiteReadRefusalSaveFailureReportsActualResult(t *testing.T) {
	svc, cache, repo, site := siteReadExtraService(t, func(r *http.Request) (*http.Response, error) {
		return siteReadExtraResponse(r, 401, map[string]any{"message": "fixture rejected"})
	})
	cache.rejectPause = true
	result, err := svc.prepareSiteReadSession(t.Context(), *site, time.Minute, true)
	var detail *RequestError
	if !result.Rejected || !errors.As(err, &detail) || detail.Stage != "save" || detail.StatusCode != 503 || !strings.Contains(detail.UpstreamMessage, "已保存") || !strings.Contains(detail.UpstreamMessage, "后续任务仍可能重试") || strings.Contains(detail.UpstreamMessage, "已暂停自动访问") {
		t.Fatal("failed runtime publication falsely claimed a confirmed pause")
	}
	current, _ := cache.Get(t.Context(), site.ID)
	if SiteReadAuthError(current) != nil || SiteReadAuthError(func() *Site { v := staleDurable(repo); return &v }()) == nil {
		t.Fatal("PG-success/Redis-failure outcome was changed")
	}
}

func TestSiteReadPauseSurvivesReloadAndDoesNotAffectOtherLoginTypes(t *testing.T) {
	var calls atomic.Int32
	svc, cache, repo, site := siteReadExtraService(t, func(r *http.Request) (*http.Response, error) { calls.Add(1); return siteReadExtraSuccess(r) })
	key := ErrorRefreshTokenRejected
	site.Status, site.ErrorKey = StatusError, &key
	_ = cache.Set(t.Context(), site)
	repo.durable = cloneSite(site)
	if _, err := svc.prepareSiteReadSession(t.Context(), *site, time.Minute, true); !isSiteReadAuthFailure(err) || calls.Load() != 0 {
		t.Fatal("reloaded refusal did not stop the saved login")
	}
	for _, session := range []Session{{Platform: PlatformNewAPI, RefreshToken: "fixture-newapi"}, {Platform: PlatformSub2API, AccessToken: "fixture-no-refresh"}} {
		if svc.platformService.sessionNeedsRefresh(session, time.Minute, true) {
			t.Fatal("unrelated login types acquired a new refresh behavior")
		}
	}
}
