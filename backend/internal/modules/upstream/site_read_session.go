package upstream

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"
)

// This record only coordinates a refresh already in progress. The saved Site
// remains the source of login state, including a rejected refresh.
type sessionRefreshFlight struct {
	done     chan struct{}
	baseline Session
	result   siteReadSessionResult
	err      error
}

type siteReadSessionResult struct {
	Session   Session
	Persisted bool
	Rejected  bool
}

func copyReadSession(session Session) Session {
	if session.ExpiresAt != nil {
		expires := *session.ExpiresAt
		session.ExpiresAt = &expires
	}
	return session
}

// SiteReadAuthError is a local check, not an upstream request. It deliberately
// does not change Enabled or make local metadata and history inaccessible.
func SiteReadAuthError(site *Site) error {
	if site == nil || site.Platform != PlatformSub2API || site.Status != StatusError ||
		site.ErrorKey == nil || *site.ErrorKey != ErrorRefreshTokenRejected {
		return nil
	}
	return &RequestError{MessageKey: ErrorAuth, Platform: PlatformSub2API,
		Reason: ErrorRefreshTokenRejected, Stage: "refresh",
		UpstreamMessage: "已保存的登录信息上次延长有效期时被上游拒绝，本次没有请求上游，也没有读取费用；先前的具体回复未保存在本站，请在编辑站点中用原账号重新登录。"}
}

// SiteReadResultError describes a local rejection discovered after an upstream
// read. Keep its classification and status, but do not claim no request was sent.
func SiteReadResultError(err error) error {
	var detail *RequestError
	if !errors.As(err, &detail) || !isSiteReadAuthFailure(err) || detail.StatusCode != 0 {
		return err
	}
	result := *detail
	result.UpstreamMessage = "读取期间发现本站登录信息已被上游拒绝，本次读取结果未采用；请在编辑站点中用原账号重新登录后重试，不会直接修改主站账号。"
	return &result
}

func isSiteReadAuthFailure(err error) bool {
	var detail *RequestError
	return errors.As(err, &detail) && detail.Platform == PlatformSub2API && detail.Stage == "refresh" &&
		detail.Reason == ErrorRefreshTokenRejected && (detail.StatusCode == 401 || detail.StatusCode == 0)
}

func isRefreshHTTPRejection(err error) bool {
	var detail *RequestError
	return errors.As(err, &detail) && detail.Platform == PlatformSub2API && detail.Stage == "refresh" && detail.StatusCode == 401
}

// Only a real refresh 401 may newly publish the persistent pause combination.
// Other errors keep their existing category, without accidentally pausing.
func siteReadStatusErrorKey(err error) string {
	var detail *RequestError
	if errors.As(err, &detail) && detail.Stage == "refresh" && detail.Reason == ErrorRefreshTokenRejected && detail.StatusCode != 401 {
		return detail.MessageKey
	}
	return siteErrorKey(err)
}

func (s *Service) prepareSiteReadSession(ctx context.Context, original Site, window time.Duration, inclusive bool) (result siteReadSessionResult, err error) {
	if original.Session == nil {
		return result, newRequestError(ErrorNotFound, original.Platform)
	}
	result.Session = copyReadSession(*original.Session)
	if err = ctx.Err(); err != nil {
		return result, err
	}
	s.mu.Lock()
	current, readErr := s.readCurrentSite(ctx, original.ID)
	if readErr != nil {
		s.mu.Unlock()
		return result, keyUsageSessionPersistenceError(original.Platform, readErr)
	}
	if _, deleted := s.deletedSites[original.ID]; deleted || !keyUsageSiteMatches(current, original) {
		s.mu.Unlock()
		return result, staleSiteRequestError()
	}
	if authErr := SiteReadAuthError(current); authErr != nil {
		s.mu.Unlock()
		return result, authErr
	}
	if !reflect.DeepEqual(current.Session, original.Session) {
		s.mu.Unlock()
		return result, staleSiteRequestError()
	}
	if !s.platformService.sessionNeedsRefresh(result.Session, window, inclusive) {
		s.mu.Unlock()
		return result, nil
	}
	if s.sessionRefreshFlights == nil {
		s.sessionRefreshFlights = make(map[string]*sessionRefreshFlight)
	}
	if flight := s.sessionRefreshFlights[original.ID]; flight != nil && reflect.DeepEqual(&flight.baseline, original.Session) {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-flight.done:
			result = flight.result
			result.Session = copyReadSession(result.Session)
			if flight.err != nil {
				return result, flight.err
			}
			return result, SiteReadResultError(s.validateSiteReadResult(ctx, original, result.Session))
		}
	}
	flight := &sessionRefreshFlight{done: make(chan struct{}), baseline: copyReadSession(result.Session)}
	s.sessionRefreshFlights[original.ID] = flight
	s.mu.Unlock()
	defer func() {
		if recover() != nil {
			err = &RequestError{MessageKey: ErrorRequest, Platform: original.Platform, Stage: "refresh",
				UpstreamMessage: "延长本站登录有效期时执行中断，具体原因未确认；本次未读取费用，请检查 Hub 后端站点读取记录后重试。"}
		}
		s.mu.Lock()
		flight.result, flight.err = result, err
		flight.result.Session = copyReadSession(result.Session)
		if s.sessionRefreshFlights[original.ID] == flight {
			delete(s.sessionRefreshFlights, original.ID)
		}
		close(flight.done)
		s.mu.Unlock()
	}()
	refreshed, refreshErr := s.platformService.refreshSub2APISessionContext(ctx, result.Session)
	if refreshErr != nil {
		if !isRefreshHTTPRejection(refreshErr) {
			return result, refreshErr
		}
		result.Rejected = true
		if err = s.persistSiteReadRejection(original); err != nil {
			var saveErr *RequestError
			if errors.As(err, &saveErr) && saveErr.Stage == "save" {
				copy := *saveErr
				copy.UpstreamMessage = "上游已拒绝延长本站登录有效期，但暂停状态保存失败：" + saveErr.UpstreamMessage + " 本次未读取费用，后续任务仍可能重试；请维护人员检查 PostgreSQL 和 Redis 保存记录，再用原账号重新登录。"
				return result, &copy
			}
			return result, err
		}
		var rejected *RequestError
		errors.As(refreshErr, &rejected)
		detail := *rejected
		detail.Reason = ErrorRefreshTokenRejected
		cause := strings.TrimSpace(safeUpstreamMessage(detail.UpstreamMessage))
		if cause == "" {
			cause = "具体拒绝原因未返回"
		}
		detail.UpstreamMessage = "上游在延长本站登录有效期时拒绝了已保存的登录信息（HTTP 401）：" + cause + "；没有读取本轮费用，已暂停自动访问，请在编辑站点中用原账号重新登录。"
		return result, &detail
	}
	result.Session = copyReadSession(refreshed)
	proceed, saveErr := s.persistKeyUsageSession(original, result.Session)
	if saveErr != nil {
		return result, SiteReadResultError(saveErr)
	}
	if !proceed {
		return result, staleSiteRequestError()
	}
	result.Persisted = true
	return result, SiteReadResultError(s.validateSiteReadResult(ctx, original, result.Session))
}

func (s *Service) persistSiteReadRejection(original Site) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer cancel()
	current, err := s.cache.Get(ctx, original.ID)
	if err != nil {
		return keyUsageSessionPersistenceError(original.Platform, err)
	}
	if _, deleted := s.deletedSites[original.ID]; deleted || !keyUsageSiteMatches(current, original) || !reflect.DeepEqual(current.Session, original.Session) {
		return staleSiteRequestError()
	}
	if SiteReadAuthError(current) != nil {
		return nil
	}
	expected, candidate := cloneSite(current), cloneSite(current)
	key := ErrorRefreshTokenRejected
	candidate.Status, candidate.ErrorKey = StatusError, &key
	if err := s.publishExistingSiteLocked(expected, candidate, false, nil, nil); err != nil {
		return err
	}
	s.clearTimerLocked(original.ID)
	return nil
}

func (s *Service) validateSiteReadResult(ctx context.Context, original Site, session Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.readCurrentSite(ctx, original.ID)
	if err != nil {
		return keyUsageSessionPersistenceError(original.Platform, err)
	}
	if _, deleted := s.deletedSites[original.ID]; deleted || !keyUsageSiteMatches(current, original) {
		return staleSiteRequestError()
	}
	if authErr := SiteReadAuthError(current); authErr != nil {
		return authErr
	}
	if !reflect.DeepEqual(current.Session, &session) {
		return staleSiteRequestError()
	}
	return nil
}
