package upstream

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestForceSaveRecoveryOnlyExplicitNeverVerifiedRecordsMatch(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Site)
	}{
		{"legacy_no_marker", func(s *Site) { s.ErrorKey = nil }},
		{"legacy_auth_error", func(s *Site) { key := ErrorAuth; s.ErrorKey = &key }},
		{"rejected_refresh", func(s *Site) { key := ErrorRefreshTokenRejected; s.ErrorKey = &key }},
		{"newapi", func(s *Site) { s.Platform = PlatformNewAPI }},
		{"connected", func(s *Site) { s.Status = StatusConnected }},
		{"previously_observed", func(s *Site) { value := int64(1); s.LastSyncedAt = &value }},
		{"password", func(s *Site) { s.Session.AuthMode = AuthModePassword }},
		{"confirmed_identity", func(s *Site) { s.Session.UserIDConfirmed = true }},
		{"identity_present", func(s *Site) { s.Session.UserID = "7" }},
		{"missing_session", func(s *Site) { s.Session = nil }},
	}
	if !siteSavedWithoutVerification(forceSaveUnverifiedSite()) {
		t.Fatal("new unverified record must match")
	}
	if siteSavedWithoutVerification(nil) {
		t.Fatal("nil record cannot match")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site := forceSaveUnverifiedSite()
			tc.change(site)
			if siteSavedWithoutVerification(site) {
				t.Fatal("legacy, bound, or incompatible record received the first-verification exception")
			}
		})
	}
}

func TestForceSaveRecoveryOldUnconfirmedSitesCannotUseFirstVerification(t *testing.T) {
	for _, key := range []string{"", ErrorAuth, ErrorRefreshTokenRejected} {
		t.Run(key, func(t *testing.T) {
			failureFixCaptureLogs(t)
			calls := 0
			platform := forceSaveValidPlatform(t, &calls)
			transport := platform.httpClient.client.Transport
			refreshes := 0
			platform.httpClient.client.Transport = keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/v1/auth/refresh" {
					refreshes++
				}
				return transport.RoundTrip(r)
			})
			svc, cache, repo := failureFixService(t, platform)
			old := forceSaveUnverifiedSite()
			if key == "" {
				old.ErrorKey = nil
			} else {
				old.ErrorKey = &key
			}
			cache.add(old)
			_, err := svc.Update(t.Context(), old.UserID, old.ID, UpdateRequest{Name: old.Name, SiteURL: old.BaseURL, Platform: PlatformSub2API, AuthMode: AuthModeToken, RefreshToken: "fixture-new-refresh", RechargeRate: 1})
			var detail *RequestError
			if !errors.As(err, &detail) || detail.MessageKey != ErrorIdentityCheckFailed || refreshes != 0 || len(repo.saved) != 0 {
				t.Fatalf("old unconfirmed record bypassed old identity protection: err=%v refreshes=%d writes=%d", err, refreshes, len(repo.saved))
			}
		})
	}
}

func TestForceSaveRecoveryRejectsDomainAndModeChangesBeforeAccess(t *testing.T) {
	for _, kind := range []string{"domain", "password", "user_key"} {
		t.Run(kind, func(t *testing.T) {
			failureFixCaptureLogs(t)
			calls := 0
			svc, cache, repo := failureFixService(t, forceSaveValidPlatform(t, &calls))
			old := forceSaveUnverifiedSite()
			cache.add(old)
			dto := UpdateRequest{Name: old.Name, SiteURL: old.BaseURL, Platform: PlatformSub2API, AuthMode: AuthModeToken, RefreshToken: "fixture-new-refresh", RechargeRate: 1}
			switch kind {
			case "domain":
				dto.SiteURL = "https://different.invalid"
				dto.ConfirmDomainMigration = true
			case "password":
				dto.AuthMode = AuthModePassword
				dto.Account = "fixture@example.com"
				dto.Password = "fixture-password"
			case "user_key":
				dto.AuthMode = AuthModeUserKey
				dto.Platform = PlatformNewAPI
				dto.AccessToken = "fixture-key"
			}
			_, err := svc.Update(t.Context(), old.UserID, old.ID, dto)
			var detail *RequestError
			if !errors.As(err, &detail) || detail.MessageKey != ErrorIdentityCheckFailed || !strings.Contains(detail.UpstreamMessage, "请重新添加") {
				t.Fatalf("missing specific unsupported edit instruction: %v", err)
			}
			after, _ := cache.Get(t.Context(), old.ID)
			if calls != 0 || len(repo.saved) != 0 || !reflect.DeepEqual(after, old) {
				t.Fatal("unsupported edit accessed upstream or mutated saved input")
			}
		})
	}
}

func TestForceSaveRecoveryFirstVerificationDoesNotOverwriteChangedOrDeletedRecord(t *testing.T) {
	for _, kind := range []string{"changed_login", "changed_marker", "deleted"} {
		t.Run(kind, func(t *testing.T) {
			failureFixCaptureLogs(t)
			calls := 0
			platform := forceSaveValidPlatform(t, &calls)
			transport := platform.httpClient.client.Transport
			reached, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
			principalReads := 0
			platform.httpClient.client.Transport = keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/v1/auth/me" {
					principalReads++
					if principalReads == 2 {
						close(reached)
						select {
						case <-release:
						case <-r.Context().Done():
							return nil, r.Context().Err()
						}
					}
				}
				return transport.RoundTrip(r)
			})
			svc, cache, repo := failureFixService(t, platform)
			old := forceSaveUnverifiedSite()
			cache.add(old)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			workerDone := make(chan struct{})
			defer func() {
				cancel()
				releaseWorker()
				select {
				case <-workerDone:
				case <-time.After(2 * time.Second):
					t.Error("first-verification fixture worker did not exit after cancellation and release")
				}
			}()
			go func() {
				defer close(workerDone)
				_, err := svc.Update(ctx, old.UserID, old.ID, UpdateRequest{Name: old.Name, SiteURL: old.BaseURL, Platform: PlatformSub2API, AuthMode: AuthModeToken, RefreshToken: "fixture-new-refresh", RechargeRate: 1})
				done <- err
			}()
			select {
			case <-reached:
			case err := <-done:
				t.Fatalf("first verification ended before the second principal read: %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("first verification did not reach its second principal read")
			}
			concurrent := cloneSite(old)
			svc.mu.Lock()
			switch kind {
			case "changed_login":
				concurrent.Session.RefreshToken = "fixture-other-refresh"
				cache.add(&concurrent)
			case "changed_marker":
				concurrent.Status = StatusConnected
				concurrent.ErrorKey = nil
				cache.add(&concurrent)
			case "deleted":
				delete(cache.sites, old.ID)
				svc.deletedSites[old.ID] = struct{}{}
			}
			svc.mu.Unlock()
			releaseWorker()
			var err error
			select {
			case err = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("first verification did not finish after the fixture was released")
			}
			if err == nil || len(repo.saved) != 0 {
				t.Fatalf("stale first verification wrote over a changed/deleted record: err=%v writes=%d", err, len(repo.saved))
			}
			after, _ := cache.Get(t.Context(), old.ID)
			if kind == "deleted" {
				if after != nil {
					t.Fatal("verification recreated deleted record")
				}
			} else if !reflect.DeepEqual(after, &concurrent) {
				t.Fatal("verification overwrote newer local state")
			}
		})
	}
}

func TestForceSaveRecoveryCancellationDoesNotPublishVerifiedLogin(t *testing.T) {
	failureFixCaptureLogs(t)
	calls := 0
	platform := forceSaveValidPlatform(t, &calls)
	transport := platform.httpClient.client.Transport
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	platform.httpClient.client.Transport = keyUsageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			cancel()
		}
		return transport.RoundTrip(r)
	})
	svc, cache, repo := failureFixService(t, platform)
	old := forceSaveUnverifiedSite()
	cache.add(old)
	_, err := svc.Update(ctx, old.UserID, old.ID, UpdateRequest{Name: old.Name, SiteURL: old.BaseURL, Platform: PlatformSub2API, AuthMode: AuthModeToken, RefreshToken: "fixture-new-refresh", RechargeRate: 1})
	after, _ := cache.Get(t.Context(), old.ID)
	if err == nil || len(repo.saved) != 0 || !reflect.DeepEqual(old, after) {
		t.Fatal("cancelled first verification published credentials")
	}
}
