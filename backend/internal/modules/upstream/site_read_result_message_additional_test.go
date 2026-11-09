package upstream

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSiteReadResultMessagePreservesThePrereadError(t *testing.T) {
	site := &Site{Platform: PlatformSub2API, Status: StatusError}
	key := ErrorRefreshTokenRejected
	site.ErrorKey = &key
	original := SiteReadAuthError(site)
	var before, after *RequestError
	if !errors.As(original, &before) || !strings.Contains(before.UpstreamMessage, "本次没有请求上游") {
		t.Fatal("the pre-request refusal lost its actual no-request outcome")
	}
	if !errors.As(SiteReadResultError(original), &after) || strings.Contains(after.UpstreamMessage, "本次没有请求上游") || !strings.Contains(after.UpstreamMessage, "未采用") || after.Reason != before.Reason || after.Stage != before.Stage || after.StatusCode != 0 {
		t.Fatal("result refusal lost its classification or actual discarded-result outcome")
	}
	if !strings.Contains(before.UpstreamMessage, "本次没有请求上游") {
		t.Fatal("formatting a post-read error modified the shared pre-read error")
	}
}

func TestSiteReadResultMessageRefreshSaveRejectionKeepsActualRequest(t *testing.T) {
	var svc *Service
	var cache *siteReadExtraCache
	var repo *singaporeFailureRepository
	var site *Site
	var calls atomic.Int32
	svc, cache, repo, site = siteReadExtraService(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		svc.mu.Lock()
		current, _ := cache.Get(t.Context(), "site")
		key := ErrorRefreshTokenRejected
		current.Status, current.ErrorKey = StatusError, &key
		_ = cache.Set(t.Context(), current)
		repo.mu.Lock()
		repo.durable = cloneSite(current)
		repo.mu.Unlock()
		svc.mu.Unlock()
		return siteReadExtraSuccess(r)
	})
	result, err := svc.prepareSiteReadSession(t.Context(), *site, time.Minute, true)
	var detail *RequestError
	if calls.Load() != 1 || result.Persisted || !errors.As(err, &detail) || detail.Reason != ErrorRefreshTokenRejected || detail.StatusCode != 0 || strings.Contains(detail.UpstreamMessage, "本次没有请求上游") || !strings.Contains(detail.UpstreamMessage, "未采用") {
		t.Fatal("refresh-save refusal must retain the already-sent request and discarded-result outcome")
	}
}

func TestSiteReadResultMessageNoRotationReadersReportDiscardedResults(t *testing.T) {
	for _, entry := range []string{"groups", "dated"} {
		t.Run(entry, func(t *testing.T) {
			f := newSiteReadREDFixture(t, 200, "")
			expires := f.svc.keyUsageNow().Add(time.Hour).UnixMilli()
			f.site.Session.ExpiresAt = &expires
			_ = f.cache.Set(t.Context(), f.site)
			f.repo.durable = cloneSite(f.site)
			var calls atomic.Int32
			f.svc.platformService = singaporeCostPlatform(t, f.svc.keyUsageNow(), func(r *http.Request) (int, any) {
				if calls.Add(1) == 1 {
					f.svc.mu.Lock()
					siteReadTotalREDPause(t, f)
					f.svc.mu.Unlock()
				}
				if r.URL.Path == "/api/v1/groups/available" {
					return 200, map[string]any{"data": []any{map[string]any{"id": 1, "name": "fixture-group", "rate_multiplier": 1}}}
				}
				if r.URL.Path == "/api/v1/admin/groups/usage-summary" {
					return 200, map[string]any{"data": []any{map[string]any{"group_id": 1, "today_cost": 7}}}
				}
				if r.URL.Path == "/api/v1/keys" {
					return 200, map[string]any{"data": []any{map[string]any{"id": 1, "name": "fixture-key", "group": map[string]any{"id": 1, "name": "fixture-group"}}}}
				}
				if r.URL.Path == "/api/v1/usage/stats" {
					return 200, map[string]any{"data": map[string]any{"total_actual_cost": 7}}
				}
				t.Errorf("unexpected statistics path: %s", r.URL.Path)
				return 503, map[string]any{}
			})
			message := ""
			if entry == "groups" {
				_, err := f.svc.FetchGroupDailyStats(t.Context(), "user", "site")
				var detail *RequestError
				if !errors.As(err, &detail) || detail.Reason != ErrorRefreshTokenRejected {
					t.Fatal("no-rotation statistics should reject the old result after the saved pause")
				}
				message = detail.UpstreamMessage
			} else {
				result, err := f.svc.KeyUsageForDate(t.Context(), "user", "workspace", "2031-02-03")
				if err != nil || result.CompletedSites != 0 || len(result.Sites) != 1 {
					t.Fatal("no-rotation dated collection did not retain an incomplete site")
				}
				message = result.Sites[0].Error
			}
			if calls.Load() == 0 || strings.Contains(message, "本次没有请求上游") || !strings.Contains(message, "未采用") {
				t.Fatal("the post-read persistence guard reported a pre-request outcome")
			}
		})
	}
}
