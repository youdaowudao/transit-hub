package upstream

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	syncRetryDelay     = 10 * time.Second
	syncRetryLimit     = 6
	syncRetryWaiting   = "waiting"
	syncRetryRunning   = "running"
	syncRetryExhausted = "exhausted"
	syncRetryStopped   = "stopped"
)

type siteSyncRetry struct {
	SiteSyncRetry
	scope refreshWorkspaceKey
}

func siteRefreshWorkspace(site *Site) refreshWorkspaceKey {
	return refreshWorkspaceKey{userID: site.UserID, adminAccountID: site.AdminAccountID}
}

func (s *Service) syncForRefresh(ctx context.Context, id string, scope refreshWorkspaceKey) (Response, error) {
	return s.syncWithScope(ctx, id, &scope)
}

func (s *Service) syncWithScope(ctx context.Context, id string, scope *refreshWorkspaceKey) (Response, error) {
	for {
		s.mu.Lock()
		retry := s.siteSyncRetryLocked(id)
		if scope != nil && retry != nil && retry.Phase == syncRetryWaiting {
			site, err := s.readCurrentSite(ctx, id)
			progress := s.siteSyncRetryProgressLocked(site)
			s.mu.Unlock()
			if err != nil {
				return Response{}, err
			}
			if site == nil || siteRefreshWorkspace(site) != *scope || progress == nil {
				return Response{}, newRequestError(ErrorNotFound, "")
			}
			response := s.toResponse(ctx, site)
			response.SyncRetry = progress
			return response, nil
		}

		flight := s.syncFlights[id]
		if flight == nil {
			flight = s.newSyncFlightLocked(id, scope, nil)
			go s.runSyncFlight(id, flight)
		} else if scope != nil {
			if flight.invalidated {
				s.mu.Unlock()
				select {
				case <-flight.done:
					if err := ctx.Err(); err != nil {
						return Response{}, err
					}
					continue
				case <-ctx.Done():
					return Response{}, ctx.Err()
				}
			}
			if flight.refreshScope == nil {
				flight.refreshScope = scope
			}
		}
		s.mu.Unlock()
		return waitForSyncFlight(ctx, flight)
	}
}

func (s *Service) newSyncFlightLocked(id string, scope *refreshWorkspaceKey, retry *siteSyncRetry) *syncFlight {
	if current := s.siteSyncRetryLocked(id); current != nil && !current.active() {
		s.syncRetries.Delete(id)
	}
	if s.syncFlights == nil {
		s.syncFlights = make(map[string]*syncFlight)
	}
	flight := &syncFlight{done: make(chan struct{}), refreshScope: scope, retry: retry}
	s.syncFlights[id] = flight
	return flight
}

func (r *siteSyncRetry) active() bool {
	return r.Phase == syncRetryWaiting || r.Phase == syncRetryRunning
}

func (s *Service) siteSyncRetryLocked(id string) *siteSyncRetry {
	value, _ := s.syncRetries.Load(id)
	retry, _ := value.(*siteSyncRetry)
	return retry
}

func (s *Service) siteSyncRetryProgressLocked(site *Site) *SiteSyncRetry {
	if site == nil || !site.IsEnabled() {
		return nil
	}
	retry := s.siteSyncRetryLocked(site.ID)
	if retry == nil || retry.scope != siteRefreshWorkspace(site) {
		return nil
	}
	progress := retry.SiteSyncRetry
	return &progress
}

// The ordinary cache read stays outside the service lock. Only a site with
// retry state needs a second, coordinated read of the two results.
func (s *Service) readSiteWithSyncRetry(ctx context.Context, id string) (*Site, *SiteSyncRetry, error) {
	if _, tracked := s.syncRetries.Load(id); !tracked {
		site, err := s.cache.Get(ctx, id)
		if _, tracked := s.syncRetries.Load(id); !tracked || err != nil {
			return site, nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	site, err := s.cache.Get(ctx, id)
	return site, s.siteSyncRetryProgressLocked(site), err
}

func (s *Service) setSyncTimerLocked(id string, delay time.Duration, callback func()) {
	afterFunc := s.afterFunc
	if afterFunc == nil {
		afterFunc = time.AfterFunc
	}
	s.timers[id] = afterFunc(delay, callback)
}

func (s *Service) scheduleSiteSyncRetryLocked(id string, scope refreshWorkspaceKey, attempted int, detail string) {
	s.clearTimerLocked(id)
	retry := &siteSyncRetry{scope: scope, SiteSyncRetry: SiteSyncRetry{
		Phase: syncRetryWaiting, Attempted: attempted, ErrorDetail: detail,
	}}
	// A new record also identifies this timer, including when Stop raced its callback.
	s.syncRetries.Store(id, retry)
	s.setSyncTimerLocked(id, syncRetryDelay, func() { s.runScheduledSiteRetry(id, retry) })
}

func (s *Service) runScheduledSiteRetry(id string, retry *siteSyncRetry) {
	for {
		s.mu.Lock()
		if s.siteSyncRetryLocked(id) != retry || retry.Phase != syncRetryWaiting {
			s.mu.Unlock()
			return
		}
		if flight := s.syncFlights[id]; flight != nil {
			s.mu.Unlock()
			<-flight.done
			continue
		}
		site, err := s.readCurrentSite(context.Background(), id)
		if err != nil {
			retry.stop(siteSnapshotReadError(err))
			s.scheduleWorkspaceSyncLocked(id, retry.scope, false)
			s.mu.Unlock()
			return
		}
		_, deleted := s.deletedSites[id]
		if deleted || site == nil || siteRefreshWorkspace(site) != retry.scope ||
			!site.IsEnabled() || site.Session == nil || SiteReadAuthError(site) != nil {
			s.cancelSiteSyncRetryLocked(id)
			s.mu.Unlock()
			return
		}
		s.clearTimerLocked(id)
		retry.Attempted++
		retry.Phase = syncRetryRunning
		flight := s.newSyncFlightLocked(id, &retry.scope, retry)
		s.mu.Unlock()
		s.runSyncFlight(id, flight)
		return
	}
}

// Called once at publication, and again at completion for a refresh caller
// that joined an otherwise independent request after publication.
func (s *Service) updatePublishedSyncRetryLocked(flight *syncFlight) {
	site := flight.published
	if site == nil || flight.invalidated || flight.retryHandled || s.backgroundTasksDisabled || s.syncRetriesClosed {
		return
	}
	retry := s.siteSyncRetryLocked(site.ID)
	if retry != nil && retry.active() && !isSiteSyncRateLimit(flight.fetchErr) {
		if retry.Phase == syncRetryWaiting {
			s.clearTimerLocked(site.ID)
		}
		if flight.fetchErr == nil {
			s.syncRetries.Delete(site.ID)
		} else {
			retry.stop(flight.fetchErr)
		}
		flight.retryHandled = true
		return
	}
	if !isSiteSyncRateLimit(flight.fetchErr) || flight.refreshScope == nil || *flight.refreshScope != siteRefreshWorkspace(site) {
		return
	}
	flight.retryHandled = true
	detail := upstreamSyncRetryDetail(flight.fetchErr)
	if retry == nil {
		s.scheduleSiteSyncRetryLocked(site.ID, *flight.refreshScope, 1, detail)
	} else if flight.retry == retry && retry.Phase == syncRetryRunning {
		if retry.Attempted == syncRetryLimit {
			retry.Phase, retry.ErrorDetail = syncRetryExhausted, detail
		} else {
			s.scheduleSiteSyncRetryLocked(site.ID, retry.scope, retry.Attempted, detail)
		}
	}
}

func (s *Service) finishSyncRetryLocked(id string, flight *syncFlight, response *Response, err error) {
	if flight.invalidated || s.backgroundTasksDisabled || s.syncRetriesClosed {
		return
	}
	s.updatePublishedSyncRetryLocked(flight)
	retry := s.siteSyncRetryLocked(id)
	if retry != nil && retry == flight.retry && retry.Phase == syncRetryRunning {
		retry.stop(err)
		site, readErr := s.readCurrentSite(context.Background(), id)
		if readErr != nil {
			s.scheduleWorkspaceSyncLocked(id, retry.scope, false)
		} else if site != nil && siteRefreshWorkspace(site) == retry.scope {
			s.scheduleSyncLocked(id, site)
		}
	}
	if retry != nil && response.ID != "" && response.UserID == retry.scope.userID && response.AdminAccountID == retry.scope.adminAccountID {
		progress := retry.SiteSyncRetry
		response.SyncRetry = &progress
	}
}

func (s *Service) cancelSiteSyncRetryLocked(id string) {
	if flight := s.syncFlights[id]; flight != nil {
		flight.invalidated = true
	}
	if retry := s.siteSyncRetryLocked(id); retry != nil && retry.Phase == syncRetryWaiting {
		s.clearTimerLocked(id)
	}
	s.syncRetries.Delete(id)
}

func (s *Service) clearSyncRetriesLocked() {
	s.syncRetries.Range(func(id, _ any) bool {
		s.cancelSiteSyncRetryLocked(id.(string))
		return true
	})
}

func (s *Service) StopSiteSyncRetries() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncRetriesClosed = true
	s.clearSyncRetriesLocked()
}

func isSiteSyncRateLimit(err error) bool {
	var detail *RequestError
	return errors.As(err, &detail) && detail.StatusCode == http.StatusTooManyRequests && siteReadStatusErrorKey(err) == ErrorRateLimited
}

func (r *siteSyncRetry) stop(err error) {
	r.Phase = syncRetryStopped
	r.ErrorKey = siteReadStatusErrorKey(err)
	r.ErrorDetail = upstreamSyncRetryDetail(err)
}

func upstreamSyncRetryDetail(err error) string {
	var detail *RequestError
	if !errors.As(err, &detail) {
		return ""
	}
	message := strings.TrimSpace(messageURL.ReplaceAllString(detail.UpstreamMessage, "[地址已遮盖]"))
	if detail.Stage == "save" || isSiteReadAuthFailure(err) || detail.Cause != nil && detail.Stage == "metrics" && detail.StatusCode == http.StatusServiceUnavailable {
		return message
	}
	if message == "" {
		return ""
	}
	step := "读取本站数据"
	switch detail.Stage {
	case "verify":
		step = "核对本站登录状态"
	case "refresh":
		step = "延长本站登录有效期"
	}
	if isSiteSyncRateLimit(err) {
		return message
	}
	return step + "失败。上游提示：" + message + "。请按上游提示处理后再刷新。"
}
