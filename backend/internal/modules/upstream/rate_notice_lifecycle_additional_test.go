package upstream

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

func notificationLifecycleUpdateRequest() UpdateRequest {
	return UpdateRequest{Name: "updated-disabled-name", SiteURL: "http://fixture.invalid", Platform: PlatformSub2API, AuthMode: AuthModeToken, Account: "fixture-user", AccessToken: "fixture-only", RechargeRate: 1}
}

func TestRateNoticeLifecycleDisabledReloginKeepsSavedCredentialsAndBaselinePostgres(t *testing.T) {
	pool := rateNoticeCriticalPool(t)
	sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
	svc, cache := rateNoticeCriticalService(t, pool, sample)
	rateNoticeCriticalSync(t, svc)
	if _, err := svc.UpdateEnabled(t.Context(), "user", "site", false); err != nil {
		t.Fatal(err)
	}
	sample.rate = 2
	response, err := svc.Update(t.Context(), "user", "site", notificationLifecycleUpdateRequest())
	if err != nil || response.IsEnabled() || response.Name != "updated-disabled-name" {
		t.Fatal("same-identity relogin should retain allowed saved updates without resuming management")
	}
	rateNoticeCriticalCount(t, pool, 0)
	var baseline float64
	var name string
	var enabled bool
	if err := pool.QueryRow(t.Context(), `SELECT multiplier FROM upstream_rate_notice_baselines WHERE site_id='site'`).Scan(&baseline); err != nil || baseline != 1 {
		t.Fatal("disabled relogin must not advance the notification comparison baseline")
	}
	if err := pool.QueryRow(t.Context(), `SELECT name,enabled FROM upstream_sites WHERE id='site'`).Scan(&name, &enabled); err != nil || name != "updated-disabled-name" || enabled {
		t.Fatal("disabled relogin must still persist permitted configuration")
	}
	stored, err := cache.Get(t.Context(), "site")
	if err != nil || stored == nil || stored.Session == nil || stored.IsEnabled() {
		t.Fatal("disabled relogin must preserve same-identity login and lifecycle")
	}
	if _, err := svc.UpdateEnabled(t.Context(), "user", "site", true); err != nil {
		t.Fatal(err)
	}
	rateNoticeCriticalCount(t, pool, 0)
	rateNoticeCriticalSync(t, svc)
	rateNoticeCriticalCount(t, pool, 1)
}

func TestRateNoticeLifecycleLateSyncOrReloginCannotNotifyAfterDisablePostgres(t *testing.T) {
	for _, entry := range []string{"sync", "relogin"} {
		t.Run(entry, func(t *testing.T) {
			pool := rateNoticeCriticalPool(t)
			sample := &rateNoticeCriticalSample{rate: 1, status: 200, name: "a"}
			svc, cache := rateNoticeCriticalService(t, pool, sample)
			rateNoticeCriticalSync(t, svc)
			sample.rate = 2
			original := svc.platformService.httpClient.client.Transport
			started, release := make(chan struct{}), make(chan struct{})
			var once, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			svc.platformService.httpClient.client.Transport = keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/v1/groups/rates" {
					once.Do(func() { close(started) })
					select {
					case <-release:
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}
				return original.RoundTrip(r)
			})
			defer unblock()
			done := make(chan error, 1)
			go func() {
				var err error
				if entry == "sync" {
					_, err = svc.syncOnce(t.Context(), "site")
				} else {
					_, err = svc.Update(t.Context(), "user", "site", notificationLifecycleUpdateRequest())
				}
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("fixture did not reach controlled in-memory observation")
			}
			if _, err := svc.UpdateEnabled(t.Context(), "user", "site", false); err != nil {
				t.Fatal(err)
			}
			// Unblock exactly this observation without making another upstream call.
			unblock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("late observation did not finish")
			}
			rateNoticeCriticalCount(t, pool, 0)
			var enabled bool
			var baseline float64
			if err := pool.QueryRow(context.Background(), `SELECT enabled FROM upstream_sites WHERE id='site'`).Scan(&enabled); err != nil || enabled {
				t.Fatal("late sync or relogin must not re-enable the persisted site")
			}
			if err := pool.QueryRow(context.Background(), `SELECT multiplier FROM upstream_rate_notice_baselines WHERE site_id='site'`).Scan(&baseline); err != nil || baseline != 1 {
				t.Fatal("late observation after committed disable must not advance notification baseline")
			}
			cached, err := cache.Get(t.Context(), "site")
			if err != nil || cached == nil || cached.IsEnabled() {
				t.Fatal("late callback must not revive cached lifecycle")
			}
		})
	}
}
