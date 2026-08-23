package connection_health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

func newUnitRefreshRun(t *testing.T) *adminGroupsRefreshRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	return &adminGroupsRefreshRun{
		id:      "run-unit",
		runMode: adminGroupsRefreshModeManual,
		key:     adminGroupsRefreshWorkspaceKey{userID: "user1", adminAccountID: "ws1"},
		ctx:     ctx,
		cancel:  cancel,
		refresh: normalizeAdminGroupsRefreshSummary(AdminGroupsRefreshSummary{State: "success"}),
		snapshot: adminGroupsRefreshSnapshot{
			RunID:     "run-unit",
			Mode:      adminGroupsRefreshModeManual,
			RunState:  adminGroupsRefreshRunStateRunning,
			Stage:     adminGroupsRefreshStageDiscovering,
			Revision:  1,
			StartedAt: now,
			UpdatedAt: now,
			Waiting:   []adminGroupsRefreshWaiting{},
			Issues:    []adminGroupsRefreshIssue{},
		},
		subscribers: make(map[uint64]chan struct{}),
	}
}

func TestRefreshRunAuthoritativeTerminalSurvivesCoalescedSignal(t *testing.T) {
	run := newUnitRefreshRun(t)
	subscription, first := run.subscribe()
	if first.Revision != 1 {
		t.Fatalf("initial revision = %d, want 1", first.Revision)
	}

	run.publishStage(adminGroupsRefreshStageSiteSync, 0, 1, []adminGroupsRefreshWaiting{{SiteID: "site-1", Phase: "site_sync"}}, nil)
	run.publishStage(adminGroupsRefreshStageMultiplierRefresh, 0, 1, []adminGroupsRefreshWaiting{{SiteID: "site-1", Phase: "multiplier_refresh"}}, nil)
	groups := []AdminGroupHealth{}
	if !run.publishTerminal(adminGroupsRefreshTerminal{Status: "success", Groups: &groups, Refresh: AdminGroupsRefreshSummary{State: "success"}}) {
		t.Fatal("terminal publish was rejected")
	}

	select {
	case _, ok := <-subscription.Signals:
		if !ok {
			t.Fatal("coalesced update signal was lost before terminal close")
		}
	case <-time.After(time.Second):
		t.Fatal("coalesced update signal did not arrive")
	}
	select {
	case _, ok := <-subscription.Signals:
		if ok {
			t.Fatal("subscriber channel remained open after terminal")
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber channel was not closed after terminal")
	}

	latest := run.latest()
	if latest.Terminal == nil || latest.Terminal.Status != "success" {
		t.Fatalf("authoritative terminal = %#v, want success", latest.Terminal)
	}
	if latest.Terminal.Revision != latest.Revision {
		t.Fatalf("terminal revision = %d, snapshot revision = %d", latest.Terminal.Revision, latest.Revision)
	}
	replay, replaySnapshot := run.subscribe()
	if replaySnapshot.Revision != latest.Revision {
		t.Fatalf("replay revision = %d, want unchanged %d", replaySnapshot.Revision, latest.Revision)
	}
	if _, ok := <-replay.Signals; ok {
		t.Fatal("terminal replay signal channel must already be closed")
	}
}

func TestRefreshRunHeartbeatIncrementsRevisionWithoutCompleting(t *testing.T) {
	run := newUnitRefreshRun(t)
	run.publishStage(adminGroupsRefreshStageSiteSync, 0, 1, []adminGroupsRefreshWaiting{{SiteID: "site-1", Phase: "site_sync"}}, nil)
	before := run.latest()
	if !run.publishHeartbeat(before.UpdatedAt.Add(30 * time.Second)) {
		t.Fatal("running heartbeat was rejected")
	}
	after := run.latest()
	if after.RunState != adminGroupsRefreshRunStateRunning || after.Terminal != nil {
		t.Fatalf("heartbeat completed run: %#v", after)
	}
	if after.Revision != before.Revision+1 {
		t.Fatalf("heartbeat revision = %d, want %d", after.Revision, before.Revision+1)
	}
	if len(after.Waiting) != 1 || after.Waiting[0].ElapsedSeconds != 30 {
		t.Fatalf("heartbeat waiting = %#v, want elapsedSeconds=30", after.Waiting)
	}
	run.publishStage(adminGroupsRefreshStageMainGroups, 0, 0, nil, nil)
	if waiting := run.latest().Waiting; len(waiting) != 0 {
		t.Fatalf("main_groups waiting = %#v, want prior phase cleared", waiting)
	}
}

func TestRefreshRunProgressKeepsRemainingWaiterClockAndDeduplicatesIssues(t *testing.T) {
	run := newUnitRefreshRun(t)
	run.publishStage(adminGroupsRefreshStageSiteSync, 0, 2, []adminGroupsRefreshWaiting{
		{SiteID: "site-fast", SiteName: "快速站点", Phase: "site_sync"},
		{SiteID: "site-slow", SiteName: "慢速站点", Phase: "site_sync"},
	}, nil)
	started := run.latest().Waiting[1].startedAt
	run.publishHeartbeat(started.Add(7 * time.Second))
	issue := adminGroupsRefreshIssue{
		SiteID: "site-fast", SiteName: "快速站点", Phase: "site_sync", Status: "auth_failed", ErrorKey: "site_sync_auth",
	}
	run.publishStage(adminGroupsRefreshStageSiteSync, 1, 2, []adminGroupsRefreshWaiting{
		{SiteID: "site-slow", SiteName: "慢速站点", Phase: "site_sync"},
	}, []adminGroupsRefreshIssue{issue})
	run.publishStage(adminGroupsRefreshStageSiteSync, 1, 2, []adminGroupsRefreshWaiting{
		{SiteID: "site-slow", SiteName: "慢速站点", Phase: "site_sync"},
	}, []adminGroupsRefreshIssue{issue})

	snapshot := run.latest()
	if len(snapshot.Waiting) != 1 || snapshot.Waiting[0].SiteID != "site-slow" {
		t.Fatalf("remaining waiting = %#v, want only site-slow", snapshot.Waiting)
	}
	if !snapshot.Waiting[0].startedAt.Equal(started) || snapshot.Waiting[0].ElapsedSeconds != 7 {
		t.Fatalf("remaining waiter clock = %#v, want startedAt=%s elapsed=7", snapshot.Waiting[0], started)
	}
	if len(snapshot.Issues) != 1 || snapshot.Issues[0].SiteName != "快速站点" {
		t.Fatalf("issues = %#v, want one named issue", snapshot.Issues)
	}

	run.publishStage(adminGroupsRefreshStageMultiplierRefresh, 0, 2, []adminGroupsRefreshWaiting{
		{SiteID: "site-fast", SiteName: "快速站点", Phase: "multiplier_refresh"},
		{SiteID: "site-slow", SiteName: "慢速站点", Phase: "multiplier_refresh"},
	}, nil)
	multiplierStarted := run.latest().Waiting[1].startedAt
	run.publishHeartbeat(multiplierStarted.Add(5 * time.Second))
	multiplierIssue := adminGroupsRefreshIssue{
		SiteID: "site-fast", SiteName: "快速站点", Phase: "multiplier_refresh", Status: "timeout", ErrorKey: "multiplier_request_timeout",
	}
	run.publishStage(adminGroupsRefreshStageMultiplierRefresh, 1, 2, []adminGroupsRefreshWaiting{
		{SiteID: "site-slow", SiteName: "慢速站点", Phase: "multiplier_refresh"},
	}, []adminGroupsRefreshIssue{multiplierIssue})
	multiplierSnapshot := run.latest()
	if len(multiplierSnapshot.Waiting) != 1 || !multiplierSnapshot.Waiting[0].startedAt.Equal(multiplierStarted) || multiplierSnapshot.Waiting[0].ElapsedSeconds != 5 {
		t.Fatalf("remaining multiplier waiter clock = %#v, want site-slow startedAt=%s elapsed=5", multiplierSnapshot.Waiting, multiplierStarted)
	}
	run.publishStage(adminGroupsRefreshStageMultiplierRefresh, 2, 2, nil, []adminGroupsRefreshIssue{multiplierIssue})

	snapshot = run.latest()
	if len(snapshot.Waiting) != 0 {
		t.Fatalf("final multiplier waiting = %#v, want none", snapshot.Waiting)
	}
	if len(snapshot.Issues) != 2 || snapshot.Issues[1] != multiplierIssue {
		t.Fatalf("cross-stage issues = %#v, want one site issue and one multiplier issue", snapshot.Issues)
	}
}

func TestRefreshRunCallerCancellationDoesNotCancelBackgroundRun(t *testing.T) {
	syncer := newBlockingRefreshRunSync()
	service := refreshRunTestService(syncer, fakePlatformGroupReader{})
	t.Cleanup(syncer.releaseAll)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	run, disposition, err := service.startOrJoinAdminGroupsRefreshRun(requestCtx, "user1", adminGroupsRefreshModeAutomatic)
	if err != nil || disposition != adminGroupsRefreshRunStarted {
		t.Fatalf("start run disposition=%v err=%v", disposition, err)
	}
	subscription, _ := run.subscribe()
	cancelRequest()
	select {
	case <-syncer.started:
	case <-time.After(time.Second):
		t.Fatal("background run did not reach site sync")
	}
	syncer.releaseAll()

	select {
	case <-subscription.Signals:
	case <-time.After(2 * time.Second):
		t.Fatal("background run did not finish after request cancellation")
	}
	deadline := time.Now().Add(2 * time.Second)
	for run.latest().Terminal == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	terminal := run.latest().Terminal
	if terminal == nil || terminal.Status != "success" {
		t.Fatalf("terminal after request cancellation = %#v, want success", terminal)
	}
}

func TestRefreshRunMainGroupsFailureCreatesSafeTerminalWithoutGroups(t *testing.T) {
	service := refreshRunTestService(&recordingUpstreamSyncCoordinator{}, failingRefreshRunPlatformReader{err: errors.New("sensitive upstream body")})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	run, disposition, err := service.startOrJoinAdminGroupsRefreshRun(context.Background(), "user1", adminGroupsRefreshModeManual)
	if err != nil || disposition != adminGroupsRefreshRunStarted {
		t.Fatalf("start run disposition=%v err=%v", disposition, err)
	}

	deadline := time.Now().Add(time.Second)
	for run.latest().Terminal == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	terminal := run.latest().Terminal
	if terminal == nil {
		t.Fatal("main-groups failure produced no terminal")
	}
	if terminal.Status != "failed" || terminal.FailedStage != adminGroupsRefreshStageMainGroups {
		t.Fatalf("terminal = %#v, want failed/main_groups", terminal)
	}
	if terminal.ErrorKey == "" || terminal.ErrorKey == "sensitive upstream body" {
		t.Fatalf("terminal errorKey = %q, want safe non-empty key", terminal.ErrorKey)
	}
	if terminal.Groups != nil {
		t.Fatalf("failed terminal groups = %#v, want omitted", terminal.Groups)
	}
	if terminal.Refresh.Sites == nil {
		t.Fatal("failed terminal refresh.sites must be a legal array")
	}
}

type failingRefreshRunSessionReader struct {
	fakeMySitesReader
	err error
}

func (r failingRefreshRunSessionReader) RequireSession(context.Context, string, string) (upstream.Session, error) {
	return upstream.Session{}, r.err
}

func TestRefreshRunPreMainGroupsFailureUsesCurrentSafeStage(t *testing.T) {
	reader := failingRefreshRunSessionReader{
		fakeMySitesReader: fakeMySitesReader{connections: []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}},
		err:               errors.New("sensitive session failure"),
	}
	service := newAdminGroupsService(fakePlatformGroupReader{}, reader, newFakeRepository())
	service.sites = snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}
	service.SetUpstreamSyncCoordinator(&recordingUpstreamSyncCoordinator{results: []upstream.SyncSiteResult{{SiteID: "site-1", Status: "success"}}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})

	run, disposition, err := service.startOrJoinAdminGroupsRefreshRun(context.Background(), "user1", adminGroupsRefreshModeManual)
	if err != nil || disposition != adminGroupsRefreshRunStarted {
		t.Fatalf("start pre-main failure run disposition=%v err=%v", disposition, err)
	}
	deadline := time.Now().Add(time.Second)
	for run.latest().Terminal == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	terminal := run.latest().Terminal
	if terminal == nil {
		t.Fatal("pre-main failure produced no terminal")
	}
	if terminal.Status != "failed" || terminal.FailedStage != adminGroupsRefreshStageMultiplierRefresh {
		t.Fatalf("terminal = %#v, want failed/multiplier_refresh", terminal)
	}
	if terminal.ErrorKey != "multiplier_unavailable" {
		t.Fatalf("terminal errorKey = %q, want safe multiplier_unavailable", terminal.ErrorKey)
	}
}

func TestRefreshRunServiceShutdownPublishesFailedTerminalAndClosesSubscriber(t *testing.T) {
	syncer := newBlockingRefreshRunSync()
	service := refreshRunTestService(syncer, fakePlatformGroupReader{})
	t.Cleanup(syncer.releaseAll)
	run, _, err := service.startOrJoinAdminGroupsRefreshRun(context.Background(), "user1", adminGroupsRefreshModeAutomatic)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	subscription, _ := run.subscribe()
	select {
	case <-syncer.started:
	case <-time.After(time.Second):
		t.Fatal("run did not reach blocked site sync")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown service: %v", err)
	}
	for range subscription.Signals {
	}
	terminal := run.latest().Terminal
	if terminal == nil || terminal.Status != "failed" || terminal.ErrorKey != "service_shutdown" {
		t.Fatalf("shutdown terminal = %#v, want failed/service_shutdown", terminal)
	}
	if terminal.FailedStage != adminGroupsRefreshStageSiteSync {
		t.Fatalf("shutdown failedStage = %q, want site_sync", terminal.FailedStage)
	}
	if terminal.Groups != nil {
		t.Fatalf("shutdown terminal groups = %#v, want omitted", terminal.Groups)
	}
}

func TestRefreshRunRetainsTerminalForReplayUntilNextWorkspaceRunReplacesIt(t *testing.T) {
	syncer := newBlockingRefreshRunSync()
	service := refreshRunTestService(syncer, fakePlatformGroupReader{})
	t.Cleanup(syncer.releaseAll)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	service.initializeAdminGroupsRefreshRuntime()
	if service.refreshRetention != 10*time.Minute {
		t.Fatalf("terminal retention = %s, want 10m", service.refreshRetention)
	}

	oldRun := newUnitRefreshRun(t)
	service.refreshRunMu.Lock()
	service.refreshActive[oldRun.key] = oldRun
	service.refreshRunsByID[oldRun.id] = oldRun
	service.refreshRunMu.Unlock()
	groups := []AdminGroupHealth{}
	service.finishAdminGroupsRefreshRun(oldRun, adminGroupsRefreshTerminal{Status: "success", Groups: &groups, Refresh: AdminGroupsRefreshSummary{State: "success"}})
	retainedRevision := oldRun.latest().Revision
	replay, ok := service.adminGroupsRefreshRunByID(context.Background(), "user1", oldRun.id)
	if !ok {
		t.Fatal("terminal was not retained for replay")
	}
	if revision := replay.latest().Revision; revision != retainedRevision {
		t.Fatalf("retained replay revision=%d, want %d", revision, retainedRevision)
	}

	newRun, disposition, err := service.startOrJoinAdminGroupsRefreshRun(context.Background(), "user1", adminGroupsRefreshModeAutomatic)
	if err != nil || disposition != adminGroupsRefreshRunStarted {
		t.Fatalf("start replacement disposition=%v err=%v", disposition, err)
	}
	if newRun.id == oldRun.id {
		t.Fatal("replacement run reused retained run id")
	}
	if _, ok := service.adminGroupsRefreshRunByID(context.Background(), "user1", oldRun.id); ok {
		t.Fatal("next workspace run did not replace retained terminal")
	}
}

func TestRefreshHandlerAcceptMediaListNegotiatesSSE(t *testing.T) {
	syncer := newBlockingRefreshRunSync()
	service := refreshRunTestService(syncer, fakePlatformGroupReader{})
	server := refreshRunTestServer(t, service)
	t.Cleanup(syncer.releaseAll)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})

	request := newRefreshRunRequest(t, http.MethodGet, server.URL+"/api/connection-health/admin-groups/refresh")
	request.Header.Set("Accept", "application/json, text/event-stream; q=0.9")
	responses, errs := startRefreshRequest(server.Client(), request)
	select {
	case <-syncer.started:
	case <-time.After(time.Second):
		t.Fatal("media-list SSE request did not start refresh")
	}
	response := awaitRefreshResponse(t, responses, errs)
	defer response.Body.Close()
	_, snapshot := requireRefreshSnapshot(t, response)
	if snapshot.data["runId"] == "" {
		t.Fatal("media-list SSE snapshot omitted runId")
	}
}

func TestRefreshHandlerUnknownRunIDReturnsNotFoundWithoutStartingRun(t *testing.T) {
	syncer := newBlockingRefreshRunSync()
	service := refreshRunTestService(syncer, fakePlatformGroupReader{})
	server := refreshRunTestServer(t, service)
	t.Cleanup(syncer.releaseAll)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})

	response, err := server.Client().Do(newRefreshRunRequest(t, http.MethodGet, server.URL+"/api/connection-health/admin-groups/refresh?run_id=missing"))
	if err != nil {
		t.Fatalf("unknown run request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown run status = %d, want 404", response.StatusCode)
	}
	if calls := syncer.callCount(); calls != 0 {
		t.Fatalf("unknown run started %d sync calls, want zero", calls)
	}
}

func TestRefreshHandlerJSONMainGroupsFailurePreservesLegacyErrorContract(t *testing.T) {
	service := refreshRunTestService(&recordingUpstreamSyncCoordinator{}, failingRefreshRunPlatformReader{err: errors.New("sensitive upstream body")})
	server := refreshRunTestServer(t, service)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})

	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/connection-health/admin-groups/refresh", nil)
	if err != nil {
		t.Fatalf("new JSON refresh request: %v", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("JSON refresh request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("JSON failure status = %d, want 500", response.StatusCode)
	}
	var payload map[string]string
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode JSON failure: %v", err)
	}
	if payload["message"] != ErrorUnknown {
		t.Fatalf("JSON failure message = %q, want legacy %q", payload["message"], ErrorUnknown)
	}
}

func TestRefreshRunTerminalPublicationBoundaryStartsNextWorkspaceRun(t *testing.T) {
	syncer := newBlockingRefreshRunSync()
	service := refreshRunTestService(syncer, fakePlatformGroupReader{})
	t.Cleanup(syncer.releaseAll)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	service.initializeAdminGroupsRefreshRuntime()

	oldRun := newUnitRefreshRun(t)
	service.refreshRunMu.Lock()
	service.refreshActive[oldRun.key] = oldRun
	service.refreshRunsByID[oldRun.id] = oldRun
	service.refreshRunMu.Unlock()
	groups := []AdminGroupHealth{}
	oldRun.publishTerminal(adminGroupsRefreshTerminal{Status: "success", Groups: &groups, Refresh: AdminGroupsRefreshSummary{State: "success"}})

	newRun, disposition, err := service.startOrJoinAdminGroupsRefreshRun(context.Background(), "user1", adminGroupsRefreshModeManual)
	if err != nil || disposition != adminGroupsRefreshRunStarted {
		t.Fatalf("terminal-boundary start disposition=%v err=%v, want new run", disposition, err)
	}
	if newRun.id == oldRun.id {
		t.Fatal("terminal-boundary start reused completed run")
	}
	service.finishAdminGroupsRefreshRun(oldRun, *oldRun.latest().Terminal)
	service.refreshRunMu.Lock()
	active := service.refreshActive[oldRun.key]
	retained := service.refreshRetained[oldRun.key]
	service.refreshRunMu.Unlock()
	if active != newRun || retained == oldRun {
		t.Fatalf("old terminal overwrote new registry active=%p retained=%p", active, retained)
	}
}

func TestRefreshRunRegistryStartFinishShutdownInterleavingDoesNotDeadlock(t *testing.T) {
	syncer := newBlockingRefreshRunSync()
	service := refreshRunTestService(syncer, fakePlatformGroupReader{})
	run, _, err := service.startOrJoinAdminGroupsRefreshRun(context.Background(), "user1", adminGroupsRefreshModeAutomatic)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	select {
	case <-syncer.started:
	case <-time.After(time.Second):
		t.Fatal("run did not reach blocked site sync")
	}

	start := make(chan struct{})
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, _ = service.startOrJoinAdminGroupsRefreshRun(context.Background(), "user1", adminGroupsRefreshModeAutomatic)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		syncer.releaseAll()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	}()
	go func() {
		wg.Wait()
		close(done)
	}()
	close(start)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("start/join/finish/shutdown interleaving deadlocked")
	}
	if terminal := run.latest().Terminal; terminal == nil {
		t.Fatal("interleaved shutdown left run without terminal")
	}
	service.refreshRunMu.Lock()
	activeCount := len(service.refreshActive)
	timerCount := len(service.refreshRetentionTimers)
	service.refreshRunMu.Unlock()
	if activeCount != 0 || timerCount != 0 {
		t.Fatalf("shutdown registry active=%d timers=%d, want zero", activeCount, timerCount)
	}
}
