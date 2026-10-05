package connection_health

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/businesstime"
)

func stageCManualRefusalFixture(t *testing.T) (*Service, *failureFixRecoverableSiteRead, *atomic.Int32, func()) {
	t.Helper()
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	var offset atomic.Int64
	requests := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"INSUFFICIENT_BALANCE"}`))
	}))
	t.Cleanup(server.Close)
	session := upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: server.URL, AccessToken: "fixture-only"}
	reader := &homeCostHTTPMetadataReader{
		fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "1")}},
		platform:          upstream.NewPlatformService(upstream.NewHTTPClient(server.Client())), session: session,
	}
	site := snapshotSite("site-1")
	site.Session = &session
	completed := now.Add(-time.Minute).UnixMilli()
	site.LastSyncedAt = &completed
	value := 3.0
	site.Metrics.TodayConsume.Value = &value
	site.Metrics.TodayConsumeDate = businesstime.DateAt(now)
	site.Metrics.TodayConsumeStatus = "ok"
	cache := &failureFixRecoverableSiteRead{site: site}
	svc := &Service{mySites: reader, sites: cache, multiplierNow: func() time.Time { return now.Add(time.Duration(offset.Load())) }}
	homeCostStopMultiplierService(t, svc)
	return svc, cache, requests, func() { offset.Add(int64(6 * time.Minute)) }
}

func TestHomeCostStageCManualMarkerSurvivesUnrelatedFingerprintChanges(t *testing.T) {
	svc, cache, requests, advance := stageCManualRefusalFixture(t)
	lookup := svc.multiplierLookupForWorkspaceWithOptions(t.Context(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, false)
	if got := lookup.byAccount["account-1"]; got.reason != upstream.ErrorUpstreamInsufficientBalance {
		t.Errorf("first refusal reason=%s", got.reason)
	}
	advance()
	site, err := cache.GetSite(t.Context(), "site-1")
	if err != nil {
		t.Fatal(err)
	}
	changed := *site
	changed.RechargeRate = 2
	cache.set(&changed, nil)
	svc.multiplierLookupForWorkspaceWithOptions(t.Context(), "user1", "ws1", string(upstream.PlatformSub2API), false, true, false)
	if requests.Load() != 1 {
		t.Fatalf("non-recovery fingerprint change released manual marker: requests=%d", requests.Load())
	}
}

func TestHomeCostStageCLoginCallbackOnlyReleasesMatchingWorkspace(t *testing.T) {
	svc, _, requests, advance := stageCManualRefusalFixture(t)
	reader := svc.mySites.(*homeCostHTTPMetadataReader)
	second := snapshotConnection("account-2", "site-1", "1")
	second.WorkspaceAdminAccountID = "ws2"
	reader.connections = append(reader.connections, second)
	refresh := func(user, workspace string) {
		svc.multiplierLookupForWorkspaceWithOptions(t.Context(), user, workspace, string(upstream.PlatformSub2API), false, true, false)
	}
	refresh("user1", "ws1")
	refresh("user1", "ws2")
	advance()
	notifier, ok := any(svc).(interface{ NotifySiteLoginSucceeded(string, string, string) })
	if !ok {
		t.Fatal("successful login cannot release the matching manual marker")
	}
	notifier.NotifySiteLoginSucceeded("other-user", "ws1", "site-1")
	notifier.NotifySiteLoginSucceeded("user1", "other-workspace", "site-1")
	notifier.NotifySiteLoginSucceeded("user1", "ws1", "other-site")
	refresh("user1", "ws1")
	refresh("user1", "ws2")
	if requests.Load() != 2 {
		t.Fatalf("unrelated successful login released marker: requests=%d", requests.Load())
	}
	notifier.NotifySiteLoginSucceeded("user1", "ws1", "site-1")
	refresh("user1", "ws2")
	refresh("user1", "ws1")
	if requests.Load() != 3 {
		t.Fatalf("matching login must release exactly its workspace: requests=%d", requests.Load())
	}
}

func TestHomeCostStageCManualPriorityReasonsAreFixedAndSafe(t *testing.T) {
	for _, reason := range []string{
		upstream.ErrorAnnouncementAckRequired, upstream.ErrorUpstreamInsufficientBalance,
		upstream.ErrorUpstreamKeyQuotaExhausted, upstream.ErrorUpstreamKeyExpired,
		upstream.ErrorRefreshTokenRejected, upstream.ErrorAccessTokenRejected,
	} {
		if got := safeMultiplierBlockReason(upstreamMultiplierResolution{status: MultiplierResolutionUnavailable, reason: reason}); got != reason {
			t.Errorf("fixed manual reason lost: got=%s want=%s", got, reason)
		}
	}
	if got := safeMultiplierBlockReason(upstreamMultiplierResolution{status: MultiplierResolutionUnavailable, reason: "admin.upstream.errors.unknown?token=secret"}); got != MultiplierReasonSiteUnavailable {
		t.Fatal("untrusted upstream reason entered Priority output")
	}
}
