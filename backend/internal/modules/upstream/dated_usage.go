package upstream

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type datedUsageScopeKey struct{}

// Only callers reading a specified date carry this scope. Today collection and
// unrelated requests continue to use an unmarked context.
func withDatedUsageScope(ctx context.Context, siteID string) context.Context {
	return context.WithValue(ctx, datedUsageScopeKey{}, strings.TrimSpace(siteID))
}

func datedUsageSiteID(ctx context.Context) string {
	siteID, _ := ctx.Value(datedUsageScopeKey{}).(string)
	return siteID
}

func (s *PlatformService) fetchCostForDateForSite(siteID string, session Session, date string) (float64, CostFetchMeta, error) {
	switch session.Platform {
	case PlatformSub2API:
		return s.fetchSub2APICostForDate(siteID, session, date)
	case PlatformNewAPI:
		return s.fetchNewAPICostForDate(session, date)
	default:
		cost, meta, err := s.fetchNewAPICostForDate(session, date)
		if err == nil {
			return cost, meta, nil
		}
		return s.fetchSub2APICostForDate(siteID, session, date)
	}
}

func (s *PlatformService) datedUsagePaused(siteID string) bool {
	if strings.TrimSpace(siteID) == "" {
		return false
	}
	now := s.keyUsageNow()
	s.keyUsageMu.Lock()
	defer s.keyUsageMu.Unlock()
	until, exists := s.usageRateLimitedUntil[siteID]
	if exists && !now.Before(until) {
		delete(s.usageRateLimitedUntil, siteID)
		return false
	}
	return exists
}

func datedUsagePauseError() *RequestError {
	err := newRequestErrorWithStatus(ErrorRequest, "", http.StatusTooManyRequests)
	err.Reason = ErrorRateLimited
	err.MutationOutcome = MutationNotSent
	return err
}

func datedUsagePauseDuration(retryAfter string, now time.Time) time.Duration {
	delay := time.Minute
	if seconds, err := strconv.ParseFloat(retryAfter, 64); err == nil && !math.IsNaN(seconds) {
		// Bound before converting to Duration so large values cannot overflow.
		if seconds >= 5*60 {
			return 5 * time.Minute
		}
		if seconds > 60 {
			delay = time.Duration(seconds * float64(time.Second))
		}
	} else if deadline, err := http.ParseTime(retryAfter); err == nil && deadline.Sub(now) > delay {
		delay = deadline.Sub(now)
	}
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	return delay
}

func (s *PlatformService) recordDatedUsageRateLimit(siteID, reqURL string, response jsonResponse, err error) {
	if strings.TrimSpace(siteID) == "" {
		return
	}
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.StatusCode != http.StatusTooManyRequests ||
		requestErr.UpstreamCode == "API_KEY_QUOTA_EXHAUSTED" || requestErr.UpstreamCode == "INSUFFICIENT_QUOTA" {
		return
	}
	now := s.keyUsageNow()
	duration := datedUsagePauseDuration(response.Header.Get("Retry-After"), now)
	until := now.Add(duration)
	s.keyUsageMu.Lock()
	oldUntil, exists := s.usageRateLimitedUntil[siteID]
	alreadyPaused := exists && now.Before(oldUntil)
	if s.usageRateLimitedUntil == nil {
		s.usageRateLimitedUntil = make(map[string]time.Time)
	}
	if !alreadyPaused || until.After(oldUntil) {
		s.usageRateLimitedUntil[siteID] = until
	}
	s.keyUsageMu.Unlock()
	if !alreadyPaused {
		log.Printf("[upstream-usage] 历史用量查询被限流，暂停 host=%s seconds=%d", safeHost(reqURL), int(duration/time.Second))
	}
}

// Account-level Sub2API dated cost retains its original one-attempt contract.
func (s *PlatformService) requestUsageJSONOnce(siteID, reqURL string, options requestOptions) (jsonResponse, error) {
	if s.datedUsagePaused(siteID) {
		return jsonResponse{}, datedUsagePauseError()
	}
	response, err := s.httpClient.requestJSON(reqURL, options)
	s.recordDatedUsageRateLimit(siteID, reqURL, response, err)
	return response, err
}

func (s *PlatformService) isCostFallbackUnsupported(siteID string) bool {
	if strings.TrimSpace(siteID) == "" {
		return false
	}
	s.keyUsageMu.Lock()
	defer s.keyUsageMu.Unlock()
	return s.costFallbackUnsupported[siteID]
}

func (s *PlatformService) rememberCostFallbackUnsupported(siteID, baseURL string, err error) {
	if strings.TrimSpace(siteID) == "" {
		return
	}
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || (requestErr.StatusCode != http.StatusNotFound && requestErr.StatusCode != http.StatusMethodNotAllowed) {
		return
	}
	s.keyUsageMu.Lock()
	alreadyUnsupported := s.costFallbackUnsupported[siteID]
	if s.costFallbackUnsupported == nil {
		s.costFallbackUnsupported = make(map[string]bool)
	}
	s.costFallbackUnsupported[siteID] = true
	s.keyUsageMu.Unlock()
	if !alreadyUnsupported {
		log.Printf("[upstream-usage] 站点不支持管理员 Key 列表回退，之后不再回退 host=%s status=%d", safeHost(baseURL), requestErr.StatusCode)
	}
}
