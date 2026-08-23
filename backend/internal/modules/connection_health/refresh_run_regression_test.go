package connection_health

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/authctx"
)

type refreshRunSyncCall struct {
	userID         string
	adminAccountID string
	force          bool
}

type blockingRefreshRunSync struct {
	mu      sync.Mutex
	calls   []refreshRunSyncCall
	started chan refreshRunSyncCall
	release chan struct{}
	once    sync.Once
	results []upstream.SyncSiteResult
}

type progressiveRefreshRunSync struct {
	firstFinished chan struct{}
	releaseSecond chan struct{}
	firstOnce     sync.Once
	releaseOnce   sync.Once
}

func newProgressiveRefreshRunSync() *progressiveRefreshRunSync {
	return &progressiveRefreshRunSync{
		firstFinished: make(chan struct{}),
		releaseSecond: make(chan struct{}),
	}
}

func (s *progressiveRefreshRunSync) finishFirst() {
	s.firstOnce.Do(func() { close(s.firstFinished) })
}

func (s *progressiveRefreshRunSync) releaseAll() {
	s.releaseOnce.Do(func() { close(s.releaseSecond) })
}

func (s *progressiveRefreshRunSync) waitForSecond(ctx context.Context) bool {
	select {
	case <-s.releaseSecond:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *progressiveRefreshRunSync) SyncSites(ctx context.Context, _ string, _ string, _ []string, _ bool) []upstream.SyncSiteResult {
	s.finishFirst()
	if !s.waitForSecond(ctx) {
		return []upstream.SyncSiteResult{
			{SiteID: "site-fast", Status: "auth_failed", ErrorKey: "site_sync_auth"},
			{SiteID: "site-slow", Status: "unavailable", ErrorKey: "site_sync_cancelled"},
		}
	}
	return []upstream.SyncSiteResult{
		{SiteID: "site-fast", Status: "auth_failed", ErrorKey: "site_sync_auth"},
		{SiteID: "site-slow", Status: "success"},
	}
}

// SyncSitesProgress is the progressive capability required by the confirmed refresh-run contract.
// The legacy SyncSites method above keeps this fake compatible with the old implementation so the
// regression fails on the missing business snapshot instead of failing to compile.
func (s *progressiveRefreshRunSync) SyncSitesProgress(ctx context.Context, _ string, _ string, _ []string, _ bool, completed func(upstream.SyncSiteResult)) []upstream.SyncSiteResult {
	fast := upstream.SyncSiteResult{SiteID: "site-fast", Status: "auth_failed", ErrorKey: "site_sync_auth"}
	completed(fast)
	s.finishFirst()
	if !s.waitForSecond(ctx) {
		slow := upstream.SyncSiteResult{SiteID: "site-slow", Status: "unavailable", ErrorKey: "site_sync_cancelled"}
		completed(slow)
		return []upstream.SyncSiteResult{fast, slow}
	}
	slow := upstream.SyncSiteResult{SiteID: "site-slow", Status: "success"}
	completed(slow)
	return []upstream.SyncSiteResult{fast, slow}
}

type progressiveMultiplierMetadataReader struct {
	*snapshotMetadataReader
	firstFinished chan struct{}
	releaseSecond chan struct{}
	firstOnce     sync.Once
	releaseOnce   sync.Once
}

func (r *progressiveMultiplierMetadataReader) GetUpstreamKeyForWorkspace(ctx context.Context, userID string, adminAccountID string, siteID string, keyID string) (upstream.Sub2APIKeyItem, error) {
	if siteID == "site-slow" {
		select {
		case <-r.releaseSecond:
		case <-ctx.Done():
			return upstream.Sub2APIKeyItem{}, ctx.Err()
		}
	}
	item, err := r.snapshotMetadataReader.GetUpstreamKeyForWorkspace(ctx, userID, adminAccountID, siteID, keyID)
	if siteID == "site-fast" {
		r.firstOnce.Do(func() { close(r.firstFinished) })
	}
	return item, err
}

func (r *progressiveMultiplierMetadataReader) releaseAll() {
	r.releaseOnce.Do(func() { close(r.releaseSecond) })
}

func newBlockingRefreshRunSync() *blockingRefreshRunSync {
	return &blockingRefreshRunSync{
		started: make(chan refreshRunSyncCall, 8),
		release: make(chan struct{}),
		results: []upstream.SyncSiteResult{{SiteID: "site-1", Status: "success"}},
	}
}

func (s *blockingRefreshRunSync) SyncSites(ctx context.Context, userID string, adminAccountID string, _ []string, force bool) []upstream.SyncSiteResult {
	call := refreshRunSyncCall{userID: userID, adminAccountID: adminAccountID, force: force}
	s.mu.Lock()
	s.calls = append(s.calls, call)
	s.mu.Unlock()
	s.started <- call
	select {
	case <-s.release:
		return append([]upstream.SyncSiteResult(nil), s.results...)
	case <-ctx.Done():
		return []upstream.SyncSiteResult{{SiteID: "site-1", Status: "unavailable", ErrorKey: "site_sync_cancelled"}}
	}
}

func (s *blockingRefreshRunSync) releaseAll() {
	s.once.Do(func() { close(s.release) })
}

func (s *blockingRefreshRunSync) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

type failingRefreshRunPlatformReader struct{ err error }

func (r failingRefreshRunPlatformReader) FetchAdminAllGroups(upstream.Session) ([]upstream.AdminGroupInfo, error) {
	return nil, r.err
}

func (failingRefreshRunPlatformReader) ListAdminGroupAccounts(upstream.Session, upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	return nil, nil
}

func (failingRefreshRunPlatformReader) ResolveProbeCredential(upstream.Session, upstream.AdminGroupAccountInfo) (upstream.ProbeCredential, error) {
	return upstream.ProbeCredential{}, nil
}

type mutableRefreshRunAccountResolver struct {
	mu sync.Mutex
	id string
}

func (r *mutableRefreshRunAccountResolver) RequireCurrentID(context.Context, string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id, nil
}

func (r *mutableRefreshRunAccountResolver) set(id string) {
	r.mu.Lock()
	r.id = id
	r.mu.Unlock()
}

type workspaceRecordingMySitesReader struct {
	fakeMySitesReader
	mu       sync.Mutex
	sessions []string
}

func (r *workspaceRecordingMySitesReader) RequireSession(_ context.Context, _ string, adminAccountID string) (upstream.Session, error) {
	r.mu.Lock()
	r.sessions = append(r.sessions, adminAccountID)
	r.mu.Unlock()
	return upstream.Session{Platform: upstream.PlatformSub2API, BaseURL: adminAccountID}, nil
}

func (r *workspaceRecordingMySitesReader) sessionWorkspaceIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sessions...)
}

type workspaceEchoPlatformReader struct{}

func (workspaceEchoPlatformReader) FetchAdminAllGroups(session upstream.Session) ([]upstream.AdminGroupInfo, error) {
	return []upstream.AdminGroupInfo{{ID: session.BaseURL, Name: session.BaseURL, Platform: string(session.Platform), Status: "active"}}, nil
}

func (workspaceEchoPlatformReader) ListAdminGroupAccounts(upstream.Session, upstream.AdminGroupInfo) ([]upstream.AdminGroupAccountInfo, error) {
	return []upstream.AdminGroupAccountInfo{}, nil
}

func (workspaceEchoPlatformReader) ResolveProbeCredential(upstream.Session, upstream.AdminGroupAccountInfo) (upstream.ProbeCredential, error) {
	return upstream.ProbeCredential{}, nil
}

type refreshSSEEvent struct {
	name string
	data map[string]any
}

func readRefreshSSEEvent(reader *bufio.Reader) (refreshSSEEvent, error) {
	event := refreshSSEEvent{data: make(map[string]any)}
	var dataLines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return refreshSSEEvent{}, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if event.name == "" && len(dataLines) == 0 {
				continue
			}
			if len(dataLines) > 0 {
				if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &event.data); err != nil {
					return refreshSSEEvent{}, fmt.Errorf("decode SSE data: %w", err)
				}
			}
			return event, nil
		}
		if strings.HasPrefix(line, "event:") {
			event.name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}

func refreshRunTestService(syncer UpstreamSyncCoordinator, reader PlatformGroupReader) *Service {
	connections := []my_sites.RealConnection{snapshotConnection("account-1", "site-1", "key-1")}
	service := newAdminGroupsService(
		reader,
		fakeMySitesReader{connections: connections, session: upstream.Session{Platform: upstream.PlatformSub2API}},
		newFakeRepository(),
	)
	service.sites = snapshotSiteLookup{sites: map[string]*upstream.Site{"site-1": snapshotSite("site-1")}}
	service.SetUpstreamSyncCoordinator(syncer)
	return service
}

func refreshRunTestServer(t *testing.T, service *Service) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	RegisterRoutes(mux, service)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-Test-User")
		if userID == "" {
			userID = "user1"
		}
		r = r.WithContext(authctx.WithUserID(r.Context(), userID))
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
	return server
}

func newRefreshRunRequest(t *testing.T, method string, url string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("new refresh request: %v", err)
	}
	request.Header.Set("Accept", "text/event-stream")
	return request
}

func awaitRefreshResponse(t *testing.T, responses <-chan *http.Response, errs <-chan error) *http.Response {
	t.Helper()
	select {
	case err := <-errs:
		t.Fatalf("refresh request error: %v", err)
	case response := <-responses:
		return response
	case <-time.After(time.Second):
		t.Fatal("SSE response did not flush its first snapshot")
	}
	return nil
}

func startRefreshRequest(client *http.Client, request *http.Request) (<-chan *http.Response, <-chan error) {
	responses := make(chan *http.Response, 1)
	errs := make(chan error, 1)
	go func() {
		response, err := client.Do(request)
		if err != nil {
			errs <- err
			return
		}
		responses <- response
	}()
	return responses, errs
}

func requireRefreshSnapshot(t *testing.T, response *http.Response) (*bufio.Reader, refreshSSEEvent) {
	t.Helper()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("SSE status = %d, body=%s", response.StatusCode, body)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("SSE content type = %q, body=%s", contentType, body)
	}
	reader := bufio.NewReader(response.Body)
	event, err := readRefreshSSEEvent(reader)
	if err != nil {
		response.Body.Close()
		t.Fatalf("read first SSE event: %v", err)
	}
	if event.name != "snapshot" {
		response.Body.Close()
		t.Fatalf("first SSE event = %q, want snapshot", event.name)
	}
	if event.data["runState"] != "running" {
		response.Body.Close()
		t.Fatalf("first snapshot runState = %#v, want running", event.data["runState"])
	}
	if revision, ok := event.data["revision"].(float64); !ok || revision < 1 {
		response.Body.Close()
		t.Fatalf("first snapshot revision = %#v, want >= 1", event.data["revision"])
	}
	return reader, event
}

func waitForRefreshRunSnapshot(run *adminGroupsRefreshRun, timeout time.Duration, predicate func(adminGroupsRefreshSnapshot) bool) (adminGroupsRefreshSnapshot, bool) {
	deadline := time.Now().Add(timeout)
	for {
		snapshot := run.latest()
		if predicate(snapshot) {
			return snapshot, true
		}
		if time.Now().After(deadline) {
			return snapshot, false
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRefreshRunPublishesEachSiteSyncTerminalWhileAnotherSiteRemainsBlocked(t *testing.T) {
	connections := []my_sites.RealConnection{
		snapshotConnection("account-fast", "site-fast", "key-fast"),
		snapshotConnection("account-slow", "site-slow", "key-slow"),
	}
	reader := fakeMySitesReader{
		connections: connections,
		session:     upstream.Session{Platform: upstream.PlatformSub2API},
	}
	syncer := newProgressiveRefreshRunSync()
	service := newAdminGroupsService(fakePlatformGroupReader{}, reader, newFakeRepository())
	service.sites = snapshotSiteLookup{sites: map[string]*upstream.Site{
		"site-fast": snapshotSite("site-fast"),
		"site-slow": snapshotSite("site-slow"),
	}}
	service.SetUpstreamSyncCoordinator(syncer)
	t.Cleanup(syncer.releaseAll)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})

	run, disposition, err := service.startOrJoinAdminGroupsRefreshRun(context.Background(), "user1", adminGroupsRefreshModeManual)
	if err != nil || disposition != adminGroupsRefreshRunStarted {
		t.Fatalf("start progressive site-sync run disposition=%v err=%v", disposition, err)
	}
	select {
	case <-syncer.firstFinished:
	case <-time.After(time.Second):
		t.Fatal("fast site sync did not reach its terminal")
	}

	snapshot, ok := waitForRefreshRunSnapshot(run, 200*time.Millisecond, func(snapshot adminGroupsRefreshSnapshot) bool {
		return snapshot.Stage == adminGroupsRefreshStageSiteSync &&
			snapshot.StageCompletedSites == 1 && snapshot.StageTotalSites == 2 &&
			len(snapshot.Waiting) == 1 && snapshot.Waiting[0].SiteID == "site-slow" &&
			len(snapshot.Issues) == 1 && snapshot.Issues[0].SiteID == "site-fast" &&
			snapshot.Issues[0].ErrorKey == "site_sync_auth" && snapshot.Terminal == nil
	})
	if !ok {
		t.Fatalf("snapshot after fast site terminal = %#v, want completed=1, only site-slow waiting, fast failure issue, and no terminal", snapshot)
	}
}

func TestRefreshRunPublishesEachMultiplierTerminalWhileAnotherSiteRemainsBlocked(t *testing.T) {
	connections := []my_sites.RealConnection{
		snapshotConnection("account-fast", "site-fast", "key-fast"),
		snapshotConnection("account-slow", "site-slow", "key-slow"),
	}
	baseReader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{
			connections: connections,
			session:     upstream.Session{Platform: upstream.PlatformSub2API},
		},
		directItems: map[string]upstream.Sub2APIKeyItem{
			"site-fast|key-fast": {ID: "key-fast", GroupID: "group-1", GroupName: "vip"},
			"site-slow|key-slow": {ID: "key-slow", GroupID: "group-1", GroupName: "vip"},
		},
	}
	reader := &progressiveMultiplierMetadataReader{
		snapshotMetadataReader: baseReader,
		firstFinished:          make(chan struct{}),
		releaseSecond:          make(chan struct{}),
	}
	service := newAdminGroupsService(fakePlatformGroupReader{}, reader, newFakeRepository())
	service.sites = snapshotSiteLookup{sites: map[string]*upstream.Site{
		"site-fast": snapshotSite("site-fast"),
		"site-slow": snapshotSite("site-slow"),
	}}
	service.SetUpstreamSyncCoordinator(&recordingUpstreamSyncCoordinator{results: []upstream.SyncSiteResult{
		{SiteID: "site-fast", Status: "success"},
		{SiteID: "site-slow", Status: "success"},
	}})
	t.Cleanup(reader.releaseAll)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})

	run, disposition, err := service.startOrJoinAdminGroupsRefreshRun(context.Background(), "user1", adminGroupsRefreshModeManual)
	if err != nil || disposition != adminGroupsRefreshRunStarted {
		t.Fatalf("start progressive multiplier run disposition=%v err=%v", disposition, err)
	}
	select {
	case <-reader.firstFinished:
	case <-time.After(time.Second):
		t.Fatal("fast multiplier refresh did not reach its terminal")
	}

	snapshot, ok := waitForRefreshRunSnapshot(run, 200*time.Millisecond, func(snapshot adminGroupsRefreshSnapshot) bool {
		return snapshot.Stage == adminGroupsRefreshStageMultiplierRefresh &&
			snapshot.StageCompletedSites == 1 && snapshot.StageTotalSites == 2 &&
			len(snapshot.Waiting) == 1 && snapshot.Waiting[0].SiteID == "site-slow" &&
			len(snapshot.Issues) == 0 && snapshot.Terminal == nil
	})
	if !ok {
		t.Fatalf("snapshot after fast multiplier terminal = %#v, want completed=1, only site-slow waiting, no issue, and no terminal", snapshot)
	}
}

func TestRefreshRunKeepsOriginalWorkspaceWhenCurrentAccountChangesMidRun(t *testing.T) {
	resolver := &mutableRefreshRunAccountResolver{id: "workspace-a"}
	mySites := &workspaceRecordingMySitesReader{fakeMySitesReader: fakeMySitesReader{
		connections: []my_sites.RealConnection{snapshotConnection("account-a", "site-a", "")},
	}}
	syncer := newBlockingRefreshRunSync()
	service := newAdminGroupsService(workspaceEchoPlatformReader{}, mySites, newFakeRepository())
	service.SetAdminAccountResolver(resolver)
	service.SetUpstreamSyncCoordinator(syncer)
	t.Cleanup(syncer.releaseAll)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})

	run, disposition, err := service.startOrJoinAdminGroupsRefreshRun(context.Background(), "user1", adminGroupsRefreshModeManual)
	if err != nil || disposition != adminGroupsRefreshRunStarted {
		t.Fatalf("start workspace-a run disposition=%v err=%v", disposition, err)
	}
	select {
	case call := <-syncer.started:
		if call.adminAccountID != "workspace-a" {
			t.Fatalf("site sync workspace = %q, want workspace-a", call.adminAccountID)
		}
	case <-time.After(time.Second):
		t.Fatal("workspace-a site sync did not start")
	}

	resolver.set("workspace-b")
	syncer.releaseAll()
	snapshot, ok := waitForRefreshRunSnapshot(run, time.Second, func(snapshot adminGroupsRefreshSnapshot) bool {
		return snapshot.Terminal != nil
	})
	if !ok || snapshot.Terminal == nil {
		t.Fatalf("workspace-a run did not reach terminal: %#v", snapshot)
	}
	if snapshot.Terminal.Status != "success" || snapshot.Terminal.Groups == nil || len(*snapshot.Terminal.Groups) != 1 {
		t.Fatalf("workspace-a terminal = %#v, want one successful group", snapshot.Terminal)
	}
	if got := (*snapshot.Terminal.Groups)[0].ID; got != "workspace-a" {
		t.Fatalf("terminal group workspace = %q, want frozen workspace-a", got)
	}
	for _, got := range mySites.sessionWorkspaceIDs() {
		if got != "workspace-a" {
			t.Fatalf("session workspace = %q after account switch, want only workspace-a", got)
		}
	}
}

func TestRefreshRunMultiplierWaitingContainsOnlySitesWithActualJobs(t *testing.T) {
	connections := []my_sites.RealConnection{
		snapshotConnection("account-active", "site-slow", "key-slow"),
		snapshotConnection("account-disabled", "site-disabled", "key-disabled"),
		snapshotConnection("account-no-key", "site-no-key", ""),
	}
	baseReader := &snapshotMetadataReader{
		fakeMySitesReader: fakeMySitesReader{connections: connections, session: upstream.Session{Platform: upstream.PlatformSub2API}},
		directItems: map[string]upstream.Sub2APIKeyItem{
			"site-slow|key-slow": {ID: "key-slow", GroupID: "group-1", GroupName: "vip"},
		},
	}
	reader := &progressiveMultiplierMetadataReader{
		snapshotMetadataReader: baseReader,
		firstFinished:          make(chan struct{}),
		releaseSecond:          make(chan struct{}),
	}
	service := newAdminGroupsService(fakePlatformGroupReader{}, reader, newFakeRepository())
	service.sites = snapshotSiteLookup{sites: map[string]*upstream.Site{
		"site-slow":     snapshotSite("site-slow"),
		"site-disabled": disabledSnapshotSite("site-disabled"),
		"site-no-key":   snapshotSite("site-no-key"),
	}}
	service.SetUpstreamSyncCoordinator(&recordingUpstreamSyncCoordinator{results: []upstream.SyncSiteResult{
		{SiteID: "site-slow", Status: "success"},
		{SiteID: "site-disabled", Status: "disabled"},
		{SiteID: "site-no-key", Status: "success"},
	}})
	t.Cleanup(reader.releaseAll)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})

	run, disposition, err := service.startOrJoinAdminGroupsRefreshRun(context.Background(), "user1", adminGroupsRefreshModeManual)
	if err != nil || disposition != adminGroupsRefreshRunStarted {
		t.Fatalf("start actual-waiting run disposition=%v err=%v", disposition, err)
	}
	snapshot, ok := waitForRefreshRunSnapshot(run, time.Second, func(snapshot adminGroupsRefreshSnapshot) bool {
		return snapshot.Stage == adminGroupsRefreshStageMultiplierRefresh && snapshot.Terminal == nil
	})
	if !ok {
		t.Fatalf("multiplier stage snapshot not published: %#v", snapshot)
	}
	if snapshot.StageTotalSites != 1 || snapshot.StageCompletedSites != 0 {
		t.Fatalf("multiplier counts = %d/%d, want 0/1 actual job", snapshot.StageCompletedSites, snapshot.StageTotalSites)
	}
	if len(snapshot.Waiting) != 1 || snapshot.Waiting[0].SiteID != "site-slow" {
		t.Fatalf("multiplier waiting = %#v, want only site-slow actual job", snapshot.Waiting)
	}
}

func TestRefreshHandlerSSEFlushesSnapshotBeforeBlockedSiteSyncCompletes(t *testing.T) {
	syncer := newBlockingRefreshRunSync()
	t.Cleanup(syncer.releaseAll)
	service := refreshRunTestService(syncer, fakePlatformGroupReader{})
	server := refreshRunTestServer(t, service)

	responses, errs := startRefreshRequest(server.Client(), newRefreshRunRequest(t, http.MethodGet, server.URL+"/api/connection-health/admin-groups/refresh"))
	response := awaitRefreshResponse(t, responses, errs)
	defer response.Body.Close()
	_, snapshot := requireRefreshSnapshot(t, response)
	if snapshot.data["stage"] != "site_sync" && snapshot.data["stage"] != "discovering" {
		t.Fatalf("first snapshot stage = %#v, want discovering or site_sync", snapshot.data["stage"])
	}
}

func TestRefreshHandlerSSEMainGroupsFailureEmitsLegalTerminal(t *testing.T) {
	service := refreshRunTestService(&recordingUpstreamSyncCoordinator{}, failingRefreshRunPlatformReader{err: errors.New("sensitive upstream body")})
	server := refreshRunTestServer(t, service)

	response, err := server.Client().Do(newRefreshRunRequest(t, http.MethodPost, server.URL+"/api/connection-health/admin-groups/refresh"))
	if err != nil {
		t.Fatalf("refresh request: %v", err)
	}
	defer response.Body.Close()
	reader, _ := requireRefreshSnapshot(t, response)
	for {
		event, readErr := readRefreshSSEEvent(reader)
		if readErr != nil {
			t.Fatalf("read terminal SSE event: %v", readErr)
		}
		if event.name != "terminal" {
			continue
		}
		if event.data["status"] != "failed" || event.data["failedStage"] != "main_groups" {
			t.Fatalf("terminal = %#v, want failed main_groups", event.data)
		}
		if event.data["errorKey"] == "" || event.data["errorKey"] == "sensitive upstream body" {
			t.Fatalf("terminal errorKey = %#v, want safe non-empty key", event.data["errorKey"])
		}
		if _, hasGroups := event.data["groups"]; hasGroups {
			t.Fatalf("failed terminal must not replace the old group list: %#v", event.data)
		}
		if _, ok := event.data["refresh"].(map[string]any); !ok {
			t.Fatalf("failed terminal refresh summary = %#v, want object", event.data["refresh"])
		}
		return
	}
}

func TestRefreshHandlerManualAgainstAutomaticReturnsConflictWithoutSecondRun(t *testing.T) {
	syncer := newBlockingRefreshRunSync()
	t.Cleanup(syncer.releaseAll)
	service := refreshRunTestService(syncer, fakePlatformGroupReader{})
	server := refreshRunTestServer(t, service)

	firstResponses, firstErrs := startRefreshRequest(server.Client(), newRefreshRunRequest(t, http.MethodGet, server.URL+"/api/connection-health/admin-groups/refresh"))
	select {
	case call := <-syncer.started:
		if call.force {
			t.Fatalf("automatic run force = true")
		}
	case <-time.After(time.Second):
		t.Fatal("automatic run did not reach site sync")
	}

	manualResponse := make(chan *http.Response, 1)
	manualErr := make(chan error, 1)
	go func() {
		response, err := server.Client().Do(newRefreshRunRequest(t, http.MethodPost, server.URL+"/api/connection-health/admin-groups/refresh"))
		if err != nil {
			manualErr <- err
			return
		}
		manualResponse <- response
	}()

	var response *http.Response
	select {
	case err := <-manualErr:
		t.Fatalf("manual conflict request: %v", err)
	case response = <-manualResponse:
	case <-time.After(100 * time.Millisecond):
		syncer.releaseAll()
		select {
		case err := <-manualErr:
			t.Fatalf("manual conflict request after release: %v", err)
		case response = <-manualResponse:
		case <-time.After(time.Second):
			t.Fatal("manual conflict request did not finish")
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("manual conflict status = %d, body=%s, want 409", response.StatusCode, body)
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode conflict response: %v", err)
	}
	if payload["runId"] == "" {
		t.Fatalf("conflict response = %#v, want current runId", payload)
	}
	if payload["errorKey"] != "refresh_run_conflict" && payload["message"] != "refresh_run_conflict" {
		t.Fatalf("conflict response = %#v, want refresh_run_conflict", payload)
	}
	if got := syncer.callCount(); got != 1 {
		t.Fatalf("site sync calls = %d, want one automatic run without queued manual run", got)
	}
	syncer.releaseAll()
	first := awaitRefreshResponse(t, firstResponses, firstErrs)
	first.Body.Close()
}

func TestRefreshHandlerJSONManualAgainstAutomaticReturnsConflictWithoutSecondRun(t *testing.T) {
	syncer := newBlockingRefreshRunSync()
	t.Cleanup(syncer.releaseAll)
	service := refreshRunTestService(syncer, fakePlatformGroupReader{})
	server := refreshRunTestServer(t, service)

	firstResponses, firstErrs := startRefreshRequest(server.Client(), newRefreshRunRequest(t, http.MethodGet, server.URL+"/api/connection-health/admin-groups/refresh"))
	select {
	case call := <-syncer.started:
		if call.force {
			t.Fatal("automatic run force = true")
		}
	case <-time.After(time.Second):
		t.Fatal("automatic run did not reach site sync")
	}

	manualRequest := newRefreshRunRequest(t, http.MethodPost, server.URL+"/api/connection-health/admin-groups/refresh")
	manualRequest.Header.Set("Accept", "application/json")
	manualResponses, manualErrs := startRefreshRequest(server.Client(), manualRequest)
	var response *http.Response
	select {
	case err := <-manualErrs:
		t.Fatalf("JSON manual conflict request: %v", err)
	case response = <-manualResponses:
	case <-time.After(100 * time.Millisecond):
		syncer.releaseAll()
		select {
		case err := <-manualErrs:
			t.Fatalf("JSON manual conflict request after release: %v", err)
		case response = <-manualResponses:
		case <-time.After(time.Second):
			t.Fatal("JSON manual conflict request did not finish")
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("JSON manual conflict status = %d, body=%s, want 409", response.StatusCode, body)
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode JSON conflict response: %v", err)
	}
	if payload["runId"] == "" || (payload["errorKey"] != "refresh_run_conflict" && payload["message"] != "refresh_run_conflict") {
		t.Fatalf("JSON conflict response = %#v, want runId and refresh_run_conflict", payload)
	}
	if got := syncer.callCount(); got != 1 {
		t.Fatalf("site sync calls = %d, want one automatic run without JSON bypass", got)
	}
	syncer.releaseAll()
	first := awaitRefreshResponse(t, firstResponses, firstErrs)
	first.Body.Close()
}

func TestRefreshHandlerAutomaticAndManualJoinMatchingActiveRun(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		firstMethod  string
		secondMethod string
	}{
		{name: "automatic GET joins active manual", firstMethod: http.MethodPost, secondMethod: http.MethodGet},
		{name: "manual POST joins active manual", firstMethod: http.MethodPost, secondMethod: http.MethodPost},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			syncer := newBlockingRefreshRunSync()
			t.Cleanup(syncer.releaseAll)
			service := refreshRunTestService(syncer, fakePlatformGroupReader{})
			server := refreshRunTestServer(t, service)

			firstResponses, firstErrs := startRefreshRequest(server.Client(), newRefreshRunRequest(t, testCase.firstMethod, server.URL+"/api/connection-health/admin-groups/refresh"))
			firstResponse := awaitRefreshResponse(t, firstResponses, firstErrs)
			defer firstResponse.Body.Close()
			_, firstSnapshot := requireRefreshSnapshot(t, firstResponse)
			firstRunID, _ := firstSnapshot.data["runId"].(string)
			if firstRunID == "" {
				t.Fatal("first snapshot did not include runId")
			}

			secondResponse, err := server.Client().Do(newRefreshRunRequest(t, testCase.secondMethod, server.URL+"/api/connection-health/admin-groups/refresh"))
			if err != nil {
				t.Fatalf("joining request: %v", err)
			}
			defer secondResponse.Body.Close()
			_, secondSnapshot := requireRefreshSnapshot(t, secondResponse)
			if secondSnapshot.data["runId"] != firstRunID {
				t.Fatalf("joined runId = %#v, want %q", secondSnapshot.data["runId"], firstRunID)
			}
			if got := syncer.callCount(); got != 1 {
				t.Fatalf("site sync calls = %d, want one joined run", got)
			}
		})
	}
}

func TestRefreshHandlerReconnectReplaysTerminalWithoutRestartingRun(t *testing.T) {
	syncer := newBlockingRefreshRunSync()
	t.Cleanup(syncer.releaseAll)
	service := refreshRunTestService(syncer, fakePlatformGroupReader{})
	server := refreshRunTestServer(t, service)

	responses, errs := startRefreshRequest(server.Client(), newRefreshRunRequest(t, http.MethodGet, server.URL+"/api/connection-health/admin-groups/refresh"))
	response := awaitRefreshResponse(t, responses, errs)
	_, snapshot := requireRefreshSnapshot(t, response)
	runID, _ := snapshot.data["runId"].(string)
	if runID == "" {
		response.Body.Close()
		t.Fatal("initial snapshot did not include runId")
	}
	response.Body.Close()
	syncer.releaseAll()

	deadline := time.Now().Add(time.Second)
	for syncer.callCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(25 * time.Millisecond)
	reconnectURL := server.URL + "/api/connection-health/admin-groups/refresh?run_id=" + runID
	reconnected, err := server.Client().Do(newRefreshRunRequest(t, http.MethodGet, reconnectURL))
	if err != nil {
		t.Fatalf("reconnect request: %v", err)
	}
	defer reconnected.Body.Close()
	reader := bufio.NewReader(reconnected.Body)
	var retainedRevision float64
	for {
		event, readErr := readRefreshSSEEvent(reader)
		if readErr != nil {
			t.Fatalf("read reconnected terminal: %v", readErr)
		}
		if event.name != "terminal" {
			continue
		}
		if event.data["status"] != "success" {
			t.Fatalf("reconnected terminal = %#v, want success", event.data)
		}
		retainedRevision, _ = event.data["revision"].(float64)
		break
	}
	if got := syncer.callCount(); got != 1 {
		t.Fatalf("site sync calls after reconnect = %d, want 1", got)
	}
	secondReplay, err := server.Client().Do(newRefreshRunRequest(t, http.MethodGet, reconnectURL))
	if err != nil {
		t.Fatalf("second terminal replay request: %v", err)
	}
	defer secondReplay.Body.Close()
	secondReplayReader := bufio.NewReader(secondReplay.Body)
	for {
		event, readErr := readRefreshSSEEvent(secondReplayReader)
		if readErr != nil {
			t.Fatalf("read second terminal replay: %v", readErr)
		}
		if event.name != "terminal" {
			continue
		}
		if event.data["revision"] != retainedRevision {
			t.Fatalf("terminal replay revision = %#v, want unchanged %.0f", event.data["revision"], retainedRevision)
		}
		break
	}

	foreign := newRefreshRunRequest(t, http.MethodGet, reconnectURL)
	foreign.Header.Set("X-Test-User", "user2")
	foreignResponse, err := server.Client().Do(foreign)
	if err != nil {
		t.Fatalf("foreign reconnect request: %v", err)
	}
	defer foreignResponse.Body.Close()
	if foreignResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign reconnect status = %d, want 404 without leaking another user's run", foreignResponse.StatusCode)
	}
}

func TestRefreshHandlerSlowSubscriberStillReceivesAuthoritativeTerminal(t *testing.T) {
	syncer := newBlockingRefreshRunSync()
	t.Cleanup(syncer.releaseAll)
	service := refreshRunTestService(syncer, fakePlatformGroupReader{})
	server := refreshRunTestServer(t, service)

	responses, errs := startRefreshRequest(server.Client(), newRefreshRunRequest(t, http.MethodPost, server.URL+"/api/connection-health/admin-groups/refresh"))
	response := awaitRefreshResponse(t, responses, errs)
	defer response.Body.Close()
	reader, first := requireRefreshSnapshot(t, response)
	firstRevision := first.data["revision"].(float64)
	syncer.releaseAll()
	time.Sleep(50 * time.Millisecond)

	for {
		event, err := readRefreshSSEEvent(reader)
		if err != nil {
			t.Fatalf("slow subscriber read: %v", err)
		}
		if event.name != "terminal" {
			continue
		}
		if event.data["status"] != "success" {
			t.Fatalf("slow subscriber terminal = %#v, want success", event.data)
		}
		if revision, ok := event.data["revision"].(float64); !ok || revision <= firstRevision {
			t.Fatalf("terminal revision = %#v, want > %.0f", event.data["revision"], firstRevision)
		}
		return
	}
}

func TestRefreshHandlerPublishesHeartbeatWithoutEndingBlockedRun(t *testing.T) {
	syncer := newBlockingRefreshRunSync()
	t.Cleanup(syncer.releaseAll)
	service := refreshRunTestService(syncer, fakePlatformGroupReader{})
	server := refreshRunTestServer(t, service)

	responses, errs := startRefreshRequest(server.Client(), newRefreshRunRequest(t, http.MethodGet, server.URL+"/api/connection-health/admin-groups/refresh"))
	response := awaitRefreshResponse(t, responses, errs)
	defer response.Body.Close()
	reader, first := requireRefreshSnapshot(t, response)
	for first.data["stage"] != "site_sync" {
		event, err := readRefreshSSEEvent(reader)
		if err != nil {
			t.Fatalf("read blocked site-sync snapshot: %v", err)
		}
		if event.name == "terminal" {
			t.Fatalf("blocked run ended before heartbeat: %#v", event.data)
		}
		if event.name == "snapshot" {
			first = event
		}
	}
	firstRevision := first.data["revision"].(float64)
	heartbeatStartedAt := time.Now()

	events := make(chan refreshSSEEvent, 1)
	readErrs := make(chan error, 1)
	go func() {
		for {
			event, err := readRefreshSSEEvent(reader)
			if err != nil {
				readErrs <- err
				return
			}
			events <- event
		}
	}()
	deadline := time.NewTimer(31 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case err := <-readErrs:
			t.Fatalf("heartbeat read: %v", err)
		case event := <-events:
			if event.name == "terminal" {
				t.Fatalf("blocked run ended before heartbeat: %#v", event.data)
			}
			if event.name != "snapshot" || event.data["runState"] != "running" {
				continue
			}
			if time.Since(heartbeatStartedAt) < 29*time.Second {
				continue
			}
			if revision, ok := event.data["revision"].(float64); !ok || revision <= firstRevision {
				t.Fatalf("heartbeat revision = %#v, want > %.0f", event.data["revision"], firstRevision)
			}
			return
		case <-deadline.C:
			t.Fatal("blocked run published no heartbeat within 30 seconds")
		}
	}
}
