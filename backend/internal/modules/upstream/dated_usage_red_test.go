package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestSub2APICostForDateFallbackRules(t *testing.T) {
	failureFixCaptureLogs(t)
	for _, tc := range []struct {
		name                                        string
		accountStatus, fallbackStatus, wantFallback int
	}{
		{"rate_limit_no_fallback", 429, 404, 0}, {"not_found_remembered", 500, 404, 1}, {"method_not_allowed_remembered", 500, 405, 1},
		{"success_keeps_fallback", 500, 200, 2}, {"server_error_not_remembered", 500, 500, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fallback atomic.Int32
			p := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/admin/keys" {
					fallback.Add(1)
					w.WriteHeader(tc.fallbackStatus)
					fmt.Fprint(w, `{"data":[],"total":0}`)
					return
				}
				w.WriteHeader(tc.accountStatus)
				fmt.Fprint(w, `{"message":"fixture failure"}`)
			})
			session := Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "fixture"}
			for i := 0; i < 2; i++ {
				cost, meta, err := p.fetchCostForDateForSite("site-1", session, "2031-02-03")
				if tc.fallbackStatus == 200 {
					if err != nil || cost != 0 || meta.Source != "key_sum_best_effort" {
						t.Errorf("fallback result cost=%v meta=%+v err=%v", cost, meta, err)
					}
				} else if err == nil || err.Error() != ErrorRequest {
					t.Errorf("caller error must stay request: %v", err)
				}
			}
			if got := int(fallback.Load()); got != tc.wantFallback {
				t.Fatalf("administrator key requests=%d want=%d", got, tc.wantFallback)
			}
		})
	}
	t.Run("empty_identity_does_not_remember", func(t *testing.T) {
		var calls atomic.Int32
		p := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/admin/keys" {
				calls.Add(1)
			}
			w.WriteHeader(404)
		})
		session := Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "fixture"}
		for i := 0; i < 2; i++ {
			_, _, err := p.FetchCostForDate(session, "2031-02-03")
			if err == nil || err.Error() != ErrorRequest {
				t.Errorf("legacy error=%v", err)
			}
		}
		if calls.Load() != 2 {
			t.Fatalf("empty-ID requests=%d want=2", calls.Load())
		}
	})
}
func datedUsageREDError(t *testing.T, err error) *RequestError {
	t.Helper()
	var re *RequestError
	if !errors.As(err, &re) || re.MessageKey != ErrorRequest || re.Reason != ErrorRateLimited || re.StatusCode != 429 {
		t.Fatalf("dated rate-limit error must preserve real429 shape: %+v", re)
	}
	return re
}
func TestDatedUsageRateLimitPausesSite(t *testing.T) {
	failureFixCaptureLogs(t)
	for _, path := range []string{"account", "sub2api_keys", "sub2api_usage", "newapi_tokens", "newapi_usage"} {
		t.Run(path, func(t *testing.T) {
			start := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
			now := start
			var calls atomic.Int32
			limited := true
			p := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if limited {
					w.WriteHeader(429)
					fmt.Fprint(w, `{"message":"fixture rate limit"}`)
				} else {
					fmt.Fprint(w, `{"data":{"total_actual_cost":4,"quota":400000}}`)
				}
			})
			p.now = func() time.Time { return now }
			session := Session{Platform: PlatformSub2API, BaseURL: "https://same.invalid", AccessToken: "fixture"}
			reqPath := map[string]string{"sub2api_keys": "/api/v1/keys", "sub2api_usage": "/api/v1/usage/stats?api_key_id=1", "newapi_tokens": "/api/token/", "newapi_usage": "/api/log/self/stat"}[path]
			request := func(siteID string) error {
				if path == "account" {
					_, _, err := p.fetchCostForDateForSite(siteID, session, "2031-02-03")
					return err
				}
				_, err := p.requestKeyUsageJSONWithContext(withDatedUsageScope(context.Background(), siteID), session.BaseURL+reqPath, requestOptions{})
				return err
			}
			datedUsageREDError(t, request("site-1"))
			want := int32(2)
			if path == "account" {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("initial attempts=%d want=%d", calls.Load(), want)
			}
			before := calls.Load()
			session.AccessToken = "fixture-rotated"
			re := datedUsageREDError(t, request("site-1"))
			if calls.Load() != before || re.MutationOutcome != MutationNotSent {
				t.Errorf("pause must block HTTP after token rotation; calls=%d before=%d outcome=%s", calls.Load(), before, re.MutationOutcome)
			}
			datedUsageREDError(t, request("site-2"))
			if calls.Load() == before {
				t.Error("different site ID on same host must send")
			}
			before = calls.Load()
			datedUsageREDError(t, request(""))
			if calls.Load() == before {
				t.Error("empty identity must send")
			}
			before = calls.Load()
			limited = false
			// Unscoped today request remains allowed despite an active historical pause.
			_, err := p.requestKeyUsageJSONWithContext(context.Background(), session.BaseURL+"/api/v1/usage/stats", requestOptions{})
			if err != nil || calls.Load() != before+1 {
				t.Errorf("today blocked by historical pause: %v", err)
			}
			now = start.Add(60*time.Second - time.Nanosecond)
			before = calls.Load()
			datedUsageREDError(t, request("site-1"))
			if calls.Load() != before {
				t.Error("request sent before pause expiry")
			}
			now = start.Add(60 * time.Second)
			err = request("site-1")
			if err != nil || calls.Load() != before+1 {
				t.Errorf("expiry must restore HTTP: calls=%d before=%d err=%v", calls.Load(), before, err)
			}
		})
	}
	t.Run("retry_checks_new_pause", func(t *testing.T) {
		var p *PlatformService
		var keyCalls atomic.Int32
		p = failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("api_key_id") != "" {
				keyCalls.Add(1)
				_, _, _ = p.fetchCostForDateForSite("site-1", Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "fixture"}, "2031-02-03")
			}
			w.WriteHeader(429)
		})
		_, err := p.requestKeyUsageJSONWithContext(withDatedUsageScope(context.Background(), "site-1"), "https://fixture.invalid/api/v1/usage/stats?api_key_id=1", requestOptions{})
		datedUsageREDError(t, err)
		if keyCalls.Load() != 1 {
			t.Fatalf("retry sent after sibling paused site: %d", keyCalls.Load())
		}
	})
	for _, code := range []string{"API_KEY_QUOTA_EXHAUSTED", "INSUFFICIENT_QUOTA"} {
		t.Run("quota_"+code, func(t *testing.T) {
			var calls atomic.Int32
			p := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(429)
				fmt.Fprintf(w, `{"code":%q}`, code)
			})
			for i := 0; i < 2; i++ {
				_, err := p.requestKeyUsageJSONWithContext(withDatedUsageScope(context.Background(), "quota-site"), "https://fixture.invalid/api/v1/keys", requestOptions{})
				if err == nil {
					t.Fatal("quota failure must remain error")
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("quota exhaustion must neither retry nor pause: %d", calls.Load())
			}
		})
	}
	for _, tc := range []struct {
		header   string
		duration time.Duration
	}{{"", 60 * time.Second}, {"30", 60 * time.Second}, {"90", 90 * time.Second}, {"900", 5 * time.Minute}, {"Mon, 03 Feb 2031 04:02:00 GMT", 2 * time.Minute}} {
		t.Run("retry_after_"+tc.header, func(t *testing.T) {
			start := time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC)
			now := start
			var calls atomic.Int32
			p := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", tc.header)
				w.WriteHeader(429)
			})
			p.now = func() time.Time { return now }
			session := Session{Platform: PlatformSub2API, BaseURL: "https://fixture.invalid", AccessToken: "fixture"}
			_, _, err := p.fetchCostForDateForSite("retry-site", session, "2031-02-03")
			datedUsageREDError(t, err)
			before := calls.Load()
			now = start.Add(tc.duration - time.Nanosecond)
			_, _, err = p.fetchCostForDateForSite("retry-site", session, "2031-02-03")
			datedUsageREDError(t, err)
			if calls.Load() != before {
				t.Errorf("pause too short header=%q", tc.header)
			}
			now = start.Add(tc.duration)
			_, _, _ = p.fetchCostForDateForSite("retry-site", session, "2031-02-03")
			if calls.Load() != before+1 {
				t.Errorf("pause expiry must send exactly one account request, calls=%d before=%d", calls.Load(), before)
			}
		})
	}
}

func TestDatedUsageRateLimitPausesSiteThroughKeyUsageForDate(t *testing.T) {
	failureFixCaptureLogs(t)
	for _, platform := range []Platform{PlatformSub2API, PlatformNewAPI} {
		t.Run(string(platform), func(t *testing.T) {
			var usageCalls atomic.Int32
			p := failureFixPlatform(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/keys", "/api/token/":
					fmt.Fprint(w, `{"data":[{"id":1,"name":"fixture","group":"vip"}],"total":1}`)
				case "/api/v1/usage/stats", "/api/log/self/stat":
					usageCalls.Add(1)
					w.WriteHeader(429)
				default:
					t.Errorf("unexpected fixture path=%s", r.URL.Path)
					w.WriteHeader(500)
				}
			})
			p.now = func() time.Time { return time.Date(2031, 2, 3, 4, 0, 0, 0, time.UTC) }
			s, cache, _ := failureFixService(t, p)
			site := newTestSite("scope-site", "fixture-user", "fixture-workspace", 1, &Session{Platform: platform, BaseURL: "https://fixture.invalid", AccessToken: "fixture", UserID: "42"})
			site.Platform = platform
			cache.add(site)
			for i := 0; i < 2; i++ {
				result, err := s.KeyUsageForDate(context.Background(), site.UserID, site.AdminAccountID, "2031-02-02")
				if err != nil || result.CompletedSites != 0 || len(result.Sites) != 1 || result.Sites[0].Error != ErrorRequest {
					t.Fatalf("failed historical result must stay unreadable: %+v err=%v", result, err)
				}
			}
			if usageCalls.Load() != 2 {
				t.Fatalf("KeyUsageForDate must tag actual date requests; sent=%d want=2", usageCalls.Load())
			}
		})
	}
}
