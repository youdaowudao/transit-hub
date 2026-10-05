package upstream

import (
	"context"
	"errors"
	"log"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"transithub/backend/internal/shared/businesstime"
)

const keyUsageCollectionTimeout = 45 * time.Second

type keyUsageFlight struct {
	pending *Site
	done    chan struct{}
}

type keyUsageCostFailure struct {
	reason   string
	failedAt time.Time
}

// A known cost refusal is a terminal attempt; it never makes a Key request.
// This runs after sync notification, just like an actual collection.
func (s *Service) recordKnownKeyUsageCostFailure(site Site, failure keyUsageCostFailure) {
	store := s.keyUsageStore()
	if store == nil {
		return
	}
	startedAt := s.keyUsageNow()
	value := KeyUsageSnapshot{BusinessDate: businesstime.DateAt(startedAt), StartedAt: startedAt,
		AttemptStartedAt: startedAt, CollectedAt: startedAt, ConsumeDate: site.Metrics.TodayConsumeDate,
		SyncedRawCost: site.Metrics.TodayConsume.Value, FailureReason: failure.reason, FailureAt: &failure.failedAt,
		Items: []KeyUsageTodayStat{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, deleted := s.deletedSites[site.ID]; deleted {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer cancel()
	current, err := s.cache.Get(ctx, site.ID)
	if err != nil || current == nil || !current.IsEnabled() {
		return
	}
	if err := store.SaveKeyUsageSnapshot(ctx, site.ID, value); err != nil {
		log.Printf("[upstream] 逐Key快照写入失败 site=%s", site.ID)
	}
	if s.keyUsageFinished == nil {
		s.keyUsageFinished = make(map[string]time.Time)
	}
	s.keyUsageFinished[site.ID] = startedAt
	s.keyUsageNotifyLocked()
}

func (s *Service) keyUsageStore() KeyUsageSnapshotStore {
	store, _ := s.cache.(KeyUsageSnapshotStore)
	return store
}
func (s *Service) keyUsageNow() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}
func (s *Service) keyUsageDate() string { return businesstime.DateAt(s.keyUsageNow()) }

// Called only after sync's done channel is closed, so upstream-page waiting is unchanged.
func (s *Service) enqueueKeyUsageCollection(site Site) {
	if s.keyUsageStore() == nil || !site.IsEnabled() || site.Session == nil {
		return
	}
	s.mu.Lock()
	if _, deleted := s.deletedSites[site.ID]; deleted {
		s.mu.Unlock()
		return
	}
	if s.keyUsageFlights == nil {
		s.keyUsageFlights = make(map[string]*keyUsageFlight)
	}
	if flight := s.keyUsageFlights[site.ID]; flight != nil {
		copy := site
		flight.pending = &copy
		s.keyUsageNotifyLocked()
		s.mu.Unlock()
		return
	}
	flight := &keyUsageFlight{done: make(chan struct{})}
	s.keyUsageFlights[site.ID] = flight
	s.keyUsageNotifyLocked()
	s.mu.Unlock()
	go s.runKeyUsageFlight(site, flight)
}

func (s *Service) keyUsageNotifyLocked() {
	if s.keyUsageChanged != nil {
		close(s.keyUsageChanged)
	}
	s.keyUsageChanged = make(chan struct{})
}

func (s *Service) runKeyUsageFlight(site Site, flight *keyUsageFlight) {
	defer func() {
		if recover() != nil {
			log.Printf("[upstream] 逐Key读取失败 site=%s name=%s host=%s reason=%s status=0", site.ID, safeUpstreamMessage(site.Name), safeHost(site.BaseURL), ErrorUnknown)
		}
		s.mu.Lock()
		if s.keyUsageFlights[site.ID] == flight {
			delete(s.keyUsageFlights, site.ID)
			close(flight.done)
			s.keyUsageNotifyLocked()
		}
		s.mu.Unlock()
	}()
	for {
		attemptStartedAt := s.keyUsageNow()
		s.collectKeyUsageSnapshot(site)
		s.mu.Lock()
		if s.keyUsageFinished == nil {
			s.keyUsageFinished = make(map[string]time.Time)
		}
		s.keyUsageFinished[site.ID] = attemptStartedAt
		s.keyUsageNotifyLocked()
		if flight.pending == nil {
			delete(s.keyUsageFlights, site.ID)
			close(flight.done)
			s.keyUsageNotifyLocked()
			s.mu.Unlock()
			return
		}
		site = *flight.pending
		flight.pending = nil
		s.mu.Unlock()
	}
}

func (s *Service) collectKeyUsageSnapshot(site Site) {
	if site.Session == nil {
		return
	}
	// Keep the original session value stable while a newer login may replace it.
	originalSession := *site.Session
	if originalSession.ExpiresAt != nil {
		expiresAt := *originalSession.ExpiresAt
		originalSession.ExpiresAt = &expiresAt
	}
	site.Session = &originalSession
	startedAt := s.keyUsageNow()
	date := businesstime.DateAt(startedAt)
	value := KeyUsageSnapshot{BusinessDate: date, StartedAt: startedAt, AttemptStartedAt: startedAt, ConsumeDate: site.Metrics.TodayConsumeDate, SyncedRawCost: site.Metrics.TodayConsume.Value, Items: []KeyUsageTodayStat{}}
	ctx, cancel := context.WithTimeout(context.Background(), keyUsageCollectionTimeout)
	defer cancel()
	if s.isSiteDeleted(site.ID) {
		return
	}
	if current, err := s.cache.Get(ctx, site.ID); err != nil || !keyUsageSiteMatches(current, site) || !reflect.DeepEqual(current.Session, site.Session) {
		return
	}
	session, err := s.platformService.RefreshSessionContext(ctx, *site.Session)
	sessionPersisted := false
	if err == nil {
		proceed, saveErr := s.persistKeyUsageSession(site, session)
		if !proceed {
			return
		}
		err = saveErr
		sessionPersisted = saveErr == nil
	}
	if err == nil {
		value.CollectedRawCost, err = s.platformService.fetchKeyCollectionTotal(ctx, session, date)
	}
	if err == nil {
		value.Items, err = s.platformService.collectKeyUsageToday(ctx, session, site.Metrics.Groups, date)
	}
	value.CollectedAt = s.keyUsageNow()
	if err == nil {
		value.Complete = true
	} else {
		reason := siteErrorKey(err)
		value.FailureReason = reason
		value.FailureAt = &value.CollectedAt
		var requestErr *RequestError
		status := 0
		if errors.As(err, &requestErr) {
			status = requestErr.StatusCode
		}
		log.Printf("[upstream] 逐Key读取失败 site=%s name=%s host=%s reason=%s status=%d", site.ID, safeUpstreamMessage(site.Name), safeHost(site.BaseURL), reason, status)
	}
	// Removal holds the same lock across deletion, preventing a late writer's resurrection.
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, deleted := s.deletedSites[site.ID]; deleted {
		return
	}
	saveCtx, saveCancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer saveCancel()
	current, cacheErr := s.cache.Get(saveCtx, site.ID)
	if cacheErr != nil || !keyUsageSiteMatches(current, site) {
		return
	}
	// An in-flight attempt must not publish results for a replaced session.
	// Failed persistence may leave the original cache value; it can only publish
	// the failure record, never a complete collection.
	if !reflect.DeepEqual(current.Session, &session) && (sessionPersisted || !reflect.DeepEqual(current.Session, site.Session)) {
		return
	}
	// Persistence gets an independent bounded context after the 45s attempt finishes.
	if saveErr := s.keyUsageStore().SaveKeyUsageSnapshot(saveCtx, site.ID, value); saveErr != nil {
		log.Printf("[upstream] 逐Key快照写入失败 site=%s", site.ID)
	}
	s.keyUsageNotifyLocked()
}

func keyUsageSiteMatches(current *Site, original Site) bool {
	return current != nil && current.ID == original.ID && current.UserID == original.UserID &&
		current.AdminAccountID == original.AdminAccountID && current.Platform == original.Platform &&
		current.IsEnabled() && current.Session != nil
}

// Persist a successful rotation before any subsequent cost read can fail.
// The service lock protects deletion and compares the current session with the
// refresh input; copy current site metadata and replace only the session.
func (s *Service) persistKeyUsageSession(original Site, refreshed Session) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, deleted := s.deletedSites[original.ID]; deleted {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer cancel()
	current, err := s.cache.Get(ctx, original.ID)
	if err != nil {
		return true, keyUsageSessionPersistenceError(original.Platform, err)
	}
	if !keyUsageSiteMatches(current, original) || !reflect.DeepEqual(current.Session, original.Session) {
		return false, nil
	}
	if reflect.DeepEqual(current.Session, &refreshed) {
		return true, nil
	}
	current.Session = &refreshed
	// Both interfaces are called under the lock: the public wrappers acquire the
	// same lock themselves. Each store is attempted independently so a cache
	// failure cannot discard a rotated refresh token; neither store is rolled back.
	cacheErr := s.cache.Set(ctx, current)
	var durableErr error
	if s.repository != nil {
		durableCtx, durableCancel := context.WithTimeout(context.Background(), persistenceTimeout)
		defer durableCancel()
		durableErr = s.repository.SaveSite(durableCtx, *current)
	}
	if err := errors.Join(cacheErr, durableErr); err != nil {
		return true, keyUsageSessionPersistenceError(original.Platform, err)
	}
	return true, nil
}

func keyUsageSessionPersistenceError(platform Platform, cause error) error {
	detail := newRequestError(ErrorRequest, platform)
	detail.Cause = cause
	return detail
}

func (s *Service) CachedKeyUsageForDate(ctx context.Context, userID, adminAccountID, date string) (KeyUsageForDateResult, error) {
	if strings.TrimSpace(adminAccountID) == "" {
		return KeyUsageForDateResult{}, errors.New("admin account id is required")
	}
	if strings.TrimSpace(date) == "" {
		date = s.keyUsageDate()
	}
	sites, err := s.cache.ListByUser(ctx, userID)
	if err != nil {
		return KeyUsageForDateResult{}, err
	}
	s.mu.Lock()
	auto := s.refreshConfigs[refreshWorkspaceKey{userID: userID, adminAccountID: adminAccountID}].Enabled
	s.mu.Unlock()
	result := KeyUsageForDateResult{BusinessDate: date, AutoRefreshEnabled: auto, Sites: []KeyUsageSiteResult{}}
	sort.Slice(sites, func(i, j int) bool { return sites[i].ID < sites[j].ID })
	for _, site := range sites {
		if site == nil || site.AdminAccountID != adminAccountID || !site.IsEnabled() || site.RechargeRate <= 0 {
			continue
		}
		item := KeyUsageSiteResult{SiteID: site.ID, SiteName: site.Name, Platform: site.Platform, RechargeRate: site.RechargeRate, Status: "missing", Items: []KeyUsageTodayItem{}}
		store := s.keyUsageStore()
		var snapshot *KeyUsageSnapshot
		if store != nil {
			snapshot, err = store.GetKeyUsageSnapshot(ctx, site.ID)
			if err != nil {
				return KeyUsageForDateResult{}, err
			}
		}
		if snapshot != nil && snapshot.BusinessDate == date {
			item.Error = snapshot.FailureReason
			item.StartedAt = snapshot.StartedAt
			item.SyncedRawCost = snapshot.SyncedRawCost
			item.CollectedRawCost = snapshot.CollectedRawCost
			item.ConsumeDate = snapshot.ConsumeDate
			if snapshot.Complete {
				item.Status = "ok"
				if snapshot.FailureAt != nil {
					item.Status = "retained"
				}
				item.Complete = true
				collected := snapshot.CollectedAt
				item.CollectedAt = &collected
				for _, stat := range snapshot.Items {
					group := strings.TrimSpace(stat.GroupName)
					if group == "" {
						group = "Ungrouped"
					}
					ids := append([]string(nil), stat.KeyIDs...)
					if len(ids) == 0 {
						ids = []string{stat.KeyID}
					}
					item.Items = append(item.Items, KeyUsageTodayItem{SiteID: site.ID, SiteName: site.Name, Platform: site.Platform, KeyID: stat.KeyID, KeyIDs: ids, Merged: stat.Merged, KeyName: stat.KeyName, GroupName: group, RawAmount: stat.TodayAmount, TodayAmount: stat.TodayAmount * site.RechargeRate, RechargeRate: site.RechargeRate})
				}
				result.CompletedSites++
			}
		}
		result.Sites = append(result.Sites, item)
	}
	result.ExpectedSites = len(result.Sites)
	return result, nil
}

// Synchronize the same sites as the upstream page, then wait only for post-refresh attempts.
func (s *Service) SyncAndCollectKeyUsage(ctx context.Context, userID, adminAccountID, date string, startedAt time.Time) (KeyUsageForDateResult, error) {
	before, err := s.CachedKeyUsageForDate(ctx, userID, adminAccountID, date)
	if err != nil {
		return KeyUsageForDateResult{}, err
	}
	ids := make([]string, 0, len(before.Sites))
	for _, site := range before.Sites {
		ids = append(ids, site.SiteID)
	}
	syncResults := s.SyncSites(ctx, userID, adminAccountID, ids, true)
	success := make(map[string]bool)
	for _, result := range syncResults {
		success[result.SiteID] = result.Status == "success"
	}
	waitIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if success[id] && s.keyUsageStore() != nil {
			waitIDs = append(waitIDs, id)
		}
	}
	var waitGroup sync.WaitGroup
	var waitMu sync.Mutex
	var waitErr error
	for _, id := range waitIDs {
		waitGroup.Add(1)
		go func(id string) {
			defer waitGroup.Done()
			waitCtx, cancel := context.WithTimeout(ctx, keyUsageCollectionTimeout)
			defer cancel()
			for {
				s.mu.Lock()
				if s.keyUsageChanged == nil {
					s.keyUsageChanged = make(chan struct{})
				}
				changed := s.keyUsageChanged
				finished := s.keyUsageFinished[id]
				s.mu.Unlock()
				snapshot, readErr := s.keyUsageStore().GetKeyUsageSnapshot(waitCtx, id)
				if readErr != nil {
					waitMu.Lock()
					if waitErr == nil {
						waitErr = readErr
					}
					success[id] = false
					waitMu.Unlock()
					return
				}
				if finished.After(startedAt) || snapshot != nil && snapshot.AttemptStartedAt.After(startedAt) {
					return
				}
				select {
				case <-waitCtx.Done():
					waitMu.Lock()
					success[id] = false
					if ctx.Err() != nil && waitErr == nil {
						waitErr = ctx.Err()
					}
					waitMu.Unlock()
					return
				case <-changed:
				}
			}
		}(id)
	}
	waitGroup.Wait()
	if waitErr != nil {
		return KeyUsageForDateResult{}, waitErr
	}

	result, err := s.CachedKeyUsageForDate(ctx, userID, adminAccountID, date)
	if err != nil {
		return result, err
	}
	result.CompletedSites = 0
	for index := range result.Sites {
		site := &result.Sites[index]
		if !success[site.SiteID] || !site.StartedAt.After(startedAt) {
			site.Complete = false
			site.Status = "missing"
			site.Items = []KeyUsageTodayItem{}
		} else if site.Complete {
			result.CompletedSites++
		}
	}
	return result, nil
}
