package connection_health

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

const (
	adminGroupsRefreshHeartbeatInterval = 30 * time.Second
	adminGroupsRefreshTerminalRetention = 10 * time.Minute
)

type adminGroupsRefreshMode string

const (
	adminGroupsRefreshModeAutomatic adminGroupsRefreshMode = "automatic"
	adminGroupsRefreshModeManual    adminGroupsRefreshMode = "manual"
)

type adminGroupsRefreshRunState string

const (
	adminGroupsRefreshRunStateRunning  adminGroupsRefreshRunState = "running"
	adminGroupsRefreshRunStateComplete adminGroupsRefreshRunState = "complete"
)

type adminGroupsRefreshStage string

const (
	adminGroupsRefreshStageDiscovering       adminGroupsRefreshStage = "discovering"
	adminGroupsRefreshStageSiteSync          adminGroupsRefreshStage = "site_sync"
	adminGroupsRefreshStageMultiplierRefresh adminGroupsRefreshStage = "multiplier_refresh"
	adminGroupsRefreshStageMainGroups        adminGroupsRefreshStage = "main_groups"
	adminGroupsRefreshStageComplete          adminGroupsRefreshStage = "complete"
)

type adminGroupsRefreshWorkspaceKey struct {
	userID         string
	adminAccountID string
}

type adminGroupsRefreshWaiting struct {
	SiteID         string `json:"siteId"`
	SiteName       string `json:"siteName"`
	Phase          string `json:"phase"`
	ElapsedSeconds int64  `json:"elapsedSeconds"`
	startedAt      time.Time
}

type adminGroupsRefreshIssue struct {
	SiteID   string `json:"siteId,omitempty"`
	SiteName string `json:"siteName,omitempty"`
	Phase    string `json:"phase"`
	Status   string `json:"status"`
	ErrorKey string `json:"errorKey"`
}

type adminGroupsRefreshTerminal struct {
	RunID       string                    `json:"runId"`
	Revision    int64                     `json:"revision"`
	Status      string                    `json:"status"`
	Groups      *[]AdminGroupHealth       `json:"groups,omitempty"`
	Refresh     AdminGroupsRefreshSummary `json:"refresh"`
	ErrorKey    string                    `json:"errorKey,omitempty"`
	FailedStage adminGroupsRefreshStage   `json:"failedStage,omitempty"`
}

type adminGroupsRefreshSnapshot struct {
	RunID               string                      `json:"runId"`
	Mode                adminGroupsRefreshMode      `json:"mode"`
	RunState            adminGroupsRefreshRunState  `json:"runState"`
	Stage               adminGroupsRefreshStage     `json:"stage"`
	Revision            int64                       `json:"revision"`
	StartedAt           time.Time                   `json:"startedAt"`
	UpdatedAt           time.Time                   `json:"updatedAt"`
	StageCompletedSites int                         `json:"stageCompletedSites"`
	StageTotalSites     int                         `json:"stageTotalSites"`
	Waiting             []adminGroupsRefreshWaiting `json:"waiting"`
	Issues              []adminGroupsRefreshIssue   `json:"issues"`
	Terminal            *adminGroupsRefreshTerminal `json:"terminal,omitempty"`
}

type adminGroupsRefreshRun struct {
	mu             sync.Mutex
	id             string
	runMode        adminGroupsRefreshMode
	key            adminGroupsRefreshWorkspaceKey
	ctx            context.Context
	cancel         context.CancelFunc
	initial        adminGroupsRefreshSnapshot
	snapshot       adminGroupsRefreshSnapshot
	refresh        AdminGroupsRefreshSummary
	subscribers    map[uint64]chan struct{}
	nextSubscriber uint64
	complete       atomic.Bool
}

type adminGroupsRefreshSubscription struct {
	ID      uint64
	Signals <-chan struct{}
}

type adminGroupsRefreshRunDisposition int

const (
	adminGroupsRefreshRunStarted adminGroupsRefreshRunDisposition = iota
	adminGroupsRefreshRunJoined
	adminGroupsRefreshRunConflict
)

func (s *Service) initializeAdminGroupsRefreshRuntime() {
	s.refreshRunMu.Lock()
	defer s.refreshRunMu.Unlock()
	s.initializeAdminGroupsRefreshRuntimeLocked()
}

func (s *Service) initializeAdminGroupsRefreshRuntimeLocked() {
	if s.refreshRootCtx == nil {
		s.refreshRootCtx, s.refreshRootCancel = context.WithCancel(context.Background())
	}
	if s.refreshActive == nil {
		s.refreshActive = make(map[adminGroupsRefreshWorkspaceKey]*adminGroupsRefreshRun)
	}
	if s.refreshRunsByID == nil {
		s.refreshRunsByID = make(map[string]*adminGroupsRefreshRun)
	}
	if s.refreshRetained == nil {
		s.refreshRetained = make(map[adminGroupsRefreshWorkspaceKey]*adminGroupsRefreshRun)
	}
	if s.refreshRetentionTimers == nil {
		s.refreshRetentionTimers = make(map[adminGroupsRefreshWorkspaceKey]*time.Timer)
	}
	if s.refreshHeartbeat <= 0 {
		s.refreshHeartbeat = adminGroupsRefreshHeartbeatInterval
	}
	if s.refreshRetention <= 0 {
		s.refreshRetention = adminGroupsRefreshTerminalRetention
	}
}

func (s *Service) startOrJoinAdminGroupsRefreshRun(ctx context.Context, userID string, mode adminGroupsRefreshMode) (*adminGroupsRefreshRun, adminGroupsRefreshRunDisposition, error) {
	adminAccountID, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return nil, adminGroupsRefreshRunConflict, err
	}
	key := adminGroupsRefreshWorkspaceKey{userID: userID, adminAccountID: adminAccountID}

	s.refreshRunMu.Lock()
	defer s.refreshRunMu.Unlock()
	s.initializeAdminGroupsRefreshRuntimeLocked()
	if s.refreshRunClosed {
		return nil, adminGroupsRefreshRunConflict, errors.New("connection health refresh service is shutting down")
	}
	if active := s.refreshActive[key]; active != nil {
		if !active.complete.Load() {
			if mode == adminGroupsRefreshModeManual && active.runMode == adminGroupsRefreshModeAutomatic {
				return active, adminGroupsRefreshRunConflict, nil
			}
			return active, adminGroupsRefreshRunJoined, nil
		}
		delete(s.refreshActive, key)
		delete(s.refreshRunsByID, active.id)
	}
	if retained := s.refreshRetained[key]; retained != nil {
		if timer := s.refreshRetentionTimers[key]; timer != nil {
			timer.Stop()
		}
		delete(s.refreshRetentionTimers, key)
		delete(s.refreshRunsByID, retained.id)
		delete(s.refreshRetained, key)
	}

	runID, err := newAdminGroupsRefreshRunID()
	if err != nil {
		return nil, adminGroupsRefreshRunConflict, err
	}
	now := time.Now()
	runCtx, cancel := context.WithCancel(s.refreshRootCtx)
	initial := adminGroupsRefreshSnapshot{
		RunID:     runID,
		Mode:      mode,
		RunState:  adminGroupsRefreshRunStateRunning,
		Stage:     adminGroupsRefreshStageDiscovering,
		Revision:  1,
		StartedAt: now,
		UpdatedAt: now,
		Waiting:   []adminGroupsRefreshWaiting{},
		Issues:    []adminGroupsRefreshIssue{},
	}
	run := &adminGroupsRefreshRun{
		id:          runID,
		runMode:     mode,
		key:         key,
		ctx:         runCtx,
		cancel:      cancel,
		refresh:     normalizeAdminGroupsRefreshSummary(AdminGroupsRefreshSummary{State: "success"}),
		initial:     cloneAdminGroupsRefreshSnapshot(initial),
		snapshot:    initial,
		subscribers: make(map[uint64]chan struct{}),
	}
	s.refreshActive[key] = run
	s.refreshRunsByID[runID] = run
	s.refreshRunWG.Add(1)
	go s.executeAdminGroupsRefreshRun(run, userID)
	return run, adminGroupsRefreshRunStarted, nil
}

func (s *Service) adminGroupsRefreshRunByID(ctx context.Context, userID string, runID string) (*adminGroupsRefreshRun, bool) {
	adminAccountID, err := s.currentAdminAccountID(ctx, userID)
	if err != nil {
		return nil, false
	}
	s.refreshRunMu.Lock()
	defer s.refreshRunMu.Unlock()
	run := s.refreshRunsByID[runID]
	if run == nil || run.key != (adminGroupsRefreshWorkspaceKey{userID: userID, adminAccountID: adminAccountID}) {
		return nil, false
	}
	return run, true
}

func (s *Service) executeAdminGroupsRefreshRun(run *adminGroupsRefreshRun, userID string) {
	defer s.refreshRunWG.Done()
	type pipelineResult struct {
		result AdminGroupsFreshResult
		err    error
	}
	resultCh := make(chan pipelineResult, 1)
	go func() {
		result, err := s.adminGroupsRefreshResultWithRun(run.ctx, userID, run)
		resultCh <- pipelineResult{result: result, err: err}
	}()

	ticker := time.NewTicker(s.adminGroupsRefreshHeartbeatDuration())
	defer ticker.Stop()
	for {
		select {
		case result := <-resultCh:
			if run.ctx.Err() != nil {
				s.finishAdminGroupsRefreshRun(run, run.failureTerminal("service_shutdown"))
				return
			}
			if result.err != nil {
				s.finishAdminGroupsRefreshRun(run, run.pipelineFailureTerminal())
				return
			}
			groups := result.result.Groups
			if groups == nil {
				groups = []AdminGroupHealth{}
			}
			s.finishAdminGroupsRefreshRun(run, adminGroupsRefreshTerminal{
				Status:  "success",
				Groups:  &groups,
				Refresh: normalizeAdminGroupsRefreshSummary(result.result.Refresh),
			})
			return
		case now := <-ticker.C:
			run.publishHeartbeat(now)
		case <-run.ctx.Done():
			s.finishAdminGroupsRefreshRun(run, run.failureTerminal("service_shutdown"))
			<-resultCh
			return
		}
	}
}

func (s *Service) adminGroupsRefreshResultWithRun(ctx context.Context, userID string, run *adminGroupsRefreshRun) (AdminGroupsFreshResult, error) {
	adminAccountID := run.key.adminAccountID
	run.publishStage(adminGroupsRefreshStageSiteSync, 0, 0, nil, nil)
	var siteProgressMu sync.Mutex
	siteProgressSeen := make(map[string]struct{})
	siteProgressNames := make(map[string]string)
	var siteProgressWaiting []adminGroupsRefreshWaiting
	siteProgressTotal := 0
	publishSiteTerminal := func(result upstream.SyncSiteResult) {
		siteProgressMu.Lock()
		defer siteProgressMu.Unlock()
		if _, seen := siteProgressSeen[result.SiteID]; seen {
			return
		}
		siteProgressSeen[result.SiteID] = struct{}{}
		remaining := make([]adminGroupsRefreshWaiting, 0, len(siteProgressWaiting))
		for _, waiting := range siteProgressWaiting {
			if waiting.SiteID != result.SiteID {
				remaining = append(remaining, waiting)
			}
		}
		siteProgressWaiting = remaining
		_, issues := adminGroupsRefreshProgressForSyncSites([]upstream.SyncSiteResult{result}, nil, "")
		for index := range issues {
			issues[index].SiteName = siteProgressNames[result.SiteID]
		}
		run.publishStage(adminGroupsRefreshStageSiteSync, len(siteProgressSeen), siteProgressTotal, remaining, issues)
	}
	syncSites, connections, connectionsReady, syncErrorKey := s.refreshRelatedUpstreamSitesProgress(ctx, userID, adminAccountID, run.runMode == adminGroupsRefreshModeManual, func(connections []my_sites.RealConnection) {
		siteProgressMu.Lock()
		siteProgressWaiting = adminGroupsRefreshWaitingForConnections(connections, "site_sync")
		siteProgressTotal = len(siteProgressWaiting)
		for _, waiting := range siteProgressWaiting {
			siteProgressNames[waiting.SiteID] = waiting.SiteName
		}
		waiting := append([]adminGroupsRefreshWaiting(nil), siteProgressWaiting...)
		siteProgressMu.Unlock()
		run.publishStage(adminGroupsRefreshStageSiteSync, 0, len(waiting), waiting, nil)
	}, publishSiteTerminal)
	if err := ctx.Err(); err != nil {
		return AdminGroupsFreshResult{}, err
	}

	for _, result := range syncSites {
		publishSiteTerminal(result)
	}
	if len(syncSites) == 0 || syncErrorKey != "" {
		siteCompleted, siteIssues := adminGroupsRefreshProgressForSyncSites(syncSites, connections, syncErrorKey)
		run.publishStage(adminGroupsRefreshStageSiteSync, siteCompleted, len(syncSites), nil, siteIssues)
	}
	run.publishStage(adminGroupsRefreshStageMultiplierRefresh, 0, 0, nil, nil)
	var multiplierProgressMu sync.Mutex
	multiplierProgressSeen := make(map[string]struct{})
	var multiplierProgressWaiting []adminGroupsRefreshWaiting
	multiplierProgressTotal := 0

	groups, err := s.adminGroupsForWorkspaceWithConnectionsProgress(ctx, userID, adminAccountID, true, run.runMode == adminGroupsRefreshModeManual, connections, connectionsReady, func(siteIDs []string) {
		multiplierProgressMu.Lock()
		multiplierProgressWaiting = adminGroupsRefreshWaitingForSiteIDs(siteIDs, connections, "multiplier_refresh")
		multiplierProgressTotal = len(multiplierProgressWaiting)
		waiting := append([]adminGroupsRefreshWaiting(nil), multiplierProgressWaiting...)
		multiplierProgressMu.Unlock()
		run.publishStage(adminGroupsRefreshStageMultiplierRefresh, 0, len(waiting), waiting, nil)
	}, func(site AdminGroupsRefreshSite) {
		multiplierProgressMu.Lock()
		defer multiplierProgressMu.Unlock()
		if _, seen := multiplierProgressSeen[site.SiteID]; seen {
			return
		}
		multiplierProgressSeen[site.SiteID] = struct{}{}
		remaining := make([]adminGroupsRefreshWaiting, 0, len(multiplierProgressWaiting))
		for _, waiting := range multiplierProgressWaiting {
			if waiting.SiteID != site.SiteID {
				remaining = append(remaining, waiting)
			}
		}
		multiplierProgressWaiting = remaining
		_, issues := adminGroupsRefreshProgressForMultiplier(AdminGroupsRefreshSummary{Sites: []AdminGroupsRefreshSite{site}}, connections)
		run.setRefreshSummary(mergeAdminGroupsRefreshSummary(syncSites, s.multiplierRefreshSummary(userID, adminAccountID), syncErrorKey))
		run.publishStage(adminGroupsRefreshStageMultiplierRefresh, len(multiplierProgressSeen), multiplierProgressTotal, remaining, issues)
	}, func(multiplier AdminGroupsRefreshSummary) {
		_, multiplierIssues := adminGroupsRefreshProgressForMultiplier(multiplier, connections)
		multiplierProgressMu.Lock()
		multiplierCompleted := len(multiplierProgressSeen)
		multiplierTotal := multiplierProgressTotal
		multiplierProgressMu.Unlock()
		refresh := mergeAdminGroupsRefreshSummary(syncSites, multiplier, syncErrorKey)
		run.setRefreshSummary(refresh)
		run.publishStage(adminGroupsRefreshStageMultiplierRefresh, multiplierCompleted, multiplierTotal, nil, multiplierIssues)
		run.publishStage(adminGroupsRefreshStageMainGroups, 0, 0, nil, nil)
	}, time.Now(), 0)
	refresh := mergeAdminGroupsRefreshSummary(syncSites, s.multiplierRefreshSummary(userID, adminAccountID), syncErrorKey)
	run.setRefreshSummary(refresh)
	return AdminGroupsFreshResult{Groups: groups, Refresh: refresh}, err
}

func (s *Service) adminGroupsRefreshHeartbeatDuration() time.Duration {
	s.refreshRunMu.Lock()
	defer s.refreshRunMu.Unlock()
	if s.refreshHeartbeat <= 0 {
		return adminGroupsRefreshHeartbeatInterval
	}
	return s.refreshHeartbeat
}

func (s *Service) finishAdminGroupsRefreshRun(run *adminGroupsRefreshRun, terminal adminGroupsRefreshTerminal) {
	run.publishTerminal(terminal)

	s.refreshRunMu.Lock()
	defer s.refreshRunMu.Unlock()
	if s.refreshActive[run.key] != run {
		if s.refreshRetained[run.key] == run {
			return
		}
		delete(s.refreshRunsByID, run.id)
		return
	}
	delete(s.refreshActive, run.key)
	if s.refreshRunClosed {
		delete(s.refreshRunsByID, run.id)
		return
	}
	s.refreshRetained[run.key] = run
	retention := s.refreshRetention
	if retention <= 0 {
		retention = adminGroupsRefreshTerminalRetention
	}
	s.refreshRetentionTimers[run.key] = time.AfterFunc(retention, func() {
		s.expireAdminGroupsRefreshRun(run)
	})
}

func (s *Service) expireAdminGroupsRefreshRun(run *adminGroupsRefreshRun) {
	s.refreshRunMu.Lock()
	defer s.refreshRunMu.Unlock()
	if s.refreshRetained[run.key] != run {
		return
	}
	delete(s.refreshRetentionTimers, run.key)
	delete(s.refreshRetained, run.key)
	delete(s.refreshRunsByID, run.id)
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.refreshRunMu.Lock()
	s.initializeAdminGroupsRefreshRuntimeLocked()
	s.refreshRunClosed = true
	if s.refreshRootCancel != nil {
		s.refreshRootCancel()
	}
	for key, run := range s.refreshRetained {
		if timer := s.refreshRetentionTimers[key]; timer != nil {
			timer.Stop()
		}
		delete(s.refreshRetentionTimers, key)
		delete(s.refreshRetained, key)
		delete(s.refreshRunsByID, run.id)
	}
	s.refreshRunMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.refreshRunWG.Wait()
		s.multiplierRefreshWG.Wait()
		close(done)
	}()
	var refreshErr error
	select {
	case <-done:
	case <-ctx.Done():
		refreshErr = ctx.Err()
	}
	return errors.Join(refreshErr, s.ShutdownQuestionAnswers(ctx))
}

func (run *adminGroupsRefreshRun) publishStage(stage adminGroupsRefreshStage, completed int, total int, waiting []adminGroupsRefreshWaiting, issues []adminGroupsRefreshIssue) bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.snapshot.RunState == adminGroupsRefreshRunStateComplete {
		return false
	}
	run.snapshot.Stage = stage
	run.snapshot.StageCompletedSites = completed
	run.snapshot.StageTotalSites = total
	now := time.Now()
	previousWaiting := make(map[string]adminGroupsRefreshWaiting, len(run.snapshot.Waiting))
	for _, item := range run.snapshot.Waiting {
		previousWaiting[item.SiteID+"\x00"+item.Phase] = item
	}
	run.snapshot.Waiting = append([]adminGroupsRefreshWaiting{}, waiting...)
	for index := range run.snapshot.Waiting {
		key := run.snapshot.Waiting[index].SiteID + "\x00" + run.snapshot.Waiting[index].Phase
		if previous, ok := previousWaiting[key]; ok {
			run.snapshot.Waiting[index].startedAt = previous.startedAt
			run.snapshot.Waiting[index].ElapsedSeconds = previous.ElapsedSeconds
		} else if run.snapshot.Waiting[index].startedAt.IsZero() {
			run.snapshot.Waiting[index].startedAt = now
		}
	}
	if issues != nil {
		existing := make(map[string]struct{}, len(run.snapshot.Issues))
		for _, issue := range run.snapshot.Issues {
			existing[issue.SiteID+"\x00"+issue.Phase+"\x00"+issue.Status+"\x00"+issue.ErrorKey] = struct{}{}
		}
		for _, issue := range issues {
			key := issue.SiteID + "\x00" + issue.Phase + "\x00" + issue.Status + "\x00" + issue.ErrorKey
			if _, duplicate := existing[key]; duplicate {
				continue
			}
			existing[key] = struct{}{}
			run.snapshot.Issues = append(run.snapshot.Issues, issue)
		}
	}
	run.bumpRevisionLocked(now)
	run.signalSubscribersLocked()
	return true
}

func (run *adminGroupsRefreshRun) publishHeartbeat(now time.Time) bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.snapshot.RunState == adminGroupsRefreshRunStateComplete {
		return false
	}
	for index := range run.snapshot.Waiting {
		startedAt := run.snapshot.Waiting[index].startedAt
		if startedAt.IsZero() {
			startedAt = run.snapshot.UpdatedAt
		}
		run.snapshot.Waiting[index].ElapsedSeconds = int64(now.Sub(startedAt).Seconds())
	}
	run.bumpRevisionLocked(now)
	run.signalSubscribersLocked()
	return true
}

func (run *adminGroupsRefreshRun) publishTerminal(terminal adminGroupsRefreshTerminal) bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.snapshot.RunState == adminGroupsRefreshRunStateComplete {
		return false
	}
	terminal.RunID = run.snapshot.RunID
	terminal.Revision = run.snapshot.Revision + 1
	terminal.Refresh = normalizeAdminGroupsRefreshSummary(terminal.Refresh)
	run.snapshot.RunState = adminGroupsRefreshRunStateComplete
	run.snapshot.Stage = adminGroupsRefreshStageComplete
	run.snapshot.StageCompletedSites = run.snapshot.StageTotalSites
	run.snapshot.Waiting = []adminGroupsRefreshWaiting{}
	run.snapshot.Terminal = &terminal
	run.bumpRevisionLocked(time.Now())
	run.complete.Store(true)
	for id, signals := range run.subscribers {
		close(signals)
		delete(run.subscribers, id)
	}
	run.cancel()
	return true
}

func (run *adminGroupsRefreshRun) subscribe() (adminGroupsRefreshSubscription, adminGroupsRefreshSnapshot) {
	run.mu.Lock()
	defer run.mu.Unlock()
	snapshot := cloneAdminGroupsRefreshSnapshot(run.snapshot)
	if run.snapshot.RunState == adminGroupsRefreshRunStateComplete {
		signals := make(chan struct{})
		close(signals)
		return adminGroupsRefreshSubscription{Signals: signals}, snapshot
	}
	run.nextSubscriber++
	signals := make(chan struct{}, 1)
	run.subscribers[run.nextSubscriber] = signals
	return adminGroupsRefreshSubscription{ID: run.nextSubscriber, Signals: signals}, snapshot
}

func (run *adminGroupsRefreshRun) unsubscribe(id uint64) {
	if id == 0 {
		return
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	delete(run.subscribers, id)
}

func (run *adminGroupsRefreshRun) latest() adminGroupsRefreshSnapshot {
	run.mu.Lock()
	defer run.mu.Unlock()
	return cloneAdminGroupsRefreshSnapshot(run.snapshot)
}

func (run *adminGroupsRefreshRun) initialSnapshot() adminGroupsRefreshSnapshot {
	return cloneAdminGroupsRefreshSnapshot(run.initial)
}

func (run *adminGroupsRefreshRun) failureTerminal(errorKey string) adminGroupsRefreshTerminal {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.failureTerminalLocked(run.snapshot.Stage, errorKey)
}

func (run *adminGroupsRefreshRun) failureTerminalAt(stage adminGroupsRefreshStage, errorKey string) adminGroupsRefreshTerminal {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.failureTerminalLocked(stage, errorKey)
}

func (run *adminGroupsRefreshRun) pipelineFailureTerminal() adminGroupsRefreshTerminal {
	run.mu.Lock()
	defer run.mu.Unlock()
	stage := run.snapshot.Stage
	errorKey := "refresh_unavailable"
	switch stage {
	case adminGroupsRefreshStageSiteSync:
		errorKey = "site_sync_unavailable"
	case adminGroupsRefreshStageMultiplierRefresh:
		errorKey = "multiplier_unavailable"
	case adminGroupsRefreshStageMainGroups:
		errorKey = "main_groups_unavailable"
	}
	return run.failureTerminalLocked(stage, errorKey)
}

func (run *adminGroupsRefreshRun) failureTerminalLocked(stage adminGroupsRefreshStage, errorKey string) adminGroupsRefreshTerminal {
	return adminGroupsRefreshTerminal{
		Status:      "failed",
		ErrorKey:    errorKey,
		FailedStage: stage,
		Refresh:     normalizeAdminGroupsRefreshSummary(run.refresh),
	}
}

func (run *adminGroupsRefreshRun) setRefreshSummary(summary AdminGroupsRefreshSummary) {
	run.mu.Lock()
	defer run.mu.Unlock()
	run.refresh = normalizeAdminGroupsRefreshSummary(summary)
}

func (run *adminGroupsRefreshRun) bumpRevisionLocked(now time.Time) {
	run.snapshot.Revision++
	run.snapshot.UpdatedAt = now
}

func (run *adminGroupsRefreshRun) signalSubscribersLocked() {
	for _, signals := range run.subscribers {
		select {
		case signals <- struct{}{}:
		default:
		}
	}
}

func adminGroupsRefreshProgressForSyncSites(sites []upstream.SyncSiteResult, connections []my_sites.RealConnection, overallErrorKey string) (int, []adminGroupsRefreshIssue) {
	names := adminGroupsRefreshSiteNames(connections)
	issues := make([]adminGroupsRefreshIssue, 0)
	for _, site := range sites {
		if site.Status == "success" || site.Status == "disabled" {
			continue
		}
		issues = append(issues, adminGroupsRefreshIssue{
			SiteID:   site.SiteID,
			SiteName: names[site.SiteID],
			Phase:    "site_sync",
			Status:   site.Status,
			ErrorKey: safeAdminGroupsRefreshErrorKey(site.ErrorKey),
		})
	}
	if overallErrorKey != "" {
		issues = append(issues, adminGroupsRefreshIssue{Phase: "site_sync", Status: "unavailable", ErrorKey: safeAdminGroupsRefreshErrorKey(overallErrorKey)})
	}
	return len(sites), issues
}

func adminGroupsRefreshProgressForMultiplier(summary AdminGroupsRefreshSummary, connections []my_sites.RealConnection) (int, []adminGroupsRefreshIssue) {
	names := adminGroupsRefreshSiteNames(connections)
	issues := make([]adminGroupsRefreshIssue, 0)
	for _, site := range summary.Sites {
		if site.Status == "success" || site.Status == "disabled" {
			continue
		}
		issues = append(issues, adminGroupsRefreshIssue{
			SiteID:   site.SiteID,
			SiteName: names[site.SiteID],
			Phase:    "multiplier_refresh",
			Status:   site.Status,
			ErrorKey: safeAdminGroupsRefreshErrorKey(multiplierRefreshErrorKey(site.ErrorKey)),
		})
	}
	return len(summary.Sites), issues
}

func adminGroupsRefreshWaitingForConnections(connections []my_sites.RealConnection, phase string) []adminGroupsRefreshWaiting {
	waiting := make([]adminGroupsRefreshWaiting, 0, len(connections))
	seen := make(map[string]struct{}, len(connections))
	for _, connection := range connections {
		if connection.UpstreamSiteID == "" {
			continue
		}
		if _, ok := seen[connection.UpstreamSiteID]; ok {
			continue
		}
		seen[connection.UpstreamSiteID] = struct{}{}
		waiting = append(waiting, adminGroupsRefreshWaiting{
			SiteID:   connection.UpstreamSiteID,
			SiteName: connection.SiteName,
			Phase:    phase,
		})
	}
	return waiting
}

func adminGroupsRefreshWaitingForSiteIDs(siteIDs []string, connections []my_sites.RealConnection, phase string) []adminGroupsRefreshWaiting {
	names := adminGroupsRefreshSiteNames(connections)
	waiting := make([]adminGroupsRefreshWaiting, 0, len(siteIDs))
	seen := make(map[string]struct{}, len(siteIDs))
	for _, siteID := range siteIDs {
		siteID = strings.TrimSpace(siteID)
		if siteID == "" {
			continue
		}
		if _, duplicate := seen[siteID]; duplicate {
			continue
		}
		seen[siteID] = struct{}{}
		waiting = append(waiting, adminGroupsRefreshWaiting{SiteID: siteID, SiteName: names[siteID], Phase: phase})
	}
	return waiting
}

func adminGroupsRefreshSiteNames(connections []my_sites.RealConnection) map[string]string {
	names := make(map[string]string, len(connections))
	for _, connection := range connections {
		if _, ok := names[connection.UpstreamSiteID]; !ok {
			names[connection.UpstreamSiteID] = connection.SiteName
		}
	}
	return names
}

func safeAdminGroupsRefreshErrorKey(errorKey string) string {
	if errorKey == "" {
		return "upstream_unavailable"
	}
	for _, char := range errorKey {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
			return "upstream_unavailable"
		}
	}
	return errorKey
}

func cloneAdminGroupsRefreshSnapshot(snapshot adminGroupsRefreshSnapshot) adminGroupsRefreshSnapshot {
	clone := snapshot
	clone.Waiting = append([]adminGroupsRefreshWaiting{}, snapshot.Waiting...)
	clone.Issues = append([]adminGroupsRefreshIssue{}, snapshot.Issues...)
	if snapshot.Terminal != nil {
		terminal := *snapshot.Terminal
		terminal.Refresh = normalizeAdminGroupsRefreshSummary(terminal.Refresh)
		if snapshot.Terminal.Groups != nil {
			groups := append([]AdminGroupHealth{}, (*snapshot.Terminal.Groups)...)
			terminal.Groups = &groups
		}
		clone.Terminal = &terminal
	}
	return clone
}

func normalizeAdminGroupsRefreshSummary(summary AdminGroupsRefreshSummary) AdminGroupsRefreshSummary {
	if summary.Sites == nil {
		summary.Sites = []AdminGroupsRefreshSite{}
	} else {
		summary.Sites = append([]AdminGroupsRefreshSite{}, summary.Sites...)
	}
	return summary
}

func newAdminGroupsRefreshRunID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generate connection health refresh run id: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}
