package connection_health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

type homeCostHTTPMetadataReader struct {
	fakeMySitesReader
	platform *upstream.PlatformService
	session  upstream.Session
}

func (r *homeCostHTTPMetadataReader) GetUpstreamKeyForWorkspace(ctx context.Context, _, _, _, id string) (upstream.Sub2APIKeyItem, error) {
	return r.platform.GetSub2APIKeyContext(ctx, r.session, id)
}
func (r *homeCostHTTPMetadataReader) ListUpstreamKeysForWorkspace(ctx context.Context, _, _, _ string) ([]upstream.Sub2APIKeyItem, error) {
	return r.platform.ListSub2APIKeysContext(ctx, r.session)
}
func homeCostStopMultiplierService(t *testing.T, s *Service) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		waitForMultiplierRefreshDispatcherIdle(t)
	})
}

// 主代理固定 C-2 每轮请求计数、三种恢复与标记后的完成时间边界。
func TestHomeCostStageCManualRefusalsStopAndRecover(t *testing.T) {
	for _, source := range []struct {
		code       string
		status     int
		syncReason string
		want       string
	}{
		{"ANNOUNCEMENT_ACK_REQUIRED", 409, "", upstream.ErrorAnnouncementAckRequired},
		{"INSUFFICIENT_BALANCE", 403, "", upstream.ErrorUpstreamInsufficientBalance},
		{"API_KEY_QUOTA_EXHAUSTED", 429, "", upstream.ErrorUpstreamKeyQuotaExhausted},
		{"INSUFFICIENT_QUOTA", 429, "", upstream.ErrorUpstreamKeyQuotaExhausted},
		{"API_KEY_EXPIRED", 403, "", upstream.ErrorUpstreamKeyExpired},
		{"", 401, upstream.ErrorRefreshTokenRejected, upstream.ErrorRefreshTokenRejected},
		{"", 401, upstream.ErrorAccessTokenRejected, upstream.ErrorAccessTokenRejected},
	} {
		for _, recovery := range []string{"manual", "credentials", "later_sync", "restart"} {
			t.Run(source.want+"/"+recovery, func(t *testing.T) {
				now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
				var offset atomic.Int64
				clock := func() time.Time { return now.Add(time.Duration(offset.Load())) }
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.WriteHeader(source.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"code": source.code})
				}))
				defer server.Close()
				session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only"}
				reader := &homeCostHTTPMetadataReader{fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "1")}}, platform: upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), session: session}
				site := snapshotSite("site-1")
				site.Status = upstream.StatusConnected
				site.Session = &session
				value := 3.0
				oldAt := now.Add(-time.Minute)
				oldCompleted := oldAt.UnixMilli()
				site.LastSyncedAt = &oldCompleted
				site.Metrics.TodayConsume.Value = &value
				site.Metrics.TodayConsumeDate = businesstime.DateAt(now)
				site.Metrics.TodayConsumeAt = &oldAt
				site.Metrics.TodayConsumeStatus = "ok"
				if source.syncReason != "" {
					site.Status = upstream.StatusError
					reason := source.syncReason
					site.ErrorKey = &reason
				}
				cache := &failureFixRecoverableSiteRead{site: site}
				newService := func() *Service {
					svc := &Service{mySites: reader, sites: cache, multiplierNow: clock}
					homeCostStopMultiplierService(t, svc)
					return svc
				}
				svc := newService()
				refresh := func(force bool) upstreamMultiplierLookup {
					return svc.multiplierLookupForWorkspaceWithOptions(t.Context(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, force)
				}
				lookup := refresh(false)
				if requests.Load() != 1 {
					t.Errorf("first confirmation requests=%d want=1", requests.Load())
				}
				if got := lookup.byAccount["account-1"]; got.status != MultiplierResolutionUnavailable || got.reason != source.want {
					t.Errorf("manual reason/status=%s/%s want unavailable/%s", got.status, got.reason, source.want)
				}
				// Expire ordinary 5m backoff, keep entry within 10m retention. Old successful sync must not release it.
				offset.Store(int64(6 * time.Minute))
				refresh(false)
				refresh(false)
				if requests.Load() != 1 {
					t.Errorf("manual refusal automatically retried after ordinary backoff: %d", requests.Load())
				}
				switch recovery {
				case "manual":
					refresh(true)
				case "credentials":
					if notifier, ok := any(svc).(interface{ NotifySiteLoginSucceeded(string, string, string) }); ok {
						notifier.NotifySiteLoginSucceeded("user1", "ws1", "site-1")
					}
					refresh(false)
				case "later_sync":
					// A newer failed cost read does not release the block.
					changed := *site
					done := clock().UnixMilli()
					changed.LastSyncedAt = &done
					changed.Metrics.TodayConsumeStatus = "unreadable"
					cache.set(&changed, nil)
					refresh(false)
					if requests.Load() != 1 {
						t.Error("unreadable sync incorrectly released manual block")
					}
					offset.Add(int64(time.Second))
					changed.Metrics.TodayConsumeStatus = "ok"
					changed.ErrorKey = nil
					done = clock().UnixMilli()
					changed.LastSyncedAt = &done
					cache.set(&changed, nil)
					refresh(false)
				case "restart":
					svc = newService()
					refresh(false)
				}
				if requests.Load() != 2 {
					t.Errorf("%s did not cause one confirmation: requests=%d want=2", recovery, requests.Load())
				}
				refresh(false)
				if requests.Load() != 2 {
					t.Error("same completed sync repeatedly released a new manual block")
				}
			})
		}
	}
}
