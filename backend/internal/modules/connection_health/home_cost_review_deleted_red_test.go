package connection_health

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

func TestHomeCostReviewDeletedKeySurvivesOrdinarySiteChanges(t *testing.T) {
	for _, change := range []string{"session_expiry", "recharge_rate", "group_multiplier", "stable_status"} {
		t.Run(change, func(t *testing.T) {
			now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
			var offset atomic.Int64
			var direct, list atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/keys/1" {
					direct.Add(1)
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"code":"NOT_FOUND"}`))
					return
				}
				list.Add(1)
				_, _ = w.Write([]byte(`{"data":{"items":[],"total":0}}`))
			}))
			defer server.Close()
			expires := now.Add(time.Hour).UnixMilli()
			session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only", ExpiresAt: &expires}
			reader := &homeCostHTTPMetadataReader{fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "1")}}, platform: upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), session: session}
			site := snapshotSite("site-1")
			site.Session = &session
			site.Status = upstream.StatusConnected
			cache := &failureFixRecoverableSiteRead{site: site}
			svc := &Service{mySites: reader, sites: cache, multiplierNow: func() time.Time { return now.Add(time.Duration(offset.Load())) }}
			homeCostStopMultiplierService(t, svc)
			refresh := func(force bool) upstreamMultiplierResolution {
				return svc.multiplierLookupForWorkspaceWithOptions(t.Context(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, force).byAccount["account-1"]
			}
			if got := refresh(false); got.reason != "key_deleted" || direct.Load() != 1 || list.Load() != 1 {
				t.Fatalf("initial complete-list deletion confirmation failed: reason=%s direct=%d list=%d", got.reason, direct.Load(), list.Load())
			}
			changed := *site
			switch change {
			case "session_expiry":
				next := session
				later := expires + int64(time.Hour/time.Millisecond)
				next.ExpiresAt = &later
				changed.Session = &next
			case "recharge_rate":
				changed.RechargeRate += 0.5
			case "group_multiplier":
				multiplier := 2.0
				changed.Metrics.Groups = []upstream.GroupInfo{{ID: "vip", Name: "vip", Multiplier: &multiplier}}
			case "stable_status":
				changed.Status = upstream.StatusError
			}
			cache.set(&changed, nil)
			offset.Store(int64(61 * time.Second))
			got := refresh(false)
			if got.status != MultiplierResolutionMissing || got.reason != "key_deleted" || safeMultiplierBlockReason(got) != "key_deleted" || !isPriorityMultiplierBlocker(got.status) {
				t.Errorf("ordinary site change lost deletion/Priority block: status=%s reason=%s", got.status, got.reason)
			}
			if direct.Load() != 1 || list.Load() != 1 {
				t.Errorf("ordinary site change re-requested deleted Key: direct=%d list=%d want1/1", direct.Load(), list.Load())
			}
			refresh(true)
			if direct.Load() != 2 || list.Load() != 2 {
				t.Errorf("manual force did not retain single re-confirmation: direct=%d list=%d want2/2", direct.Load(), list.Load())
			}
		})
	}
}
