package connection_health

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/upstream"
)

const (
	multiplierSnapshotTTL        = 60 * time.Second
	multiplierFailureBackoff     = 30 * time.Second
	multiplierRefreshTimeout     = 90 * time.Second
	multiplierSnapshotRetention  = 10 * time.Minute
	multiplierResolutionStale    = "stale"
	multiplierResolutionUpdating = "updating"
	multiplierResolutionPartial  = "partial"
	multiplierDirectUnknown      = "unknown"
	multiplierDirectSupported    = "supported"
	multiplierDirectUnsupported  = "unsupported"
	multiplierRefreshQueueSize   = 64
)

var errMultiplierDirectLookupAmbiguous = errors.New("direct key lookup returned ambiguous not found")

type multiplierRefreshJob struct {
	service  *Service
	parent   context.Context
	reader   UpstreamKeyMetadataReader
	captured *multiplierSnapshotEntry
	target   *multiplierSnapshotEntry
}

// multiplierRefreshDispatcher bounds the whole process, rather than only one HTTP
// request, to four queued/active upstream metadata refresh workers. Workers exit when
// idle so short-lived test services and inactive deployments retain no worker pool.
var multiplierRefreshDispatcher = struct {
	mu      sync.Mutex
	queue   chan multiplierRefreshJob
	workers int
}{queue: make(chan multiplierRefreshJob, multiplierRefreshQueueSize)}

func enqueueMultiplierRefresh(job multiplierRefreshJob) bool {
	select {
	case multiplierRefreshDispatcher.queue <- job:
	default:
		return false
	}
	startMultiplierRefreshWorkers()
	return true
}

func startMultiplierRefreshWorkers() {
	multiplierRefreshDispatcher.mu.Lock()
	defer multiplierRefreshDispatcher.mu.Unlock()
	for multiplierRefreshDispatcher.workers < upstreamMetadataFetchConcurrency && len(multiplierRefreshDispatcher.queue) > 0 {
		multiplierRefreshDispatcher.workers++
		go runMultiplierRefreshWorker()
	}
}

func runMultiplierRefreshWorker() {
	for {
		select {
		case job := <-multiplierRefreshDispatcher.queue:
			job.service.refreshMultiplierSnapshot(job.parent, job.reader, job.captured, job.target)
		default:
			multiplierRefreshDispatcher.mu.Lock()
			if len(multiplierRefreshDispatcher.queue) == 0 {
				multiplierRefreshDispatcher.workers--
				multiplierRefreshDispatcher.mu.Unlock()
				return
			}
			multiplierRefreshDispatcher.mu.Unlock()
		}
	}
}

type multiplierSnapshotEntry struct {
	workspaceKey     string
	siteID           string
	userID           string
	adminAccountID   string
	platform         upstream.Platform
	bindingSignature string
	keyIDs           []string
	generation       uint64
	capability       string
	keys             map[string]upstreamKeyMetadata
	keyFailures      map[string]string
	site             multiplierSiteMetadata
	siteFingerprint  string
	status           string
	fetchedAt        time.Time
	expiresAt        time.Time
	nextRetryAt      time.Time
	lastAccessAt     time.Time
	lastError        string
	lastOutcome      string
	inFlight         bool
	done             chan struct{}
	supersededDone   []chan struct{}
}

// multiplierSiteMetadata is deliberately narrower than upstream.Site. A snapshot must not
// retain a Session or any other credential-bearing object after an upstream read finishes.
type multiplierSiteMetadata struct {
	rechargeRate float64
	groups       []upstream.GroupInfo
}

func (s *Service) multiplierLookupForWorkspace(ctx context.Context, userID string, adminAccountID string, platform string, allowStale bool, waitForFresh bool) upstreamMultiplierLookup {
	return s.multiplierLookupForWorkspaceWithOptions(ctx, userID, adminAccountID, platform, allowStale, waitForFresh, false)
}

func (s *Service) multiplierLookupForWorkspaceWithOptions(ctx context.Context, userID string, adminAccountID string, platform string, allowStale bool, waitForFresh bool, forceRefresh bool) upstreamMultiplierLookup {
	if s.mySites == nil || s.sites == nil {
		return upstreamMultiplierLookup{byAccount: make(map[string]upstreamMultiplierResolution), unavailable: true}
	}
	_, ok := s.mySites.(UpstreamKeyMetadataReader)
	if !ok {
		// Test doubles and older injected readers keep the old bounded behavior.
		return s.legacyMultiplierLookup(ctx, userID, adminAccountID, platform)
	}
	connections, err := s.mySites.ListRealConnectionsForWorkspace(ctx, userID, adminAccountID)
	return s.multiplierLookupForWorkspaceWithConnections(ctx, userID, adminAccountID, platform, connections, err == nil, allowStale, waitForFresh, forceRefresh)
}

func (s *Service) multiplierLookupForWorkspaceWithConnections(ctx context.Context, userID string, adminAccountID string, platform string, connections []my_sites.RealConnection, connectionsReady bool, allowStale bool, waitForFresh bool, forceRefresh bool) upstreamMultiplierLookup {
	lookup := upstreamMultiplierLookup{byAccount: make(map[string]upstreamMultiplierResolution)}
	if s.mySites == nil || s.sites == nil {
		lookup.unavailable = true
		return lookup
	}
	metadataReader, ok := s.mySites.(UpstreamKeyMetadataReader)
	if !ok {
		return s.legacyMultiplierLookup(ctx, userID, adminAccountID, platform)
	}
	if !connectionsReady {
		lookup.unavailable = true
		return lookup
	}
	connectionsByAccount := make(map[string][]my_sites.RealConnection)
	bindingKeys := make(map[string]map[string]struct{})
	disabledSiteByAccount := make(map[string]string)
	siteEnabled := make(map[string]*bool)
	for _, connection := range connections {
		accountID := strings.TrimSpace(connection.AdminAccountID)
		if accountID == "" {
			continue
		}
		if connectionPlatform := strings.TrimSpace(connection.AdminPlatform); connectionPlatform != "" && !strings.EqualFold(connectionPlatform, platform) {
			continue
		}
		siteID, keyID := strings.TrimSpace(connection.UpstreamSiteID), strings.TrimSpace(connection.UpstreamKeyID)
		if siteID != "" {
			enabled, checked := siteEnabled[siteID]
			if !checked {
				if site, siteErr := s.sites.GetSite(ctx, siteID); siteErr == nil && site != nil {
					value := site.IsEnabled()
					enabled = &value
				}
				siteEnabled[siteID] = enabled
			}
			if enabled != nil && !*enabled {
				disabledSiteByAccount[accountID] = siteID
				continue
			}
		}
		connectionsByAccount[accountID] = append(connectionsByAccount[accountID], connection)
		if siteID == "" || keyID == "" {
			continue
		}
		if bindingKeys[siteID] == nil {
			bindingKeys[siteID] = make(map[string]struct{})
		}
		bindingKeys[siteID][keyID] = struct{}{}
	}

	freshReady := s.prepareMultiplierSnapshots(ctx, metadataReader, userID, adminAccountID, upstream.Platform(platform), bindingKeys, waitForFresh, forceRefresh)
	if waitForFresh && !freshReady {
		lookup.unavailable = true
		return lookup
	}
	s.multiplierSnapshotMu.Lock()
	defer s.multiplierSnapshotMu.Unlock()
	for accountID, accountConnections := range connectionsByAccount {
		status := MultiplierResolutionResolved
		reason := ""
		var resolved upstreamKeyGroupInfo
		for index, connection := range accountConnections {
			candidate := s.resolveMultiplierSnapshotLocked(connection, userID, adminAccountID, allowStale)
			if candidate.status == MultiplierResolutionUnavailable || candidate.status == multiplierResolutionUpdating {
				status = MultiplierResolutionUnavailable
				reason = candidate.reason
				resolved = candidate.info
				break
			}
			if candidate.status == multiplierResolutionStale && status == MultiplierResolutionResolved {
				status = multiplierResolutionStale
				reason = candidate.reason
			}
			if candidate.status != MultiplierResolutionResolved && candidate.status != multiplierResolutionStale {
				if len(accountConnections) > 1 {
					status = MultiplierResolutionConflict
					reason = ""
				} else {
					status = candidate.status
					reason = candidate.reason
				}
				resolved = candidate.info
				break
			}
			if index > 0 && !sameUpstreamKeyGroup(resolved, candidate.info) {
				status = MultiplierResolutionConflict
				break
			}
			resolved = candidate.info
		}
		lookup.byAccount[accountID] = upstreamMultiplierResolution{status: status, reason: reason, info: resolved}
	}
	for accountID, siteID := range disabledSiteByAccount {
		if _, active := connectionsByAccount[accountID]; active {
			continue
		}
		lookup.byAccount[accountID] = upstreamMultiplierResolution{
			status: MultiplierResolutionDisabled,
			info:   upstreamKeyGroupInfo{siteID: siteID},
		}
	}
	for siteID := range bindingKeys {
		entry := s.multiplierSnapshots[multiplierSnapshotKey(userID, adminAccountID, siteID)]
		if entry == nil || entry.status == "" || entry.status == "unavailable" || entry.status == multiplierResolutionUpdating {
			lookup.unavailable = true
		}
	}
	return lookup
}

func (s *Service) prepareMultiplierSnapshots(ctx context.Context, reader UpstreamKeyMetadataReader, userID string, adminAccountID string, platform upstream.Platform, bindingKeys map[string]map[string]struct{}, waitForFresh bool, forceRefresh bool) bool {
	orderedSites := make([]string, 0, len(bindingKeys))
	for siteID := range bindingKeys {
		orderedSites = append(orderedSites, siteID)
	}
	sort.Strings(orderedSites)
	now := time.Now()
	waiters := make([]chan struct{}, 0, len(orderedSites))
	// This is a local cache read, not an upstream request. It invalidates a fresh snapshot
	// immediately when a site, its group metadata, or its session identity changes.
	currentFingerprints := make(map[string]string, len(orderedSites))
	for _, siteID := range orderedSites {
		if site, err := s.sites.GetSite(ctx, siteID); err == nil && site != nil {
			currentFingerprints[siteID] = multiplierSiteFingerprint(site)
		}
	}

	s.multiplierSnapshotMu.Lock()
	if s.multiplierSnapshots == nil {
		s.multiplierSnapshots = make(map[string]*multiplierSnapshotEntry)
	}
	prefix := userID + "\x00" + adminAccountID + "\x00"
	for key, entry := range s.multiplierSnapshots {
		if !entry.inFlight && !entry.lastAccessAt.IsZero() && now.Sub(entry.lastAccessAt) > multiplierSnapshotRetention {
			delete(s.multiplierSnapshots, key)
			continue
		}
		if strings.HasPrefix(key, prefix) {
			if _, needed := bindingKeys[entry.siteID]; !needed {
				delete(s.multiplierSnapshots, key)
			}
		}
	}
	for _, siteID := range orderedSites {
		keyIDs := sortedKeys(bindingKeys[siteID])
		signature := strings.Join(keyIDs, "\x00")
		cacheKey := multiplierSnapshotKey(userID, adminAccountID, siteID)
		entry := s.multiplierSnapshots[cacheKey]
		fingerprint := currentFingerprints[siteID]
		if entry == nil || entry.bindingSignature != signature || entry.platform != platform || (fingerprint != "" && entry.siteFingerprint != "" && entry.siteFingerprint != fingerprint) {
			generation := uint64(1)
			var supersededDone []chan struct{}
			if entry != nil {
				generation = entry.generation + 1
				supersededDone = append(supersededDone, entry.supersededDone...)
				if entry.inFlight && entry.done != nil {
					supersededDone = append(supersededDone, entry.done)
				}
			}
			entry = &multiplierSnapshotEntry{
				workspaceKey: cacheKey, siteID: siteID, userID: userID, adminAccountID: adminAccountID,
				platform: platform, bindingSignature: signature, keyIDs: keyIDs, generation: generation,
				capability: multiplierDirectUnknown, keys: make(map[string]upstreamKeyMetadata), siteFingerprint: fingerprint,
				status: multiplierResolutionUpdating, lastAccessAt: now, supersededDone: supersededDone,
			}
			s.multiplierSnapshots[cacheKey] = entry
		}
		if fingerprint != "" {
			entry.siteFingerprint = fingerprint
		}
		entry.lastAccessAt = now
		if entry.inFlight {
			if waitForFresh {
				waiters = append(waiters, entry.done)
			}
			continue
		}
		if !forceRefresh && entry.status == "complete" && entry.expiresAt.After(now) {
			continue
		}
		if !forceRefresh && now.Before(entry.nextRetryAt) {
			continue
		}
		entry.inFlight = true
		entry.done = make(chan struct{})
		if len(entry.keys) == 0 {
			entry.status = multiplierResolutionUpdating
		} else {
			entry.status = multiplierResolutionStale
		}
		if waitForFresh {
			waiters = append(waiters, entry.done)
		}
		captured := *entry
		captured.keyIDs = append([]string(nil), entry.keyIDs...)
		captured.keys = nil
		refreshContext := ctx
		if !waitForFresh {
			refreshContext = context.Background()
		}
		if !enqueueMultiplierRefresh(multiplierRefreshJob{
			service: s, parent: refreshContext, reader: reader, captured: &captured, target: entry,
		}) {
			entry.inFlight = false
			if len(entry.keys) == 0 {
				entry.status = "unavailable"
			} else {
				entry.status = multiplierResolutionStale
			}
			entry.nextRetryAt = now.Add(multiplierFailureBackoff)
			entry.lastError = "multiplier metadata refresh queue is full"
			entry.lastOutcome = "unavailable"
			closeMultiplierSnapshotWaiters(entry)
		}
	}
	s.multiplierSnapshotMu.Unlock()

	if !waitForFresh {
		return true
	}
	for _, done := range waiters {
		select {
		case <-done:
		case <-ctx.Done():
			return false
		}
	}
	return true
}

func (s *Service) refreshMultiplierSnapshot(parent context.Context, reader UpstreamKeyMetadataReader, captured *multiplierSnapshotEntry, target *multiplierSnapshotEntry) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.finishMultiplierSnapshot(target, captured, nil, nil, multiplierSiteMetadata{}, errors.New("multiplier snapshot refresh panic"))
			log.Printf("[connection-health] multiplier snapshot refresh panic recovered site_id=%s", captured.siteID)
		}
	}()
	if parent == nil {
		parent = context.Background()
	}
	if !s.multiplierSnapshotRefreshCurrent(target, captured) {
		return
	}
	ctx, cancel := context.WithTimeout(parent, multiplierRefreshTimeout)
	defer cancel()

	keys, keyFailures, site, capability, err := s.fetchMultiplierSnapshot(ctx, reader, captured)
	captured.capability = capability
	s.finishMultiplierSnapshot(target, captured, keys, keyFailures, site, err)
	if err != nil {
		log.Printf("[connection-health] multiplier snapshot refresh failed site_id=%s err=%v", captured.siteID, err)
	}
}

func (s *Service) multiplierSnapshotRefreshCurrent(target *multiplierSnapshotEntry, captured *multiplierSnapshotEntry) bool {
	s.multiplierSnapshotMu.Lock()
	defer s.multiplierSnapshotMu.Unlock()
	current := s.multiplierSnapshots[target.workspaceKey]
	return current == target && current.generation == captured.generation && current.bindingSignature == captured.bindingSignature
}

func (s *Service) fetchMultiplierSnapshot(ctx context.Context, reader UpstreamKeyMetadataReader, entry *multiplierSnapshotEntry) (map[string]upstreamKeyMetadata, map[string]string, multiplierSiteMetadata, string, error) {
	site, err := s.sites.GetSite(ctx, entry.siteID)
	if err != nil || site == nil || site.Session == nil {
		if err == nil {
			err = errors.New("site session unavailable")
		}
		return nil, nil, multiplierSiteMetadata{}, entry.capability, err
	}
	siteMetadata := newMultiplierSiteMetadata(site)
	if site.Platform == upstream.PlatformSub2API {
		if entry.capability != multiplierDirectUnsupported {
			keys := make(map[string]upstreamKeyMetadata, len(entry.keyIDs))
			keyFailures := make(map[string]string)
			fallbackKeyIDs := make([]string, 0)
			for _, keyID := range entry.keyIDs {
				item, getErr := reader.GetUpstreamKeyForWorkspace(ctx, entry.userID, entry.adminAccountID, entry.siteID, keyID)
				if getErr != nil {
					var requestErr *upstream.RequestError
					if errors.As(getErr, &requestErr) && requestErr.StatusCode == http.StatusNotFound {
						fallbackKeyIDs = append(fallbackKeyIDs, keyID)
						continue
					}
					keyFailures[keyID] = multiplierRefreshOutcome(getErr)
					continue
				}
				if strings.TrimSpace(item.ID) != keyID {
					keyFailures[keyID] = "invalid"
					continue
				}
				keys[keyID] = upstreamKeyMetadata{id: item.ID, groupID: strings.TrimSpace(item.GroupID), groupName: strings.TrimSpace(item.GroupName)}
			}
			if len(fallbackKeyIDs) == 0 {
				return keys, keyFailures, siteMetadata, multiplierDirectSupported, nil
			}
			items, listErr := listMultiplierKeys(ctx, reader, entry, fallbackKeyIDs)
			if listErr != nil {
				for _, keyID := range fallbackKeyIDs {
					keyFailures[keyID] = multiplierRefreshOutcome(listErr)
				}
				return keys, keyFailures, siteMetadata, entry.capability, nil
			}
			fallbackSet := stringSet(fallbackKeyIDs)
			for _, item := range items {
				id := strings.TrimSpace(item.ID)
				if _, needed := fallbackSet[id]; id != "" && needed {
					keys[id] = upstreamKeyMetadata{id: id, groupID: strings.TrimSpace(item.GroupID), groupName: strings.TrimSpace(item.GroupName)}
				}
			}
			capability := entry.capability
			if len(keys) == len(fallbackKeyIDs) {
				capability = multiplierDirectUnsupported
			}
			return keys, keyFailures, siteMetadata, capability, nil
		}
	}
	items, err := listMultiplierKeys(ctx, reader, entry, entry.keyIDs)
	if err != nil {
		return nil, nil, siteMetadata, entry.capability, err
	}
	keys := make(map[string]upstreamKeyMetadata, len(items))
	neededIDs := stringSet(entry.keyIDs)
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, needed := neededIDs[id]; needed {
			keys[id] = upstreamKeyMetadata{id: id, groupID: strings.TrimSpace(item.GroupID), groupName: strings.TrimSpace(item.GroupName)}
		}
	}
	capability := entry.capability
	if site.Platform == upstream.PlatformSub2API && capability != multiplierDirectUnsupported {
		capability = multiplierDirectUnsupported
	}
	return keys, nil, siteMetadata, capability, nil
}

func listMultiplierKeys(ctx context.Context, reader UpstreamKeyMetadataReader, entry *multiplierSnapshotEntry, keyIDs []string) ([]upstream.Sub2APIKeyItem, error) {
	if selective, ok := reader.(UpstreamKeyMetadataSelectiveReader); ok {
		return selective.ListUpstreamKeysForWorkspaceUntil(ctx, entry.userID, entry.adminAccountID, entry.siteID, keyIDs)
	}
	return reader.ListUpstreamKeysForWorkspace(ctx, entry.userID, entry.adminAccountID, entry.siteID)
}

func (s *Service) finishMultiplierSnapshot(target *multiplierSnapshotEntry, captured *multiplierSnapshotEntry, keys map[string]upstreamKeyMetadata, keyFailures map[string]string, site multiplierSiteMetadata, err error) {
	s.multiplierSnapshotMu.Lock()
	defer s.multiplierSnapshotMu.Unlock()
	current := s.multiplierSnapshots[target.workspaceKey]
	if current != target || current.generation != captured.generation || current.bindingSignature != captured.bindingSignature {
		return
	}
	if err == nil {
		now := time.Now()
		if len(keyFailures) > 0 && len(keys) == 0 && len(current.keys) > 0 {
			current.status = multiplierResolutionStale
			current.nextRetryAt = now.Add(multiplierFailureBackoff)
			current.lastError = "key metadata unavailable"
			current.lastOutcome = "unavailable"
		} else {
			current.keys = keys
			current.keyFailures = keyFailures
			current.site = site
			current.siteFingerprint = captured.siteFingerprint
			current.status = "complete"
			if len(keyFailures) > 0 {
				current.status = multiplierResolutionPartial
				if len(keys) == 0 {
					current.status = "unavailable"
				}
			}
			current.fetchedAt = now
			current.expiresAt = current.fetchedAt.Add(multiplierSnapshotTTL)
			current.nextRetryAt = time.Time{}
			if len(keyFailures) > 0 {
				current.nextRetryAt = current.fetchedAt.Add(multiplierFailureBackoff)
			}
			current.lastError = ""
			current.lastOutcome = "success"
			if len(keyFailures) > 0 {
				current.lastOutcome = "unavailable"
			}
			current.capability = captured.capability
		}
	} else {
		if len(current.keys) == 0 {
			current.status = "unavailable"
		} else {
			current.status = multiplierResolutionStale
		}
		current.nextRetryAt = time.Now().Add(multiplierFailureBackoff)
		current.lastError = err.Error()
		current.lastOutcome = multiplierRefreshOutcome(err)
		if errors.Is(err, errMultiplierDirectLookupAmbiguous) {
			current.capability = multiplierDirectUnsupported
		}
	}
	current.inFlight = false
	closeMultiplierSnapshotWaiters(current)
}

func closeMultiplierSnapshotWaiters(entry *multiplierSnapshotEntry) {
	if entry.done != nil {
		close(entry.done)
		entry.done = nil
	}
	for _, done := range entry.supersededDone {
		close(done)
	}
	entry.supersededDone = nil
}

func multiplierRefreshOutcome(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var requestErr *upstream.RequestError
	if errors.As(err, &requestErr) && (requestErr.StatusCode == http.StatusUnauthorized || requestErr.StatusCode == http.StatusForbidden || requestErr.MessageKey == upstream.ErrorAuth) {
		return "auth_failed"
	}
	return "unavailable"
}

func (s *Service) multiplierRefreshSummary(userID string, adminAccountID string) AdminGroupsRefreshSummary {
	prefix := userID + "\x00" + adminAccountID + "\x00"
	s.multiplierSnapshotMu.Lock()
	sites := make([]AdminGroupsRefreshSite, 0)
	anySuccess := false
	anyFailure := false
	anyTimeout := false
	for key, entry := range s.multiplierSnapshots {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		status := entry.lastOutcome
		errorKey := ""
		switch {
		case entry.status == "complete":
			status = "success"
			anySuccess = true
		case entry.status == multiplierResolutionPartial:
			status = "partial"
			anySuccess = true
			anyFailure = true
			errorKey = "unavailable"
		case entry.status == multiplierResolutionStale:
			status = "stale"
			anyFailure = true
			errorKey = multiplierOutcomeErrorKey(entry.lastOutcome)
			if entry.lastOutcome == "timeout" {
				anyTimeout = true
			}
		case entry.lastOutcome == "timeout":
			status = "timeout"
			anyFailure = true
			anyTimeout = true
			errorKey = "timeout"
		case entry.lastOutcome == "auth_failed":
			status = "auth_failed"
			anyFailure = true
			errorKey = "auth"
		default:
			status = "unavailable"
			anyFailure = true
			errorKey = multiplierOutcomeErrorKey(entry.lastOutcome)
		}
		sites = append(sites, AdminGroupsRefreshSite{SiteID: entry.siteID, Status: status, ErrorKey: errorKey})
	}
	s.multiplierSnapshotMu.Unlock()
	sort.Slice(sites, func(i, j int) bool { return sites[i].SiteID < sites[j].SiteID })
	state := "success"
	switch {
	case len(sites) == 0:
		state = "success"
	case anyTimeout:
		state = "timeout"
	case anyFailure && anySuccess:
		state = "partial"
	case anyFailure:
		state = "failure"
	}
	return AdminGroupsRefreshSummary{State: state, Sites: sites}
}

func multiplierOutcomeErrorKey(outcome string) string {
	switch outcome {
	case "timeout":
		return "timeout"
	case "auth_failed":
		return "auth"
	case "unavailable":
		return "unavailable"
	default:
		return "unavailable"
	}
}

func (s *Service) resolveMultiplierSnapshotLocked(connection my_sites.RealConnection, userID string, adminAccountID string, allowStale bool) upstreamMultiplierResolution {
	siteID, keyID := strings.TrimSpace(connection.UpstreamSiteID), strings.TrimSpace(connection.UpstreamKeyID)
	info := upstreamKeyGroupInfo{siteID: siteID, keyID: keyID}
	if siteID == "" || keyID == "" {
		return upstreamMultiplierResolution{status: MultiplierResolutionMissing, reason: MultiplierReasonBindingMissing, info: info}
	}
	entry := s.multiplierSnapshots[multiplierSnapshotKey(userID, adminAccountID, siteID)]
	if entry == nil {
		return upstreamMultiplierResolution{status: MultiplierResolutionUnavailable, reason: MultiplierReasonSiteUnavailable, info: info}
	}
	if entry.status == multiplierResolutionUpdating {
		return upstreamMultiplierResolution{status: MultiplierResolutionUnavailable, reason: MultiplierReasonSnapshotUpdating, info: info}
	}
	if _, failed := entry.keyFailures[keyID]; failed {
		return upstreamMultiplierResolution{status: MultiplierResolutionUnavailable, reason: MultiplierReasonKeyUnavailable, info: info}
	}
	if entry.status == "unavailable" {
		return upstreamMultiplierResolution{status: MultiplierResolutionUnavailable, reason: MultiplierReasonSiteUnavailable, info: info}
	}
	if entry.status == multiplierResolutionStale && !allowStale {
		return upstreamMultiplierResolution{status: MultiplierResolutionUnavailable, reason: MultiplierReasonSnapshotStale, info: info}
	}
	key, ok := entry.keys[keyID]
	if !ok {
		if entry.status == "complete" || entry.status == multiplierResolutionStale {
			return upstreamMultiplierResolution{status: MultiplierResolutionMissing, reason: MultiplierReasonKeyMissing, info: info}
		}
		if entry.status == multiplierResolutionPartial {
			return upstreamMultiplierResolution{status: MultiplierResolutionMissing, reason: MultiplierReasonKeyMissing, info: info}
		}
		return upstreamMultiplierResolution{status: MultiplierResolutionUnavailable, reason: MultiplierReasonSiteUnavailable, info: info}
	}
	if len(entry.site.groups) == 0 {
		return upstreamMultiplierResolution{status: MultiplierResolutionUnavailable, reason: MultiplierReasonGroupsUnavailable, info: info}
	}
	matched := findSiteGroup(entry.site.groups, key)
	if matched == nil {
		return upstreamMultiplierResolution{status: MultiplierResolutionMissing, reason: MultiplierReasonGroupNotFound, info: info}
	}
	info = newUpstreamKeyGroupInfo(siteID, keyID, *matched, entry.site.rechargeRate)
	if info.multiplier == nil || info.effectiveMultiplier == nil {
		return upstreamMultiplierResolution{status: MultiplierResolutionMissing, reason: MultiplierReasonMultiplierMissing, info: info}
	}
	status := MultiplierResolutionResolved
	reason := ""
	if entry.status == multiplierResolutionStale {
		status = multiplierResolutionStale
		reason = MultiplierReasonSnapshotStale
	}
	return upstreamMultiplierResolution{status: status, reason: reason, info: info}
}

func findSiteGroup(groups []upstream.GroupInfo, key upstreamKeyMetadata) *upstream.GroupInfo {
	var matched *upstream.GroupInfo
	if key.groupID != "" {
		for index := range groups {
			if strings.TrimSpace(groups[index].ID) == key.groupID {
				if matched != nil {
					return nil
				}
				matched = &groups[index]
			}
		}
	}
	if matched == nil && key.groupName != "" {
		for index := range groups {
			if strings.EqualFold(strings.TrimSpace(groups[index].Name), key.groupName) {
				if matched != nil {
					return nil
				}
				matched = &groups[index]
			}
		}
	}
	return matched
}

func newMultiplierSiteMetadata(site *upstream.Site) multiplierSiteMetadata {
	metadata := multiplierSiteMetadata{rechargeRate: site.RechargeRate, groups: make([]upstream.GroupInfo, 0, len(site.Metrics.Groups))}
	for _, group := range site.Metrics.Groups {
		copy := upstream.GroupInfo{ID: group.ID, Name: group.Name}
		if group.Multiplier != nil {
			multiplier := *group.Multiplier
			copy.Multiplier = &multiplier
		}
		metadata.groups = append(metadata.groups, copy)
	}
	return metadata
}

func multiplierSiteFingerprint(site *upstream.Site) string {
	if site == nil {
		return ""
	}
	parts := []string{
		strings.TrimSpace(site.ID), strings.TrimSpace(site.UserID), strings.TrimSpace(site.AdminAccountID),
		string(site.Platform), string(site.RequestedPlatform), strings.TrimSpace(site.BaseURL), string(site.Status),
		strconv.FormatBool(site.IsEnabled()), strconv.FormatFloat(site.RechargeRate, 'g', -1, 64),
	}
	if session := site.Session; session != nil {
		parts = append(parts, string(session.Platform), strings.TrimSpace(session.BaseURL), strings.TrimSpace(session.UserID), strings.TrimSpace(session.TokenType))
		if session.ExpiresAt != nil {
			parts = append(parts, strconv.FormatInt(*session.ExpiresAt, 10))
		}
		// The Redis cache deserializes a new Session pointer on every read, so pointer
		// identity and the routine LastSyncedAt timestamp must never take part in this
		// version. These booleans cover credential shape changes without retaining or
		// hashing credential values.
		parts = append(parts, strconv.FormatBool(session.Cookie != ""), strconv.FormatBool(session.AccessToken != ""), strconv.FormatBool(session.AdminAPIKey != ""), strconv.FormatBool(session.RefreshToken != ""))
	}
	for _, group := range site.Metrics.Groups {
		parts = append(parts, strings.TrimSpace(group.ID), strings.TrimSpace(group.Name))
		if group.Multiplier != nil {
			parts = append(parts, strconv.FormatFloat(*group.Multiplier, 'g', -1, 64))
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func multiplierSnapshotKey(userID string, adminAccountID string, siteID string) string {
	return userID + "\x00" + adminAccountID + "\x00" + siteID
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func (s *Service) legacyMultiplierLookup(ctx context.Context, userID string, adminAccountID string, platform string) upstreamMultiplierLookup {
	return s.upstreamMultiplierResolutionsByAdminAccountLegacy(ctx, userID, adminAccountID, platform)
}

// freshMultiplierLookupForWorkspace waits for this request's external refreshes but
// allows a retained in-process snapshot to remain visible when a site refresh fails.
// The retained value is display-only; Priority decisions continue through their existing
// safety checks and are not changed by this lookup.
func (s *Service) freshMultiplierLookupForWorkspace(ctx context.Context, userID string, adminAccountID string, platform string) upstreamMultiplierLookup {
	return s.freshMultiplierLookupForWorkspaceWithOptions(ctx, userID, adminAccountID, platform, true)
}

func (s *Service) freshMultiplierLookupForWorkspaceWithOptions(ctx context.Context, userID string, adminAccountID string, platform string, forceRefresh bool) upstreamMultiplierLookup {
	if _, ok := s.mySites.(UpstreamKeyMetadataReader); ok {
		return s.multiplierLookupForWorkspaceWithOptions(ctx, userID, adminAccountID, platform, true, true, forceRefresh)
	}
	return s.upstreamMultiplierResolutionsByAdminAccountLegacy(ctx, userID, adminAccountID, platform)
}
