package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

type targetRefreshSyncFunc func(context.Context, string, string, []string, bool) []upstream.SyncSiteResult

func (f targetRefreshSyncFunc) SyncSites(ctx context.Context, userID, workspaceID string, ids []string, force bool) []upstream.SyncSiteResult {
	return f(ctx, userID, workspaceID, ids, force)
}

type targetRefreshSiteLookup struct {
	snapshotSiteLookup
	readErrorID string
}

func (f targetRefreshSiteLookup) GetSite(ctx context.Context, id string) (*upstream.Site, error) {
	if id == f.readErrorID {
		return nil, errors.New("local site read failed")
	}
	return f.snapshotSiteLookup.GetSite(ctx, id)
}

func targetRefreshFixtureSite(id string) *upstream.Site {
	site := snapshotSite(id)
	site.UserID, site.AdminAccountID, site.Name = "user1", "ws1", "name-"+id
	return site
}

func targetRefreshFixtureEntry(now time.Time) multiplierSnapshotEntry {
	return multiplierSnapshotEntry{
		status: "complete", generation: 1, expiresAt: now.Add(time.Minute), nextRetryAt: now.Add(time.Second),
		manualReason: MultiplierReasonSiteUnavailable, manualBlockedAt: now,
		confirmedDeletedKeys: map[string]struct{}{"key-removed": {}},
	}
}

func targetRefreshFixture(t *testing.T, connections []my_sites.RealConnection, sites SiteLookup, syncer targetRefreshSyncFunc) (*Service, *snapshotMetadataReader, *http.ServeMux) {
	t.Helper()
	reader := &snapshotMetadataReader{fakeMySitesReader: fakeMySitesReader{
		connections: connections, session: upstream.Session{Platform: upstream.PlatformSub2API},
	}}
	service := newAdminGroupsService(fakePlatformGroupReader{
		groups: []upstream.AdminGroupInfo{{ID: "own-group", Name: "own group"}},
		accountsByGrp: map[string][]upstream.AdminGroupAccountInfo{
			"own-group": {{ID: "account-1", Name: "account", Status: "active"}},
		},
	}, reader, newFakeRepository())
	service.sites = sites
	service.SetUpstreamSyncCoordinator(syncer)
	service.multiplierSnapshots = make(map[string]*multiplierSnapshotEntry)
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Shutdown(ctx); err != nil {
			t.Errorf("shutdown refresh test service: %v", err)
		}
	})
	return service, reader, mux
}

func targetRefreshRequest(ctx context.Context, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(authctx.WithUserID(ctx, "user1"), method, path, strings.NewReader(body))
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	return resp
}

const (
	targetRefreshPath = "/api/connection-health/targets/sub2api:ws1:account-1/multiplier-refresh"
	targetRefreshBody = `{"workspaceId":"ws1"}`
)

type targetRefreshResponseSite struct {
	upstream.SyncSiteResult
	SiteName *string `json:"siteName"`
}

func requireTargetRefreshResult(t *testing.T, resp *httptest.ResponseRecorder) (bool, []targetRefreshResponseSite) {
	t.Helper()
	if resp.Code != http.StatusOK {
		t.Fatalf("target refresh returned HTTP %d: %s", resp.Code, resp.Body)
	}
	var result struct {
		NoBinding *bool                       `json:"noBinding"`
		Sites     []targetRefreshResponseSite `json:"sites"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil || result.NoBinding == nil || result.Sites == nil {
		t.Fatalf("invalid target refresh response: %s (decode error: %v)", resp.Body, err)
	}
	return *result.NoBinding, result.Sites
}

func TestTargetMultiplierRefreshExpiresOnlyEnabledBoundSites(t *testing.T) {
	tests := []struct {
		name     string
		inFlight bool
		absent   bool
	}{
		{name: "ready snapshot"},
		{name: "in-flight snapshot", inFlight: true},
		{name: "snapshot absent", absent: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connections := []my_sites.RealConnection{
				snapshotConnection("account-1", "bound", "key-1"),
				snapshotConnection("account-1", "bound", "key-2"),
				snapshotConnection("account-1", "other-platform", "key-3"),
				snapshotConnection("account-1", "inactive", "key-4"),
				snapshotConnection("account-2", "other-account", "key-5"),
			}
			connections[0].AdminPlatform = ""
			connections[1].AdminPlatform, connections[1].Status = "SUB2API", "active"
			connections[2].AdminPlatform = string(upstream.PlatformNewAPI)
			connections[3].Status = "disabled"
			sites := map[string]*upstream.Site{}
			for _, conn := range connections {
				sites[conn.UpstreamSiteID] = targetRefreshFixtureSite(conn.UpstreamSiteID)
			}
			calls := 0
			service, reader, mux := targetRefreshFixture(t, connections, snapshotSiteLookup{sites: sites}, func(ctx context.Context, user, workspace string, ids []string, force bool) []upstream.SyncSiteResult {
				calls++
				if user != "user1" || workspace != "ws1" || !reflect.DeepEqual(ids, []string{"bound"}) || !force {
					t.Errorf("sync scope = %q %q %v force=%v", user, workspace, ids, force)
				}
				return []upstream.SyncSiteResult{{SiteID: "bound", Status: "success"}}
			})
			now := time.Now()
			before := make(map[string]multiplierSnapshotEntry)
			for _, site := range []string{"bound", "other-platform", "inactive", "other-account"} {
				entry := targetRefreshFixtureEntry(now)
				if site == "bound" {
					if test.absent {
						continue
					}
					entry.inFlight = test.inFlight
				}
				key := multiplierSnapshotKey("user1", "ws1", site)
				before[key], service.multiplierSnapshots[key] = entry, &entry
				entry.confirmedDeletedKeys = maps.Clone(entry.confirmedDeletedKeys)
			}
			otherWorkspace := targetRefreshFixtureEntry(now)
			otherKey := multiplierSnapshotKey("user1", "ws2", "bound")
			before[otherKey], service.multiplierSnapshots[otherKey] = otherWorkspace, &otherWorkspace
			otherWorkspace.confirmedDeletedKeys = maps.Clone(otherWorkspace.confirmedDeletedKeys)

			noBinding, results := requireTargetRefreshResult(t, targetRefreshRequest(t.Context(), mux, http.MethodPost, targetRefreshPath, targetRefreshBody))
			if noBinding || len(results) != 1 || results[0].SiteID != "bound" || results[0].Status != "success" || results[0].SiteName == nil || *results[0].SiteName != "name-bound" || calls != 1 {
				t.Fatalf("noBinding=%v results=%+v sync calls=%d", noBinding, results, calls)
			}
			if direct, list := reader.callCounts("bound"); direct != 0 || list != 0 {
				t.Errorf("icon requested multiplier metadata directly: direct=%d list=%d", direct, list)
			}
			boundKey := multiplierSnapshotKey("user1", "ws1", "bound")
			if !test.absent && !test.inFlight {
				want := before[boundKey]
				want.expiresAt, want.nextRetryAt, want.manualBlockedAt = time.Time{}, time.Time{}, time.Time{}
				want.manualReason, want.confirmedDeletedKeys = "", nil
				before[boundKey] = want
			}
			if len(service.multiplierSnapshots) != len(before) {
				t.Fatalf("snapshot count changed: got %d, want %d", len(service.multiplierSnapshots), len(before))
			}
			for key, want := range before {
				if got := service.multiplierSnapshots[key]; !reflect.DeepEqual(got, &want) {
					t.Errorf("snapshot %q: got %+v, want %+v", key, got, want)
				}
			}
		})
	}
}

func TestTargetMultiplierRefreshHandlesSiteReadAndSyncResults(t *testing.T) {
	sites := map[string]*upstream.Site{}
	var connections []my_sites.RealConnection
	for _, id := range []string{"ok", "failed", "timeout", "cancelled", "read-error", "other-user", "other-workspace", "disabled", "absent"} {
		sites[id] = targetRefreshFixtureSite(id)
		connections = append(connections, snapshotConnection("account-1", id, "key-1"))
	}
	sites["other-user"].UserID = "user2"
	sites["other-workspace"].AdminAccountID = "ws2"
	enabled := false
	sites["disabled"].Enabled = &enabled
	delete(sites, "absent")
	upstreamResults := []upstream.SyncSiteResult{
		{SiteID: "ok", Status: "success", GroupRatesWarning: &upstream.GroupRatesWarning{Message: "save warning", Detail: "reason", Next: "retry"}},
		{SiteID: "failed", Status: "unavailable", ErrorKey: "site_sync_failed", ErrorDetail: "upstream service unavailable"},
		{SiteID: "timeout", Status: "timeout", ErrorKey: "site_sync_timeout"},
		{SiteID: "cancelled", Status: "unavailable", ErrorKey: "site_sync_cancelled"},
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lookup := targetRefreshSiteLookup{snapshotSiteLookup: snapshotSiteLookup{sites: sites}, readErrorID: "read-error"}
	calls := 0
	var stopRefreshRoot context.CancelFunc
	service, _, mux := targetRefreshFixture(t, connections, lookup, func(syncCtx context.Context, user, workspace string, ids []string, force bool) []upstream.SyncSiteResult {
		calls++
		if !reflect.DeepEqual(ids, []string{"cancelled", "failed", "ok", "timeout"}) || !force {
			t.Errorf("got sync sites %v force=%v", ids, force)
		}
		cancel()
		if syncCtx.Err() != nil {
			t.Errorf("request cancellation propagated to site sync: %v", syncCtx.Err())
		}
		if deadline, ok := syncCtx.Deadline(); !ok || time.Until(deadline) > 2*time.Minute || time.Until(deadline) < 110*time.Second {
			t.Errorf("unexpected site sync deadline: %v (present: %v)", deadline, ok)
		}
		stopRefreshRoot()
		if !errors.Is(syncCtx.Err(), context.Canceled) {
			t.Errorf("refresh runtime cancellation did not reach site sync: %v", syncCtx.Err())
		}
		return upstreamResults
	})
	service.initializeAdminGroupsRefreshRuntime()
	stopRefreshRoot = service.refreshRootCancel
	for _, conn := range connections {
		entry := targetRefreshFixtureEntry(time.Now())
		service.multiplierSnapshots[multiplierSnapshotKey("user1", "ws1", conn.UpstreamSiteID)] = &entry
	}

	noBinding, results := requireTargetRefreshResult(t, targetRefreshRequest(ctx, mux, http.MethodPost, targetRefreshPath, targetRefreshBody))
	if noBinding || calls != 1 || len(results) != 7 {
		t.Fatalf("noBinding=%v calls=%d results=%+v", noBinding, calls, results)
	}
	bySite := make(map[string]targetRefreshResponseSite)
	for _, result := range results {
		bySite[result.SiteID] = result
	}
	for _, want := range upstreamResults {
		got := bySite[want.SiteID]
		if !reflect.DeepEqual(got.SyncSiteResult, want) || got.SiteName == nil || *got.SiteName != "name-"+want.SiteID {
			t.Errorf("sync result was lost: got %+v, want %+v", got, want)
		}
		entry := service.multiplierSnapshots[multiplierSnapshotKey("user1", "ws1", want.SiteID)]
		if !entry.expiresAt.IsZero() || !entry.nextRetryAt.IsZero() || entry.manualReason != "" || len(entry.confirmedDeletedKeys) != 0 {
			t.Errorf("sync did not expire site %s: %+v", want.SiteID, entry)
		}
	}
	for _, id := range []string{"read-error", "other-user", "other-workspace"} {
		got := bySite[id]
		if got.Status != "site_read_failed" || got.SiteName != nil || got.ErrorDetail != "" {
			t.Errorf("site lookup leaked information for %s: %+v", id, got)
		}
	}
	for _, id := range []string{"read-error", "other-user", "other-workspace", "disabled", "absent"} {
		entry := service.multiplierSnapshots[multiplierSnapshotKey("user1", "ws1", id)]
		if entry.expiresAt.IsZero() || entry.nextRetryAt.IsZero() || entry.manualReason == "" || len(entry.confirmedDeletedKeys) == 0 {
			t.Errorf("unprocessed site %s changed: %+v", id, entry)
		}
	}
}

func TestTargetMultiplierRefreshReportsNoBinding(t *testing.T) {
	site := targetRefreshFixtureSite("bound")
	enabled := false
	site.Enabled = &enabled
	lookup := targetRefreshSiteLookup{snapshotSiteLookup: snapshotSiteLookup{sites: map[string]*upstream.Site{"bound": site}}, readErrorID: "read-error"}
	for _, test := range []struct {
		name        string
		connections []my_sites.RealConnection
		readFailed  bool
	}{
		{name: "no connection"},
		{name: "disabled site", connections: []my_sites.RealConnection{snapshotConnection("account-1", "bound", "key-1")}},
		{name: "only failed site read", connections: []my_sites.RealConnection{snapshotConnection("account-1", "read-error", "key-1")}, readFailed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, mux := targetRefreshFixture(t, test.connections, lookup, func(context.Context, string, string, []string, bool) []upstream.SyncSiteResult {
				t.Error("account without eligible upstream sites started sync")
				return nil
			})
			noBinding, results := requireTargetRefreshResult(t, targetRefreshRequest(t.Context(), mux, http.MethodPost, targetRefreshPath, targetRefreshBody))
			if test.readFailed {
				if noBinding || len(results) != 1 || results[0].Status != "site_read_failed" {
					t.Errorf("site read error was reported as a lack of bindings: noBinding=%v results=%+v", noBinding, results)
				}
				return
			}
			if !noBinding || len(results) != 0 {
				t.Errorf("noBinding=%v results=%+v", noBinding, results)
			}
		})
	}
}

func TestTargetMultiplierRefreshRejectsWrongScopeWithoutEffects(t *testing.T) {
	for _, test := range []struct {
		name, path, body, message string
		status                    int
		platform                  upstream.Platform
	}{
		{"workspace", targetRefreshPath, `{"workspaceId":"ws2"}`, "workspace_changed", http.StatusConflict, upstream.PlatformSub2API},
		{"platform", targetRefreshPath, targetRefreshBody, "unsupported_platform", http.StatusBadRequest, upstream.PlatformNewAPI},
		{"target", strings.Replace(targetRefreshPath, ":ws1:", ":ws2:", 1), targetRefreshBody, "target_not_found", http.StatusNotFound, upstream.PlatformSub2API},
	} {
		t.Run(test.name, func(t *testing.T) {
			sites := snapshotSiteLookup{sites: map[string]*upstream.Site{"bound": targetRefreshFixtureSite("bound")}}
			service, reader, mux := targetRefreshFixture(t, []my_sites.RealConnection{snapshotConnection("account-1", "bound", "key-1")}, sites, func(context.Context, string, string, []string, bool) []upstream.SyncSiteResult {
				t.Error("invalid request started sync")
				return nil
			})
			reader.session.Platform = test.platform
			key := multiplierSnapshotKey("user1", "ws1", "bound")
			expected := targetRefreshFixtureEntry(time.Now())
			entry := expected
			entry.confirmedDeletedKeys = maps.Clone(expected.confirmedDeletedKeys)
			service.multiplierSnapshots[key] = &entry
			resp := targetRefreshRequest(t.Context(), mux, http.MethodPost, test.path, test.body)
			var result struct {
				Message string `json:"message"`
			}
			if resp.Code != test.status || json.Unmarshal(resp.Body.Bytes(), &result) != nil || result.Message != test.message {
				t.Errorf("HTTP %d: %s; want HTTP %d: %s", resp.Code, resp.Body, test.status, test.message)
			}
			if !reflect.DeepEqual(service.multiplierSnapshots[key], &expected) {
				t.Errorf("rejected request changed snapshot: %+v", service.multiplierSnapshots[key])
			}
		})
	}
}

func TestTargetMultiplierRefreshRechecksMovedKeyThroughAutomaticGET(t *testing.T) {
	const siteID = "bound"
	const targetID = "sub2api:ws1:account-1"
	const autoPath = "/api/connection-health/admin-groups/refresh"
	site := targetRefreshFixtureSite(siteID)
	site.Metrics.Groups = append(site.Metrics.Groups, upstream.GroupInfo{ID: "group-2", Name: "new group", Multiplier: float64Ptr(.25)})
	var forced atomic.Int32
	service, reader, mux := targetRefreshFixture(t,
		[]my_sites.RealConnection{snapshotConnection("account-1", siteID, "key-1")},
		snapshotSiteLookup{sites: map[string]*upstream.Site{siteID: site}},
		func(_ context.Context, _, _ string, _ []string, force bool) []upstream.SyncSiteResult {
			if force {
				forced.Add(1)
			}
			return []upstream.SyncSiteResult{{SiteID: siteID, Status: "success"}}
		},
	)
	reader.directItems = map[string]upstream.Sub2APIKeyItem{
		"bound|key-1": {ID: "key-1", GroupID: "group-2", GroupName: "new group"},
	}
	reader.started = make(chan string, 2)
	reader.release = make(chan struct{})
	unblock := registerSnapshotReaderCleanup(t, service, reader.release)
	key := multiplierSnapshotKey("user1", "ws1", siteID)
	service.multiplierSnapshots[key] = &multiplierSnapshotEntry{
		workspaceKey: key, siteID: siteID, userID: "user1", adminAccountID: "ws1", platform: upstream.PlatformSub2API,
		bindingSignature: "key-1", keyIDs: []string{"key-1"}, generation: 1, capability: multiplierDirectSupported,
		status: "complete", fetchedAt: time.Now(), expiresAt: time.Now().Add(time.Hour), siteFingerprint: multiplierSiteFingerprint(site),
		keys: map[string]upstreamKeyMetadata{"key-1": {id: "key-1", groupID: "group-1", groupName: "vip"}},
		site: multiplierSiteMetadata{groups: site.Metrics.Groups, rechargeRate: site.RechargeRate},
	}

	noBinding, results := requireTargetRefreshResult(t, targetRefreshRequest(t.Context(), mux, http.MethodPost, targetRefreshPath, targetRefreshBody))
	if noBinding || len(results) != 1 || results[0].Status != "success" {
		t.Fatalf("icon failed to sync site: noBinding=%v results=%+v", noBinding, results)
	}
	if direct, list := reader.callCounts(siteID); direct != 0 || list != 0 {
		t.Fatalf("icon must return with zero upstream Key reads, got direct=%d list=%d", direct, list)
	}

	checkMultiplier := func(resp *httptest.ResponseRecorder, want float64, status string) {
		t.Helper()
		var terminal AdminGroupsFreshResult
		if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &terminal) != nil {
			t.Fatalf("automatic GET HTTP %d: %s", resp.Code, resp.Body)
		}
		for _, group := range terminal.Groups {
			for _, account := range group.Accounts {
				if account.TargetID != targetID {
					continue
				}
				if account.EffectiveMultiplier == nil || *account.EffectiveMultiplier != want || account.MultiplierResolutionStatus != status {
					t.Fatalf("automatic GET accounts = %s; want %s multiplier=%g resolution=%s", resp.Body, targetID, want, status)
				}
				return
			}
		}
		t.Fatalf("automatic GET did not include account %s: %s", targetID, resp.Body)
	}
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() { responses <- targetRefreshRequest(t.Context(), mux, http.MethodGet, autoPath, "") }()
	select {
	case started := <-reader.started:
		if started != siteID {
			t.Fatalf("automatic GET queued wrong site: %s", started)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic GET did not queue the expired site's Key read")
	}
	select {
	case resp := <-responses:
		checkMultiplier(resp, .5, MultiplierResolutionStale)
	case <-time.After(time.Second):
		t.Fatal("automatic GET did not return before releasing the upstream Key read")
	}
	if direct, list := reader.callCounts(siteID); direct != 1 || list != 0 {
		t.Fatalf("expected one queued Key read, got direct=%d list=%d", direct, list)
	}
	service.multiplierSnapshotMu.Lock()
	done := service.multiplierSnapshots[key].done
	service.multiplierSnapshotMu.Unlock()
	unblock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background multiplier snapshot did not finish")
	}
	checkMultiplier(targetRefreshRequest(t.Context(), mux, http.MethodGet, autoPath, ""), .25, MultiplierResolutionResolved)
	if direct, list := reader.callCounts(siteID); direct != 1 || list != 0 || forced.Load() != 1 {
		t.Fatalf("unexpected reads after new snapshot: direct=%d list=%d forced site sync=%d", direct, list, forced.Load())
	}
}
