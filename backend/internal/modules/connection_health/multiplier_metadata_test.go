package connection_health

import (
	"context"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

type snapshotMetadataReader struct {
	fakeMySitesReader
	mu          sync.Mutex
	directItems map[string]upstream.Sub2APIKeyItem
	directErrs  map[string]error
	listItems   map[string][]upstream.Sub2APIKeyItem
	directCalls map[string]int
	listCalls   map[string]int
	started     chan string
	release     chan struct{}
	active      int
	maxActive   int
}

func (f *snapshotMetadataReader) GetUpstreamKeyForWorkspace(ctx context.Context, userID string, adminAccountID string, siteID string, keyID string) (upstream.Sub2APIKeyItem, error) {
	f.mu.Lock()
	if f.directCalls == nil {
		f.directCalls = make(map[string]int)
	}
	f.directCalls[siteID]++
	err := f.directErrs[siteID]
	item := f.directItems[siteID+"|"+keyID]
	f.mu.Unlock()
	if f.started != nil {
		f.started <- siteID
	}
	if f.release != nil {
		f.mu.Lock()
		f.active++
		if f.active > f.maxActive {
			f.maxActive = f.active
		}
		f.mu.Unlock()
		defer func() {
			f.mu.Lock()
			f.active--
			f.mu.Unlock()
		}()
		select {
		case <-ctx.Done():
			return upstream.Sub2APIKeyItem{}, ctx.Err()
		case <-f.release:
		}
	}
	return item, err
}

func (f *snapshotMetadataReader) currentMaxActive() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

func (f *snapshotMetadataReader) activeAndDirectCalls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	totalCalls := 0
	for _, calls := range f.directCalls {
		totalCalls += calls
	}
	return f.active, totalCalls
}

func (f *snapshotMetadataReader) ListUpstreamKeysForWorkspace(ctx context.Context, userID string, adminAccountID string, siteID string) ([]upstream.Sub2APIKeyItem, error) {
	return f.ListUpstreamKeysForWorkspaceUntil(ctx, userID, adminAccountID, siteID, nil)
}

func (f *snapshotMetadataReader) ListUpstreamKeysForWorkspaceUntil(ctx context.Context, userID string, adminAccountID string, siteID string, keyIDs []string) ([]upstream.Sub2APIKeyItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listCalls == nil {
		f.listCalls = make(map[string]int)
	}
	f.listCalls[siteID]++
	return append([]upstream.Sub2APIKeyItem(nil), f.listItems[siteID]...), nil
}

func (f *snapshotMetadataReader) callCounts(siteID string) (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.directCalls[siteID], f.listCalls[siteID]
}

type snapshotSiteLookup struct {
	sites map[string]*upstream.Site
}

func (f snapshotSiteLookup) GetSite(ctx context.Context, siteID string) (*upstream.Site, error) {
	return f.sites[siteID], nil
}

type cloningSnapshotSiteLookup struct {
	sites map[string]*upstream.Site
}

func (f cloningSnapshotSiteLookup) GetSite(ctx context.Context, siteID string) (*upstream.Site, error) {
	site := f.sites[siteID]
	if site == nil {
		return nil, nil
	}
	copy := *site
	if site.LastSyncedAt != nil {
		lastSyncedAt := *site.LastSyncedAt
		copy.LastSyncedAt = &lastSyncedAt
	}
	if site.Session != nil {
		session := *site.Session
		if site.Session.ExpiresAt != nil {
			expiresAt := *site.Session.ExpiresAt
			session.ExpiresAt = &expiresAt
		}
		copy.Session = &session
	}
	copy.Metrics.Groups = make([]upstream.GroupInfo, 0, len(site.Metrics.Groups))
	for _, group := range site.Metrics.Groups {
		groupCopy := group
		if group.Multiplier != nil {
			multiplier := *group.Multiplier
			groupCopy.Multiplier = &multiplier
		}
		copy.Metrics.Groups = append(copy.Metrics.Groups, groupCopy)
	}
	return &copy, nil
}

func snapshotSite(siteID string) *upstream.Site {
	multiplier := 0.5
	return &upstream.Site{
		ID: siteID, Platform: upstream.PlatformSub2API, RechargeRate: 1,
		Session: &upstream.Session{Platform: upstream.PlatformSub2API, AccessToken: "test"},
		Metrics: upstream.Metrics{Groups: []upstream.GroupInfo{{ID: "group-1", Name: "vip", Multiplier: &multiplier}}},
	}
}

func snapshotConnection(accountID string, siteID string, keyID string) my_sites.RealConnection {
	return my_sites.RealConnection{
		UserID: "user1", WorkspaceAdminAccountID: "ws1", AdminAccountID: accountID,
		AdminPlatform: string(upstream.PlatformSub2API), UpstreamSiteID: siteID, UpstreamKeyID: keyID,
	}
}

func TestMultiplierSnapshotPageReturnsWhileBackgroundRefreshIsBlocked(t *testing.T) {
	release := make(chan struct{})
	reader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}},
		directItems:       map[string]upstream.Sub2APIKeyItem{"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"}},
		started:           make(chan string, 2), release: release,
	}
	service := &Service{mySites: reader, sites: snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}}

	startedAt := time.Now()
	lookup := service.cachedAdminGroupMultiplierLookup(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
	if time.Since(startedAt) > 100*time.Millisecond {
		t.Fatal("page multiplier lookup waited for the external refresh")
	}
	if lookup.byAccount["account-1"].status != MultiplierResolutionUnavailable {
		t.Fatalf("cold page lookup=%+v", lookup)
	}
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("background metadata refresh did not start")
	}
	service.cachedAdminGroupMultiplierLookup(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
	if direct, _ := reader.callCounts("site-1"); direct != 1 {
		t.Fatalf("same-site concurrent page refreshes=%d, want 1", direct)
	}
	close(release)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		lookup = service.cachedAdminGroupMultiplierLookup(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
		if lookup.byAccount["account-1"].status == MultiplierResolutionResolved {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("completed background snapshot was not reused: %+v", lookup)
}

func TestMultiplierSnapshotFailureIsIsolatedPerSite(t *testing.T) {
	reader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{
			snapshotConnection("account-1", "site-1", "key-1"),
			snapshotConnection("account-2", "site-2", "key-2"),
		}},
		directItems: map[string]upstream.Sub2APIKeyItem{"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"}},
		directErrs:  map[string]error{"site-2": &upstream.RequestError{MessageKey: upstream.ErrorUnknown, Platform: upstream.PlatformSub2API}},
	}
	service := &Service{mySites: reader, sites: snapshotSiteLookup{sites: map[string]*upstream.Site{
		"site-1": snapshotSite("site-1"), "site-2": snapshotSite("site-2"),
	}}}

	lookup := service.upstreamMultiplierResolutionsByAdminAccount(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
	if lookup.byAccount["account-1"].status != MultiplierResolutionResolved {
		t.Fatalf("healthy site was polluted by another site failure: %+v", lookup)
	}
	if lookup.byAccount["account-2"].status != MultiplierResolutionUnavailable {
		t.Fatalf("failed site resolution=%+v", lookup.byAccount["account-2"])
	}
}

func TestFreshMultiplierLookupRetainsOldSnapshotForDisplayAfterRefreshFailure(t *testing.T) {
	reader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}},
		directItems:       map[string]upstream.Sub2APIKeyItem{"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"}},
		directErrs:        map[string]error{},
	}
	service := &Service{mySites: reader, sites: snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}}
	first := service.freshMultiplierLookupForWorkspace(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
	if first.byAccount["account-1"].status != MultiplierResolutionResolved {
		t.Fatalf("initial fresh lookup=%+v", first)
	}
	reader.directErrs["site-1"] = &upstream.RequestError{MessageKey: upstream.ErrorAuth, Platform: upstream.PlatformSub2API, StatusCode: 401}
	service.multiplierSnapshotMu.Lock()
	entry := service.multiplierSnapshots[multiplierSnapshotKey("user1", "ws1", "site-1")]
	entry.expiresAt = time.Now().Add(-time.Second)
	entry.nextRetryAt = time.Time{}
	service.multiplierSnapshotMu.Unlock()

	second := service.freshMultiplierLookupForWorkspace(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
	if second.byAccount["account-1"].status != MultiplierResolutionStale {
		t.Fatalf("failed refresh must retain stale display snapshot, lookup=%+v", second)
	}
}

func TestFreshMultiplierLookupRefreshesHotSnapshot(t *testing.T) {
	reader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}},
		directItems:       map[string]upstream.Sub2APIKeyItem{"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"}},
	}
	service := &Service{mySites: reader, sites: snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}}

	first := service.freshMultiplierLookupForWorkspace(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
	if first.byAccount["account-1"].status != MultiplierResolutionResolved {
		t.Fatalf("initial fresh lookup=%+v", first)
	}
	second := service.freshMultiplierLookupForWorkspace(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
	if second.byAccount["account-1"].status != MultiplierResolutionResolved {
		t.Fatalf("second fresh lookup=%+v", second)
	}
	if direct, _ := reader.callCounts("site-1"); direct != 2 {
		t.Fatalf("hot snapshot direct reads=%d, want a new manual refresh", direct)
	}
}

func TestMultiplierSnapshotAmbiguous404DefersPaginationToNextRound(t *testing.T) {
	reader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}},
		directErrs: map[string]error{"site-1": &upstream.RequestError{
			MessageKey: upstream.ErrorNotFound, Platform: upstream.PlatformSub2API, StatusCode: 404,
		}},
		listItems: map[string][]upstream.Sub2APIKeyItem{"site-1": {{ID: "key-1", GroupID: "group-1", GroupName: "vip"}}},
	}
	service := &Service{mySites: reader, sites: snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}}

	first := service.upstreamMultiplierResolutionsByAdminAccount(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
	if first.byAccount["account-1"].status != MultiplierResolutionUnavailable {
		t.Fatalf("ambiguous first round=%+v", first)
	}
	if _, lists := reader.callCounts("site-1"); lists != 0 {
		t.Fatalf("ambiguous 404 triggered same-round pagination calls=%d", lists)
	}

	service.multiplierSnapshotMu.Lock()
	service.multiplierSnapshots[multiplierSnapshotKey("user1", "ws1", "site-1")].nextRetryAt = time.Now().Add(-time.Second)
	service.multiplierSnapshotMu.Unlock()
	second := service.upstreamMultiplierResolutionsByAdminAccount(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
	if second.byAccount["account-1"].status != MultiplierResolutionResolved {
		t.Fatalf("safe next-round pagination did not resolve metadata: %+v", second)
	}
	if direct, lists := reader.callCounts("site-1"); direct != 1 || lists != 1 {
		t.Fatalf("direct=%d list=%d, want one direct round and one fallback round", direct, lists)
	}
}

func TestMultiplierSnapshotReusesDeepCopiedSiteUntilStableVersionChanges(t *testing.T) {
	site := snapshotSite("site-1")
	lastSyncedAt := int64(100)
	site.LastSyncedAt = &lastSyncedAt
	reader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}},
		directItems:       map[string]upstream.Sub2APIKeyItem{"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"}},
	}
	lookup := cloningSnapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": site}}
	service := &Service{mySites: reader, sites: lookup}

	first := service.upstreamMultiplierResolutionsByAdminAccount(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
	if first.byAccount["account-1"].status != MultiplierResolutionResolved {
		t.Fatalf("first lookup=%+v", first)
	}
	if direct, _ := reader.callCounts("site-1"); direct != 1 {
		t.Fatalf("initial direct reads=%d, want 1", direct)
	}

	// Redis deserializes a fresh Site and Session pointer on every GetSite call. Stable
	// contents must keep the snapshot hot instead of starting another upstream read.
	second := service.upstreamMultiplierResolutionsByAdminAccount(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
	if second.byAccount["account-1"].status != MultiplierResolutionResolved {
		t.Fatalf("deep-copied lookup=%+v", second)
	}
	if direct, _ := reader.callCounts("site-1"); direct != 1 {
		t.Fatalf("deep-copied stable site invalidated the snapshot, direct reads=%d", direct)
	}

	// Upstream reconnect/credential refresh updates LastSyncedAt. That stable version
	// signal must invalidate the cached key metadata even though Redis still deep-copies.
	lastSyncedAt++
	third := service.upstreamMultiplierResolutionsByAdminAccount(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
	if third.byAccount["account-1"].status != MultiplierResolutionResolved {
		t.Fatalf("credential-refresh lookup=%+v", third)
	}
	if direct, _ := reader.callCounts("site-1"); direct != 2 {
		t.Fatalf("stable site version change did not invalidate snapshot, direct reads=%d", direct)
	}

	service.multiplierSnapshotMu.Lock()
	entry := service.multiplierSnapshots[multiplierSnapshotKey("user1", "ws1", "site-1")]
	service.multiplierSnapshotMu.Unlock()
	if entry == nil || len(entry.site.groups) != 1 || entry.site.groups[0].Name != "vip" {
		t.Fatalf("snapshot must retain only copied group metadata: %+v", entry)
	}
}

func TestMultiplierSnapshotBoundsProcessWideRefreshWorkers(t *testing.T) {
	release := make(chan struct{})
	connections := make([]my_sites.RealConnection, 0, upstreamMetadataFetchConcurrency+1)
	sites := make(map[string]*upstream.Site)
	directItems := make(map[string]upstream.Sub2APIKeyItem)
	for index := 0; index <= upstreamMetadataFetchConcurrency; index++ {
		siteID := "site-" + string(rune('a'+index))
		keyID := "key-" + string(rune('a'+index))
		connections = append(connections, snapshotConnection("account-"+string(rune('a'+index)), siteID, keyID))
		sites[siteID] = snapshotSite(siteID)
		directItems[siteID+"|"+keyID] = upstream.Sub2APIKeyItem{ID: keyID, GroupID: "group-1", GroupName: "vip"}
	}
	reader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{connections: connections},
		directItems:       directItems,
		started:           make(chan string, len(connections)),
		release:           release,
	}
	service := &Service{mySites: reader, sites: snapshotSiteLookup{sites: sites}}

	service.cachedAdminGroupMultiplierLookup(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
	for index := 0; index < upstreamMetadataFetchConcurrency; index++ {
		select {
		case <-reader.started:
		case <-time.After(time.Second):
			t.Fatal("bounded refresh workers did not start")
		}
	}
	if got := reader.currentMaxActive(); got != upstreamMetadataFetchConcurrency {
		t.Fatalf("active upstream refreshes=%d, want %d", got, upstreamMetadataFetchConcurrency)
	}
	select {
	case <-reader.started:
		t.Fatal("refresh queue started more than the configured global worker limit")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("queued site refresh did not run after a worker became available")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		active, calls := reader.activeAndDirectCalls()
		if active == 0 && calls == len(connections) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	active, calls := reader.activeAndDirectCalls()
	t.Fatalf("refresh workers did not settle: active=%d directCalls=%d want active=0 directCalls=%d", active, calls, len(connections))
}

func TestFreshMultiplierLookupWaitsForReplacementGeneration(t *testing.T) {
	release := make(chan struct{})
	reader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{
			connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")},
		},
		directItems: map[string]upstream.Sub2APIKeyItem{
			"site-1|key-1": {ID: "key-1", GroupID: "group-1", GroupName: "vip"},
			"site-1|key-2": {ID: "key-2", GroupID: "group-1", GroupName: "vip"},
		},
		started: make(chan string, 4),
		release: release,
	}
	service := &Service{mySites: reader, sites: snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}}

	firstDone := make(chan struct{})
	go func() {
		service.freshMultiplierLookupForWorkspace(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
		close(firstDone)
	}()
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("first generation did not start")
	}

	reader.fakeMySitesReader.connections = []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-2")}
	secondDone := make(chan struct{})
	go func() {
		service.freshMultiplierLookupForWorkspace(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
		close(secondDone)
	}()
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("replacement generation did not start")
	}

	select {
	case <-firstDone:
		t.Fatal("old waiter returned before replacement generation reached a terminal state")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	for _, done := range []<-chan struct{}{firstDone, secondDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("fresh lookup did not finish after replacement generation release")
		}
	}
}

func TestMultiplierSnapshotInvalidDirectMetadataFailsTheWholeSite(t *testing.T) {
	tests := []struct {
		name       string
		directItem upstream.Sub2APIKeyItem
		directErr  error
	}{
		{
			name:      "malformed response",
			directErr: &upstream.RequestError{MessageKey: upstream.ErrorInvalidResponse, Platform: upstream.PlatformSub2API},
		},
		{
			name:       "mismatched key id",
			directItem: upstream.Sub2APIKeyItem{ID: "other-key", GroupID: "group-1", GroupName: "vip"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &snapshotMetadataReader{
				fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}},
				directItems:       map[string]upstream.Sub2APIKeyItem{"site-1|key-1": test.directItem},
				directErrs:        map[string]error{"site-1": test.directErr},
			}
			service := &Service{mySites: reader, sites: snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}}

			lookup := service.upstreamMultiplierResolutionsByAdminAccount(context.Background(), "user1", "ws1", string(upstream.PlatformSub2API))
			if lookup.byAccount["account-1"].status != MultiplierResolutionUnavailable {
				t.Fatalf("invalid direct metadata must be unavailable, lookup=%+v", lookup)
			}
			service.multiplierSnapshotMu.Lock()
			entry := service.multiplierSnapshots[multiplierSnapshotKey("user1", "ws1", "site-1")]
			service.multiplierSnapshotMu.Unlock()
			if entry == nil || entry.status != "unavailable" || len(entry.keys) != 0 {
				t.Fatalf("invalid direct metadata produced a usable snapshot: %+v", entry)
			}
		})
	}
}
