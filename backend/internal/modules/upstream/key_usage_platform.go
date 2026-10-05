package upstream

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"
)

func (s *PlatformService) keyUsageNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func keyUsageRetryAfter(value string, now time.Time) time.Duration {
	delay := time.Duration(0)
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		delay = time.Duration(seconds * float64(time.Second))
	} else if deadline, err := http.ParseTime(value); err == nil {
		delay = deadline.Sub(now)
	}
	if delay < 0 {
		delay = 0
	}
	if delay > 2*time.Second {
		delay = 2 * time.Second
	}
	return delay
}

func (s *PlatformService) fetchSub2APIKeyUsageBatch(ctx context.Context, session Session, records []sub2APIKeyRecord, includeZero bool) ([]KeyUsageTodayStat, bool, error) {
	s.keyUsageMu.Lock()
	unsupported := s.keyUsageBatchUnsupported[session.BaseURL]
	s.keyUsageMu.Unlock()
	if unsupported {
		return nil, false, nil
	}
	stats := make([]KeyUsageTodayStat, 0, len(records))
	for start := 0; start < len(records); start += 100 {
		end := start + 100
		if end > len(records) {
			end = len(records)
		}
		ids := make([]int64, 0, end-start)
		for _, record := range records[start:end] {
			id, err := strconv.ParseInt(record.id, 10, 64)
			if err != nil {
				return nil, true, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
			ids = append(ids, id)
		}
		options := sub2APIUserAuthOptions(session)
		options.Method = http.MethodPost
		options.Body = map[string]any{"api_key_ids": ids}
		response, err := s.requestKeyUsageJSONWithContext(ctx, session.BaseURL+"/api/v1/usage/dashboard/api-keys-usage", options)
		if err != nil {
			var requestErr *RequestError
			if errors.As(err, &requestErr) && (requestErr.StatusCode == 404 || requestErr.StatusCode == 405) {
				s.keyUsageMu.Lock()
				if s.keyUsageBatchUnsupported == nil {
					s.keyUsageBatchUnsupported = make(map[string]bool)
				}
				s.keyUsageBatchUnsupported[session.BaseURL] = true
				s.keyUsageMu.Unlock()
				return nil, false, nil
			}
			return nil, true, err
		}
		data := dataRecord(response.Payload)
		usage, ok := data["stats"].(map[string]any)
		if !ok {
			return nil, true, newRequestError(ErrorInvalidResponse, PlatformSub2API)
		}
		for _, record := range records[start:end] {
			amount := firstNumber(usage[record.id], []string{"today_actual_cost"})
			// Ownership-filtered or incomplete rows cannot become a fabricated zero.
			if amount == nil {
				return nil, true, newRequestError(ErrorInvalidResponse, PlatformSub2API)
			}
			if *amount <= 0 && !includeZero {
				continue
			}
			stats = append(stats, KeyUsageTodayStat{KeyID: record.id, KeyIDs: []string{record.id}, KeyName: record.name, GroupName: record.groupName, TodayAmount: *amount})
		}
	}
	return stats, true, nil
}

func (s *PlatformService) fetchKeyCollectionTotal(ctx context.Context, session Session, date string) (*float64, error) {
	if session.Platform == PlatformSub2API {
		response, err := s.requestKeyUsageJSONWithContext(ctx, session.BaseURL+"/api/v1/usage/dashboard/stats", sub2APIUserAuthOptions(session))
		if err != nil {
			return nil, err
		}
		amount := firstNumber(dataRecord(response.Payload), []string{"today_actual_cost"})
		if amount == nil {
			return nil, newRequestError(ErrorInvalidResponse, session.Platform)
		}
		return amount, nil
	}
	start, end, err := businessDayUnixBounds(date)
	if err != nil {
		return nil, err
	}
	response, err := s.requestKeyUsageJSONWithContext(ctx, session.BaseURL+"/api/log/self/stat?type=2&start_timestamp="+strconvInt(start)+"&end_timestamp="+strconvInt(end), newAPIAuthOptions(session))
	if err != nil {
		return nil, err
	}
	amount := quotaToUSDWithUnit(firstNumber(dataRecord(response.Payload), []string{"quota", "used_quota", "usedQuota"}), session.QuotaPerUnit)
	if amount == nil {
		return nil, newRequestError(ErrorInvalidResponse, session.Platform)
	}
	return amount, nil
}

// Historical daily closing preserves its explicit date; collectors always use today.
func (s *PlatformService) collectKeyUsageToday(ctx context.Context, session Session, groups []GroupInfo, date string) ([]KeyUsageTodayStat, error) {
	if session.Platform == PlatformSub2API {
		return s.fetchSub2APIKeyUsage(ctx, session, true, date, true)
	}
	if session.Platform == PlatformNewAPI {
		return s.fetchNewAPIKeyUsageToday(ctx, session, groups, true, date)
	}
	return nil, newRequestError(ErrorNotFound, "")
}

func (s *PlatformService) fetchKeyUsageForDate(ctx context.Context, session Session, groups []GroupInfo, date string) ([]KeyUsageTodayStat, error) {
	if session.Platform == PlatformSub2API {
		return s.fetchSub2APIKeyUsage(ctx, session, true, date, false)
	}
	if session.Platform == PlatformNewAPI {
		return s.fetchNewAPIKeyUsageToday(ctx, session, groups, true, date)
	}
	return nil, newRequestError(ErrorNotFound, "")
}
