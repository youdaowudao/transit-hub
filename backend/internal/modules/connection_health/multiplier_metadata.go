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
	multiplierDirectUnknown      = "unknown"
	multiplierDirectSupported    = "supported"
	multiplierDirectUnsupported  = "unsupported"
	multiplierRefreshQueueSize   = 64
)

var errMultiplierDirectLookupAmbiguous = errors.New("direct key lookup returned ambiguous not found")

var errMultiplierDirectLookupInvalid = errors.New("direct key lookup returned invalid metadata")

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
	site             multiplierSiteMetadata
	siteFingerprint  string
	status           string
	fetchedAt        time.Time
	expiresAt        time.Time
	nextRetryAt      time.Time
	lastAccessAt     time.Time
	lastError        string
	inFlight         bool
	done             chan struct{}
}

// multiplierSiteMetadata is deliberately narrower than upstream.Site. A snapshot must not
// retain a Session or any other credential-bearing object after an upstream read finishes.
type multiplierSiteMetadata struct {
	rechargeRate float64
	groups       []upstream.GroupInfo
}

func (s *Service) multiplierLookupForWorkspace(ctx context.Context, userID string, adminAccountID string, platform string, allowStale bool, waitForFresh bool) upstreamMultiplierLookup {
	lookup := upstreamMultiplierLookup{byAccount: make(map[string]upstreamMultiplierResolution)}
	if s.mySites == nil || s.sites == nil {
		lookup.unavailable = true
		return lookup
	}
	metadataReader, ok := s.mySites.(UpstreamKeyMetadataReader)
	if !ok {
		// Test doubles and older injected readers keep the old bounded behavior.
		return s.legacyMultiplierLookup(ctx, userID, adminAccountID, platform)
	}
	connections, err := s.mySites.ListRealConnectionsForWorkspace(ctx, userID, adminAccountID)
	if err != nil {
		lookup.unavailable = true
		return lookup
	}
	connectionsByAccount := make(map[string][]my_sites.RealConnection)
	bindingKeys := make(map[string]map[string]struct{})
	for _, connection := range connections {
		accountID := strings.TrimSpace(connection.AdminAccountID)
		if accountID == "" {
			continue
		}
		if connectionPlatform := strings.TrimSpace(connection.AdminPlatform); connectionPlatform != "" && !strings.EqualFold(connectionPlatform, platform) {
			continue
		}
		connectionsByAccount[accountID] = append(connectionsByAccount[accountID], connection)
		siteID, keyID := strings.TrimSpace(connection.UpstreamSiteID), strings.TrimSpace(connection.UpstreamKeyID)
		if siteID == "" || keyID == "" {
			continue
		}
		if bindingKeys[siteID] == nil {
			bindingKeys[siteID] = make(map[string]struct{})
		}
		bindingKeys[siteID][keyID] = struct{}{}
	}

	s.prepareMultiplierSnapshots(ctx, metadataReader, userID, adminAccountID, upstream.Platform(platform), bindingKeys, waitForFresh)
	s.multiplierSnapshotMu.Lock()
	defer s.multiplierSnapshotMu.Unlock()
	for accountID, accountConnections := range connectionsByAccount {
		status := MultiplierResolutionResolved
		var resolved upstreamKeyGroupInfo
		for index, connection := range accountConnections {
			candidate := s.resolveMultiplierSnapshotLocked(connection, userID, adminAccountID, allowStale)
			if candidate.status == MultiplierResolutionUnavailable || candidate.status == multiplierResolutionUpdating {
				status = MultiplierResolutionUnavailable
				break
			}
			if candidate.status == multiplierResolutionStale && status == MultiplierResolutionResolved {
				status = multiplierResolutionStale
			}
			if candidate.status != MultiplierResolutionResolved && candidate.status != multiplierResolutionStale {
				if len(accountConnections) > 1 {
					status = MultiplierResolutionConflict
				} else {
					status = candidate.status
				}
				break
			}
			if index > 0 && !sameUpstreamKeyGroup(resolved, candidate.info) {
				status = MultiplierResolutionConflict
				break
			}
			resolved = candidate.info
		}
		lookup.byAccount[accountID] = upstreamMultiplierResolution{status: status, info: resolved}
	}
	for siteID := range bindingKeys {
		entry := s.multiplierSnapshots[multiplierSnapshotKey(userID, adminAccountID, siteID)]
		if entry == nil || entry.status == "" || entry.status == "unavailable" || entry.status == multiplierResolutionUpdating {
			lookup.unavailable = true
		}
	}
	return lookup
}

func (s *Service) prepareMultiplierSnapshots(ctx context.Context, reader UpstreamKeyMetadataReader, userID string, adminAccountID string, platform upstream.Platform, bindingKeys map[string]map[string]struct{}, waitForFresh bool) {
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
			if entry != nil {
				generation = entry.generation + 1
				if entry.inFlight && entry.done != nil {
					close(entry.done)
					entry.done = nil
					entry.inFlight = false
				}
			}
			entry = &multiplierSnapshotEntry{
				workspaceKey: cacheKey, siteID: siteID, userID: userID, adminAccountID: adminAccountID,
				platform: platform, bindingSignature: signature, keyIDs: keyIDs, generation: generation,
				capability: multiplierDirectUnknown, keys: make(map[string]upstreamKeyMetadata), siteFingerprint: fingerprint,
				status: multiplierResolutionUpdating, lastAccessAt: now,
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
		if entry.status == "complete" && entry.expiresAt.After(now) {
			continue
		}
		if now.Before(entry.nextRetryAt) {
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
			close(entry.done)
			entry.done = nil
		}
	}
	s.multiplierSnapshotMu.Unlock()

	if !waitForFresh {
		return
	}
	for _, done := range waiters {
		select {
		case <-done:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) refreshMultiplierSnapshot(parent context.Context, reader UpstreamKeyMetadataReader, captured *multiplierSnapshotEntry, target *multiplierSnapshotEntry) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.finishMultiplierSnapshot(target, captured, nil, multiplierSiteMetadata{}, errors.New("multiplier snapshot refresh panic"))
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

	keys, site, capability, err := s.fetchMultiplierSnapshot(ctx, reader, captured)
	captured.capability = capability
	s.finishMultiplierSnapshot(target, captured, keys, site, err)
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

func (s *Service) fetchMultiplierSnapshot(ctx context.Context, reader UpstreamKeyMetadataReader, entry *multiplierSnapshotEntry) (map[string]upstreamKeyMetadata, multiplierSiteMetadata, string, error) {
	site, err := s.sites.GetSite(ctx, entry.siteID)
	if err != nil || site == nil || site.Session == nil {
		if err == nil {
			err = errors.New("site session unavailable")
		}
		return nil, multiplierSiteMetadata{}, entry.capability, err
	}
	siteMetadata := newMultiplierSiteMetadata(site)
	if site.Platform == upstream.PlatformSub2API {
		if entry.capability != multiplierDirectUnsupported {
			keys := make(map[string]upstreamKeyMetadata, len(entry.keyIDs))
			for _, keyID := range entry.keyIDs {
				item, getErr := reader.GetUpstreamKeyForWorkspace(ctx, entry.userID, entry.adminAccountID, entry.siteID, keyID)
				if getErr != nil {
					var requestErr *upstream.RequestError
					if errors.As(getErr, &requestErr) && requestErr.StatusCode == http.StatusNotFound {
						return nil, siteMetadata, multiplierDirectUnknown, errMultiplierDirectLookupAmbiguous
					}
					if errors.As(getErr, &requestErr) && requestErr.MessageKey == upstream.ErrorInvalidResponse && requestErr.StatusCode == 0 {
						return nil, siteMetadata, entry.capability, errMultiplierDirectLookupInvalid
					}
					return nil, siteMetadata, entry.capability, getErr
				}
				if strings.TrimSpace(item.ID) != keyID {
					return nil, siteMetadata, entry.capability, errMultiplierDirectLookupInvalid
				}
				keys[keyID] = upstreamKeyMetadata{id: item.ID, groupID: strings.TrimSpace(item.GroupID), groupName: strings.TrimSpace(item.GroupName)}
			}
			return keys, siteMetadata, multiplierDirectSupported, nil
		}
	}
	var items []upstream.Sub2APIKeyItem
	if selective, ok := reader.(UpstreamKeyMetadataSelectiveReader); ok {
		items, err = selective.ListUpstreamKeysForWorkspaceUntil(ctx, entry.userID, entry.adminAccountID, entry.siteID, entry.keyIDs)
	} else {
		items, err = reader.ListUpstreamKeysForWorkspace(ctx, entry.userID, entry.adminAccountID, entry.siteID)
	}
	if err != nil {
		return nil, siteMetadata, entry.capability, err
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
	if site.Platform == upstream.PlatformSub2API && capability == multiplierDirectUnknown {
		capability = multiplierDirectUnsupported
	}
	return keys, siteMetadata, capability, nil
}

func (s *Service) finishMultiplierSnapshot(target *multiplierSnapshotEntry, captured *multiplierSnapshotEntry, keys map[string]upstreamKeyMetadata, site multiplierSiteMetadata, err error) {
	s.multiplierSnapshotMu.Lock()
	defer s.multiplierSnapshotMu.Unlock()
	current := s.multiplierSnapshots[target.workspaceKey]
	if current != target || current.generation != captured.generation || current.bindingSignature != captured.bindingSignature {
		return
	}
	if err == nil {
		current.keys = keys
		current.site = site
		current.siteFingerprint = captured.siteFingerprint
		current.status = "complete"
		current.fetchedAt = time.Now()
		current.expiresAt = current.fetchedAt.Add(multiplierSnapshotTTL)
		current.nextRetryAt = time.Time{}
		current.lastError = ""
		current.capability = captured.capability
	} else {
		if len(current.keys) == 0 {
			current.status = "unavailable"
		} else {
			current.status = multiplierResolutionStale
		}
		current.nextRetryAt = time.Now().Add(multiplierFailureBackoff)
		current.lastError = err.Error()
		if errors.Is(err, errMultiplierDirectLookupAmbiguous) {
			current.capability = multiplierDirectUnsupported
		}
	}
	current.inFlight = false
	if current.done != nil {
		close(current.done)
		current.done = nil
	}
}

func (s *Service) resolveMultiplierSnapshotLocked(connection my_sites.RealConnection, userID string, adminAccountID string, allowStale bool) upstreamMultiplierResolution {
	siteID, keyID := strings.TrimSpace(connection.UpstreamSiteID), strings.TrimSpace(connection.UpstreamKeyID)
	if siteID == "" || keyID == "" {
		return upstreamMultiplierResolution{status: MultiplierResolutionMissing}
	}
	entry := s.multiplierSnapshots[multiplierSnapshotKey(userID, adminAccountID, siteID)]
	if entry == nil || entry.status == "unavailable" || entry.status == multiplierResolutionUpdating {
		return upstreamMultiplierResolution{status: MultiplierResolutionUnavailable}
	}
	if entry.status == multiplierResolutionStale && !allowStale {
		return upstreamMultiplierResolution{status: MultiplierResolutionUnavailable}
	}
	key, ok := entry.keys[keyID]
	if !ok {
		if entry.status == "complete" || entry.status == multiplierResolutionStale {
			return upstreamMultiplierResolution{status: MultiplierResolutionMissing}
		}
		return upstreamMultiplierResolution{status: MultiplierResolutionUnavailable}
	}
	if len(entry.site.groups) == 0 {
		return upstreamMultiplierResolution{status: MultiplierResolutionUnavailable}
	}
	matched := findSiteGroup(entry.site.groups, key)
	if matched == nil {
		return upstreamMultiplierResolution{status: MultiplierResolutionMissing}
	}
	info := newUpstreamKeyGroupInfo(siteID, keyID, *matched, entry.site.rechargeRate)
	if info.multiplier == nil || info.effectiveMultiplier == nil {
		return upstreamMultiplierResolution{status: MultiplierResolutionMissing, info: info}
	}
	status := MultiplierResolutionResolved
	if entry.status == multiplierResolutionStale {
		status = multiplierResolutionStale
	}
	return upstreamMultiplierResolution{status: status, info: info}
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
	if site.LastSyncedAt != nil {
		parts = append(parts, strconv.FormatInt(*site.LastSyncedAt, 10))
	}
	if session := site.Session; session != nil {
		parts = append(parts, string(session.Platform), strings.TrimSpace(session.BaseURL), strings.TrimSpace(session.UserID), strings.TrimSpace(session.TokenType))
		if session.ExpiresAt != nil {
			parts = append(parts, strconv.FormatInt(*session.ExpiresAt, 10))
		}
		// The Redis cache deserializes a new Session pointer on every read, so pointer
		// identity must never take part in this version. LastSyncedAt changes when a
		// site reconnects or refreshes credentials; these booleans cover credential
		// shape changes without retaining or hashing credential values.
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
