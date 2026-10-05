package connection_health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

// 确认404且完整列表缺失之后，自动刷新不得继续查该Key；手动/绑定变化/重启各重新确认。
func TestHomeCostStageCConfirmedDeletedKeyStopsAutomaticRequests(t *testing.T) {
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	var offset atomic.Int64
	clock := func() time.Time { return now.Add(time.Duration(offset.Load())) }
	var missingReads, listReads, goodReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/keys/1":
			missingReads.Add(1)
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "NOT_FOUND"})
		case "/api/v1/keys":
			listReads.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"items": []any{map[string]any{"id": 2, "name": "验收-Key2", "group_id": "group-1", "group": map[string]any{"id": "group-1", "name": "vip"}}}, "total": 1}})
		default:
			goodReads.Add(1)
			id := 2
			if r.URL.Path == "/api/v1/keys/3" {
				id = 3
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": id, "name": "验收-Key", "group_id": "group-1", "group": map[string]any{"id": "group-1", "name": "vip"}}})
		}
	}))
	defer server.Close()
	session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only"}
	reader := &homeCostHTTPMetadataReader{fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("missing", "site-1", "1"), snapshotConnection("good", "site-1", "2")}}, platform: upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), session: session}
	site := snapshotSite("site-1")
	site.Status = upstream.StatusConnected
	site.Session = &session
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
	deleted := lookup.byAccount["missing"]
	if deleted.status != MultiplierResolutionMissing || deleted.reason != "key_deleted" || safeMultiplierBlockReason(deleted) != "key_deleted" || !isPriorityMultiplierBlocker(deleted.status) {
		t.Errorf("deleted Key lost safe missing/Priority-block state: status=%s reason=%s", deleted.status, deleted.reason)
	}
	if lookup.byAccount["good"].status != MultiplierResolutionResolved {
		t.Fatal("unrelated valid Key was blocked")
	}
	if missingReads.Load() != 1 || listReads.Load() != 1 {
		t.Fatalf("confirmation reads=%d list=%d", missingReads.Load(), listReads.Load())
	}
	offset.Store(int64(61 * time.Second))
	refresh(false)
	if missingReads.Load() != 1 || listReads.Load() != 1 {
		t.Errorf("confirmed deleted Key still queried after TTL: missing=%d list=%d", missingReads.Load(), listReads.Load())
	}
	refresh(true)
	if missingReads.Load() != 2 || listReads.Load() != 2 {
		t.Errorf("manual did not reconfirm exactly once: missing=%d list=%d", missingReads.Load(), listReads.Load())
	}
	reader.fakeMySitesReader.connections = append(reader.fakeMySitesReader.connections, snapshotConnection("new", "site-1", "3"))
	refresh(false)
	if missingReads.Load() != 3 || listReads.Load() != 3 {
		t.Errorf("binding change did not reconfirm deletion: missing=%d list=%d", missingReads.Load(), listReads.Load())
	}
	svc = newService()
	refresh(false)
	if missingReads.Load() != 4 || listReads.Load() != 4 {
		t.Errorf("restart did not reconfirm deletion: missing=%d list=%d", missingReads.Load(), listReads.Load())
	}
	if goodReads.Load() < 1 {
		t.Fatal("ordinary Key refresh was silently stopped")
	}
}
func TestHomeCostStageCPartialSnapshotAbsenceRemainsKeyMissing(t *testing.T) {
	key := multiplierSnapshotKey("user1", "ws1", "site-1")
	svc := &Service{multiplierSnapshots: map[string]*multiplierSnapshotEntry{key: {status: multiplierResolutionPartial, keys: map[string]upstreamKeyMetadata{"2": {id: "2"}}, site: multiplierSiteMetadata{groups: snapshotSite("site-1").Metrics.Groups, rechargeRate: 1}}}}
	got := svc.resolveMultiplierSnapshotLocked(snapshotConnection("missing", "site-1", "1"), "user1", "ws1", false)
	if got.status != MultiplierResolutionMissing || got.reason != MultiplierReasonKeyMissing {
		t.Errorf("unconfirmed partial absence became deleted: %s/%s", got.status, got.reason)
	}
}
