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

func TestHomeCostStageCDeletedKeyManualRefreshCanConfirmRestoredKey(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	var offset atomic.Int64
	var present atomic.Bool
	var direct, list atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/keys/1" {
			direct.Add(1)
			if present.Load() {
				_, _ = w.Write([]byte(`{"data":{"id":1,"group":{"name":"vip"}}}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NOT_FOUND"}`))
			return
		}
		list.Add(1)
		_, _ = w.Write([]byte(`{"data":{"items":[],"total":0}}`))
	}))
	defer server.Close()
	session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only"}
	reader := &homeCostHTTPMetadataReader{fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "1")}}, platform: upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), session: session}
	site := snapshotSite("site-1")
	site.Session = &session
	svc := &Service{mySites: reader, sites: &failureFixRecoverableSiteRead{site: site}, multiplierNow: func() time.Time { return now.Add(time.Duration(offset.Load())) }}
	homeCostStopMultiplierService(t, svc)
	refresh := func(force bool) upstreamMultiplierResolution {
		return svc.multiplierLookupForWorkspaceWithOptions(t.Context(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, force).byAccount["account-1"]
	}
	if got := refresh(false); got.status != MultiplierResolutionMissing || got.reason != "key_deleted" {
		t.Errorf("complete-list confirmation did not identify deleted Key: %+v", got)
	}
	present.Store(true)
	offset.Store(int64(61 * time.Second))
	if got := refresh(false); got.reason != "key_deleted" || direct.Load() != 1 || list.Load() != 1 {
		t.Errorf("automatic refresh rechecked confirmed deletion: result=%+v direct=%d list=%d", got, direct.Load(), list.Load())
	}
	if got := refresh(true); got.status != MultiplierResolutionResolved || got.reason != "" || direct.Load() != 2 || list.Load() != 1 {
		t.Fatalf("manual refresh did not confirm restored Key: result=%+v direct=%d list=%d", got, direct.Load(), list.Load())
	}
}

func TestHomeCostStageCFailedListDoesNotConfirmDeletedKey(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	var offset atomic.Int64
	var direct, list atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/keys/1" {
			direct.Add(1)
			w.WriteHeader(http.StatusNotFound)
		} else {
			list.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = w.Write([]byte(`{"code":"UNAVAILABLE"}`))
	}))
	defer server.Close()
	session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only"}
	reader := &homeCostHTTPMetadataReader{fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "1")}}, platform: upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), session: session}
	site := snapshotSite("site-1")
	site.Session = &session
	svc := &Service{mySites: reader, sites: &failureFixRecoverableSiteRead{site: site}, multiplierNow: func() time.Time { return now.Add(time.Duration(offset.Load())) }}
	homeCostStopMultiplierService(t, svc)
	for i := 0; i < 2; i++ {
		got := svc.multiplierLookupForWorkspaceWithOptions(t.Context(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, false).byAccount["account-1"]
		if got.status != MultiplierResolutionUnavailable || got.reason != MultiplierReasonKeyUnavailable {
			t.Fatalf("failed list incorrectly confirmed deletion: %+v", got)
		}
		offset.Add(int64(6 * time.Minute))
	}
	if direct.Load() != 2 || list.Load() != 2 {
		t.Fatalf("unconfirmed absence stopped ordinary retries: direct=%d list=%d", direct.Load(), list.Load())
	}
}
