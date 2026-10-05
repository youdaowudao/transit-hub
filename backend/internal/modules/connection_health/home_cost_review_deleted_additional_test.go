package connection_health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

func TestHomeCostReviewDeletedMarkerKeepsValidKeyRefreshAndBindingRecovery(t *testing.T) {
	for _, change := range []string{"recharge_rate", "group_multiplier", "base_url", "session_user"} {
		t.Run(change, func(t *testing.T) {
			now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
			var offset atomic.Int64
			var missing, list, valid atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				path := strings.TrimPrefix(r.URL.Path, "/alternate")
				switch path {
				case "/api/v1/keys/1":
					missing.Add(1)
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"code":"NOT_FOUND"}`))
				case "/api/v1/keys":
					list.Add(1)
					_, _ = w.Write([]byte(`{"data":{"items":[],"total":0}}`))
				case "/api/v1/keys/2", "/api/v1/keys/3":
					valid.Add(1)
					id := 2
					if strings.HasSuffix(path, "/3") {
						id = 3
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": id, "group_id": "group-1", "group": map[string]any{"name": "vip"}}})
				default:
					t.Errorf("unexpected fixture route %s", path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only", UserID: "source-user"}
			reader := &homeCostHTTPMetadataReader{
				fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("deleted", "site-1", "1"), snapshotConnection("valid", "site-1", "2")}},
				platform:          upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), session: session,
			}
			site := snapshotSite("site-1")
			site.Session = &session
			site.BaseURL = server.URL
			cache := &failureFixRecoverableSiteRead{site: site}
			newService := func() *Service {
				svc := &Service{mySites: reader, sites: cache, multiplierNow: func() time.Time { return now.Add(time.Duration(offset.Load())) }}
				homeCostStopMultiplierService(t, svc)
				return svc
			}
			svc := newService()
			refresh := func(force bool) upstreamMultiplierLookup {
				return svc.multiplierLookupForWorkspaceWithOptions(t.Context(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, force)
			}
			assertResult := func(lookup upstreamMultiplierLookup, wantEffective float64, wantMissing, wantList, wantValid int32) {
				t.Helper()
				deleted := lookup.byAccount["deleted"]
				if deleted.status != MultiplierResolutionMissing || deleted.reason != MultiplierReasonKeyDeleted || safeMultiplierBlockReason(deleted) != MultiplierReasonKeyDeleted || !isPriorityMultiplierBlocker(deleted.status) {
					t.Errorf("deleted Key no longer blocks Priority: %+v", deleted)
				}
				good := lookup.byAccount["valid"]
				if good.status != MultiplierResolutionResolved || good.info.effectiveMultiplier == nil || *good.info.effectiveMultiplier != wantEffective {
					t.Errorf("valid Key multiplier was not refreshed: %+v want=%g", good, wantEffective)
				}
				if missing.Load() != wantMissing || list.Load() != wantList || valid.Load() != wantValid {
					t.Errorf("reads deleted/list/valid=%d/%d/%d want=%d/%d/%d", missing.Load(), list.Load(), valid.Load(), wantMissing, wantList, wantValid)
				}
			}
			assertResult(refresh(false), 0.5, 1, 1, 1)
			changed := *site
			nextSession := session
			wantEffective := 0.5
			switch change {
			case "recharge_rate":
				changed.RechargeRate = 2
				wantEffective = 1
			case "group_multiplier":
				multiplier := 2.0
				changed.Metrics.Groups = []upstream.GroupInfo{{ID: "group-1", Name: "vip", Multiplier: &multiplier}}
				wantEffective = 2
			case "base_url":
				changed.BaseURL = server.URL + "/alternate"
				nextSession.BaseURL = changed.BaseURL
			case "session_user":
				nextSession.UserID = "changed-source-user"
			}
			changed.Session = &nextSession
			reader.session = nextSession
			cache.set(&changed, nil)
			offset.Store(int64(61 * time.Second))
			assertResult(refresh(false), wantEffective, 1, 1, 2)
			reader.connections = append(reader.connections, snapshotConnection("new", "site-1", "3"))
			assertResult(refresh(false), wantEffective, 2, 2, 4)
			assertResult(refresh(true), wantEffective, 3, 3, 6)
			svc = newService()
			assertResult(refresh(false), wantEffective, 4, 4, 8)
		})
	}
}

func TestHomeCostReviewDeletedMarkerDoesNotCrossQueryPlatformScope(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
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
	session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only"}
	reader := &homeCostHTTPMetadataReader{platform: upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), session: session}
	site := snapshotSite("site-1")
	site.Session = &session
	svc := &Service{mySites: reader, sites: &failureFixRecoverableSiteRead{site: site}, multiplierNow: func() time.Time { return now }}
	homeCostStopMultiplierService(t, svc)
	bindings := map[string]map[string]struct{}{"site-1": {"1": {}}}
	for _, platform := range []upstream.Platform{upstream.PlatformSub2API, upstream.PlatformNewAPI} {
		if !svc.prepareMultiplierSnapshots(t.Context(), reader, "user1", "ws1", platform, bindings, true, false) {
			t.Fatal("query-scope refresh did not finish")
		}
	}
	if direct.Load() != 2 || list.Load() != 2 {
		t.Errorf("query platform switch reused another scope's deletion: direct/list=%d/%d", direct.Load(), list.Load())
	}
}

type homeCostReviewPlatformMetadataReader struct {
	*homeCostHTTPMetadataReader
}

func (r *homeCostReviewPlatformMetadataReader) ListUpstreamKeysForWorkspace(ctx context.Context, _, _, _ string) ([]upstream.Sub2APIKeyItem, error) {
	if r.session.Platform == upstream.PlatformNewAPI {
		return r.platform.ListNewAPITokensContext(ctx, r.session)
	}
	return r.platform.ListSub2APIKeysContext(ctx, r.session)
}

func TestHomeCostReviewDeletedMarkerDoesNotCrossActualUpstreamPlatform(t *testing.T) {
	for _, change := range []string{"site_platform", "session_platform", "both"} {
		t.Run(change, func(t *testing.T) {
			now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
			var offset atomic.Int64
			var direct atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/keys/1" {
					direct.Add(1)
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"code":"NOT_FOUND"}`))
					return
				}
				_, _ = w.Write([]byte(`{"data":{"items":[],"total":0}}`))
			}))
			defer server.Close()
			session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only", UserID: "123"}
			reader := &homeCostReviewPlatformMetadataReader{homeCostHTTPMetadataReader: &homeCostHTTPMetadataReader{
				fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("deleted", "site-1", "1")}},
				platform:          upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), session: session,
			}}
			site := snapshotSite("site-1")
			site.Session = &session
			cache := &failureFixRecoverableSiteRead{site: site}
			svc := &Service{mySites: reader, sites: cache, multiplierNow: func() time.Time { return now.Add(time.Duration(offset.Load())) }}
			homeCostStopMultiplierService(t, svc)
			refresh := func(force bool) upstreamMultiplierResolution {
				return svc.multiplierLookupForWorkspaceWithOptions(t.Context(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, force).byAccount["deleted"]
			}
			if got := refresh(false); got.reason != MultiplierReasonKeyDeleted || direct.Load() != 1 {
				t.Fatalf("initial Sub2API deletion confirmation=%+v direct=%d", got, direct.Load())
			}
			changed := *site
			next := session
			if change != "session_platform" {
				changed.Platform = upstream.PlatformNewAPI
			}
			if change != "site_platform" {
				next.Platform = upstream.PlatformNewAPI
			}
			changed.Session = &next
			reader.session = next
			cache.set(&changed, nil)
			offset.Store(int64(61 * time.Second))
			for _, force := range []bool{false, true} {
				if got := refresh(force); got.reason == MultiplierReasonKeyDeleted || safeMultiplierBlockReason(got) == MultiplierReasonKeyDeleted {
					t.Errorf("Sub2API deletion evidence crossed actual upstream platform: %+v", got)
				}
			}
			if direct.Load() != 1 {
				t.Errorf("non-Sub2API platform triggered Sub2API direct reads: %d", direct.Load())
			}
		})
	}
}

type homeCostReviewPlatformChangeDuringRefresh struct {
	before, after *upstream.Site
	armed         atomic.Bool
	reads         atomic.Int32
}

func (r *homeCostReviewPlatformChangeDuringRefresh) GetSite(context.Context, string) (*upstream.Site, error) {
	// The first two reads check the binding and prepare its current generation.
	// The source switches before the queued worker starts its actual fetch.
	if r.armed.Load() && r.reads.Add(1) > 2 {
		return r.after, nil
	}
	return r.before, nil
}

func TestHomeCostReviewDeletedMarkerRechecksPlatformWhenWorkerStarts(t *testing.T) {
	for _, outcome := range []string{"available", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
			var offset atomic.Int64
			var direct, tokenList atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/keys/1":
					direct.Add(1)
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"code":"NOT_FOUND"}`))
				case "/api/v1/keys":
					_, _ = w.Write([]byte(`{"data":{"items":[],"total":0}}`))
				case "/api/token/":
					tokenList.Add(1)
					if outcome == "failed" {
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = w.Write([]byte(`{"code":"UNAVAILABLE"}`))
						return
					}
					_, _ = w.Write([]byte(`{"success":true,"data":{"items":[{"id":1,"name":"验收-有效NewAPIKey","group":"vip"}],"total":1}}`))
				default:
					t.Errorf("unexpected fixture route %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only", UserID: "123"}
			reader := &homeCostReviewPlatformMetadataReader{homeCostHTTPMetadataReader: &homeCostHTTPMetadataReader{
				fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "1")}},
				platform:          upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), session: session,
			}}
			site := snapshotSite("site-1")
			site.Session = &session
			next := session
			next.Platform = upstream.PlatformNewAPI
			after := *site
			after.Platform = upstream.PlatformNewAPI
			after.Session = &next
			cache := &homeCostReviewPlatformChangeDuringRefresh{before: site, after: &after}
			svc := &Service{mySites: reader, sites: cache, multiplierNow: func() time.Time { return now.Add(time.Duration(offset.Load())) }}
			homeCostStopMultiplierService(t, svc)
			refresh := func() upstreamMultiplierResolution {
				return svc.multiplierLookupForWorkspaceWithOptions(t.Context(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, false).byAccount["account-1"]
			}
			if got := refresh(); got.reason != MultiplierReasonKeyDeleted || direct.Load() != 1 {
				t.Fatalf("initial deletion confirmation=%+v direct=%d", got, direct.Load())
			}
			reader.session = next
			cache.armed.Store(true)
			offset.Store(int64(61 * time.Second))
			got := refresh()
			if outcome == "available" && (got.status != MultiplierResolutionResolved || got.reason != "") {
				t.Errorf("worker applied Sub2API deletion to available NewAPI Key: result=%+v", got)
			}
			if outcome == "failed" && (got.status != MultiplierResolutionUnavailable || got.reason == MultiplierReasonKeyDeleted) {
				t.Errorf("failed NewAPI read retained Sub2API deletion evidence: result=%+v", got)
			}
			if tokenList.Load() != 1 || direct.Load() != 1 {
				t.Errorf("platform switch request counts: direct=%d tokenList=%d", direct.Load(), tokenList.Load())
			}
		})
	}
}
